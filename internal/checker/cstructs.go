package checker

import (
	"mlc/internal/ast"
)

// C structs and constants from headers
//
//	extern "C" struct esp_timer_create_args_t from "esp_timer.h" {
//	    callback fn(&Blink)
//	    arg kept *Blink
//	    name string
//	}
//	extern "C" const GPIO_MODE_OUTPUT int32 from "driver/gpio.h"
//
// MLC doesn't copy a C struct's layout: the generated C includes the header
// and uses C's own struct, so unions, bitfields and the fields that only
// exist on some chips stay as the header says. The declaration names the
// fields the program uses, with MLC types for the checker; the others are
// zero. The C compiler checks the names (a field that doesn't exist is its
// error) and converts the values.
//
// A C struct is a value, without owners. Its fields are read and written,
// but not borrowed (a bitfield has no address). Some are written only: a
// string (a const char * that C may keep, so only MLC's literals, which live
// as long as the program), a callback (the name of an MLC function, which
// gets the C wrapper) and its context, a kept *T, whose value comes from
// keep(x): x moves to C for good. An anonymous nested struct (flags { ... })
// is only a path to its fields.

// Kept is the type of what keep(x) gave to C for good: a callback's context
// (T a struct of the program), or data the library keeps, a C struct or an
// array of them (Slice). It's a pointer that lives until the program ends,
// copied freely (it owns nothing); the program no longer reaches the memory.
type Kept struct {
	Elem  *Struct
	Slice bool // kept []T: the array's pointer
}

func (k *Kept) String() string {
	if k.Slice {
		return "kept []" + k.Elem.String()
	}
	return "kept *" + k.Elem.String()
}

func (c *checker) keptType(s *Struct, slice bool) *Kept {
	key := keptKey{s, slice}
	if k, ok := c.kepts[key]; ok {
		return k
	}
	k := &Kept{Elem: s, Slice: slice}
	c.kepts[key] = k
	return k
}

type keptKey struct {
	s     *Struct
	slice bool
}

// keptOf is the kept type for kept T written in a declaration: kept *T (a
// context, or a C struct C keeps) or kept []T (an array of C structs), or nil.
func (c *checker) keptOf(t Type) *Kept {
	switch tt := t.(type) {
	case *Pointer:
		if s, ok := tt.Elem.(*Struct); ok && !tt.Nullable && !s.Extern && !s.Anon {
			return c.keptType(s, false)
		}
	case *Slice:
		if s, ok := tt.Elem.(*Struct); ok && s.CStruct && !s.Anon {
			return c.keptType(s, true)
		}
	}
	return nil
}

// isCStruct says whether t is a C struct from a header (or one of its
// anonymous nested structs).
func isCStruct(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.CStruct
}

func isAnonCStruct(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.CStruct && s.Anon
}

// addHeader registers a header for the generated C to include.
func (c *checker) addHeader(h string) {
	for _, prev := range c.info.CHeaders {
		if prev == h {
			return
		}
	}
	c.info.CHeaders = append(c.info.CHeaders, h)
}

// cStructFields resolves the fields of an extern "C" struct (or of one of
// its anonymous nested structs).
func (c *checker) cStructFields(s *Struct, decls []*ast.FieldDecl) {
	seen := map[string]bool{}
	for _, fd := range decls {
		if seen[fd.Name] {
			c.errorf(fd.P, "repeated field %s in %s", fd.Name, s.Name)
			continue
		}
		seen[fd.Name] = true
		if fd.Inner != nil {
			inner := &Struct{Name: s.Name + "." + fd.Name, Methods: map[string]*Func{}, File: s.File, Pub: s.Pub,
				Decl: s.Decl, mod: s.mod, CStruct: true, Anon: true}
			c.cStructFields(inner, fd.Inner)
			s.Fields = append(s.Fields, &Field{Name: fd.Name, Type: inner})
			continue
		}
		ctx := ctxField
		switch {
		case fd.Kept:
			ctx = ctxVar // kept []T: the slice's element only matters
		case fd.Type.Mod == ast.TypeBorrow:
			ctx = ctxParam // &X: C's pointer, checked by cFieldOK
		}
		t := c.resolveType(fd.Type, ctx)
		if t != Invalid {
			if why := c.cFieldOK(t, fd.Kept); why != "" {
				c.errorf(fd.P, "field %s of %s (%s): %s", fd.Name, s.Name, t, why)
				t = Invalid
			} else if fd.Kept {
				t = c.keptOf(t)
			}
		}
		s.Fields = append(s.Fields, &Field{Name: fd.Name, Type: t})
	}
	c.pairFields(s, decls)
}

// cFieldOK explains why t can't be the type of a C struct's field, or
// returns "".
func (c *checker) cFieldOK(t Type, kept bool) string {
	if kept {
		if c.keptOf(t) != nil {
			return ""
		}
		return "kept marks what keep(x) gives C for good: a callback's context (kept *T, with T a struct of the program), a C struct (kept *T) or an array of them (kept []T)"
	}
	switch tt := t.(type) {
	case *FuncType:
		return cCallbackOK(tt)
	case *Array:
		if cScalar(tt.Elem) {
			return ""
		}
		return "an array field of a C struct must have numeric or bool elements"
	case *Struct:
		if tt.CStruct {
			return ""
		}
		return "a struct inside a C struct must be a C struct too (extern \"C\" struct)"
	case *Pointer:
		if s, ok := tt.Elem.(*Struct); ok && !s.Extern {
			return "a pointer to the program's memory in a C struct is a callback's context: declare it kept *" + s.Name + " and give it with keep(x)"
		}
		return "handles and pointers as fields of a C struct aren't supported in this version"
	case *Borrow:
		if s, ok := tt.Elem.(*Struct); ok && s.Extern && s.ChanEnd == 0 && s.MutexKind == 0 {
			return "" // C's pointer to an opaque type, read from what C hands over
		}
		return "a C struct's pointer field is &X, with X an opaque C type (extern \"C\" type X), read from what C hands over"
	}
	if cScalar(t) || t == String {
		return ""
	}
	return "a field of a C struct is a number, bool, [N]T of numbers, a C struct, a string, a callback or its kept context"
}

// pairFields checks that a callback field that receives the program's
// memory has its context in a kept field of the same struct: C may keep a
// struct it receives, so a callback in a field is a kept one.
func (c *checker) pairFields(s *Struct, decls []*ast.FieldDecl) {
	kept := map[*Struct]bool{}
	for _, f := range s.Fields {
		if k, ok := f.Type.(*Kept); ok {
			kept[k.Elem] = true
		}
	}
	for i, f := range s.Fields {
		ft, ok := f.Type.(*FuncType)
		if !ok {
			continue
		}
		for _, p := range ft.Params {
			b, ok := p.(*Borrow)
			if !ok {
				continue
			}
			ms, ok := b.Elem.(*Struct)
			if !ok || ms.Extern || ms.CStruct || kept[ms] || cStructOK(ms) {
				continue
			}
			c.errorf(decls[i].P, "the callback %s receives &%s, MLC memory that C keeps with the struct: declare its context as a field kept *%s, given with keep(x)", f.Name, ms.Name, ms.Name)
		}
	}
	for _, f := range s.Fields {
		k, ok := f.Type.(*Kept)
		if !ok || k.Elem.CStruct {
			continue // C's data (a UUID, an array of definitions), not a context
		}
		used := false
		for _, g := range s.Fields {
			if ft, ok := g.Type.(*FuncType); ok {
				for _, p := range ft.Params {
					if b, ok := p.(*Borrow); ok && b.Elem == k.Elem {
						used = true
					}
				}
			}
		}
		if !used {
			for i := range decls {
				if decls[i].Name == f.Name {
					c.errorf(decls[i].P, "the kept context %s isn't received by any callback of %s (a callback field fn(&%s))", f.Name, s.Name, k.Elem.Name)
				}
			}
		}
	}
}

// keepCall checks keep(x): x, an owner from alloc, moves to C for good, and
// the result is the pointer C keeps (kept *T), for a context field.
func (c *checker) keepCall(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "keep takes one argument: keep(alloc(T{...})), the context C keeps")
		c.checkArgs(e)
		return Invalid
	}
	t := c.value(e.Args[0])
	if t == Invalid {
		return Invalid
	}
	if k := c.keptOf(t); k != nil {
		c.info.Calls[e] = Callee{Builtin: Keep}
		return k
	}
	c.errorf(e.Args[0].Pos(), "keep moves an owner to C: it takes a *T (keep(alloc(T{...}))), with T a struct, or a []T of C structs, not %s", t)
	return Invalid
}

// keptValue checks the value of a kept field: what keep(x) gave (or another
// kept value, copied).
func (c *checker) keptValue(v ast.Expr, k *Kept, context string) {
	t := c.value(v)
	if t == Invalid || t == k {
		return
	}
	if c.keptOf(t) == k {
		c.errorf(v.Pos(), "%s: a kept field takes what C keeps for good, given with keep(%s)", context, exprName(v))
		return
	}
	c.errorf(v.Pos(), "%s: expected %s (given with keep(x)), found %s", context, k, t)
}

// cFieldRead checks the reading of a C struct's field: the ones C may keep
// (strings, callbacks, kept contexts) are written only, and an anonymous
// nested struct only leads to its fields.
func (c *checker) cFieldRead(e *ast.SelectorExpr, t Type) Type {
	if !isCStruct(c.structBase(e.X)) {
		return t
	}
	switch tt := t.(type) {
	case *FuncType, *Kept:
		c.errorf(e.SelPos, "%s is a field C may keep: it's written, not read", e.Sel)
		return Invalid
	case *Struct:
		if tt.Anon && !c.anonOK {
			c.errorf(e.SelPos, "%s is an anonymous C struct: use its fields (%s.%s.field)", e.Sel, exprName(e.X), e.Sel)
			return Invalid
		}
	}
	if t == String {
		c.errorf(e.SelPos, "%s is a C string (const char *): MLC writes it, but reading it would need its length and its lifetime", e.Sel)
		return Invalid
	}
	return t
}

// structBase is the struct type behind x (through pointers and borrows), or nil.
func (c *checker) structBase(x ast.Expr) Type {
	t := c.info.Types[x]
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	if b, ok := t.(*Borrow); ok {
		t = b.Elem
	}
	if s, ok := t.(*Struct); ok {
		return s
	}
	return nil
}

// cFieldBorrow refuses &x.f with x a C struct.
func (c *checker) cFieldBorrow(x ast.Expr) bool {
	sel, ok := x.(*ast.SelectorExpr)
	if !ok || !isCStruct(c.structBase(sel.X)) {
		return false
	}
	c.errorf(sel.SelPos, "the fields of a C struct aren't borrowed (a bitfield has no address): read or write %s, or borrow the whole struct", sel.Sel)
	return true
}

// cVarDecl declares extern "C" var N T from "h.h", a global variable of the
// C library: a number or bool, read and written, or a C struct, whose fields
// are read and written (not the whole struct). MLC has no mutable global
// variables, so that two threads never reach the same memory; a C global is
// the library's, written before starting it and the program's threads.
func (c *checker) cVarDecl(d *ast.ConstDecl) {
	t := c.resolveType(d.Type, ctxVar)
	if t != Invalid && !isNumeric(t) && t != Bool && !(isCStruct(t) && !isAnonCStruct(t)) {
		c.errorf(d.Type.P, "a C global variable is a number, bool or a C struct (extern \"C\" struct)")
		t = Invalid
	}
	var l *Local
	if st := c.mod.structs[d.Name]; st != nil && st.CStruct && st.Decl != nil && st.Decl.Tag {
		// struct ble_hs_cfg ble_hs_cfg: in C a tag and a variable don't clash,
		// and in MLC the type and the value are in different places
		l = &Local{Name: d.Name, Type: t, Pos: d.P}
		c.scope.names[d.Name] = l
		c.scope.order = append(c.scope.order, l)
	} else {
		l = c.declare(d.Name, d.P, t, false)
	}
	l.CName = d.Name // C's own name
	l.CVar = true
	l.Pub = d.Pub
	l.Used = true
	c.addHeader(d.Header)
}

// cVarWhole refuses a C global struct used as a whole (only its fields).
func (c *checker) cVarWhole(e *ast.Ident, l *Local) bool {
	if !l.CVar || !isCStruct(l.Type) || c.anonOK {
		return false
	}
	c.errorf(e.P, "%s is a C global struct: read and write its fields (%s.field), not the whole struct", e.Name, e.Name)
	return true
}

// cConstDecl declares extern "C" const N T from "h.h": its value is the
// header's, known only when C compiles, so it isn't a compile-time constant
// for MLC (array sizes, patterns, other constants).
func (c *checker) cConstDecl(d *ast.ConstDecl) {
	t := c.resolveType(d.Type, ctxVar)
	if t != Invalid && !isNumeric(t) && t != Bool {
		c.errorf(d.Type.P, "a C constant is a number or bool (from a #define or an enum)")
		t = Invalid
	}
	l := c.declare(d.Name, d.P, t, false)
	l.IsConst = true
	l.CConst = d.Name
	l.Pub = d.Pub
	l.Used = true
	c.addHeader(d.Header)
}

// cConstUse records the use of a C constant (nil if l isn't one).
func (c *checker) cConstUse(e ast.Expr, l *Local) bool {
	if l == nil || l.CConst == "" {
		return false
	}
	c.info.CConsts[e] = l
	return true
}
