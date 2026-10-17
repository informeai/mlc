package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/token"
)

// Ordem de avaliação
//
// O MLC garante que os operandos são avaliados da esquerda para a direita:
// argumentos de chamada, os dois lados de um operador e os campos de um
// literal. O C não garante isso: em f(a(), b()) o compilador C pode chamar
// b() primeiro. Quando a ordem pode ser observada, o gerador calcula os
// operandos em temporárias, sequenciadas pelo operador vírgula do C:
//
//	soma(a(), b())  →  (mlc_t1 = mlc_fn_a(), mlc_fn_soma(mlc_t1, mlc_fn_b()))
//
// As temporárias são declaradas no começo da função. O último operando que
// não é literal fica no lugar, porque nada é avaliado depois dele.

// effectful diz se avaliar e pode ter efeito observável: chamar uma função
// ou dar panic (aritmética pode estourar ou dividir por zero).
func (g *gen) effectful(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.CallExpr:
		return true
	case *ast.IndexExpr:
		return true // pode dar panic fora dos limites
	case *ast.SliceExpr:
		return true // pode dar panic fora dos limites
	case *ast.BinaryExpr:
		if _, isArith := opNames[e.Op]; isArith || e.Op == token.SHL || e.Op == token.SHR {
			return true // pode dar panic (estouro, divisão por zero, deslocamento)
		}
		return g.effectful(e.X) || g.effectful(e.Y)
	case *ast.UnaryExpr:
		return e.Op == token.SUB || g.effectful(e.X)
	case *ast.SelectorExpr:
		return g.effectful(e.X)
	case *ast.CompositeLit:
		for _, f := range e.Fields {
			if g.effectful(f.Value) {
				return true
			}
		}
	}
	return false
}

// isLiteral diz se e é um valor fixo, que pode ser avaliado a qualquer momento.
func (g *gen) isLiteral(e ast.Expr) bool {
	if _, isConst := g.info.Values[e]; isConst {
		return true
	}
	switch e := e.(type) {
	case *ast.IntLit, *ast.StringLit:
		return true
	case *ast.SelectorExpr:
		_, isConst := g.info.Values[e]
		return isConst
	case *ast.Ident:
		_, isConst := g.info.Values[e]
		return isConst || g.info.Locals[e] == nil // constante, true ou false
	case *ast.UnaryExpr:
		_, isOut := g.info.Types[e].(*checker.OutHandle)
		return isOut // &h de um parâmetro &?*X: um endereço, sem efeitos
	}
	return false
}

// operands traduz os operandos de uma operação. pre são as atribuições a
// temporárias que precisam rodar antes, na ordem; vals são os valores a usar.
func (g *gen) operands(es []ast.Expr) (pre, vals []string) {
	effects, last, nonLit := false, -1, 0
	for i, e := range es {
		if !g.isLiteral(e) {
			nonLit++
			last = i
		}
		effects = effects || g.effectful(e)
	}
	vals = make([]string, len(es))
	for i, e := range es {
		v := g.expr(e)
		if effects && nonLit >= 2 && i != last && !g.isLiteral(e) {
			t := g.temp(g.info.Types[e])
			pre = append(pre, t+" = "+v)
			v = t
		}
		vals[i] = v
	}
	return pre, vals
}

// sequence junta as atribuições de pre e a expressão final com o operador vírgula.
func sequence(pre []string, final string) string {
	if len(pre) == 0 {
		return final
	}
	return "(" + strings.Join(pre, ", ") + ", " + final + ")"
}

// temp cria uma temporária do tipo t, declarada no começo da função.
func (g *gen) temp(t checker.Type) string {
	name := g.newName("mlc_t")
	g.temps = append(g.temps, cType(t)+" "+name)
	return name
}

// binary traduz um operador binário. top indica a expressão de nível mais
// alto de uma condição, que sai sem parênteses externos ("if ((a == b))"
// gera aviso no clang).
func (g *gen) binary(e *ast.BinaryExpr, top bool) string {
	if e.Op == token.LAND || e.Op == token.LOR {
		// O C já avalia && e || da esquerda para a direita, com curto-circuito.
		s := fmt.Sprintf("%s %s %s", g.expr(e.X), e.Op, g.expr(e.Y))
		if !top {
			s = "(" + s + ")"
		}
		return s
	}
	pre, v := g.operands([]ast.Expr{e.X, e.Y})
	var core string
	bare := false // core é "x op y", sem parênteses próprios
	t := g.info.Types[e.X]
	switch {
	case opNames[e.Op] != "" || intOps[e.Op] != "":
		core = binop(e.Op, g.typeOf(e.X), v[0], v[1])
	case (e.Op == token.EQL || e.Op == token.NEQ) && (t == checker.String || isStruct(t) || isArray(t) || isEnum(t)):
		core = eqExpr(t, v[0], v[1])
		if e.Op == token.NEQ {
			core = "!" + core
		}
	default:
		core = fmt.Sprintf("%s %s %s", v[0], e.Op, v[1])
		bare = true
	}
	if len(pre) > 0 {
		return sequence(pre, core)
	}
	if bare && !top {
		core = "(" + core + ")"
	}
	return core
}
