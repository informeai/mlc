package checker

import (
	"math/big"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Bits e aritmética que dá a volta
//
//	a & b, a | b, a ^ b, ^a      e, ou, ou exclusivo, inverte os bits
//	a << n, a >> n               deslocamentos (>> com sinal repete o bit de sinal)
//	a +% b, a -% b, a *% b       soma, subtração e multiplicação módulo 2^bits
//
// Só valem para inteiros. A aritmética comum (+ - *) continua dando panic no
// estouro; quem quer que a conta dê a volta (hash, criptografia, geradores
// aleatórios) diz isso com +%, -% e *%. Um deslocamento por n fora de
// 0..bits-1 dá panic (em C seria comportamento indefinido); os bits que saem
// pela esquerda num << são descartados.

// isBitOp diz se op é um dos operadores que só valem para inteiros e nunca
// estouram: & | ^ +% -% *%.
func isBitOp(op token.Kind) bool {
	switch op {
	case token.AMP, token.OR, token.XOR, token.ADD_WRAP, token.SUB_WRAP, token.MUL_WRAP:
		return true
	}
	return false
}

// intLike diz se t é um inteiro (ou um parâmetro de tipo integer).
func intLike(t Type) bool { return isInteger(t) || t == UntypedInt || tvHas(t, "mod") }

// compoundOps liga cada atribuição composta ao operador binário.
var compoundOps = map[token.Kind]token.Kind{
	token.ADD_ASSIGN: token.ADD, token.SUB_ASSIGN: token.SUB, token.MUL_ASSIGN: token.MUL,
	token.QUO_ASSIGN: token.QUO, token.REM_ASSIGN: token.REM,
	token.AND_ASSIGN: token.AMP, token.OR_ASSIGN: token.OR, token.XOR_ASSIGN: token.XOR,
	token.SHL_ASSIGN: token.SHL, token.SHR_ASSIGN: token.SHR,
	token.ADD_WRAP_ASSIGN: token.ADD_WRAP, token.SUB_WRAP_ASSIGN: token.SUB_WRAP, token.MUL_WRAP_ASSIGN: token.MUL_WRAP,
}

// CompoundOp devolve o operador binário de uma atribuição composta (+= → +).
func CompoundOp(op token.Kind) token.Kind { return compoundOps[op] }

// shift checa a << n e a >> n. O tipo do resultado é o de a; n pode ser de
// qualquer tipo inteiro.
func (c *checker) shift(e *ast.BinaryExpr, x, y Type) Type {
	nv, nConst := c.info.Values[e.Y]
	switch {
	case !intLike(y):
		c.errorf(e.OpPos, "a quantidade de bits de %s precisa ser um inteiro, não %s", e.Op, y)
		return Invalid
	case nConst && nv.(*big.Int).Sign() < 0:
		c.errorf(e.Y.Pos(), "deslocamento negativo (%s)", nv)
		return Invalid
	}
	if y == UntypedInt {
		c.convertUntyped(e.Y, Int)
	}
	xv, xConst := c.info.Values[e.X]
	if x == UntypedInt && !(xConst && nConst) {
		c.convertUntyped(e.X, Int) // 1 << n, com n variável: o 1 é int
		x = Int
	}
	if !intLike(x) {
		c.errorf(e.OpPos, "operador %s só se aplica a inteiros, não a %s", e.Op, x)
		return Invalid
	}
	if nConst && isInteger(x) && nv.(*big.Int).Cmp(big.NewInt(int64(IntBits(x)))) >= 0 {
		c.errorf(e.Y.Pos(), "deslocamento de %s bits num %s, que tem %d (use de 0 a %d)", nv, x, IntBits(x), IntBits(x)-1)
		return Invalid
	}
	if xConst && nConst {
		n := nv.(*big.Int)
		if n.Cmp(big.NewInt(4096)) > 0 {
			c.errorf(e.Y.Pos(), "deslocamento grande demais numa constante (%s)", n)
			return Invalid
		}
		r := new(big.Int)
		if e.Op == token.SHL {
			r.Lsh(xv.(*big.Int), uint(n.Uint64()))
		} else {
			r.Rsh(xv.(*big.Int), uint(n.Uint64()))
		}
		if isInteger(x) {
			if _, msg := representable(r, x); msg != "" {
				c.errorf(e.OpPos, "estouro de inteiro na constante: %s", msg)
				return x
			}
		}
		c.info.Values[e] = r
	}
	return x
}

// wrapInt reduz v ao intervalo de t, módulo 2^bits (a conta que dá a volta).
func wrapInt(v *big.Int, t Type) *big.Int {
	bits := uint(IntBits(t))
	m := new(big.Int).Lsh(big.NewInt(1), bits)
	r := new(big.Int).Mod(v, m) // Mod é sempre >= 0
	if IsSigned(t) && r.Cmp(new(big.Int).Rsh(m, 1)) >= 0 {
		r.Sub(r, m)
	}
	return r
}

// foldBits calcula & | ^ +% -% *% entre constantes inteiras.
func foldBits(op token.Kind, a, b *big.Int, t Type) *big.Int {
	r := new(big.Int)
	switch op {
	case token.AMP:
		r.And(a, b)
	case token.OR:
		r.Or(a, b)
	case token.XOR:
		r.Xor(a, b)
	case token.ADD_WRAP:
		r.Add(a, b)
	case token.SUB_WRAP:
		r.Sub(a, b)
	case token.MUL_WRAP:
		r.Mul(a, b)
	}
	if isInteger(t) {
		return wrapInt(r, t)
	}
	return r
}

// complement calcula ^v (inverte os bits) para uma constante do tipo t.
func complement(v *big.Int, t Type) *big.Int {
	r := new(big.Int).Not(v) // -v-1: o valor com sinal
	if isInteger(t) && !IsSigned(t) {
		return wrapInt(r, t)
	}
	return r
}
