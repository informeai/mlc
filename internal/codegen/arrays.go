package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Arrays become "struct mlc_ar_<N>_<T> { T a[N]; }": inside a struct, the
// C array can be assigned, passed and returned by value, like the
// MLC array. The index goes through MLC_INDEX with the constant size.

func arrayCType(a *checker.Array) string { return "mlc_ar_" + mangle(a) }

func isArray(t checker.Type) bool {
	_, ok := t.(*checker.Array)
	return ok
}

func (g *gen) arrayDef(a *checker.Array) {
	t := arrayCType(a)
	g.raw("struct %s {\n    %s a[%d];\n};\n\n", t, cType(a.Elem), a.Len)
	g.raw("static bool mlc_eq_%s(%s x, %s y) {\n", mangle(a), t, t)
	g.raw("    for (int64_t i = 0; i < %d; i++) {\n", a.Len)
	g.raw("        if (!(%s)) return false;\n", eqExpr(a.Elem, "x.a[i]", "y.a[i]"))
	g.raw("    }\n    return true;\n}\n\n")
}

// arrayOf returns the array of e (following the borrow, if e is &[N]T) and
// its type.
func (g *gen) arrayOf(e ast.Expr) (string, *checker.Array) {
	switch t := g.info.Types[e].(type) {
	case *checker.Array:
		return g.expr(e), t
	case *checker.Borrow:
		if a, ok := t.Elem.(*checker.Array); ok {
			return "(*" + g.expr(e) + ")", a
		}
	}
	return "", nil
}

func (g *gen) arrayIndex(e *ast.IndexExpr) string {
	a, at := g.arrayOf(e.X)
	i, ok := g.indexOverride[e]
	if !ok {
		i = g.expr(e.Index)
	}
	return fmt.Sprintf("%s.a[MLC_INDEX(%s, INT64_C(%d))]", a, i, at.Len)
}

func (g *gen) arrayLit(e *ast.ArrayLit) string {
	a := g.info.Types[e].(*checker.Array)
	if len(e.Elems) == 0 {
		return zeroValue(a)
	}
	pre, vals := g.operands(e.Elems)
	return sequence(pre, fmt.Sprintf("((%s){{%s}})", arrayCType(a), strings.Join(vals, ", ")))
}

// arrayView translates &a (or &a[i:j]), with a [N]T, into a slice borrow:
// a temporary slice that points to the array's elements.
func (g *gen) arrayView(x ast.Expr) string {
	a, at := g.arrayOf(x)
	return fmt.Sprintf("&(%s){%s.a, INT64_C(%d)}", sliceCType(&checker.Slice{Elem: at.Elem}), a, at.Len)
}

// forInArray translates "for x in a" with a [N]T.
func (g *gen) forInArray(s *ast.ForStmt) {
	i := g.newName("mlc_i")
	a, at := g.arrayOf(s.Iter)
	g.line("for (int64_t %s = 0; %s < %d; %s++) {", i, i, at.Len, i)
	if l := g.info.ForVars[s]; l != nil {
		g.indent++
		g.line("%s %s = %s.a[%s];", cType(l.Type), l.CName, a, i)
		g.indent--
	}
}

// arrayType returns the array of t ([N]T or &[N]T), or nil.
func arrayType(t checker.Type) *checker.Array {
	if b, ok := t.(*checker.Borrow); ok {
		t = b.Elem
	}
	a, _ := t.(*checker.Array)
	return a
}

func isArrayBorrow(t checker.Type) bool {
	b, ok := t.(*checker.Borrow)
	return ok && isArray(b.Elem)
}

// arraySubSlice translates &a[i:j] with a [N]T: bounds checked against N.
func (g *gen) arraySubSlice(e *ast.SliceExpr) string {
	a, at := g.arrayOf(e.X)
	lo := g.temp(checker.Int)
	hi := g.temp(checker.Int)
	low, high := "INT64_C(0)", fmt.Sprintf("INT64_C(%d)", at.Len)
	if e.Low != nil {
		low = g.expr(e.Low)
	}
	if e.High != nil {
		high = g.expr(e.High)
	}
	return fmt.Sprintf("(%s = %s, %s = %s, mlc_check_slice(%s, %s, INT64_C(%d), __FILE__, __LINE__), &(%s){%s.a + %s, %s - %s})",
		lo, low, hi, high, lo, hi, at.Len, sliceCType(&checker.Slice{Elem: at.Elem}), a, lo, hi, lo)
}
