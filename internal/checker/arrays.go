package checker

import (
	"fmt"
	"math/big"

	"mlc/internal/ast"
)

// Arrays [N]T
//
// An array has a fixed size, known at compile time, and is a value: it lives on the
// stack or inside a struct, without alloc or free. Assigning copies. &a
// borrows the array as &[]T, to use with slice and text functions.
// In this version the elements can't be owners.

type Array struct {
	Elem Type
	Len  int64
}

func (a *Array) String() string { return fmt.Sprintf("[%d]%s", a.Len, a.Elem) }

type arrKey struct {
	elem Type
	n    int64
}

// maxArrayBytes limits the size of an array: it can go on the stack, which
// usually has 8 MB. For more, the way is alloc([]T, n).
const maxArrayBytes = 1 << 20

func (c *checker) arrayOf(elem Type, n int64) *Array {
	key := arrKey{elem, n}
	if a, ok := c.arrays[key]; ok {
		return a
	}
	a := &Array{Elem: elem, Len: n}
	c.arrays[key] = a
	if !abstract(elem) {
		c.info.Arrays = append(c.info.Arrays, a)
	}
	return a
}

func isArray(t Type) bool {
	_, ok := t.(*Array)
	return ok
}

// arrayElem returns the element type of an array or of an array
// borrow.
func arrayElem(t Type) (*Array, bool) {
	if b, ok := t.(*Borrow); ok {
		t = b.Elem
	}
	a, ok := t.(*Array)
	return a, ok
}

func (c *checker) resolveArray(t *ast.TypeExpr, ctx typeCtx) Type {
	if ctx == ctxElem {
		c.errorf(t.P, "pointer to array isn't supported: use the array directly ([N]T), or alloc([]T, n) for heap memory")
		return Invalid
	}
	n, ok := c.arrayLen(t.Len)
	elem := c.resolveType(t.Elem, ctxArrayElem)
	if !ok || elem == Invalid {
		return Invalid
	}
	if IsOwner(elem) || isResource(elem) {
		c.errorf(t.Elem.P, "arrays of %s aren't supported yet: the elements can't be owners", elem)
		return Invalid
	}
	if size := sizeOf(elem) * n; size > maxArrayBytes {
		c.errorf(t.P, "array too large (%d bytes; the limit is %d, so the stack doesn't overflow): use alloc([]%s, %d)", size, maxArrayBytes, elem, n)
		return Invalid
	}
	return c.arrayOf(elem, n)
}

// arrayLen evaluates the size of [N]T: an integer constant greater than zero.
func (c *checker) arrayLen(e ast.Expr) (int64, bool) {
	if c.scope == nil {
		// types of fields and parameters: only the top-level constants apply
		c.scope = c.mod.global
		defer func() { c.scope = nil }()
	}
	t := c.value(e)
	if t == Invalid {
		return 0, false
	}
	v, isConst := c.info.Values[e].(*big.Int)
	if !isConst || (!isInteger(t) && t != UntypedInt) {
		c.errorf(e.Pos(), "an array's size must be an integer constant")
		return 0, false
	}
	if v.Sign() <= 0 || !v.IsInt64() {
		c.errorf(e.Pos(), "an array's size must be greater than zero, found %s", v)
		return 0, false
	}
	return v.Int64(), true
}

// sizeOf estimates how many bytes a value of type t takes (without alignment).
func sizeOf(t Type) int64 {
	switch t := t.(type) {
	case *Enum:
		var max int64
		for _, v := range t.Variants {
			var n int64
			for _, f := range v.Fields {
				n += sizeOf(f.Type)
			}
			if n > max {
				max = n
			}
		}
		return max + 8
	case *Array:
		return sizeOf(t.Elem) * t.Len
	case *Struct:
		var n int64
		for _, f := range t.Fields {
			n += sizeOf(f.Type)
		}
		return n
	case *Pointer, *Borrow:
		return 8
	case *Slice:
		return 16
	}
	switch t {
	case Int8, Byte, Bool:
		return 1
	case Int16, Uint16:
		return 2
	case Int32, Uint32, Float32:
		return 4
	case String:
		return 16
	}
	return 8
}

// arrayLit checks "[3]int{1, 2, 3}": every element, or none (zero value).
func (c *checker) arrayLit(e *ast.ArrayLit) Type {
	t := c.resolveType(e.Type, ctxVar)
	a, ok := t.(*Array)
	if !ok {
		for _, el := range e.Elems {
			c.value(el)
		}
		return Invalid
	}
	if n := int64(len(e.Elems)); n != 0 && n != a.Len {
		c.errorf(e.Lbrace, "the literal of %s has %d element(s): list all %d, or use %s{} for the zero value", a, n, a.Len, a)
	}
	for i, el := range e.Elems {
		c.valueAs(el, a.Elem, fmt.Sprintf("elemento %d", i))
	}
	return a
}

// checkArrayIndex refuses at compile time a constant index outside the array.
func (c *checker) checkArrayIndex(a *Array, i ast.Expr) {
	if v, ok := c.info.Values[i].(*big.Int); ok && (v.Sign() < 0 || v.Cmp(big.NewInt(a.Len)) >= 0) {
		c.errorf(i.Pos(), "index %s out of bounds of %s (0 to %d)", v, a, a.Len-1)
	}
}
