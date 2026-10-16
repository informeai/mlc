package codegen

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Slices viram "struct { T *ptr; int64_t len; }", um tipo C por tipo de
// elemento (mlc_sl_int, mlc_sl_st_Ponto). O índice passa por MLC_INDEX, que
// dá panic fora dos limites. alloc([]T, n) vira mlc_alloc_array (zerado), e
// free(s) libera s.ptr.

func sliceCType(s *checker.Slice) string { return "mlc_sl_" + mangle(s.Elem) }

func mangle(t checker.Type) string {
	switch t := t.(type) {
	case *checker.Struct:
		return "st_" + t.Name
	}
	switch t {
	case checker.Int:
		return "int"
	case checker.Bool:
		return "bool"
	case checker.String:
		return "str"
	}
	panic(fmt.Sprintf("codegen: elemento de slice sem nome em C: %s", t))
}

func (g *gen) sliceTypedefs() {
	for _, s := range g.info.Slices {
		g.raw("typedef struct { %s *ptr; int64_t len; } %s;\n", cType(s.Elem), sliceCType(s))
	}
	if len(g.info.Slices) > 0 {
		g.raw("\n")
	}
}

// slicePrints gera a impressão de cada tipo slice: [a b c].
func (g *gen) slicePrints() {
	for _, s := range g.info.Slices {
		g.raw("static void mlc_print_%s(%s v) {\n", sliceCType(s), sliceCType(s))
		g.raw("    mlc_io_print_str(MLC_STR_LIT(\"[\", 1));\n")
		g.raw("    for (int64_t i = 0; i < v.len; i++) {\n")
		g.raw("        if (i > 0) mlc_io_print_sep();\n")
		g.raw("        %s;\n", printCall(s.Elem, "v.ptr[i]"))
		g.raw("    }\n")
		g.raw("    mlc_io_print_str(MLC_STR_LIT(\"]\", 1));\n}\n\n")
	}
}

// sliceVal devolve o slice de e em C, seguindo o empréstimo se e for &[]T.
func (g *gen) sliceVal(e ast.Expr) string {
	if _, ok := g.info.Types[e].(*checker.Borrow); ok {
		return "(*" + g.expr(e) + ")"
	}
	return g.expr(e)
}

func (g *gen) index(e *ast.IndexExpr) string {
	s := g.sliceVal(e.X)
	i, ok := g.indexOverride[e]
	if !ok {
		i = g.expr(e.Index)
	}
	elem := fmt.Sprintf("%s.ptr[MLC_INDEX(%s, %s.len)]", s, i, s)
	if g.info.Types[e.X] == checker.String {
		return "((int64_t)(unsigned char)" + elem + ")"
	}
	return elem
}

// allocSlice traduz alloc([]T, n): o tamanho é calculado uma vez só.
func (g *gen) allocSlice(e *ast.CallExpr, callee checker.Callee) string {
	s := callee.Elem.(*checker.Slice)
	n := g.temp(checker.Int)
	elem := cType(s.Elem)
	return fmt.Sprintf("(%s = %s, ((%s){(%s *)mlc_alloc_array(%s, sizeof(%s), __FILE__, __LINE__), %s}))",
		n, g.expr(e.Args[1]), sliceCType(s), elem, n, elem, n)
}

// forIn traduz "for x in s": o slice é lido de novo a cada volta (ele é
// sempre uma variável ou um campo), e x recebe uma cópia do elemento.
func (g *gen) forIn(s *ast.ForStmt) {
	i := g.newName("mlc_i")
	sl := g.sliceVal(s.Iter)
	g.line("for (int64_t %s = 0; %s < %s.len; %s++) {", i, i, sl, i)
	if l := g.info.ForVars[s]; l != nil {
		g.indent++
		g.line("%s %s = %s.ptr[%s];", cType(l.Type), l.CName, sl, i)
		g.indent--
	}
}

// hoistIndex trata o destino s[i] de uma atribuição: se o índice tem efeito
// (ou o lado direito tem), o índice é calculado antes, uma vez só, numa
// temporária. Assim s[f()] += 1 chama f uma vez, e s[i] = g() checa o
// índice antes de chamar g, da esquerda para a direita.
func (g *gen) hoistIndex(target ast.Expr, rhsEffects bool) []string {
	ix, ok := target.(*ast.IndexExpr)
	if !ok || g.isLiteral(ix.Index) || (!g.effectful(ix.Index) && !rhsEffects) {
		return nil
	}
	t := g.temp(checker.Int)
	s := g.sliceVal(ix.X)
	pre := fmt.Sprintf("%s = MLC_INDEX(%s, %s.len)", t, g.expr(ix.Index), s)
	g.indexOverride[ix] = t
	return []string{pre}
}

func (g *gen) dropIndex(target ast.Expr) {
	if ix, ok := target.(*ast.IndexExpr); ok {
		delete(g.indexOverride, ix)
	}
}

// realloc traduz realloc(s, n). O slice e o tamanho são calculados antes,
// da esquerda para a direita; mlc_realloc_array copia os elementos antigos e
// zera os novos.
func (g *gen) realloc(e *ast.CallExpr, callee checker.Callee) string {
	s := callee.Elem.(*checker.Slice)
	old := g.temp(s)
	n := g.temp(checker.Int)
	elem := cType(s.Elem)
	return fmt.Sprintf("(%s = %s, %s = %s, ((%s){(%s *)mlc_realloc_array(%s.ptr, %s.len, %s, sizeof(%s), __FILE__, __LINE__), %s}))",
		old, g.expr(e.Args[0]), n, g.expr(e.Args[1]), sliceCType(s), elem, old, old, n, elem, n)
}

// subSlice traduz &s[a:b]: um slice temporário (literal composto do C, que
// vive até o fim da chamada) apontando para os elementos de a até b-1. Os
// limites são checados antes: 0 <= a <= b <= len(s).
func (g *gen) subSlice(e *ast.SliceExpr) string {
	sv := g.sliceVal(e.X)
	st := g.info.Types[e.X]
	if b, ok := st.(*checker.Borrow); ok {
		st = b.Elem
	}
	s := st.(*checker.Slice)
	lo := g.temp(checker.Int)
	hi := g.temp(checker.Int)
	low, high := "INT64_C(0)", sv+".len"
	if e.Low != nil {
		low = g.expr(e.Low)
	}
	if e.High != nil {
		high = g.expr(e.High)
	}
	// Somar a NULL é comportamento indefinido em C, mesmo somando zero: o
	// slice vazio fica com ptr NULL.
	return fmt.Sprintf("(%s = %s, %s = %s, mlc_check_slice(%s, %s, %s.len, __FILE__, __LINE__), &(%s){%s.ptr == NULL ? NULL : %s.ptr + %s, %s - %s})",
		lo, low, hi, high, lo, hi, sv, sliceCType(s), sv, sv, lo, hi, lo)
}
