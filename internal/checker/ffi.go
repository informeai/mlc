package checker

import (
	"fmt"
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
//
// Callbacks: a parameter of fn type receives the name of an MLC function,
// and the generator passes a C wrapper of it (mlc_fn_x__cb), which prepares
// the runtime when C calls from a thread MLC didn't create. kept on a
// parameter says C keeps it after the call: a kept callback's context is
// moved (kept *T), because a borrow would be left dangling.
//
//	extern "C" fn qsort(base &[]P, n uint64, size uint64, cmp fn(&P, &P) int32)
//	extern "C" fn on_event(h kept fn(&Ctx, int32), arg kept *Ctx)

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
// returns "". kept: the parameter is marked kept (C keeps it).
func cParamOK(t Type, kept bool) string {
	if kept {
		switch tt := t.(type) {
		case *FuncType:
			return cCallbackOK(tt)
		case *Pointer:
			if s, ok := tt.Elem.(*Struct); ok && !s.Extern && !tt.Nullable {
				return "" // the callback's context, moved to C
			}
			if isExternStruct(tt.Elem) {
				return "kept marks a callback or its context; a C handle given with *X already belongs to C"
			}
		case *Borrow:
			return "a kept context is moved, not lent: C keeps it after the call, and a borrow would be left dangling when its owner frees it; declare kept *T and pass what alloc returned"
		}
		return "kept only marks a callback (a fn type) or its context (kept *T, with T a struct of the program)"
	}
	switch tt := t.(type) {
	case *FuncType:
		return cCallbackOK(tt)
	case *OutHandle:
		return ""
	case *OptArg:
		return cParamOK(tt.Inner, false)
	case *Borrow:
		switch e := tt.Elem.(type) {
		case *Slice:
			if cScalar(e.Elem) {
				return ""
			}
			if s, ok := e.Elem.(*Struct); ok && cStructOK(s) {
				return "" // an array of C structs with the same layout (qsort's)
			}
			return "a slice lent to C must have numeric or bool elements, or structs of scalar fields"
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
		return "MLC memory can't be handed to C (it wouldn't know how to free it); lend it with &, or, as a callback's context that C keeps, move it with kept *T"
	case *Slice:
		return "an owning slice can't be handed to C; lend it with &[]T"
	}
	if cScalar(t) || t == String {
		return ""
	}
	return "that type has no representation in C"
}

// cCallbackOK explains why ft can't be the type of a callback C calls, or
// returns "". What C passes the callback follows the extern rules, but
// nothing that owns: C can't hand MLC memory over.
func cCallbackOK(ft *FuncType) string {
	for i, p := range ft.Params {
		switch pt := p.(type) {
		case *Borrow:
			switch e := pt.Elem.(type) {
			case *Slice:
				// C passes the pointer and, right after it, the count: the wrapper
				// joins them into the slice, and the callback also gets the count
				if i+1 >= len(ft.Params) || !isInteger(ft.Params[i+1]) {
					return "a callback's slice must be followed by its length, as in C (fn(&[]byte, int32) for const uint8_t *data, int len): C passes the pointer and the count"
				}
				if !cScalar(e.Elem) {
					if s, ok := e.Elem.(*Struct); !ok || !cStructOK(s) {
						return "a callback's slice must have numeric or bool elements, or structs of scalar fields"
					}
				}
				continue
			case *Struct:
				if e.ChanEnd != 0 || e.MutexKind != 0 {
					return "a callback can't receive a channel end or a mutex: put it in the context struct"
				}
				continue // a C handle, a C struct or the context (paired in checkCallbacks)
			}
			if cScalar(pt.Elem) {
				continue
			}
			return fmt.Sprintf("a callback can't receive %s: only numbers, bool, structs and C handles, borrowed", p)
		case *OptArg:
			return "a callback can't receive ?&T (MLC functions don't take it): for a C handle that C may pass as NULL, receive &X (MLC never looks inside it)"
		}
		if cScalar(p) {
			continue
		}
		if IsOwner(p) {
			return fmt.Sprintf("a callback can't receive an owner (%s): C can't hand MLC memory over", p)
		}
		return fmt.Sprintf("a callback's parameter %s has no representation in C", p)
	}
	if ft.Result != Void && !cScalar(ft.Result) {
		return "a callback returns only a number or bool (or nothing)"
	}
	return ""
}

// callbackContexts is the structs of the program that the callbacks among
// fn's parameters receive borrowed: lent with the call (&T), C passes them
// back to the callback as an opaque pointer, so any struct goes, not only C's
// layout.
func callbackContexts(fn *Func) map[Type]bool {
	m := map[Type]bool{}
	for _, p := range fn.Params {
		ft, ok := p.Type.(*FuncType)
		if !ok {
			continue
		}
		for _, cp := range ft.Params {
			if b, ok := cp.(*Borrow); ok {
				if s, ok := b.Elem.(*Struct); ok && !s.Extern {
					m[s] = true
				}
			}
		}
	}
	return m
}

// checkCallbacks pairs, in a C function's parameters, each callback with the
// context it receives. A callback's &T, with T a struct of the program, is
// MLC memory C only has because the call gave it: a borrow (&T, &[]T) for a
// synchronous callback, or the kept *T for a kept one. A struct of scalar
// fields only may also be C's own data (it has C's layout), so it goes
// unpaired.
func (c *checker) checkCallbacks(fn *Func, d *ast.FuncDecl) {
	var keptCtx *Local
	keptFns := 0
	for i, p := range fn.Params {
		if !p.Kept || p.Type == Invalid {
			continue
		}
		switch p.Type.(type) {
		case *FuncType:
			keptFns++
		case *Pointer:
			if keptCtx != nil {
				c.errorf(d.Params[i].P, "%s has two kept contexts: C passes one pointer back to the callback", d.Name)
			}
			keptCtx = p
		}
	}
	if keptCtx != nil && keptFns == 0 {
		c.errorf(keptCtx.Pos, "kept context %s without a kept callback: mark the callback C keeps with kept too", keptCtx.Name)
	}
	lent := func(s *Struct) bool {
		for _, p := range fn.Params {
			if p.Kept {
				continue
			}
			t := p.Type
			if o, ok := t.(*OptArg); ok {
				t = o.Inner
			}
			if b, ok := t.(*Borrow); ok {
				if b.Elem == s {
					return true
				}
				if sl, ok := b.Elem.(*Slice); ok && sl.Elem == s {
					return true
				}
			}
		}
		return false
	}
	for i, p := range fn.Params {
		ft, ok := p.Type.(*FuncType)
		if !ok {
			continue
		}
		for _, cp := range ft.Params {
			b, ok := cp.(*Borrow)
			if !ok {
				continue
			}
			s, ok := b.Elem.(*Struct)
			if !ok || s.Extern {
				continue
			}
			kept := keptCtx != nil && keptCtx.Type.(*Pointer).Elem == s
			switch {
			case p.Kept && kept:
			case p.Kept && lent(s):
				c.errorf(d.Params[i].P, "the callback %s is kept, so the %s it receives must be kept too: declare the context kept *%s (a borrow would be left dangling)", p.Name, s.Name, s.Name)
			case p.Kept && !cStructOK(s):
				c.errorf(d.Params[i].P, "the kept callback %s receives &%s, MLC memory: declare its context as a parameter kept *%s", p.Name, s.Name, s.Name)
			case !p.Kept && kept:
				c.errorf(d.Params[i].P, "the context is kept, so the callback %s must be kept too", p.Name)
			case !p.Kept && !lent(s) && !cStructOK(s):
				c.errorf(d.Params[i].P, "the callback %s receives &%s, MLC memory that C only has if the call gives it: add the context as a parameter (&%s)", p.Name, s.Name, s.Name)
			}
		}
	}
}

// callbackArg checks the argument of a C function's fn parameter: the name of
// an MLC function (not a fn variable: the C wrapper is written at compile
// time, for that function).
func (c *checker) callbackArg(a ast.Expr, ft *FuncType, context string) {
	before := len(c.errs)
	c.valueAs(a, ft, context)
	if len(c.errs) > before {
		return
	}
	fn, ok := c.info.FuncRefs[a]
	if !ok {
		c.errorf(a.Pos(), "%s: pass the name of an MLC function; the compiler writes a C wrapper for it at compile time, so a fn variable isn't accepted", context)
		return
	}
	if fn.CSym != "" {
		c.errorf(a.Pos(), "%s: %s is a C function; pass an MLC function (or call the C one from it)", context, fn.Name)
		return
	}
	if !c.callbacks[fn] {
		c.callbacks[fn] = true
		c.info.Callbacks = append(c.info.Callbacks, fn)
	}
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
	contexts := callbackContexts(fn)
	for i, p := range fn.Params {
		if p.Type == Invalid {
			continue // the type already gave an error
		}
		if b, ok := p.Type.(*Borrow); ok && contexts[b.Elem] && !p.Kept {
			continue // a callback's context: C only hands it back, without looking inside
		}
		if why := cParamOK(p.Type, p.Kept); why != "" {
			c.errorf(d.Params[i].P, "parameter %s of %s (%s): %s", p.Name, d.Name, p.Type, why)
		}
	}
	c.checkCallbacks(fn, d)
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
