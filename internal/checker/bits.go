package checker

import (
	"math/big"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Bits and wrapping arithmetic
//
//	a & b, a | b, a ^ b, ^a      and, or, exclusive or, invert the bits
//	a << n, a >> n               shifts (a signed >> repeats the sign bit)
//	a +% b, a -% b, a *% b       addition, subtraction and multiplication modulo 2^bits
//
// They only apply to integers. Ordinary arithmetic (+ - *) still panics on
// overflow; whoever wants the arithmetic to wrap (hashes, cryptography, random
// generators) says so with +%, -% and *%. A shift by an n outside
// 0..bits-1 panics (in C it would be undefined behavior); the bits that leave
// on the left in a << are discarded.

// isBitOp says whether op is one of the operators that only apply to integers and never
// overflow: & | ^ +% -% *%.
func isBitOp(op token.Kind) bool {
	switch op {
	case token.AMP, token.OR, token.XOR, token.ADD_WRAP, token.SUB_WRAP, token.MUL_WRAP:
		return true
	}
	return false
}

// intLike says whether t is an integer (or an integer type parameter).
func intLike(t Type) bool { return isInteger(t) || t == UntypedInt || tvHas(t, "mod") }

// compoundOps maps each compound assignment to its binary operator.
var compoundOps = map[token.Kind]token.Kind{
	token.ADD_ASSIGN: token.ADD, token.SUB_ASSIGN: token.SUB, token.MUL_ASSIGN: token.MUL,
	token.QUO_ASSIGN: token.QUO, token.REM_ASSIGN: token.REM,
	token.AND_ASSIGN: token.AMP, token.OR_ASSIGN: token.OR, token.XOR_ASSIGN: token.XOR,
	token.SHL_ASSIGN: token.SHL, token.SHR_ASSIGN: token.SHR,
	token.ADD_WRAP_ASSIGN: token.ADD_WRAP, token.SUB_WRAP_ASSIGN: token.SUB_WRAP, token.MUL_WRAP_ASSIGN: token.MUL_WRAP,
}

// CompoundOp returns the binary operator of a compound assignment (+= → +).
func CompoundOp(op token.Kind) token.Kind { return compoundOps[op] }

// shift checks a << n and a >> n. The result's type is a's; n can be of
// any integer type.
func (c *checker) shift(e *ast.BinaryExpr, x, y Type) Type {
	nv, nConst := c.info.Values[e.Y]
	switch {
	case !intLike(y):
		c.errorf(e.OpPos, "the number of bits of %s must be an integer, not %s", e.Op, y)
		return Invalid
	case nConst && nv.(*big.Int).Sign() < 0:
		c.errorf(e.Y.Pos(), "negative shift (%s)", nv)
		return Invalid
	}
	if y == UntypedInt {
		c.convertUntyped(e.Y, Int)
	}
	xv, xConst := c.info.Values[e.X]
	if x == UntypedInt && !(xConst && nConst) {
		c.convertUntyped(e.X, Int) // 1 << n, with a variable n: the 1 is int
		x = Int
	}
	if !intLike(x) {
		c.errorf(e.OpPos, "operator %s only applies to integers, not to %s", e.Op, x)
		return Invalid
	}
	if nConst && isInteger(x) && nv.(*big.Int).Cmp(big.NewInt(int64(IntBits(x)))) >= 0 {
		c.errorf(e.Y.Pos(), "shift of %s bits on a %s, which has %d (use 0 to %d)", nv, x, IntBits(x), IntBits(x)-1)
		return Invalid
	}
	if xConst && nConst {
		n := nv.(*big.Int)
		if n.Cmp(big.NewInt(4096)) > 0 {
			c.errorf(e.Y.Pos(), "shift too large in a constant (%s)", n)
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
				c.errorf(e.OpPos, "integer overflow in the constant: %s", msg)
				return x
			}
		}
		c.info.Values[e] = r
	}
	return x
}

// wrapInt reduces v to t's range, modulo 2^bits (the wrapping arithmetic).
func wrapInt(v *big.Int, t Type) *big.Int {
	bits := uint(IntBits(t))
	m := new(big.Int).Lsh(big.NewInt(1), bits)
	r := new(big.Int).Mod(v, m) // Mod is always >= 0
	if IsSigned(t) && r.Cmp(new(big.Int).Rsh(m, 1)) >= 0 {
		r.Sub(r, m)
	}
	return r
}

// foldBits computes & | ^ +% -% *% between integer constants.
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

// complement computes ^v (inverts the bits) for a constant of type t.
func complement(v *big.Int, t Type) *big.Int {
	r := new(big.Int).Not(v) // -v-1: the signed value
	if isInteger(t) && !IsSigned(t) {
		return wrapInt(r, t)
	}
	return r
}
