package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Arrays viram "struct mlc_ar_<N>_<T> { T a[N]; }": dentro de uma struct, o
// array do C pode ser atribuído, passado e devolvido por valor, como o
// array do MLC. O índice passa por MLC_INDEX com o tamanho constante.

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
	g.raw("static void mlc_print_%s(%s v) {\n", mangle(a), t)
	g.raw("    mlc_io_print_str(MLC_STR_LIT(\"[\", 1));\n")
	g.raw("    for (int64_t i = 0; i < %d; i++) {\n", a.Len)
	g.raw("        if (i > 0) mlc_io_print_sep();\n")
	g.raw("        %s;\n", printCall(a.Elem, "v.a[i]"))
	g.raw("    }\n")
	g.raw("    mlc_io_print_str(MLC_STR_LIT(\"]\", 1));\n}\n\n")
}

// arrayOf devolve o array de e (seguindo o empréstimo, se e for &[N]T) e o
// seu tipo.
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

// arrayView traduz &a (ou &a[i:j]), com a [N]T, para um empréstimo de slice:
// um slice temporário que aponta para os elementos do array.
func (g *gen) arrayView(x ast.Expr) string {
	a, at := g.arrayOf(x)
	return fmt.Sprintf("&(%s){%s.a, INT64_C(%d)}", sliceCType(&checker.Slice{Elem: at.Elem}), a, at.Len)
}

// forInArray traduz "for x in a" com a [N]T.
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

func isArrayBorrow(t checker.Type) bool {
	b, ok := t.(*checker.Borrow)
	return ok && isArray(b.Elem)
}

// arraySubSlice traduz &a[i:j] com a [N]T: limites checados contra N.
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
