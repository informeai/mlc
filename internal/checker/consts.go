package checker

import (
	"math/big"

	"mlc/internal/ast"
)

// collectConsts evaluates the top-level constants in file order. A
// constant can only use the ones declared before it.
func (c *checker) collectConsts(f *ast.File) {
	for _, d := range f.Consts {
		c.mod.pendingConsts[d.Name] = true
	}
	c.scope = c.mod.global
	c.cnames = map[string]int{} // constants don't produce a C name, but declare counts the names
	for _, d := range f.Consts {
		delete(c.mod.pendingConsts, d.Name)
		c.constDecl(d)
	}
	c.scope = nil
}

// constDecl evaluates a constant and declares it in the current scope. The value is
// computed at compile time (numeric.go): overflow and division by zero are errors.
// Without a written type, the constant stays untyped (const N = 5 works for
// int8 and for uint64); with a type, the value must fit in it.
func (c *checker) constDecl(d *ast.ConstDecl) {
	if hasCall(d.Value) {
		c.errorf(d.Value.Pos(), "a const's value must be a constant expression: function calls are evaluated at run time")
		c.declare(d.Name, d.P, Invalid, false).Used = true
		return
	}
	t := c.value(d.Value)
	v, isConst := c.info.Values[d.Value]
	if t != Invalid && !isConst {
		c.errorf(d.Value.Pos(), "a const's value must be a constant expression (literals, other constants and operators)")
		t = Invalid
	}
	switch {
	case d.Type != nil:
		want := c.resolveType(d.Type, ctxVar)
		if want != Invalid && !isNumeric(want) && want != Bool && want != String {
			c.errorf(d.Type.P, "constants can only be numbers, bool or string")
			want = Invalid
		}
		if t != Invalid && want != Invalid {
			if isUntyped(t) {
				if !c.convertUntyped(d.Value, want) {
					c.errorf(d.Value.Pos(), "constant %s: expected %s, found %s", d.Name, want, t)
				}
			} else {
				c.assignable(t, want, d.Value.Pos(), "constant "+d.Name)
			}
		}
		t = want
		v = c.info.Values[d.Value]
	case t == UntypedInt:
		// untyped, but it must fit in some 64-bit integer
		if b, ok := v.(*big.Int); ok && (b.Cmp(minInt64) < 0 || b.Cmp(maxUint64) > 0) {
			c.errorf(d.Value.Pos(), "constant %s too large: doesn't fit in 64 bits", b)
			t = Invalid
		}
	case t != Invalid && !isNumeric(t) && !isUntyped(t) && t != Bool && t != String:
		c.errorf(d.Value.Pos(), "constants can only be numbers, bool or string")
		t = Invalid
	}
	l := c.declare(d.Name, d.P, t, false)
	l.IsConst = true
	l.Const = v
	l.Pub = d.Pub
	l.Used = true // an unused constant isn't an error
}

var (
	minInt64, _  = IntRange(Int)
	_, maxUint64 = IntRange(Uint64)
)

// hasCall says whether evaluating e calls some function. Conversions (int32(5)) don't
// count: converting a constant gives a constant.
func hasCall(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && basicTypes[id.Name] != nil && len(e.Args) == 1 {
			return hasCall(e.Args[0])
		}
		return true
	case *ast.UnaryExpr:
		return hasCall(e.X)
	case *ast.BinaryExpr:
		return hasCall(e.X) || hasCall(e.Y)
	case *ast.SelectorExpr:
		return hasCall(e.X)
	case *ast.IndexExpr:
		return hasCall(e.X) || hasCall(e.Index)
	}
	return false
}
