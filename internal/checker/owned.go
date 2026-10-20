package checker

import (
	"mlc/internal/ast"
	"mlc/internal/token"
)

// Owners in generics
//
//	struct Vec[T owned] { items []T; count int }
//	fn (v &Vec[T]) push(x T) { ... }        // x is moved inside
//	fn (v &Vec[T any]) get(i int) T { ... } // only when T is copyable
//
// The owned constraint accepts types without owners and simple owners: ?*X and []X,
// with X without owners inside. Inside the body, a value of type T is treated as
// an owner: assigning, passing or returning moves; using after moving is an error;
// every value needs a destination (move, store or drop(x)). drop(x) frees x
// if it's an owner (free) and does nothing otherwise.
//
// Ownership is checked in the concrete instances, as in every generic. So that
// errors show up already in the definition, each generic with an owned parameter
// gets a checking instance with T = []byte (an owner with a zero value),
// which goes through the ownership checker; its errors say "checked with an owning
// T".

// ownedVar says whether t is a type parameter with the owned constraint.
func ownedVar(t Type) bool {
	tv, ok := t.(*TypeVar)
	return ok && tv.Constraint == "owned"
}

// ownedOK says whether t can be the argument of an owned parameter, and why not.
// Types without owners are valid, and so are owners that have a zero value and know how to free themselves:
// ?*X (X without owners, or with a method fn (x *X) free()) and []E, with each
// element E also like that (nested owners: [][]byte, []?*Node). The existence
// of free is checked later (requireFree), when the methods already exist.
func ownedOK(t Type) (bool, string) {
	switch t := t.(type) {
	case *Pointer:
		if !t.Nullable {
			return false, "*T has no zero value and collections need empty slots: use ?" + t.String()
		}
		return ownedPointee(t.Elem)
	case *Slice:
		return ownedElem(t.Elem)
	case *Borrow:
		return false, "borrows can't be type arguments"
	}
	if isResource(t) {
		return false, "a struct with owners can't be copied: use ?*" + t.String()
	}
	if t == Void || t == NilType || t == Invalid || isUntyped(t) {
		return false, ""
	}
	return true, ""
}

// ownedPointee checks the type pointed to by a ?*X used as an owned T.
func ownedPointee(t Type) (bool, string) {
	if isThread(t) {
		return false, "a thread needs thread.join, and drop doesn't know how to wait for it"
	}
	if isChanEnd(t) {
		return true, "" // drop closes the end
	}
	if isGuard(t) {
		return false, "a guard needs unlock() on the thread that locked, and drop doesn't know that"
	}
	if isMutexType(t) {
		return true, "" // drop closes the mutex's handle
	}
	if isExternStruct(t) {
		return false, "a C handle needs the C function that closes it, and drop doesn't know which one"
	}
	if IsOwner(t) {
		return false, "pointer to owner isn't supported"
	}
	return true, ""
}

// ownedElem checks the element of a []E used as an owned T: it must have a
// zero value (the new positions come zeroed) and know how to free itself.
func ownedElem(t Type) (bool, string) {
	switch t := t.(type) {
	case *Pointer:
		if !t.Nullable {
			return false, "slice elements start zeroed: use []?" + t.String()
		}
		return ownedPointee(t.Elem)
	case *Slice:
		return ownedElem(t.Elem)
	}
	if isResource(t) {
		return false, "structs with owners only exist behind a pointer: use []?*" + t.String()
	}
	return true, ""
}

// freeNeed is a struct with owners that showed up inside an owned T: it
// needs a method fn (x *X) free() (checked after collection).
type freeNeed struct {
	s   *Struct
	pos token.Pos
	mod *module
	in  string // where it showed up, for the message
}

// requireFree notes the structs with owners reached by t (as ?*X or
// inside []?*X), which need to know how to free themselves.
func (c *checker) requireFree(t Type, pos token.Pos, in string) {
	switch t := t.(type) {
	case *Pointer:
		if s, ok := t.Elem.(*Struct); ok && s.Resource {
			c.freeNeeds = append(c.freeNeeds, freeNeed{s, pos, c.mod, in})
		}
	case *Slice:
		c.requireFree(t.Elem, pos, in)
	}
}

// checkFreeNeeds checks, with every method collected, that each struct
// with owners used inside an owned T has the free method.
func (c *checker) checkFreeNeeds() {
	saved := c.mod
	for _, n := range c.freeNeeds {
		if c.freeMethod(n.s) == nil {
			if c.freeMissing[n.s] {
				continue // one error per struct is enough
			}
			c.freeMissing[n.s] = true
			c.mod = n.mod
			c.errorf(n.pos, "%s has owning fields and doesn't know how to free itself inside %s: declare fn (x *%s) free() freeing the fields and x itself", n.s, n.in, n.s.Name)
		}
	}
	c.mod = saved
	c.freeNeeds = nil
}

// freeMethod returns s's method fn (x *X) free() (instantiating it, in a
// generic struct), or nil.
func (c *checker) freeMethod(s *Struct) *Func {
	m := s.Methods["free"]
	if m == nil && s.Origin != nil {
		if d := s.Origin.methodDecls["free"]; d != nil {
			m = c.instMethod(s, "free")
		}
	}
	if m == nil || m.recv != recvOwner || len(m.Params) != 1 || m.Result != Void {
		return nil
	}
	return m
}

// dropPlan records what drop(x) does with a concrete value of type t: nothing
// (no owner), free (simple owner), x.free() (a struct that knows how to free itself) or
// freeing each element and then the slice. The types with a non-trivial drop go
// to the generator, which writes an mlc_drop_ function for each one.
func (c *checker) dropPlan(t Type, pos token.Pos) {
	if abstract(t) || !IsOwner(t) || c.info.DropTypes[typeKey(t)] != nil {
		return
	}
	switch tt := t.(type) {
	case *Pointer:
		if s, ok := tt.Elem.(*Struct); ok && s.Resource {
			m := c.freeMethod(s)
			if m == nil {
				if !c.freeMissing[s] { // otherwise, the error already came out where the type was used
					c.errorf(pos, "drop of %s: %s has owning fields and needs fn (x *%s) free()", t, s, s.Name)
				}
				return
			}
			c.info.DropFree[s] = m
		}
	case *Slice:
		c.dropPlan(tt.Elem, pos)
	case *Enum:
		for _, v := range tt.Variants {
			for _, f := range v.Fields {
				c.dropPlan(f.Type, pos)
			}
		}
	}
	c.info.DropTypes[typeKey(t)] = t
	c.info.DropOrder = append(c.info.DropOrder, t)
}

// deepDrop says whether drop of a value of type t frees other owners besides the
// value itself (so it's a move into the drop function, not a free).
func deepDrop(t Type) bool {
	switch tt := t.(type) {
	case *Pointer:
		return isResource(tt.Elem)
	case *Slice:
		return IsOwner(tt.Elem)
	case *Enum:
		return tt.Resource
	}
	return false
}

// hasOwnerFields says whether t is a struct with owning fields.
func hasOwnerFields(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.Resource
}

// genericContext says whether the checked code is inside a generic (in the
// definition or in an instance): there, take, replace, drain and drop also apply
// to elements that aren't owners (T can be int in an instance).
func (c *checker) genericContext() bool { return c.typeParams != nil }

// probeArgs picks the types of the ownership-checking instance: []byte
// for the owned parameters and int for the others. Returns nil if there's no
// owned parameter, or if some parameter asks for an interface (there's no way to
// pick a type that implements it).
func (c *checker) probeArgs(params []*ast.TypeParam) []Type {
	hasOwned := false
	var args []Type
	for _, p := range params {
		builtin, _, _ := splitConstraint(p.Constraint)
		candidates := []Type{Int}
		if builtin == "owned" {
			hasOwned = true
			candidates = []Type{c.sliceOf(Byte), Int}
		}
		var pick Type
		for _, cand := range candidates {
			if c.satisfiesParam(cand, p) {
				pick = cand
				break
			}
		}
		if pick == nil {
			return nil // no simple type satisfies it (an interface without a known impl)
		}
		args = append(args, pick)
	}
	if !hasOwned {
		return nil
	}
	return args
}

// probeOwned creates the checking instances of the generics with owned
// parameters declared in the file.
func (c *checker) probeOwned(f *ast.File) {
	for _, d := range f.Funcs {
		if d.Recv != nil || len(d.TypeParams) == 0 {
			continue
		}
		g := c.mod.generics[d.Name]
		args := c.probeArgs(d.TypeParams)
		if g == nil || args == nil {
			continue
		}
		if inst := c.instFunc(g, args, d.P); inst != nil {
			c.probes[inst] = true
		}
	}
	for _, sd := range f.Structs {
		s := c.mod.structs[sd.Name]
		if s == nil || s.Params == nil {
			continue
		}
		args := c.probeArgs(s.Params)
		if args == nil {
			continue
		}
		inst, ok := c.instStruct(s, args, sd.P).(*Struct)
		if !ok {
			continue
		}
		for name := range s.methodDecls {
			if msg := c.methodConstraintError(inst, name); msg != "" {
				continue // a method only for a copyable T: not valid with an owning T
			}
			if m := c.instMethod(inst, name); m != nil {
				c.probes[m] = true
			}
		}
	}
}

// methodConstraintError explains why the method name doesn't exist in the
// instance s (the receiver asks for a constraint the arguments don't satisfy),
// or returns "".
func (c *checker) methodConstraintError(s *Struct, name string) string {
	if s.Origin == nil {
		return ""
	}
	cons := s.Origin.methodConstraints[name]
	for i, p := range cons {
		if p == nil || i >= len(s.Args) {
			continue
		}
		if !c.satisfiesParam(s.Args[i], p) {
			reason := ""
			if IsOwner(s.Args[i]) {
				reason = " (" + s.Args[i].String() + " is an owner, and the method copies): use the methods that move ownership, like take or put"
			}
			return s.Name + "." + name + " requires " + p.Name + " " + p.Constraint + reason
		}
	}
	return ""
}

// dropCall checks drop(x): x is consumed; if it's an owner, it's freed.
func (c *checker) dropCall(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "drop takes a value: drop(x)")
		c.checkArgs(e)
		return Void
	}
	t := c.value(e.Args[0])
	switch {
	case t == Invalid:
	case !c.genericContext():
		c.errorf(e.Lparen, "drop only exists in generic code, for an owned T; outside it, use free")
	case isBorrow(t):
		c.errorf(e.Args[0].Pos(), "drop takes the owner, not a borrow (%s)", t)
	default:
		c.dropPlan(t, e.Args[0].Pos())
	}
	c.info.Calls[e] = Callee{Builtin: Drop}
	return Void
}
