package checker

import (
	"fmt"
	"math"
	"math/big"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Numbers
//
// Integers with an explicit size (int8, int16, int32, int = 64 bits; byte =
// 8 unsigned bits, uint16, uint32, uint64) and reals (float32, float64).
// Arithmetic only between values of the same type: mixing requires converting, with
// int32(x), float64(n)... Integer overflow panics, at every size.
//
// Literals are untyped constants: 5 becomes int32 in an arithmetic with int32, and
// float64 in a float64 variable. Without context, they become int (5) or float64
// (2.5). Constant-only expressions are computed at compile time, with
// overflow and division by zero as compile errors.
//
// Constant values live in Info.Values: *big.Int for integers (and bytes),
// float64 for reals, string and bool.

var (
	Int8    = &Basic{"int8"}
	Int16   = &Basic{"int16"}
	Int32   = &Basic{"int32"}
	Uint16  = &Basic{"uint16"}
	Uint32  = &Basic{"uint32"}
	Uint64  = &Basic{"uint64"}
	Float32 = &Basic{"float32"}
	Float64 = &Basic{"float64"}

	UntypedInt   = &Basic{"integer constant"}
	UntypedFloat = &Basic{"decimal constant"}
)

type intKind struct {
	bits   int
	signed bool
}

var intKinds = map[Type]intKind{
	Int8: {8, true}, Int16: {16, true}, Int32: {32, true}, Int: {64, true},
	Byte: {8, false}, Uint16: {16, false}, Uint32: {32, false}, Uint64: {64, false},
}

func isInteger(t Type) bool { _, ok := intKinds[t]; return ok }
func isFloat(t Type) bool   { return t == Float32 || t == Float64 }
func isNumeric(t Type) bool { return isInteger(t) || isFloat(t) }
func isUntyped(t Type) bool { return t == UntypedInt || t == UntypedFloat }

// IsInteger, IsSigned and IntBits describe the integer types for the generator.
func IsInteger(t Type) bool { return isInteger(t) }
func IsFloat(t Type) bool   { return isFloat(t) }
func IsSigned(t Type) bool  { return intKinds[t].signed }
func IntBits(t Type) int    { return intKinds[t].bits }

// DefaultType is the type an untyped constant takes without context.
func DefaultType(t Type) Type {
	switch t {
	case UntypedInt:
		return Int
	case UntypedFloat:
		return Float64
	}
	return t
}

// IntRange returns the smallest and the largest value of an integer type.
func IntRange(t Type) (lo, hi *big.Int) {
	k := intKinds[t]
	one := big.NewInt(1)
	if k.signed {
		hi = new(big.Int).Lsh(one, uint(k.bits-1))
		lo = new(big.Int).Neg(hi)
		hi.Sub(hi, one)
		return lo, hi
	}
	hi = new(big.Int).Lsh(one, uint(k.bits))
	return big.NewInt(0), hi.Sub(hi, one)
}

// representable converts the constant value v to the type t, or explains why
// it doesn't fit.
func representable(v any, t Type) (any, string) {
	switch {
	case isInteger(t):
		var b *big.Int
		switch v := v.(type) {
		case *big.Int:
			b = v
		case float64:
			return nil, fmt.Sprintf("decimal constant %s isn't an integer: use %s(x) to convert a value, or write an integer", formatFloat(v), t)
		default:
			return nil, fmt.Sprintf("non-numeric constant doesn't fit in %s", t)
		}
		lo, hi := IntRange(t)
		if b.Cmp(lo) < 0 || b.Cmp(hi) > 0 {
			return nil, fmt.Sprintf("constant %s doesn't fit in %s (%s to %s)", b, t, lo, hi)
		}
		return b, ""
	case isFloat(t):
		var f float64
		switch v := v.(type) {
		case *big.Int:
			f, _ = new(big.Float).SetInt(v).Float64()
		case float64:
			f = v
		default:
			return nil, fmt.Sprintf("non-numeric constant doesn't fit in %s", t)
		}
		if math.IsInf(f, 0) || (t == Float32 && math.Abs(f) > math.MaxFloat32) {
			return nil, fmt.Sprintf("constant %s doesn't fit in %s", formatFloat(f), t)
		}
		if t == Float32 {
			f = float64(float32(f))
		}
		return f, ""
	}
	return nil, fmt.Sprintf("constant doesn't fit in %s", t)
}

func formatFloat(f float64) string { return fmt.Sprint(f) }

// convertUntyped gives the type target to an untyped constant and checks whether the value
// fits. Returns false if target isn't numeric (the caller reports the error).
func (c *checker) convertUntyped(e ast.Expr, target Type) bool {
	t := c.info.Types[e]
	if !isUntyped(t) {
		return true
	}
	if _, isVar := target.(*TypeVar); isVar {
		// in the generic definition: the value is checked in each instance
		if !numLike(target) {
			return false
		}
		c.info.Types[e] = target
		return true
	}
	if !isNumeric(target) {
		return false
	}
	c.info.Types[e] = target
	v, ok := c.info.Values[e]
	if !ok {
		return true // there was an error in the constant; it was already reported
	}
	nv, msg := representable(v, target)
	if msg != "" {
		c.errorf(e.Pos(), "%s", msg)
		delete(c.info.Values, e)
		return true
	}
	c.info.Values[e] = nv
	return true
}

// valueAs checks an expression that goes to a place of type want (variable,
// argument, return, field). An untyped constant takes want.
func (c *checker) valueAs(e ast.Expr, want Type, context string) Type {
	savedT, savedE := c.expected, c.expectedFor
	c.expected, c.expectedFor = want, e
	t := c.value(e)
	c.expected, c.expectedFor = savedT, savedE
	if isUntyped(t) && want != Invalid {
		if c.convertUntyped(e, want) {
			return want
		}
		c.errorf(e.Pos(), "%s: expected %s, found %s", context, want, t)
		return Invalid
	}
	c.assignable(t, want, e.Pos(), context)
	return t
}

// valueDefault checks an expression without an expected type: an untyped constant
// becomes int or float64.
func (c *checker) valueDefault(e ast.Expr) Type {
	t := c.value(e)
	if isUntyped(t) {
		c.convertUntyped(e, DefaultType(t))
		return DefaultType(t)
	}
	return t
}

// intValue checks an expression that must be int (index, size).
func (c *checker) intValue(e ast.Expr, what string) {
	t := c.value(e)
	if isUntyped(t) {
		c.convertUntyped(e, Int)
		return
	}
	if t != Int && t != Invalid {
		c.errorf(e.Pos(), "%s must be int, found %s", what, t)
	}
}

func (c *checker) unary(e *ast.UnaryExpr) Type {
	switch e.Op {
	case token.AMP:
		c.errorf(e.P, "& can only appear as a call argument, to borrow: f(&x)")
		c.value(e.X)
		return Invalid
	case token.MUL:
		t := c.value(e.X)
		if t == Invalid {
			return Invalid
		}
		if !isPointerType(t) {
			c.errorf(e.P, "operator * only applies to pointers and borrows, not to %s", t)
			return Invalid
		}
		elem, _ := c.deref(t, e.X.Pos(), exprName(e.X))
		if isExternStruct(elem) {
			c.errorf(e.P, "%s is an opaque C type: its content can't be read", elem)
			return Invalid
		}
		return elem
	}
	t := c.value(e.X)
	switch {
	case t == Invalid:
		return Invalid
	case e.Op == token.NOT && t != Bool:
		c.errorf(e.P, "operator ! doesn't apply to %s", t)
		return Invalid
	case e.Op == token.XOR && !intLike(t):
		c.errorf(e.P, "operator ^ (bit inversion) only applies to integers, not to %s", t)
		return Invalid
	case e.Op == token.SUB && !numLike(t) && !isUntyped(t):
		c.errorf(e.P, "operator - doesn't apply to %s", t)
		return Invalid
	case e.Op == token.SUB && isInteger(t) && !IsSigned(t):
		c.errorf(e.P, "operator - doesn't apply to %s, which is unsigned", t)
		return Invalid
	}
	if v, ok := c.info.Values[e.X]; ok {
		if r, msg := foldUnary(e.Op, v, t); msg != "" {
			c.errorf(e.P, "%s", msg)
		} else {
			c.info.Values[e] = r
		}
	}
	return t
}

func foldUnary(op token.Kind, v any, t Type) (any, string) {
	if b, ok := v.(*big.Int); ok && op == token.XOR {
		return complement(b, t), ""
	}
	switch v := v.(type) {
	case bool:
		return !v, ""
	case float64:
		return -v, ""
	case *big.Int:
		r := new(big.Int).Neg(v)
		if isInteger(t) {
			if _, msg := representable(r, t); msg != "" {
				return nil, "integer overflow in the constant: " + msg
			}
		}
		return r, ""
	}
	return nil, "invalid constant"
}

func (c *checker) binary(e *ast.BinaryExpr) Type {
	x, y := c.value(e.X), c.value(e.Y)
	if x == Invalid || y == Invalid {
		return Invalid
	}
	if e.Op == token.SHL || e.Op == token.SHR {
		return c.shift(e, x, y)
	}
	// An untyped constant takes the type of the other side: in n + 1, with n
	// int32, the 1 is int32.
	switch {
	case isUntyped(x) && !isUntyped(y) && numLike(y):
		c.convertUntyped(e.X, y)
		x = y
	case isUntyped(y) && !isUntyped(x) && numLike(x):
		c.convertUntyped(e.Y, x)
		y = x
	case isUntyped(x) && isUntyped(y) && x != y:
		x, y = UntypedFloat, UntypedFloat // 1 + 2.5
	}
	mismatch := func() Type {
		hint := ""
		if isNumeric(x) && isNumeric(y) {
			hint = fmt.Sprintf(" (convert one of the sides: %s(x) or %s(x))", x, y)
		}
		c.errorf(e.OpPos, "operator %s doesn't apply to %s and %s%s", e.Op, x, y, hint)
		return Invalid
	}
	var result Type
	switch e.Op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		switch {
		case e.Op == token.ADD && x == String && y == String:
			c.errorf(e.OpPos, "string concatenation requires explicit allocation: build the text in a buffer (see docs/text.md)")
			return Invalid
		case x != y || (!numLike(x) && !isUntyped(x)):
			return mismatch()
		case e.Op == token.REM && (isFloat(x) || x == UntypedFloat || (abstract(x) && !tvHas(x, "mod"))):
			c.errorf(e.OpPos, "operator %% doesn't apply to %s (only to integers)", x)
			return Invalid
		}
		result = x
	case token.AMP, token.OR, token.XOR, token.ADD_WRAP, token.SUB_WRAP, token.MUL_WRAP:
		switch {
		case x != y && intLike(x) && intLike(y):
			return mismatch()
		case !intLike(x) || !intLike(y):
			c.errorf(e.OpPos, "operator %s only applies to integers, not to %s", e.Op, x)
			return Invalid
		}
		result = x
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		if x != y || (!orderLike(x) && !isUntyped(x)) {
			return mismatch()
		}
		result = Bool
	case token.EQL, token.NEQ:
		switch {
		case x == NilType || y == NilType:
			return c.nilComparison(e, x, y)
		case isPointerType(x) || isPointerType(y):
			c.errorf(e.OpPos, "pointers can only be compared with nil")
			return Invalid
		case c.noSliceCompare(e, x) || c.noSliceCompare(e, y):
			return Invalid
		case isFuncType(x) || isFuncType(y):
			c.errorf(e.OpPos, "fn values can't be compared")
			return Invalid
		case ownedVar(x) || ownedVar(y):
			c.errorf(e.OpPos, "values of an owned T can't be compared (T may be an owner)")
			return Invalid
		case x != y:
			c.errorf(e.OpPos, "comparison between different types: %s and %s", x, y)
			return Invalid
		case isResource(x):
			c.errorf(e.OpPos, "structs that contain owning pointers can't be compared with ==")
			return Invalid
		case isEnum(x) && IsOwner(x):
			c.errorf(e.OpPos, "enums that hold owners can't be compared with ==: use match")
			return Invalid
		}
		result = Bool
	case token.LAND, token.LOR:
		if x != Bool || y != Bool {
			return mismatch()
		}
		result = Bool
	default:
		panic(fmt.Sprintf("checker: unknown binary operator %s", e.Op))
	}
	a, okA := c.info.Values[e.X]
	b, okB := c.info.Values[e.Y]
	if okA && okB {
		if r, msg := foldBinary(e.Op, a, b, x); msg != "" {
			c.errorf(e.OpPos, "%s", msg)
		} else if r != nil {
			c.info.Values[e] = r
		}
	}
	return result
}

// foldBinary computes an operation between constants. t is the operands' type.
func foldBinary(op token.Kind, a, b any, t Type) (any, string) {
	// With a real t, integers become reals (1 + 2.5).
	if isFloat(t) || t == UntypedFloat {
		a, b = toFloat(a), toFloat(b)
	}
	if isBitOp(op) {
		if a, ok := a.(*big.Int); ok {
			return foldBits(op, a, b.(*big.Int), t), ""
		}
		return nil, ""
	}
	switch a := a.(type) {
	case *big.Int:
		b := b.(*big.Int)
		r := new(big.Int)
		switch op {
		case token.ADD:
			r.Add(a, b)
		case token.SUB:
			r.Sub(a, b)
		case token.MUL:
			r.Mul(a, b)
		case token.QUO, token.REM:
			if b.Sign() == 0 {
				return nil, "division by zero in the constant"
			}
			if op == token.QUO {
				r.Quo(a, b) // truncates toward zero, like C
			} else {
				r.Rem(a, b)
			}
		default:
			return compareInts(op, a.Cmp(b)), ""
		}
		if isInteger(t) {
			if _, msg := representable(r, t); msg != "" {
				return nil, "integer overflow in the constant: " + msg
			}
		}
		return r, ""
	case float64:
		b := b.(float64)
		switch op {
		case token.ADD:
			return roundFloat(a+b, t), ""
		case token.SUB:
			return roundFloat(a-b, t), ""
		case token.MUL:
			return roundFloat(a*b, t), ""
		case token.QUO:
			if b == 0 {
				return nil, "division by zero in the constant"
			}
			return roundFloat(a/b, t), ""
		}
		cmp := 0
		if a < b {
			cmp = -1
		} else if a > b {
			cmp = 1
		}
		return compareInts(op, cmp), ""
	case string:
		eq := a == b.(string)
		return eq == (op == token.EQL), ""
	case bool:
		b := b.(bool)
		switch op {
		case token.LAND:
			return a && b, ""
		case token.LOR:
			return a || b, ""
		}
		return (a == b) == (op == token.EQL), ""
	}
	return nil, ""
}

func compareInts(op token.Kind, cmp int) bool {
	switch op {
	case token.EQL:
		return cmp == 0
	case token.NEQ:
		return cmp != 0
	case token.LSS:
		return cmp < 0
	case token.LEQ:
		return cmp <= 0
	case token.GTR:
		return cmp > 0
	}
	return cmp >= 0
}

func toFloat(v any) any {
	if b, ok := v.(*big.Int); ok {
		f, _ := new(big.Float).SetInt(b).Float64()
		return f
	}
	return v
}

func roundFloat(f float64, t Type) float64 {
	if t == Float32 {
		return float64(float32(f))
	}
	return f
}

// conversion checks int(x), byte(x), float64(x) and the other conversions between
// numbers. Converting a constant is checked at compile time; a value, at
// run time (panic if it doesn't fit).
func (c *checker) conversion(e *ast.CallExpr, target Type) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "%s(x) takes one value", target)
		c.checkArgs(e)
		return Invalid
	}
	a := e.Args[0]
	t := c.value(a)
	switch {
	case t == Invalid:
		return Invalid
	case !numLike(target) || (!numLike(t) && !isUntyped(t)):
		c.errorf(a.Pos(), "can't convert %s to %s", t, target)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: Convert, Elem: target}
	if abstract(target) || abstract(t) {
		if isUntyped(t) {
			c.convertUntyped(a, target)
		}
		return target // in the generic definition, the conversion is checked in each instance
	}
	if f, ok := c.info.Values[a].(float64); ok && t == UntypedFloat && isInteger(target) {
		// explicit conversion of a decimal constant: truncates, as int(x) does
		// at run time (int(-2.9) is -2)
		c.info.Values[a], _ = new(big.Float).SetFloat64(math.Trunc(f)).Int(nil)
		c.info.Types[a] = UntypedInt
		t = UntypedInt
	}
	if isUntyped(t) {
		c.convertUntyped(a, target)
		if v, ok := c.info.Values[a]; ok {
			c.info.Values[e] = v
		}
		return target
	}
	if v, ok := c.info.Values[a]; ok {
		// conversion of a typed constant: computed here
		if f, isF := v.(float64); isF && isInteger(target) {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				c.errorf(a.Pos(), "constant %s doesn't fit in %s", formatFloat(f), target)
				return target
			}
			v, _ = new(big.Float).SetFloat64(math.Trunc(f)).Int(nil)
		}
		if r, msg := representable(v, target); msg != "" {
			c.errorf(a.Pos(), "%s", msg)
		} else {
			c.info.Values[e] = r
		}
	}
	return target
}
