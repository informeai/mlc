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
	if en, ok := t.(*checker.Enum); ok {
		return "en_" + en.CName
	}
	if a, ok := t.(*checker.Array); ok {
		return fmt.Sprintf("%d_%s", a.Len, mangle(a.Elem))
	}
	if s, ok := suffix[t]; ok && t != checker.Int && t != checker.Byte {
		return s
	}
	switch t := t.(type) {
	case *checker.Struct:
		return "st_" + t.CName
	case *checker.Pointer:
		return "p_" + mangle(t.Elem)
	case *checker.Slice:
		return "sl_" + mangle(t.Elem)
	}
	switch t {
	case checker.Int:
		return "int"
	case checker.Byte:
		return "byte"
	case checker.Bool:
		return "bool"
	case checker.String:
		return "str"
	}
	panic(fmt.Sprintf("codegen: elemento de slice sem nome em C: %s", t))
}

func (g *gen) sliceTypedefs() {
	for _, s := range g.info.Slices {
		if s.Elem == checker.Byte {
			continue // mlc_sl_byte vem do runtime, que também o usa
		}
		g.raw("typedef struct { %s *ptr; int64_t len; } %s;\n", cType(s.Elem), sliceCType(s))
	}
	if len(g.info.Slices) > 0 {
		g.raw("\n")
	}
}

// slicePrints gera a impressão de cada tipo slice: [a b c].
func (g *gen) slicePrints() {
	for _, s := range g.info.Slices {
		if _, ptrs := s.Elem.(*checker.Pointer); ptrs {
			continue // slices de ponteiros não são impressos
		}
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
	if _, at := g.arrayOf(e.X); at != nil {
		return g.arrayIndex(e)
	}
	s := g.sliceVal(e.X)
	i, ok := g.indexOverride[e]
	if !ok {
		i = g.expr(e.Index)
	}
	if st, isSl := g.info.Types[e.X].(*checker.Slice); isSl && hasCall(e.X) {
		// m[f()][j]: o slice de dentro aparece duas vezes no C (.ptr e .len);
		// guardar o endereço dele numa temporária chama f uma vez só, e o
		// resultado continua podendo receber atribuição.
		p := g.temp(&checker.Borrow{Elem: st})
		return fmt.Sprintf("(*(%s = &%s, &(*%s).ptr[MLC_INDEX(%s, (*%s).len)]))", p, s, p, i, p)
	}
	elem := fmt.Sprintf("%s.ptr[MLC_INDEX(%s, %s.len)]", s, i, s)
	if g.info.Types[e.X] == checker.String {
		return "((uint8_t)" + elem + ")"
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
	if _, at := g.arrayOf(s.Iter); at != nil && g.info.Drains[s] == nil {
		g.forInArray(s)
		return
	}
	i := g.newName("mlc_i")
	iter, drain := s.Iter, false
	if d := g.info.Drains[s]; d != nil {
		iter, drain = d, true
	}
	sl := g.sliceVal(iter)
	g.line("for (int64_t %s = 0; %s < %s.len; %s++) {", i, i, sl, i)
	g.indent++
	if l := g.info.ForVars[s]; l != nil {
		g.line("%s %s = %s.ptr[%s];", cType(l.Type), l.CName, sl, i)
	}
	if drain {
		// o elemento sai do slice: o lugar fica vazio
		st, _ := g.info.Types[iter].(*checker.Slice)
		if b, ok := g.info.Types[iter].(*checker.Borrow); ok {
			st = b.Elem.(*checker.Slice)
		}
		g.line("%s.ptr[%s] = %s;", sl, i, zeroValue(st.Elem))
	}
	g.indent--
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
	// Slice de donos só pode crescer: encolher descartaria elementos que
	// podem ter memória (o runtime dá panic).
	growOnly := 0
	if _, ok := s.Elem.(*checker.Pointer); ok {
		growOnly = 1
	} else if _, ok := s.Elem.(*checker.Slice); ok {
		growOnly = 1
	}
	return fmt.Sprintf("(%s = %s, %s = %s, ((%s){(%s *)mlc_realloc_array(%s.ptr, %s.len, %s, sizeof(%s), %d, __FILE__, __LINE__), %s}))",
		old, g.expr(e.Args[0]), n, g.expr(e.Args[1]), sliceCType(s), elem, old, old, n, elem, growOnly, n)
}

// subSlice traduz &s[a:b]: um slice temporário (literal composto do C, que
// vive até o fim da chamada) apontando para os elementos de a até b-1. Os
// limites são checados antes: 0 <= a <= b <= len(s).
func (g *gen) subSlice(e *ast.SliceExpr) string {
	if _, at := g.arrayOf(e.X); at != nil {
		return g.arraySubSlice(e)
	}
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

// hasCall diz se avaliar e chama alguma função.
func hasCall(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.CallExpr:
		return true
	case *ast.IndexExpr:
		return hasCall(e.X) || hasCall(e.Index)
	case *ast.SelectorExpr:
		return hasCall(e.X)
	case *ast.UnaryExpr:
		return hasCall(e.X)
	case *ast.BinaryExpr:
		return hasCall(e.X) || hasCall(e.Y)
	}
	return false
}

// takeReplace traduz take(&s[i]) e replace(&s[i], v): o índice é checado
// uma vez, o elemento antigo vai para uma temporária, e o lugar recebe o
// valor zero (take) ou v (replace).
func (g *gen) takeReplace(e *ast.CallExpr, callee checker.Callee) string {
	ix := e.Args[0].(*ast.UnaryExpr).X.(*ast.IndexExpr)
	s := g.sliceVal(ix.X)
	ti := g.temp(checker.Int)
	old := g.temp(callee.Elem)
	parts := []string{fmt.Sprintf("%s = MLC_INDEX(%s, %s.len)", ti, g.expr(ix.Index), s)}
	put := zeroValue(callee.Elem)
	if callee.Builtin == checker.Replace {
		nv := g.temp(callee.Elem)
		parts = append(parts, fmt.Sprintf("%s = %s", nv, g.expr(e.Args[1])))
		put = nv
	}
	parts = append(parts,
		fmt.Sprintf("%s = %s.ptr[%s]", old, s, ti),
		fmt.Sprintf("%s.ptr[%s] = %s", s, ti, put))
	return sequence(parts, old)
}
