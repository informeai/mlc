package checker

import (
	"math"
	"math/big"
	"strconv"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// collectConsts avalia as constantes do topo na ordem do arquivo. Uma
// constante só pode usar as declaradas antes dela.
func (c *checker) collectConsts(f *ast.File) {
	for _, d := range f.Consts {
		c.mod.pendingConsts[d.Name] = true
	}
	c.cnames = map[string]int{} // constantes não geram nome no C, mas declare conta os nomes
	c.scope = c.mod.global
	for _, d := range f.Consts {
		delete(c.mod.pendingConsts, d.Name)
		c.constDecl(d)
	}
	c.scope = nil
}

// constDecl avalia uma constante e a declara no escopo atual.
func (c *checker) constDecl(d *ast.ConstDecl) {
	v, t := c.constValue(d.Value)
	if d.Type != nil {
		want := c.resolveType(d.Type, ctxVar)
		if want != Int && want != Bool && want != String && want != Byte && want != Invalid {
			c.errorf(d.Type.P, "constantes só podem ser int, byte, bool ou string")
			want = Invalid
		}
		c.assignable(t, want, d.Value.Pos(), "constante "+d.Name)
		t = want
	}
	l := c.declare(d.Name, d.P, t, false)
	l.IsConst = true
	l.Pub = d.Pub
	l.Const = v
	l.Used = true // constante não usada não é erro
}

// constValue avalia uma expressão constante em tempo de compilação. Estouro e
// divisão por zero viram erro de compilação.
func (c *checker) constValue(e ast.Expr) (any, Type) {
	switch e := e.(type) {
	case *ast.IntLit:
		v, err := strconv.ParseInt(e.Value, 10, 64)
		if err != nil {
			c.errorf(e.P, "constante %s não cabe em int (64 bits)", e.Value)
			return nil, Invalid
		}
		return v, Int
	case *ast.StringLit:
		return e.Value, String
	case *ast.CharLit:
		return int64(e.Value), Byte
	case *ast.Ident:
		if l := c.scope.lookup(e.Name); l != nil {
			if !l.IsConst {
				c.errorf(e.P, "%s não é constante", e.Name)
				return nil, Invalid
			}
			return l.Const, l.Type
		}
		switch {
		case e.Name == "true" || e.Name == "false":
			return e.Name == "true", Bool
		case c.mod.pendingConsts[e.Name]:
			c.errorf(e.P, "constante %s usada antes de ser declarada", e.Name)
		default:
			c.errorf(e.P, "identificador não declarado: %s", e.Name)
		}
		return nil, Invalid
	case *ast.UnaryExpr:
		v, t := c.constValue(e.X)
		switch {
		case t == Invalid:
			return nil, Invalid
		case e.Op == token.SUB && t == Int:
			if v.(int64) == math.MinInt64 {
				c.errorf(e.P, "estouro de inteiro na constante")
				return nil, Invalid
			}
			return -v.(int64), Int
		case e.Op == token.NOT && t == Bool:
			return !v.(bool), Bool
		}
		c.errorf(e.P, "operador %s não se aplica a %s", e.Op, t)
		return nil, Invalid
	case *ast.BinaryExpr:
		return c.constBinary(e)
	}
	c.errorf(e.Pos(), "o valor de uma const precisa ser uma expressão constante")
	return nil, Invalid
}

func (c *checker) constBinary(e *ast.BinaryExpr) (any, Type) {
	xv, xt := c.constValue(e.X)
	yv, yt := c.constValue(e.Y)
	if xt == Invalid || yt == Invalid {
		return nil, Invalid
	}
	mismatch := func() (any, Type) {
		c.errorf(e.OpPos, "operador %s não se aplica a %s e %s", e.Op, xt, yt)
		return nil, Invalid
	}
	switch e.Op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		if xt != Int || yt != Int {
			if e.Op == token.ADD && xt == String && yt == String {
				c.errorf(e.OpPos, "concatenação de strings exige alocação explícita e ainda não é suportada")
				return nil, Invalid
			}
			return mismatch()
		}
		a, b := xv.(int64), yv.(int64)
		if (e.Op == token.QUO || e.Op == token.REM) && b == 0 {
			c.errorf(e.OpPos, "divisão por zero na constante")
			return nil, Invalid
		}
		x, y, r := big.NewInt(a), big.NewInt(b), new(big.Int)
		switch e.Op {
		case token.ADD:
			r.Add(x, y)
		case token.SUB:
			r.Sub(x, y)
		case token.MUL:
			r.Mul(x, y)
		case token.QUO:
			r.Quo(x, y) // trunca em direção a zero, como o C
		case token.REM:
			r.Rem(x, y)
		}
		if !r.IsInt64() {
			c.errorf(e.OpPos, "estouro de inteiro na constante")
			return nil, Invalid
		}
		return r.Int64(), Int
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		if xt != Int || yt != Int {
			return mismatch()
		}
		a, b := xv.(int64), yv.(int64)
		switch e.Op {
		case token.LSS:
			return a < b, Bool
		case token.LEQ:
			return a <= b, Bool
		case token.GTR:
			return a > b, Bool
		}
		return a >= b, Bool
	case token.EQL, token.NEQ:
		if xt != yt {
			c.errorf(e.OpPos, "comparação entre tipos diferentes: %s e %s", xt, yt)
			return nil, Invalid
		}
		return (xv == yv) == (e.Op == token.EQL), Bool
	case token.LAND, token.LOR:
		if xt != Bool || yt != Bool {
			return mismatch()
		}
		if e.Op == token.LAND {
			return xv.(bool) && yv.(bool), Bool
		}
		return xv.(bool) || yv.(bool), Bool
	}
	return mismatch()
}
