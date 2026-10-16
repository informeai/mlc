package codegen

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Ponteiros donos e empréstimos viram ponteiros do C. A posse só existe na
// compilação: no C gerado, alloc vira mlc_alloc (calloc que dá panic sem
// memória) e free vira mlc_free.

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

// alloc traduz alloc(T) (memória zerada) e alloc(valor).
func (g *gen) alloc(e *ast.CallExpr, callee checker.Callee) string {
	if callee.Slice {
		return g.allocSlice(e, callee)
	}
	elem := cType(callee.Elem)
	if callee.Zero {
		return fmt.Sprintf("MLC_ALLOC(%s)", elem)
	}
	// O valor é calculado antes da alocação, da esquerda para a direita.
	v := g.temp(callee.Elem)
	p := g.temp(g.info.Types[e])
	return fmt.Sprintf("(%s = %s, %s = MLC_ALLOC(%s), *%s = %s, %s)",
		v, g.expr(e.Args[0]), p, elem, p, v, p)
}

// borrow traduz &x. Emprestar um ponteiro empresta o conteúdo: o ponteiro já
// é o endereço. Emprestar uma variável ou um campo toma o endereço dele.
func (g *gen) borrow(x ast.Expr) string {
	if sl, ok := x.(*ast.SliceExpr); ok {
		return g.subSlice(sl)
	}
	if isPointer(g.info.Types[x]) {
		return g.expr(x)
	}
	return "&" + g.expr(x)
}
