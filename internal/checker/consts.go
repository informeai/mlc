package checker

import (
	"math/big"

	"mlc/internal/ast"
)

// collectConsts avalia as constantes do topo na ordem do arquivo. Uma
// constante só pode usar as declaradas antes dela.
func (c *checker) collectConsts(f *ast.File) {
	for _, d := range f.Consts {
		c.mod.pendingConsts[d.Name] = true
	}
	c.scope = c.mod.global
	c.cnames = map[string]int{} // constantes não geram nome no C, mas declare conta os nomes
	for _, d := range f.Consts {
		delete(c.mod.pendingConsts, d.Name)
		c.constDecl(d)
	}
	c.scope = nil
}

// constDecl avalia uma constante e a declara no escopo atual. O valor é
// calculado na compilação (numeric.go): estouro e divisão por zero são erros.
// Sem tipo escrito, a constante continua sem tipo (const N = 5 serve para
// int8 e para uint64); com tipo, o valor precisa caber nele.
func (c *checker) constDecl(d *ast.ConstDecl) {
	if hasCall(d.Value) {
		c.errorf(d.Value.Pos(), "o valor de uma const precisa ser uma expressão constante: chamadas de função são avaliadas durante a execução")
		c.declare(d.Name, d.P, Invalid, false).Used = true
		return
	}
	t := c.value(d.Value)
	v, isConst := c.info.Values[d.Value]
	if t != Invalid && !isConst {
		c.errorf(d.Value.Pos(), "o valor de uma const precisa ser uma expressão constante (literais, outras constantes e operadores)")
		t = Invalid
	}
	switch {
	case d.Type != nil:
		want := c.resolveType(d.Type, ctxVar)
		if want != Invalid && !isNumeric(want) && want != Bool && want != String {
			c.errorf(d.Type.P, "constantes só podem ser números, bool ou string")
			want = Invalid
		}
		if t != Invalid && want != Invalid {
			if isUntyped(t) {
				if !c.convertUntyped(d.Value, want) {
					c.errorf(d.Value.Pos(), "constante %s: esperado %s, encontrado %s", d.Name, want, t)
				}
			} else {
				c.assignable(t, want, d.Value.Pos(), "constante "+d.Name)
			}
		}
		t = want
		v = c.info.Values[d.Value]
	case t == UntypedInt:
		// sem tipo, mas precisa caber em algum inteiro de 64 bits
		if b, ok := v.(*big.Int); ok && (b.Cmp(minInt64) < 0 || b.Cmp(maxUint64) > 0) {
			c.errorf(d.Value.Pos(), "constante %s grande demais: não cabe em 64 bits", b)
			t = Invalid
		}
	case t != Invalid && !isNumeric(t) && !isUntyped(t) && t != Bool && t != String:
		c.errorf(d.Value.Pos(), "constantes só podem ser números, bool ou string")
		t = Invalid
	}
	l := c.declare(d.Name, d.P, t, false)
	l.IsConst = true
	l.Const = v
	l.Pub = d.Pub
	l.Used = true // constante não usada não é erro
}

var (
	minInt64, _  = IntRange(Int)
	_, maxUint64 = IntRange(Uint64)
)

// hasCall diz se avaliar e chama alguma função. Conversões (int32(5)) não
// contam: converter uma constante dá uma constante.
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
