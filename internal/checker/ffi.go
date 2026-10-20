package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// C functions (FFI)
//
//	link "sqlite3"
//	extern "C" type Db                                     // opaque C handle
//	extern "C" fn open(name string) ?*Db                  // a handle (or NULL)
//	extern "C" fn strlen(s string) uint64
//	extern "C" fn memcmp(a &[]byte, b &[]byte, n uint64) int32
//	extern "C" fn fclose(f *File) int32                    // consumes the handle
//
// Any file can declare C functions. The boundary is checked: only
// numbers, bool, string (a literal, zero-terminated), &[]T (the pointer to the
// data goes), &T and C handles (*X and ?*X, with X an extern
// "C" type) go through. Nothing that owns MLC memory crosses: C can't
// free or keep MLC memory. The signature must match C's
// (C's int is int32; size_t is uint64), and variadic functions (printf) aren't
// accepted.
//
// A C handle is an owner like the others: the checker requires it to be
// passed to a C function that takes it by ownership (*X), and doesn't allow free
// on it nor looking at its content. A C function that returns a handle returns ?*X,
// because it may return NULL.
//
// A &?*X parameter (OutHandle) is a handle returned through a pointer, as in
// int sqlite3_open(const char *, sqlite3 **): the caller passes &db, with db
// a ?*X variable that holds no handle (nil or already closed), and after the
// call db may hold a new handle, which the checker forces you to close.

// OptArg is ?&T (or ?&?*X), a C function parameter that may be NULL:
// the caller passes nil or the same as it would pass for &T (or &?*X).
type OptArg struct{ Inner Type } // *Borrow or *OutHandle

func (o *OptArg) String() string { return "?" + o.Inner.String() }

func isOptArg(t Type) bool {
	_, ok := t.(*OptArg)
	return ok
}

func (c *checker) optArgType(inner Type) *OptArg {
	if o, ok := c.optArgs[inner]; ok {
		return o
	}
	o := &OptArg{Inner: inner}
	c.optArgs[inner] = o
	return o
}

// OutHandle is the &?*X type of a C function's out parameter: it
// writes a new handle (or NULL) into the borrowed variable.
type OutHandle struct{ Ptr *Pointer }

func (o *OutHandle) String() string { return "&" + o.Ptr.String() }

func isOutHandle(t Type) bool {
	_, ok := t.(*OutHandle)
	return ok
}

// outHandleType resolves &?*X in a C function's signature.
func (c *checker) outHandleType(t *ast.TypeExpr) Type {
	elem := c.resolveType(t.Elem.Elem, ctxElem)
	if elem == Invalid {
		return Invalid
	}
	if !isExternStruct(elem) {
		c.errorf(t.P, "&?*T only serves C handles (?*X, with X an extern \"C\" type): the C function writes the handle into the variable")
		return Invalid
	}
	p := c.pointerTo(elem, true)
	if oh, ok := c.outHandles[p]; ok {
		return oh
	}
	oh := &OutHandle{Ptr: p}
	c.outHandles[p] = oh
	return oh
}

// outArg checks the argument of a &?*X parameter: &v, with v a variable or
// field of type ?*X.
func (c *checker) outArg(a ast.Expr, oh *OutHandle, context string) {
	u, ok := a.(*ast.UnaryExpr)
	if !ok || u.Op != token.AMP {
		c.value(a)
		c.errorf(a.Pos(), "%s: expected %s; pass the variable that receives the handle with &: f(&h)", context, oh)
		return
	}
	switch u.X.(type) {
	case *ast.Ident, *ast.SelectorExpr:
	default:
		c.peekType(a)
		c.errorf(a.Pos(), "%s: the handle must go into a ?*X variable or field: f(&h)", context)
		return
	}
	t := c.value(u.X)
	if t != Invalid && t != oh.Ptr {
		c.errorf(a.Pos(), "%s: expected %s, found &%s (declare var h %s = nil)", context, oh, t, oh.Ptr)
	}
	c.info.Types[a] = oh
}

// cScalar says whether t goes straight to C.
func cScalar(t Type) bool {
	return isNumeric(t) || t == Bool
}

// cStructOK says whether an MLC struct can be lent to C: only scalar
// fields (C sees the same layout in memory).
func cStructOK(s *Struct) bool {
	if s.Resource || s.Origin != nil {
		return false
	}
	for _, f := range s.Fields {
		if !cScalar(f.Type) {
			return false
		}
	}
	return true
}

func isExternStruct(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.Extern
}

// cParamOK explains why t can't be a parameter of a C function, or
// returns "".
func cParamOK(t Type) string {
	switch tt := t.(type) {
	case *OutHandle:
		return ""
	case *OptArg:
		return cParamOK(tt.Inner)
	case *Borrow:
		switch e := tt.Elem.(type) {
		case *Slice:
			if cScalar(e.Elem) {
				return ""
			}
			return "a slice lent to C must have numeric or bool elements"
		case *Struct:
			if e.Extern || cStructOK(e) {
				return ""
			}
			return "a struct lent to C can only have numeric or bool fields"
		}
		if cScalar(tt.Elem) {
			return ""
		}
		return "that borrow has no representation in C"
	case *Pointer:
		if isExternStruct(tt.Elem) {
			return "" // the C function takes ownership of the handle
		}
		return "MLC memory can't be handed to C (it wouldn't know how to free it); lend it with &"
	case *Slice:
		return "an owning slice can't be handed to C; lend it with &[]T"
	}
	if cScalar(t) || t == String {
		return ""
	}
	return "that type has no representation in C"
}

// cResultOK explains why t can't be the result of a C function, or
// returns "".
func cResultOK(t Type) string {
	if t == Void || cScalar(t) {
		return ""
	}
	if p, ok := t.(*Pointer); ok && isExternStruct(p.Elem) {
		if !p.Nullable {
			return "a C function may return NULL: the return type must be ?" + t.String()
		}
		return ""
	}
	return "a C function only returns numbers, bool or a handle (?*X, with X an extern \"C\" type)"
}

// collectExternC registers a C function declared with extern "C" fn.
func (c *checker) collectExternC(fn *Func, d *ast.FuncDecl) {
	if len(d.TypeParams) > 0 || d.Recv != nil {
		c.errorf(d.P, "a C function can't be generic or a method")
		return
	}
	for i, p := range fn.Params {
		if p.Type == Invalid {
			continue // the type already gave an error
		}
		if why := cParamOK(p.Type); why != "" {
			c.errorf(d.Params[i].P, "parameter %s of %s (%s): %s", p.Name, d.Name, p.Type, why)
		}
	}
	if why := cResultOK(fn.Result); why != "" && fn.Result != Invalid {
		c.errorf(d.P, "return of %s (%s): %s", d.Name, fn.Result, why)
	}
	fn.CSym = d.Name
	fn.CName = "mlc_c_" + d.Name
	c.mod.funcs[d.Name] = fn
	for _, prev := range c.info.CExterns {
		if prev.CSym == d.Name {
			return // the same C function declared in two files
		}
	}
	c.info.CExterns = append(c.info.CExterns, fn)
}

// collectLinks registers the C libraries asked for with link "name".
func (c *checker) collectLinks(f *ast.File) {
	for _, l := range f.Links {
		if l.Lib == "" || strings.ContainsAny(l.Lib, "/\\ ") || strings.HasPrefix(l.Lib, "-") {
			c.errorf(l.P, "link %q: write only the library's name, like link \"sqlite3\" (for -lsqlite3)", l.Lib)
			continue
		}
		dup := false
		for _, prev := range c.info.Links {
			dup = dup || prev == l.Lib
		}
		if !dup {
			c.info.Links = append(c.info.Links, l.Lib)
		}
	}
}
