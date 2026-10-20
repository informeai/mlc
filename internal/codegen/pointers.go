package codegen

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Owning pointers and borrows become C pointers. Ownership only exists at
// compile time: in the generated C, alloc becomes mlc_alloc (a calloc that panics without
// memory) and free becomes mlc_free.

func isPointer(t checker.Type) bool {
	switch t.(type) {
	case *checker.Pointer, *checker.Borrow:
		return true
	}
	return false
}

func isStruct(t checker.Type) bool {
	_, ok := t.(*checker.Struct)
	return ok
}

// alloc translates alloc(T) (zeroed memory) and alloc(value).
func (g *gen) alloc(e *ast.CallExpr, callee checker.Callee) string {
	if callee.Slice {
		return g.allocSlice(e, callee)
	}
	elem := cType(callee.Elem)
	if callee.Zero {
		return fmt.Sprintf("MLC_ALLOC(%s)", elem)
	}
	// The value is computed before the allocation, left to right.
	v := g.temp(callee.Elem)
	p := g.temp(g.info.Types[e])
	return fmt.Sprintf("(%s = %s, %s = MLC_ALLOC(%s), *%s = %s, %s)",
		v, g.expr(e.Args[0]), p, elem, p, v, p)
}

// borrow translates &x. Borrowing a pointer borrows the pointee: the pointer is already
// the address. Borrowing a variable or a field takes its address.
func (g *gen) borrow(x ast.Expr) string {
	if sl, ok := x.(*ast.SliceExpr); ok {
		return g.subSlice(sl)
	}
	if p, ok := g.info.Types[x].(*checker.Pointer); ok {
		if p.Nullable {
			// &p with p ?*T (in a generic, &v.items[i] with T = ?*X): checks nil
			return "MLC_NONNULL(" + g.expr(x) + ")"
		}
		return g.expr(x)
	}
	return "&" + g.expr(x)
}
