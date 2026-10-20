package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Slices become "struct { T *ptr; int64_t len; }", one C type per element
// type (mlc_sl_int, mlc_sl_st_Point). The index goes through MLC_INDEX, which
// panics out of bounds. alloc([]T, n) becomes mlc_alloc_array (zeroed), and
// free(s) frees s.ptr.

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
	case *checker.FuncType:
		return fmt.Sprintf("fn%d", t.ID)
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
	panic(fmt.Sprintf("codegen: slice element without a C name: %s", t))
}

func (g *gen) sliceTypedefs() {
	// first the names, then the fn types (which can take slices) and only
	// then the bodies (a slice can have fn elements)
	// different types in MLC can be the same in C ([]?*thread.Thread[int]
	// and []?*thread.Thread[[]byte]): each name comes out only once
	named, defined := map[string]bool{}, map[string]bool{}
	for _, s := range g.info.Slices {
		if name := sliceCType(s); s.Elem != checker.Byte && !named[name] { // mlc_sl_byte comes from the runtime, which also uses it
			named[name] = true
			g.raw("typedef struct %s %s;\n", name, name)
		}
	}
	g.funcTypedefs()
	for _, s := range g.info.Slices {
		if name := sliceCType(s); s.Elem != checker.Byte && !defined[name] {
			defined[name] = true
			g.raw("struct %s { %s *ptr; int64_t len; };\n", name, cType(s.Elem))
		}
	}
	if len(g.info.Slices) > 0 {
		g.raw("\n")
	}
}

// funcTypedefs declares a C type (function pointer) for each fn type.
func (g *gen) funcTypedefs() {
	for _, f := range g.info.FuncTypes {
		params := []string{}
		for _, p := range f.Params {
			params = append(params, cType(p))
		}
		if len(params) == 0 {
			params = append(params, "void")
		}
		g.raw("typedef %s (*%s)(%s);\n", cType(f.Result), funcCType(f), strings.Join(params, ", "))
	}
}

func funcCType(f *checker.FuncType) string { return fmt.Sprintf("mlc_fnt_%d", f.ID) }

// sliceVal returns e's slice in C, following the borrow if e is &[]T.
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
		// m[f()][j]: the inner slice appears twice in C (.ptr and .len);
		// keeping its address in a temporary calls f only once, and the
		// result can still be assigned to.
		p := g.temp(&checker.Borrow{Elem: st})
		return fmt.Sprintf("(*(%s = &%s, &(*%s).ptr[MLC_INDEX(%s, (*%s).len)]))", p, s, p, i, p)
	}
	elem := fmt.Sprintf("%s.ptr[MLC_INDEX(%s, %s.len)]", s, i, s)
	if g.info.Types[e.X] == checker.String {
		return "((uint8_t)" + elem + ")"
	}
	return elem
}

// allocSlice translates alloc([]T, n): the size is computed only once.
func (g *gen) allocSlice(e *ast.CallExpr, callee checker.Callee) string {
	s := callee.Elem.(*checker.Slice)
	n := g.temp(checker.Int)
	elem := cType(s.Elem)
	return fmt.Sprintf("(%s = %s, ((%s){(%s *)mlc_alloc_array(%s, sizeof(%s), __FILE__, __LINE__), %s}))",
		n, g.expr(e.Args[1]), sliceCType(s), elem, n, elem, n)
}

// forIn translates "for x in s": the slice is read again on each iteration (it's
// always a variable or a field), and x receives a copy of the element.
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
		// the element leaves the slice: the slot stays empty
		st, _ := g.info.Types[iter].(*checker.Slice)
		if b, ok := g.info.Types[iter].(*checker.Borrow); ok {
			st = b.Elem.(*checker.Slice)
		}
		g.line("%s.ptr[%s] = %s;", sl, i, zeroValue(st.Elem))
	}
	g.indent--
}

// hoistIndex handles the target s[i] of an assignment: if the index has an effect
// (or the right side has), the index is computed first, only once, in a
// temporary. So s[f()] += 1 calls f once, and s[i] = g() checks the
// index before calling g, left to right.
func (g *gen) hoistIndex(target ast.Expr, rhsEffects bool) []string {
	ix, ok := target.(*ast.IndexExpr)
	if !ok || g.isLiteral(ix.Index) || (!g.effectful(ix.Index) && !rhsEffects) {
		return nil
	}
	t := g.temp(checker.Int)
	var pre string
	if a := arrayType(g.info.Types[ix.X]); a != nil {
		// [N]T: the size is constant
		pre = fmt.Sprintf("%s = MLC_INDEX(%s, INT64_C(%d))", t, g.expr(ix.Index), a.Len)
	} else {
		pre = fmt.Sprintf("%s = MLC_INDEX(%s, %s.len)", t, g.expr(ix.Index), g.sliceVal(ix.X))
	}
	g.indexOverride[ix] = t
	return []string{pre}
}

func (g *gen) dropIndex(target ast.Expr) {
	if ix, ok := target.(*ast.IndexExpr); ok {
		delete(g.indexOverride, ix)
	}
}

// realloc translates realloc(s, n). The slice and the size are computed first,
// left to right; mlc_realloc_array copies the old elements and
// zeroes the new ones.
func (g *gen) realloc(e *ast.CallExpr, callee checker.Callee) string {
	s := callee.Elem.(*checker.Slice)
	old := g.temp(s)
	n := g.temp(checker.Int)
	elem := cType(s.Elem)
	// A slice of owners can only grow: shrinking would discard elements that
	// may have memory (the runtime panics).
	growOnly := 0
	if _, ok := s.Elem.(*checker.Pointer); ok {
		growOnly = 1
	} else if _, ok := s.Elem.(*checker.Slice); ok {
		growOnly = 1
	}
	return fmt.Sprintf("(%s = %s, %s = %s, ((%s){(%s *)mlc_realloc_array(%s.ptr, %s.len, %s, sizeof(%s), %d, __FILE__, __LINE__), %s}))",
		old, g.expr(e.Args[0]), n, g.expr(e.Args[1]), sliceCType(s), elem, old, old, n, elem, growOnly, n)
}

// subSlice translates &s[a:b]: a temporary slice (a C compound literal, which
// lives until the end of the call) pointing to the elements from a to b-1. The
// bounds are checked first: 0 <= a <= b <= len(s).
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
	// Adding to NULL is undefined behavior in C, even adding zero: the
	// empty slice keeps ptr NULL.
	return fmt.Sprintf("(%s = %s, %s = %s, mlc_check_slice(%s, %s, %s.len, __FILE__, __LINE__), &(%s){%s.ptr == NULL ? NULL : %s.ptr + %s, %s - %s})",
		lo, low, hi, high, lo, hi, sv, sliceCType(s), sv, sv, lo, hi, lo)
}

// hasCall says whether evaluating e calls some function.
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

// takeReplace translates take(&s[i]) and replace(&s[i], v): the index is checked
// once, the old element goes into a temporary, and the slot receives the
// zero value (take) or v (replace).
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
