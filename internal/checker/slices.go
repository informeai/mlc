package checker

import (
	"math/big"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Slices (Milestone 6)
//
// []T owns the memory of its elements, like *T: created with alloc([]T, n),
// with n zeroed elements, and freed with free(s). The zero value (var s []T)
// is the empty slice, which doesn't need free. s[i] has bounds checking at
// run time; len(s) gives the length; for x in s walks copies of the
// elements. &[]T borrows the slice: reads and writes elements, without freeing.
//
// In this version the elements can't be owners ([]*Node, [][]int): ownership
// per element would require tracking indices.

type Slice struct{ Elem Type }

func (s *Slice) String() string { return "[]" + s.Elem.String() }

func (c *checker) sliceOf(elem Type) *Slice {
	if s, ok := c.slices[elem]; ok {
		return s
	}
	s := &Slice{Elem: elem}
	c.slices[elem] = s
	if !abstract(elem) { // the generic definition's []T doesn't go to C
		c.info.Slices = append(c.info.Slices, s)
	}
	return s
}

func isSlice(t Type) bool {
	_, ok := t.(*Slice)
	return ok
}

// sliceElem returns the element type of a slice or of a slice
// borrow.
func sliceElem(t Type) (Type, bool) {
	if b, ok := t.(*Borrow); ok {
		t = b.Elem
	}
	if s, ok := t.(*Slice); ok {
		return s.Elem, true
	}
	return nil, false
}

func (c *checker) resolveSlice(t *ast.TypeExpr, ctx typeCtx) Type {
	switch ctx {
	case ctxElem:
		c.errorf(t.P, "pointer to slice isn't supported: the slice already owns the memory (use []T, or &[]T to borrow)")
		return Invalid
	}
	elem := c.resolveType(t.Elem, ctxSliceElem)
	if elem == Invalid {
		return Invalid
	}
	if ctx == ctxSliceElem && IsOwner(elem) {
		c.errorf(t.P, "slices of slices of owners aren't supported yet ([][]T only with a simple T)")
		return Invalid
	}
	if isResource(elem) {
		c.errorf(t.Elem.P, "slices of %s aren't supported yet: %s contains owning pointers, and the elements can't be owners", elem, elem)
		return Invalid
	}
	return c.sliceOf(elem)
}

// allocSlice checks alloc([]T, n).
func (c *checker) allocSlice(e *ast.CallExpr, ta *ast.TypeArg) Type {
	t := c.resolveType(ta.Type, ctxVar)
	if len(e.Args) != 2 {
		c.errorf(e.Lparen, "alloc of a slice needs the size: alloc(%s, n)", t)
		return Invalid
	}
	c.intValue(e.Args[1], "the size in alloc")
	if t == Invalid {
		return Invalid
	}
	if !isSlice(t) {
		c.errorf(ta.Type.P, "alloc(T, n) is for slices; for %s use alloc(%s)", t, t)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: Alloc, Elem: t, Slice: true}
	return t
}

// lenCall checks len(x), for slices and strings.
func (c *checker) lenCall(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "len takes one argument")
		c.checkArgs(e)
		return Invalid
	}
	t := c.value(e.Args[0])
	if a, ok := arrayElem(t); ok {
		c.info.Calls[e] = Callee{Builtin: Len}
		if isPath(e.Args[0]) {
			c.info.Values[e] = big.NewInt(a.Len) // known at compile time
		}
		return Int
	}
	if _, ok := sliceElem(t); !ok && t != String && t != Invalid {
		c.errorf(e.Args[0].Pos(), "len applies to slices, arrays and strings, not to %s", t)
	}
	c.info.Calls[e] = Callee{Builtin: Len}
	return Int
}

// index checks s[i] when read: a slice element, or a byte of a string (int).
func (c *checker) index(e *ast.IndexExpr) Type {
	bt := c.value(e.X)
	c.indexValue(e.Index)
	if a, ok := arrayElem(bt); ok {
		c.checkArrayIndex(a, e.Index)
		return a.Elem
	}
	if elem, ok := sliceElem(bt); ok {
		return elem
	}
	switch bt {
	case String:
		return Byte
	case Invalid:
		return Invalid
	}
	c.errorf(e.Lbrack, "a value of type %s can't be indexed", bt)
	return Invalid
}

func (c *checker) indexValue(i ast.Expr) {
	c.intValue(i, "the index")
}

// targetIndex checks s[i] as an assignment target.
func (c *checker) targetIndex(e *ast.IndexExpr) Type {
	bt := c.value(e.X)
	c.indexValue(e.Index)
	if a, ok := arrayElem(bt); ok {
		c.checkArrayIndex(a, e.Index)
		return a.Elem
	}
	if elem, ok := sliceElem(bt); ok {
		return elem
	}
	switch bt {
	case String:
		c.errorf(e.Lbrack, "strings can't be changed")
	case Invalid:
	default:
		c.errorf(e.Lbrack, "a value of type %s can't be indexed", bt)
	}
	return Invalid
}

// checkForIn checks "for x in s { }": s is a slice variable or field, and x
// receives a copy of each element. The name _ discards the element.
func (c *checker) checkForIn(s *ast.ForStmt) {
	c.pushScope()
	iter := s.Iter
	drain := false
	if call, ok := iter.(*ast.CallExpr); ok {
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "drain" && c.scope.lookup("drain") == nil {
			drain = true
			if len(call.Args) != 1 {
				c.errorf(call.Lparen, "drain takes a slice: for x in drain(s)")
				c.checkArgs(call)
				iter = nil
			} else {
				iter = call.Args[0]
				c.info.Drains[s] = iter
			}
		}
	}
	t, elem, ok := Type(Invalid), Type(Invalid), false
	if iter != nil {
		t = c.value(iter)
		elem, ok = sliceElem(t)
		if a, isArr := arrayElem(t); isArr {
			elem, ok = a.Elem, true
			if drain {
				c.errorf(iter.Pos(), "drain is for slices of owners; to walk %s, use for x in a", t)
			}
		}
	}
	switch {
	case t == Invalid:
		elem = Invalid
	case !ok:
		c.errorf(iter.Pos(), "for in walks slices, found %s", t)
		elem = Invalid
	case !isPath(iter):
		c.errorf(iter.Pos(), "for in walks a variable or field: store the slice in a variable first")
	case drain && s.Var.Name == "_":
		c.errorf(s.Var.P, "the element taken out by drain needs a name, to be freed or moved")
	case drain && !IsOwner(elem) && !c.genericContext():
		c.errorf(iter.Pos(), "drain is for slices of owners; to walk %s, use for x in s", t)
	case !drain && (IsOwner(elem) || ownedVar(elem)):
		c.errorf(iter.Pos(), "the elements of %s are owners and can't be copied: use for x in drain(s) to empty the slice", t)
	}
	if s.Var.Name != "_" {
		c.info.ForVars[s] = c.declare(s.Var.Name, s.Var.P, elem, false)
	}
	c.loops = append(c.loops, s)
	c.checkBlock(s.Body)
	c.loops = c.loops[:len(c.loops)-1]
	c.popScope()
}

func (c *checker) noSliceCompare(e *ast.BinaryExpr, t Type) bool {
	if isSlice(t) {
		c.errorf(e.OpPos, "slices can't be compared with ==; compare the elements")
		return true
	}
	return false
}

// reallocCall checks realloc(s, n): consumes the slice s and returns a new slice
// with n elements. The elements that already existed are copied; the new ones come
// zeroed.
func (c *checker) reallocCall(e *ast.CallExpr) Type {
	if len(e.Args) != 2 {
		c.errorf(e.Lparen, "realloc takes the slice and the new size: s = realloc(s, n)")
		c.checkArgs(e)
		return Invalid
	}
	t := c.value(e.Args[0])
	c.intValue(e.Args[1], "the size in realloc")
	switch {
	case t == Invalid:
		return Invalid
	case isBorrow(t):
		c.errorf(e.Args[0].Pos(), "realloc needs the slice's owner: a borrow (%s) can't be reallocated", t)
		return Invalid
	case !isSlice(t):
		c.errorf(e.Args[0].Pos(), "realloc applies to slices, not to %s", t)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: Realloc, Elem: t}
	return t
}

// subSlice checks s[a:b] and returns the borrow's type: &[]T. A sub-slice
// owns nothing: it sees s's memory.
func (c *checker) subSlice(e *ast.SliceExpr) Type {
	bt := c.value(e.X)
	for _, b := range []ast.Expr{e.Low, e.High} {
		if b != nil {
			c.indexValue(b)
		}
	}
	if bt == Invalid {
		return Invalid
	}
	if b, ok := bt.(*Borrow); ok {
		bt = b.Elem
	}
	if a, ok := bt.(*Array); ok {
		return c.borrowOf(c.sliceOf(a.Elem)) // part of an array, viewed as a slice
	}
	s, ok := bt.(*Slice)
	if !ok {
		c.errorf(e.Lbrack, "sub-slicing applies to slices and arrays, not to %s", bt)
		return Invalid
	}
	return c.borrowOf(s)
}

// isPath says whether e is a variable or a field (p, p.a.b), without indices.
func isPath(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return isPath(e.X)
	}
	return false
}

// ownerElem checks the argument &s[i] of take and replace and returns the
// element's type, which must be an owner.
func (c *checker) ownerElem(a ast.Expr, fn string) Type {
	u, ok := a.(*ast.UnaryExpr)
	var ix *ast.IndexExpr
	ok = ok && u.Op == token.AMP // !s[i] and -s[i] aren't borrows
	if ok {
		ix, ok = u.X.(*ast.IndexExpr)
	}
	if !ok {
		c.errorf(a.Pos(), "%s takes a borrowed element: %s(&s[i]...)", fn, fn)
		c.value(a)
		return Invalid
	}
	t := c.targetIndex(ix)
	c.info.Types[ix] = t
	c.info.Types[a] = t
	if t != Invalid && !IsOwner(t) && !c.genericContext() {
		c.errorf(a.Pos(), "%s is for owning elements; the elements here are %s (use s[i] directly)", fn, t)
		return Invalid
	}
	return t
}

// takeCall checks take(&s[i]): returns the element and leaves the slot empty.
func (c *checker) takeCall(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "take takes one element: take(&s[i])")
		c.checkArgs(e)
		return Invalid
	}
	t := c.ownerElem(e.Args[0], "take")
	c.info.Calls[e] = Callee{Builtin: Take, Elem: t}
	return t
}

// replaceCall checks replace(&s[i], v): puts v and returns the old element.
func (c *checker) replaceCall(e *ast.CallExpr) Type {
	if len(e.Args) != 2 {
		c.errorf(e.Lparen, "replace takes the element and the new value: replace(&s[i], v)")
		c.checkArgs(e)
		return Invalid
	}
	t := c.ownerElem(e.Args[0], "replace")
	if t != Invalid {
		c.valueAs(e.Args[1], t, "the value of replace")
	} else {
		c.value(e.Args[1])
	}
	c.info.Calls[e] = Callee{Builtin: Replace, Elem: t}
	return t
}

// isSlicePtrElem says whether t is a slice of pointers (for []([]?*T), which doesn't
// exist today, but keeps io.print right if it ever does).
func isSlicePtrElem(t Type) bool {
	e, ok := sliceElem(t)
	return ok && isPointerType(e)
}
