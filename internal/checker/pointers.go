package checker

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Pointers and borrows (docs/memory.md)
//
//   - *T: owning pointer, never nil. Whoever has a *T must free it (free)
//     or pass ownership along, on every path.
//   - ?*T: owning pointer that can be nil. To use the pointee, it must be
//     checked: inside "if q != nil { }", q is treated as *T.
//   - &T: borrow. It reads and writes the pointee,
//     but doesn't free or pass ownership. Created with &x.
//
// This file handles the types; the ownership rules are in ownership.go.

type Pointer struct {
	Elem     Type
	Nullable bool
}

func (p *Pointer) String() string {
	if p.Nullable {
		return "?*" + p.Elem.String()
	}
	return "*" + p.Elem.String()
}

type Borrow struct{ Elem Type }

func (b *Borrow) String() string { return "&" + b.Elem.String() }

// NilType is the type of the nil literal, which only fits in ?*T.
var NilType = &Basic{"nil"}

// pointerTo always returns the same *Pointer for the same type, so
// types can be compared with ==.
func (c *checker) pointerTo(elem Type, nullable bool) *Pointer {
	key := ptrKey{elem, nullable}
	if p, ok := c.pointers[key]; ok {
		return p
	}
	p := &Pointer{Elem: elem, Nullable: nullable}
	c.pointers[key] = p
	return p
}

type ptrKey struct {
	elem     Type
	nullable bool
}

func (c *checker) borrowOf(elem Type) *Borrow {
	if p, ok := elem.(*Pointer); ok {
		// &p, with p a pointer, borrows the pointed-to object: in a generic with
		// T = ?*X, &T is &X (there's no borrow of a pointer)
		elem = p.Elem
	}
	if b, ok := c.borrows[elem]; ok {
		return b
	}
	b := &Borrow{Elem: elem}
	c.borrows[elem] = b
	return b
}

// IsOwner says whether values of type t carry memory ownership: owning
// pointers and slices.
func IsOwner(t Type) bool {
	switch tt := t.(type) {
	case *Pointer, *Slice:
		return true
	case *Enum:
		return tt.Resource // an enum that holds owners is an owner by value
	}
	return false
}

// typeCtx says where a type appears, because not every type applies everywhere.
type typeCtx int

const (
	ctxVar        typeCtx = iota // local variable or constant
	ctxParam                     // parameter: accepts &T
	ctxResult                    // function result
	ctxField                     // struct field
	ctxElem                      // the T of *T
	ctxBorrowElem                // the T of &T (accepts slices)
	ctxSliceElem                 // the T of []T
	ctxArrayElem                 // the T of [N]T
	ctxTypeArg                   // type argument (Vec[T]): like ctxVar, but accepts opaque C types (the ref constraint decides)
)

func (c *checker) resolveType(t *ast.TypeExpr, ctx typeCtx) Type {
	for _, a := range t.Args {
		if a.Constraint != "" && !c.inReceiver {
			c.errorf(a.P, "a constraint (%s %s) can only appear in a method's receiver", a.Name, a.Constraint)
		}
	}
	switch t.Mod {
	case ast.TypeFunc:
		return c.resolveFuncType(t)
	case ast.TypeSlice:
		return c.resolveSlice(t, ctx)
	case ast.TypeArray:
		return c.resolveArray(t, ctx)
	case ast.TypeOptBorrow:
		if ctx != ctxParam || !c.inExternC {
			c.errorf(t.P, "?&T only exists in parameters of extern \"C\" fn (a pointer C accepts as NULL); in an MLC function, use ?*T or two functions")
			return Invalid
		}
		inner := c.resolveType(&ast.TypeExpr{Mod: ast.TypeBorrow, Elem: t.Elem, P: t.P}, ctxParam)
		if inner == Invalid {
			return Invalid
		}
		return c.optArgType(inner)
	case ast.TypePointer, ast.TypeNullPtr, ast.TypeBorrow:
		if t.Mod == ast.TypeBorrow && t.Elem != nil && t.Elem.Mod == ast.TypeNullPtr && ctx == ctxParam {
			if c.inExternC {
				return c.outHandleType(t) // &?*X: out parameter of a C function
			}
			c.errorf(t.P, "&?*T only exists in parameters of extern \"C\" fn (a handle returned by the C function); in an MLC function, return the ?*T")
			return Invalid
		}
		switch {
		case ctx == ctxArrayElem:
			c.errorf(t.P, "arrays of pointers aren't supported yet: the elements can't be owners or borrows")
			return Invalid
		case ctx == ctxSliceElem && t.Mod == ast.TypePointer:
			c.errorf(t.P, "slice elements start zeroed (nil), so they can't be *T: use []?*T")
			return Invalid
		case ctx == ctxSliceElem && t.Mod == ast.TypeBorrow:
			c.errorf(t.P, "&T can only be a parameter type: a borrow can't be stored in a slice")
			return Invalid
		case ctx == ctxElem || ctx == ctxBorrowElem:
			c.errorf(t.P, "pointer to pointer isn't supported")
			return Invalid
		case t.Mod == ast.TypeBorrow && ctx != ctxParam && ctx != ctxResult && ctx != ctxVar && ctx != ctxTypeArg:
			c.errorf(t.P, "a borrow (&T) can't be stored in fields, slices or arrays: only in parameters, variables and return values")
			return Invalid
		}
		elemCtx := ctxElem
		if t.Mod == ast.TypeBorrow {
			elemCtx = ctxBorrowElem
		}
		elem := c.resolveType(t.Elem, elemCtx)
		if elem == Invalid {
			return Invalid
		}
		if isGuard(elem) && (ctx == ctxField || ctx == ctxSliceElem) {
			c.errorf(t.P, "a guard belongs to the thread that locked the mutex: it can't be in fields or slices (which can go to another thread)")
			return Invalid
		}
		if t.Mod == ast.TypeBorrow {
			return c.borrowOf(elem)
		}
		return c.pointerTo(elem, t.Mod == ast.TypeNullPtr)
	}
	var s *Struct
	switch {
	case t.Module != "" && t.Name == "Thread" && c.mod.uses[t.Module] != nil && c.mod.uses[t.Module].path == "std/thread" && c.mod.uses[t.Module].std:
		th := c.threadTypeExpr(t)
		if th != Invalid && ctx != ctxElem && ctx != ctxBorrowElem {
			c.errorf(t.P, "%s is a handle: it only exists behind a pointer (*%s, ?*%s)", th, th, th)
			return Invalid
		}
		return th
	case t.Module != "" && (t.Name == "Mutex" || t.Name == "Guard") && c.mod.uses[t.Module] != nil && c.mod.uses[t.Module].path == "std/mutex" && c.mod.uses[t.Module].std:
		mt := c.mutexTypeExpr(t)
		if mt != Invalid && ctx != ctxElem && ctx != ctxBorrowElem {
			c.errorf(t.P, "%s only exists behind a pointer (*%s, ?*%s)", mt, mt, mt)
			return Invalid
		}
		return mt
	case t.Module != "" && (t.Name == "Sender" || t.Name == "Receiver") && c.mod.uses[t.Module] != nil && c.mod.uses[t.Module].path == "std/chan" && c.mod.uses[t.Module].std:
		ch := c.chanTypeExpr(t)
		if ch != Invalid && ctx != ctxElem && ctx != ctxBorrowElem {
			c.errorf(t.P, "%s is a channel end: it only exists behind a pointer (*%s, ?*%s)", ch, ch, ch)
			return Invalid
		}
		return ch
	case t.Module != "":
		// a type from another file: geometry.Point
		if m := c.mod.uses[t.Module]; m != nil && m.ifaces[t.Name] != nil {
			c.errorf(t.P, "%s.%s is an interface: in this version, interfaces only serve as generic constraints, like fn f[T %s.%s](x &T)", t.Module, t.Name, t.Module, t.Name)
			return Invalid
		}
		if m := c.mod.uses[t.Module]; m != nil && m.enums[t.Name] != nil {
			en := m.enums[t.Name]
			if !en.Pub {
				c.errorf(t.P, "%s.%s isn't public (declare it with pub enum)", t.Module, t.Name)
				return Invalid
			}
			return c.resolveEnum(en, t)
		}
		st, ok := c.moduleStruct(t.Module, t.Name, t.P).(*Struct)
		if !ok {
			return Invalid
		}
		s = st
	case c.typeParams[t.Name] != nil:
		if len(t.Args) > 0 {
			c.errorf(t.P, "%s is a type parameter and takes no arguments", t.Name)
		}
		if tv, ok := c.typeParams[t.Name].(*TypeVar); ok && tv.Constraint == "ref" && ctx != ctxBorrowElem && ctx != ctxTypeArg {
			c.errorf(t.P, "%s has the ref constraint and can only appear borrowed (&%s): the type may have owners and not be copyable", t.Name, t.Name)
			return Invalid
		}
		return c.typeParams[t.Name]
	case basicTypes[t.Name] != nil:
		if len(t.Args) > 0 {
			c.errorf(t.P, "%s isn't generic", t.Name)
		}
		return basicTypes[t.Name]
	case c.mod.structs[t.Name] != nil:
		s = c.mod.structs[t.Name]
	case c.mod.enums[t.Name] != nil:
		return c.resolveEnum(c.mod.enums[t.Name], t)
	case c.mod.ifaces[t.Name] != nil:
		c.errorf(t.P, "%s is an interface: in this version, interfaces only serve as generic constraints, like fn f[T %s](x &T)", t.Name, t.Name)
		return Invalid
	default:
		c.errorf(t.P, "unknown type %s", t.Name)
		return Invalid
	}
	switch {
	case s.Params != nil && len(t.Args) == 0:
		c.errorf(t.P, "%s is generic: write the type arguments, like %s[int]", s.Name, s.Name)
		return Invalid
	case s.Params == nil && len(t.Args) > 0:
		c.errorf(t.P, "%s isn't generic", s.Name)
		return Invalid
	case s.Params != nil:
		var args []Type
		for _, a := range t.Args {
			args = append(args, c.resolveType(a, ctxTypeArg))
		}
		inst, ok := c.instStruct(s, args, t.P).(*Struct)
		if !ok {
			return Invalid
		}
		s = inst
	}
	if s.Extern && ctx != ctxElem && ctx != ctxBorrowElem && ctx != ctxTypeArg {
		c.errorf(t.P, "%s is an opaque C type: it only exists behind a pointer (?*%s, *%s or &%s)", s.Name, s.Name, s.Name, s.Name)
		return Invalid
	}
	if s.Resource && (ctx == ctxVar || ctx == ctxTypeArg || ctx == ctxParam || ctx == ctxResult) {
		c.errorf(t.P, "%s contains owning pointers and can only be used through a pointer: use *%s, ?*%s or &%s", s.Name, s.Name, s.Name, s.Name)
		return Invalid
	}
	return s
}

// assignable checks whether a value of type got can go to a place of type want.
// Besides equal types: nil fits in ?*T, and *T fits in ?*T.
func (c *checker) assignable(got, want Type, pos token.Pos, context string) {
	if got == Invalid || want == Invalid || got == want {
		return
	}
	wp, wantPtr := want.(*Pointer)
	gp, gotPtr := got.(*Pointer)
	switch {
	case got == NilType && wantPtr && wp.Nullable:
		return
	case got == NilType && isSlice(want):
		c.errorf(pos, "%s: nil isn't a slice; the zero value of %s is already the empty slice", context, want)
		return
	case got == NilType && wantPtr:
		c.errorf(pos, "%s: nil only fits in ?*%s, not in %s (a *T pointer is never nil)", context, wp.Elem, want)
		return
	case gotPtr && wantPtr && gp.Elem == wp.Elem && !gp.Nullable && wp.Nullable:
		return
	case gotPtr && wantPtr && gp.Elem == wp.Elem && gp.Nullable:
		// ?*T where *T is expected: the ownership checker requires the value
		// not to be possibly nil at that point.
		return
	}
	c.errorf(pos, "%s: expected %s, found %s", context, want, got)
}

func isNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// nilComparison checks x == nil and x != nil. Only ?*T can be nil.
func (c *checker) nilComparison(e *ast.BinaryExpr, x, y Type) Type {
	other := x
	if x == NilType {
		other = y
	}
	p, isPtr := other.(*Pointer)
	switch {
	case other == NilType:
		c.errorf(e.OpPos, "comparison between nil and nil")
	case isPtr && p.Nullable:
		return Bool
	case isPtr || isBorrow(other):
		c.errorf(e.OpPos, "a value of type %s is never nil", other)
	default:
		c.errorf(e.OpPos, "comparison between different types: %s and %s", x, y)
	}
	return Invalid
}

func isBorrow(t Type) bool {
	_, ok := t.(*Borrow)
	return ok
}

// deref returns the pointee's type of a pointer or borrow of type t,
// for *p and for p.field. For ?*T, whether the pointer can be nil at that point
// is decided by the ownership checker, which follows the flow.
func (c *checker) deref(t Type, pos token.Pos, what string) (Type, bool) {
	switch t := t.(type) {
	case *Pointer:
		return t.Elem, true
	case *Borrow:
		return t.Elem, true
	}
	return t, false
}

// builtinCall checks alloc and free. ok is false if the call isn't one of them.
func (c *checker) builtinCall(e *ast.CallExpr, name string) (Type, bool) {
	switch name {
	case "len":
		return c.lenCall(e), true
	case "realloc":
		return c.reallocCall(e), true
	case "take":
		return c.takeCall(e), true
	case "replace":
		return c.replaceCall(e), true
	case "drain":
		c.errorf(e.Lparen, "drain can only be used in for x in drain(s)")
		c.checkArgs(e)
		return Invalid, true
	case "drop":
		return c.dropCall(e), true
	case "alloc":
		if len(e.Args) > 0 {
			if ta, ok := e.Args[0].(*ast.TypeArg); ok {
				return c.allocSlice(e, ta), true
			}
		}
		if len(e.Args) != 1 {
			c.errorf(e.Lparen, "alloc takes one argument: a type (alloc(Point)) or an initial value (alloc(Point{x: 1, y: 2}))")
			c.checkArgs(e)
			return Invalid, true
		}
		elem, zero := c.allocArg(e.Args[0])
		if elem == Invalid {
			return Invalid, true
		}
		if isThread(elem) {
			c.errorf(e.Args[0].Pos(), "%s comes from thread.spawn, not from alloc", elem)
			return Invalid, true
		}
		if isExternStruct(elem) {
			c.errorf(e.Args[0].Pos(), "%s is an opaque C type: the values come from C functions, not from alloc", elem)
			return Invalid, true
		}
		c.info.Calls[e] = Callee{Builtin: Alloc, Elem: elem, Zero: zero}
		return c.pointerTo(elem, false), true
	case "free":
		if len(e.Args) != 1 {
			c.errorf(e.Lparen, "free takes one argument")
			c.checkArgs(e)
			return Invalid, true
		}
		switch t := c.value(e.Args[0]); tt := t.(type) {
		case *Pointer:
			if isThread(tt) {
				c.errorf(e.Args[0].Pos(), "%s is a thread: wait for it with thread.join, not with free", t)
			} else if isChanEnd(tt) {
				c.errorf(e.Args[0].Pos(), "%s is a channel end: close it with close(), not with free", t)
			} else if isGuard(tt) {
				c.errorf(e.Args[0].Pos(), "%s is a guard: unlock it with unlock(), not with free", t)
			} else if isMutexType(tt) {
				c.errorf(e.Args[0].Pos(), "%s is a mutex: close it with close(), not with free", t)
			} else if isExternStruct(tt.Elem) {
				c.errorf(e.Args[0].Pos(), "%s is a C handle: release it with the C function that closes it, not with free", t)
			}
		case *Slice:
		case *Enum:
			if !tt.Resource {
				c.errorf(e.Args[0].Pos(), "free expects an owner; %s holds no owners", t)
			} else {
				c.dropPlan(t, e.Args[0].Pos()) // frees the data of whichever variant is there
			}
		case *Borrow:
			c.errorf(e.Args[0].Pos(), "a borrow (%s) can't be freed: only the owner frees", t)
		default:
			if t != Invalid {
				c.errorf(e.Args[0].Pos(), "free expects an owning pointer, found %s", t)
			}
		}
		c.info.Calls[e] = Callee{Builtin: Free}
		return Void, true
	}
	return nil, false
}

// allocArg resolves alloc's argument: a type name (zeroed memory) or
// an initial value.
func (c *checker) allocArg(a ast.Expr) (Type, bool) {
	// standard library handles: thread.Thread[R], chan.Sender[T], ...
	if base, _ := splitIndex(a); base != nil {
		if sel, ok := base.(*ast.SelectorExpr); ok {
			if m := c.userModule(sel.X); m != nil && m.std {
				switch {
				case m.path == "std/thread" && sel.Sel == "Thread":
					c.errorf(a.Pos(), "thread.Thread comes from thread.spawn, not from alloc")
					return Invalid, false
				case m.path == "std/chan" && (sel.Sel == "Sender" || sel.Sel == "Receiver"):
					c.errorf(a.Pos(), "channel ends come from chan.new, sender() and clone(), not from alloc")
					return Invalid, false
				case m.path == "std/mutex" && (sel.Sel == "Mutex" || sel.Sel == "Guard"):
					c.errorf(a.Pos(), "a mutex comes from mutex.new (and a guard, from lock()), not from alloc")
					return Invalid, false
				}
			}
		}
	}
	switch a.(type) {
	case *ast.IndexExpr, *ast.IndexListExpr:
		// alloc(Pair[int, string]): generic struct, zeroed memory
		if base, _ := splitIndex(a); c.namesType(base) {
			t := c.exprType(a)
			if s, ok := t.(*Struct); ok {
				if field, ok := hasNonNullOwner(s); ok {
					c.errorf(a.Pos(), "alloc(%s) zeroes the memory, but %s is a *T pointer and can't be nil; use alloc(%s{...}) with every field", s.Name, field, s.Name)
					return Invalid, false
				}
				return s, true
			}
			return Invalid, false
		}
	}
	if sel, ok := a.(*ast.SelectorExpr); ok && c.userModule(sel.X) != nil {
		// alloc(geometry.Point): a struct from another file, zeroed memory
		m := c.userModule(sel.X)
		if s := m.structs[sel.Sel]; s != nil {
			if !s.Pub {
				c.errorf(sel.SelPos, "%s.%s isn't public (declare it with pub struct)", m.name, sel.Sel)
				return Invalid, false
			}
			if field, ok := hasNonNullOwner(s); ok {
				c.errorf(a.Pos(), "alloc(%s) zeroes the memory, but %s is a *T pointer and can't be nil; use alloc(%s{...}) with every field", s.Name, field, s.Name)
				return Invalid, false
			}
			return s, true
		}
	}
	if id, ok := a.(*ast.Ident); ok && c.scope.lookup(id.Name) == nil {
		if t, ok := basicTypes[id.Name]; ok {
			return t, true
		}
		if s, ok := c.mod.structs[id.Name]; ok {
			if field, ok := hasNonNullOwner(s); ok {
				c.errorf(a.Pos(), "alloc(%s) zeroes the memory, but %s is a *T pointer and can't be nil; use alloc(%s{...}) with every field", s.Name, field, s.Name)
				return Invalid, false
			}
			return s, true
		}
	}
	if _, isLit := a.(*ast.CompositeLit); isLit {
		c.litOK = true // alloc(Node{...}): the literal goes straight to the new memory
	}
	t := c.valueDefault(a)
	if _, isLit := a.(*ast.CompositeLit); !isLit {
		c.noCopy(t, a.Pos())
	}
	switch t.(type) {
	case *Pointer, *Borrow:
		c.errorf(a.Pos(), "pointer to pointer isn't supported")
		return Invalid, false
	}
	if isArray(t) {
		c.errorf(a.Pos(), "pointer to array isn't supported: use the array directly, or alloc([]T, n)")
		return Invalid, false
	}
	if t == NilType {
		c.errorf(a.Pos(), "alloc(nil) makes no sense: pass a type or a value")
		return Invalid, false
	}
	return t, false
}

// borrowArg checks an argument passed to a &T parameter: "&x" or a
// received borrow, passed along as is.
func (c *checker) borrowArg(a ast.Expr, want *Borrow, context string) {
	u, ok := a.(*ast.UnaryExpr)
	if !ok || u.Op != token.AMP {
		t := c.value(a)
		if t == Invalid || t == want {
			return
		}
		if IsOwner(t) || t == want.Elem {
			c.errorf(a.Pos(), "%s: expected %s; to borrow, write &%s", context, want, exprName(a))
			return
		}
		c.errorf(a.Pos(), "%s: expected %s, found %s", context, want, t)
		return
	}
	if sl, isSub := u.X.(*ast.SliceExpr); isSub {
		// &s[a:b] borrows part of a slice
		got := c.subSlice(sl)
		c.info.Types[a] = got
		c.assignable(got, want, a.Pos(), context)
		return
	}
	t := c.borrowPlace(u.X)
	if t == Invalid {
		c.info.Types[a] = Invalid
		return
	}
	if arr, ok := t.(*Array); ok {
		if s, isSl := want.Elem.(*Slice); isSl && s.Elem == arr.Elem {
			// &a, with a [N]T, borrows the array as &[]T
			c.info.Types[a] = want
			return
		}
	}
	got := c.borrowOf(t)
	c.info.Types[a] = got
	c.assignable(got, want, a.Pos(), context)
}

// borrowPlace checks the x of &x and returns the borrowed type. &p, with p *T,
// borrows p's pointee; &s, with s a local struct, borrows s.
func (c *checker) borrowPlace(x ast.Expr) Type {
	if !isPlace(x) {
		c.errorf(x.Pos(), "only a variable, a field or a pointee can be borrowed (&x, &x.field, &*p)")
		c.value(x)
		return Invalid
	}
	if id, ok := x.(*ast.Ident); ok {
		if l := c.scope.lookup(id.Name); l != nil && l.IsConst {
			c.errorf(x.Pos(), "the constant %s can't be borrowed", id.Name)
			return Invalid
		}
	}
	t := c.value(x)
	switch t := t.(type) {
	case *Pointer:
		return t.Elem // if it may be nil, the ownership checker reports it
	case *Borrow:
		c.errorf(x.Pos(), "%s is already a borrow: pass %s directly, without &", exprName(x), exprName(x))
		return Invalid
	}
	if t == NilType {
		c.errorf(x.Pos(), "nil can't be borrowed")
		return Invalid
	}
	return t
}

// isPlace says whether e designates a place in memory: a variable, a field or *p.
func isPlace(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name != "nil" && e.Name != "true" && e.Name != "false"
	case *ast.SelectorExpr:
		return isPlace(e.X)
	case *ast.UnaryExpr:
		return e.Op == token.MUL
	case *ast.IndexExpr:
		return isPlace(e.X)
	}
	return false
}

func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel
	case *ast.UnaryExpr:
		return fmt.Sprintf("%s%s", e.Op, exprName(e.X))
	}
	return "x"
}

func isPointerType(t Type) bool {
	_, ok := t.(*Pointer)
	return ok || isBorrow(t)
}

// markUsed marks as used the variable at the root of e (p in p.a.b).
func (c *checker) markUsed(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Ident:
		if l := c.info.Locals[e]; l != nil {
			l.Used = true
		}
	case *ast.SelectorExpr:
		c.markUsed(e.X)
	}
}
