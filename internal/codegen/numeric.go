package codegen

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/token"
)

// Numbers in the generated C
//
// Each integer type has its operations checked in the runtime (mlc_add_i32,
// mlc_div_u64, ...), which panic on overflow and division by zero.
// Reals use C's operations directly: in floating point, overflow becomes
// infinity and division by zero gives infinity or NaN, without undefined
// behavior.

var numericC = map[checker.Type]string{
	checker.Int8: "int8_t", checker.Int16: "int16_t", checker.Int32: "int32_t", checker.Int: "int64_t",
	checker.Byte: "uint8_t", checker.Uint16: "uint16_t", checker.Uint32: "uint32_t", checker.Uint64: "uint64_t",
	checker.Float32: "float", checker.Float64: "double",
}

// suffix names the type in the runtime's functions: i8, i32, u64...
var suffix = map[checker.Type]string{
	checker.Int8: "i8", checker.Int16: "i16", checker.Int32: "i32", checker.Int: "i64",
	checker.Byte: "u8", checker.Uint16: "u16", checker.Uint32: "u32", checker.Uint64: "u64",
	checker.Float32: "f32", checker.Float64: "f64",
}

var opNames = map[token.Kind]string{
	token.ADD: "add", token.SUB: "sub", token.MUL: "mul", token.QUO: "div", token.REM: "mod",
}

// typeOf returns e's final type (an untyped constant becomes int or float64).
func (g *gen) typeOf(e ast.Expr) checker.Type {
	return checker.DefaultType(g.info.Types[e])
}

// intOps are the integer-only operators: bits, shifts and the wrapping
// arithmetic.
var intOps = map[token.Kind]string{
	token.AMP: "&", token.OR: "|", token.XOR: "^",
	token.SHL: "shl", token.SHR: "shr",
	token.ADD_WRAP: "addw", token.SUB_WRAP: "subw", token.MUL_WRAP: "mulw",
}

// binop writes the binary operation op (arithmetic or integer) between a and
// b, of type t.
func binop(op token.Kind, t checker.Type, a, b string) string {
	switch op {
	case token.AMP, token.OR, token.XOR:
		return fmt.Sprintf("((%s)(%s %s %s))", numericC[t], a, intOps[op], b)
	case token.SHL, token.SHR:
		return fmt.Sprintf("mlc_%s_%s(%s, (int64_t)(%s), __FILE__, __LINE__)", intOps[op], suffix[t], a, b)
	case token.ADD_WRAP, token.SUB_WRAP, token.MUL_WRAP:
		return fmt.Sprintf("mlc_%s_%s(%s, %s)", intOps[op], suffix[t], a, b)
	}
	return arith(op, t, a, b)
}

// arith writes the operation op between a and b, of type t.
func arith(op token.Kind, t checker.Type, a, b string) string {
	if checker.IsFloat(t) {
		return fmt.Sprintf("(%s %s %s)", a, op, b)
	}
	return fmt.Sprintf("mlc_%s_%s(%s, %s, __FILE__, __LINE__)", opNames[op], suffix[t], a, b)
}

func neg(t checker.Type, x string) string {
	if checker.IsFloat(t) {
		return "(-" + x + ")"
	}
	return fmt.Sprintf("mlc_neg_%s(%s, __FILE__, __LINE__)", suffix[t], x)
}

// constant writes the constant value v as a C literal of type t.
func constant(v any, t checker.Type) string {
	switch v := v.(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	case string:
		return fmt.Sprintf("MLC_STR_LIT(%s, %d)", cString(v), len(v))
	case float64:
		return floatLit(v, t)
	case *big.Int:
		if checker.IsFloat(t) {
			f, _ := new(big.Float).SetInt(v).Float64()
			return floatLit(f, t)
		}
		return intLit(v, t)
	}
	panic(fmt.Sprintf("codegen: constant of unexpected type %T", v))
}

func intLit(v *big.Int, t checker.Type) string {
	if !checker.IsSigned(t) {
		lit := "UINT64_C(" + v.String() + ")"
		if t == checker.Uint64 {
			return lit
		}
		return "((" + numericC[t] + ")" + lit + ")"
	}
	var lit string
	if v.Cmp(big.NewInt(math.MinInt64)) == 0 {
		lit = "INT64_MIN" // -9223372036854775808 isn't a valid literal in C
	} else {
		lit = "INT64_C(" + v.String() + ")"
	}
	if t == checker.Int {
		return lit
	}
	return "((" + numericC[t] + ")" + lit + ")"
}

func floatLit(f float64, t checker.Type) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEn") { // "n": Inf and NaN don't get here, but just in case
		s += ".0"
	}
	if t == checker.Float32 {
		return "((float)" + s + ")"
	}
	return s
}

// convert translates the conversions between numbers. Widening is just a cast; what
// may not fit goes through a runtime check, which panics.
func (g *gen) convert(e *ast.CallExpr, callee checker.Callee) string {
	x := g.expr(e.Args[0])
	from, to := g.typeOf(e.Args[0]), callee.Elem
	cast := func(s string) string { return "((" + numericC[to] + ")(" + s + "))" }
	switch {
	case from == to:
		return x
	case checker.IsFloat(to) && checker.IsFloat(from):
		if to == checker.Float32 {
			return "mlc_f64_to_f32(" + x + ")" // overflow becomes infinity, without undefined behavior
		}
		return cast(x)
	case checker.IsFloat(to):
		return cast(x) // integer to real: always defined
	case checker.IsFloat(from):
		lo, hi := floatBounds(to)
		return cast(fmt.Sprintf("mlc_conv_f((double)(%s), %s, %s, %q, __FILE__, __LINE__)", x, lo, hi, to.String()))
	}
	// integer to integer
	lo, hi := checker.IntRange(to)
	flo, fhi := checker.IntRange(from)
	if lo.Cmp(flo) <= 0 && hi.Cmp(fhi) >= 0 {
		return cast(x) // every value of from fits in to
	}
	if checker.IsSigned(from) {
		// the value fits in int64: checks against to's range (cut to int64)
		maxI64 := big.NewInt(math.MaxInt64)
		if hi.Cmp(maxI64) > 0 {
			hi = maxI64
		}
		return cast(fmt.Sprintf("mlc_conv_i((int64_t)(%s), %s, %s, %q, __FILE__, __LINE__)",
			x, intLit(lo, checker.Int), intLit(hi, checker.Int), to.String()))
	}
	return cast(fmt.Sprintf("mlc_conv_u((uint64_t)(%s), %s, %q, __FILE__, __LINE__)",
		x, intLit(hi, checker.Uint64), to.String()))
}

// floatBounds gives the exclusive bounds for converting a real to the integer t:
// the value, truncated, fits in t if lo < x < hi.
func floatBounds(t checker.Type) (string, string) {
	bits := checker.IntBits(t)
	var lo, hi float64
	if checker.IsSigned(t) {
		hi = math.Ldexp(1, bits-1)
		lo = -hi - 1
		if lo == -hi { // -2^63 - 1 doesn't exist in double: uses the previous one
			lo = math.Nextafter(-hi, math.Inf(-1))
		}
	} else {
		hi = math.Ldexp(1, bits)
		lo = -1
	}
	return floatLit(lo, checker.Float64), floatLit(hi, checker.Float64)
}
