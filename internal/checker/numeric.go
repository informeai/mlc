package checker

import (
	"fmt"
	"math"
	"math/big"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Números
//
// Inteiros com tamanho explícito (int8, int16, int32, int = 64 bits; byte =
// 8 bits sem sinal, uint16, uint32, uint64) e decimais (float32, float64).
// Contas só entre valores do mesmo tipo: misturar exige converter, com
// int32(x), float64(n)... Estouro de inteiro dá panic, em todos os tamanhos.
//
// Literais são constantes sem tipo: 5 vira int32 numa conta com int32, e
// float64 numa variável float64. Sem contexto, viram int (5) ou float64
// (2.5). Expressões só de constantes são calculadas na compilação, com
// estouro e divisão por zero como erro de compilação.
//
// Valores constantes ficam em Info.Values: *big.Int para inteiros (e bytes),
// float64 para decimais, string e bool.

var (
	Int8    = &Basic{"int8"}
	Int16   = &Basic{"int16"}
	Int32   = &Basic{"int32"}
	Uint16  = &Basic{"uint16"}
	Uint32  = &Basic{"uint32"}
	Uint64  = &Basic{"uint64"}
	Float32 = &Basic{"float32"}
	Float64 = &Basic{"float64"}

	UntypedInt   = &Basic{"constante inteira"}
	UntypedFloat = &Basic{"constante decimal"}
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

// IsInteger, IsSigned e IntBits descrevem os tipos inteiros para o gerador.
func IsInteger(t Type) bool { return isInteger(t) }
func IsFloat(t Type) bool   { return isFloat(t) }
func IsSigned(t Type) bool  { return intKinds[t].signed }
func IntBits(t Type) int    { return intKinds[t].bits }

// DefaultType é o tipo que uma constante sem tipo assume sem contexto.
func DefaultType(t Type) Type {
	switch t {
	case UntypedInt:
		return Int
	case UntypedFloat:
		return Float64
	}
	return t
}

// IntRange devolve o menor e o maior valor de um tipo inteiro.
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

// representable converte o valor constante v para o tipo t, ou explica por
// que não cabe.
func representable(v any, t Type) (any, string) {
	switch {
	case isInteger(t):
		var b *big.Int
		switch v := v.(type) {
		case *big.Int:
			b = v
		case float64:
			return nil, fmt.Sprintf("constante decimal %s não é inteira: use %s(x) para converter um valor, ou escreva um inteiro", formatFloat(v), t)
		default:
			return nil, fmt.Sprintf("constante não numérica não cabe em %s", t)
		}
		lo, hi := IntRange(t)
		if b.Cmp(lo) < 0 || b.Cmp(hi) > 0 {
			return nil, fmt.Sprintf("constante %s não cabe em %s (%s a %s)", b, t, lo, hi)
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
			return nil, fmt.Sprintf("constante não numérica não cabe em %s", t)
		}
		if math.IsInf(f, 0) || (t == Float32 && math.Abs(f) > math.MaxFloat32) {
			return nil, fmt.Sprintf("constante %s não cabe em %s", formatFloat(f), t)
		}
		if t == Float32 {
			f = float64(float32(f))
		}
		return f, ""
	}
	return nil, fmt.Sprintf("constante não cabe em %s", t)
}

func formatFloat(f float64) string { return fmt.Sprint(f) }

// convertUntyped dá o tipo target a uma constante sem tipo e checa se o valor
// cabe. Devolve false se target não é numérico (quem chama reporta o erro).
func (c *checker) convertUntyped(e ast.Expr, target Type) bool {
	t := c.info.Types[e]
	if !isUntyped(t) {
		return true
	}
	if _, isVar := target.(*TypeVar); isVar {
		// na definição genérica: o valor é checado em cada instância
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
		return true // houve erro na constante; já foi reportado
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

// valueAs checa uma expressão que vai para um lugar do tipo want (variável,
// argumento, retorno, campo). Uma constante sem tipo assume want.
func (c *checker) valueAs(e ast.Expr, want Type, context string) Type {
	savedT, savedE := c.expected, c.expectedFor
	c.expected, c.expectedFor = want, e
	t := c.value(e)
	c.expected, c.expectedFor = savedT, savedE
	if isUntyped(t) && want != Invalid {
		if c.convertUntyped(e, want) {
			return want
		}
		c.errorf(e.Pos(), "%s: esperado %s, encontrado %s", context, want, t)
		return Invalid
	}
	c.assignable(t, want, e.Pos(), context)
	return t
}

// valueDefault checa uma expressão sem tipo esperado: uma constante sem tipo
// vira int ou float64.
func (c *checker) valueDefault(e ast.Expr) Type {
	t := c.value(e)
	if isUntyped(t) {
		c.convertUntyped(e, DefaultType(t))
		return DefaultType(t)
	}
	return t
}

// intValue checa uma expressão que precisa ser int (índice, tamanho).
func (c *checker) intValue(e ast.Expr, what string) {
	t := c.value(e)
	if isUntyped(t) {
		c.convertUntyped(e, Int)
		return
	}
	if t != Int && t != Invalid {
		c.errorf(e.Pos(), "%s precisa ser int, encontrado %s", what, t)
	}
}

func (c *checker) unary(e *ast.UnaryExpr) Type {
	switch e.Op {
	case token.AMP:
		c.errorf(e.P, "& só pode aparecer como argumento de chamada, para emprestar: f(&x)")
		c.value(e.X)
		return Invalid
	case token.MUL:
		t := c.value(e.X)
		if t == Invalid {
			return Invalid
		}
		if !isPointerType(t) {
			c.errorf(e.P, "operador * só se aplica a ponteiros e empréstimos, não a %s", t)
			return Invalid
		}
		elem, _ := c.deref(t, e.X.Pos(), exprName(e.X))
		if isExternStruct(elem) {
			c.errorf(e.P, "%s é um tipo de C opaco: não dá para ler o conteúdo", elem)
			return Invalid
		}
		return elem
	}
	t := c.value(e.X)
	switch {
	case t == Invalid:
		return Invalid
	case e.Op == token.NOT && t != Bool:
		c.errorf(e.P, "operador ! não se aplica a %s", t)
		return Invalid
	case e.Op == token.XOR && !intLike(t):
		c.errorf(e.P, "operador ^ (inverter os bits) só se aplica a inteiros, não a %s", t)
		return Invalid
	case e.Op == token.SUB && !numLike(t) && !isUntyped(t):
		c.errorf(e.P, "operador - não se aplica a %s", t)
		return Invalid
	case e.Op == token.SUB && isInteger(t) && !IsSigned(t):
		c.errorf(e.P, "operador - não se aplica a %s, que não tem sinal", t)
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
				return nil, "estouro de inteiro na constante: " + msg
			}
		}
		return r, ""
	}
	return nil, "constante inválida"
}

func (c *checker) binary(e *ast.BinaryExpr) Type {
	x, y := c.value(e.X), c.value(e.Y)
	if x == Invalid || y == Invalid {
		return Invalid
	}
	if e.Op == token.SHL || e.Op == token.SHR {
		return c.shift(e, x, y)
	}
	// Uma constante sem tipo assume o tipo do outro lado: em n + 1, com n
	// int32, o 1 é int32.
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
			hint = fmt.Sprintf(" (converta um dos lados: %s(x) ou %s(x))", x, y)
		}
		c.errorf(e.OpPos, "operador %s não se aplica a %s e %s%s", e.Op, x, y, hint)
		return Invalid
	}
	var result Type
	switch e.Op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		switch {
		case e.Op == token.ADD && x == String && y == String:
			c.errorf(e.OpPos, "concatenação de strings exige alocação explícita: monte o texto num buffer (veja docs/texto.md)")
			return Invalid
		case x != y || (!numLike(x) && !isUntyped(x)):
			return mismatch()
		case e.Op == token.REM && (isFloat(x) || x == UntypedFloat || (abstract(x) && !tvHas(x, "mod"))):
			c.errorf(e.OpPos, "operador %% não se aplica a %s (só a inteiros)", x)
			return Invalid
		}
		result = x
	case token.AMP, token.OR, token.XOR, token.ADD_WRAP, token.SUB_WRAP, token.MUL_WRAP:
		switch {
		case x != y && intLike(x) && intLike(y):
			return mismatch()
		case !intLike(x) || !intLike(y):
			c.errorf(e.OpPos, "operador %s só se aplica a inteiros, não a %s", e.Op, x)
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
			c.errorf(e.OpPos, "ponteiros só podem ser comparados com nil")
			return Invalid
		case c.noSliceCompare(e, x) || c.noSliceCompare(e, y):
			return Invalid
		case isFuncType(x) || isFuncType(y):
			c.errorf(e.OpPos, "valores fn não podem ser comparados")
			return Invalid
		case ownedVar(x) || ownedVar(y):
			c.errorf(e.OpPos, "valores de um T owned não podem ser comparados (T pode ser um dono)")
			return Invalid
		case x != y:
			c.errorf(e.OpPos, "comparação entre tipos diferentes: %s e %s", x, y)
			return Invalid
		case isResource(x):
			c.errorf(e.OpPos, "structs que contêm ponteiros donos não podem ser comparadas com ==")
			return Invalid
		case isEnum(x) && IsOwner(x):
			c.errorf(e.OpPos, "enums que guardam donos não podem ser comparados com ==: use match")
			return Invalid
		}
		result = Bool
	case token.LAND, token.LOR:
		if x != Bool || y != Bool {
			return mismatch()
		}
		result = Bool
	default:
		panic(fmt.Sprintf("checker: operador binário desconhecido %s", e.Op))
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

// foldBinary calcula uma operação entre constantes. t é o tipo dos operandos.
func foldBinary(op token.Kind, a, b any, t Type) (any, string) {
	// Com t decimal, inteiros viram decimais (1 + 2.5).
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
				return nil, "divisão por zero na constante"
			}
			if op == token.QUO {
				r.Quo(a, b) // trunca em direção a zero, como o C
			} else {
				r.Rem(a, b)
			}
		default:
			return compareInts(op, a.Cmp(b)), ""
		}
		if isInteger(t) {
			if _, msg := representable(r, t); msg != "" {
				return nil, "estouro de inteiro na constante: " + msg
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
				return nil, "divisão por zero na constante"
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

// conversion checa int(x), byte(x), float64(x) e as demais conversões entre
// números. Converter uma constante é checado na compilação; um valor, em
// tempo de execução (panic se não couber).
func (c *checker) conversion(e *ast.CallExpr, target Type) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "%s(x) recebe um valor", target)
		c.checkArgs(e)
		return Invalid
	}
	a := e.Args[0]
	t := c.value(a)
	switch {
	case t == Invalid:
		return Invalid
	case !numLike(target) || (!numLike(t) && !isUntyped(t)):
		c.errorf(a.Pos(), "não é possível converter %s em %s", t, target)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: Convert, Elem: target}
	if abstract(target) || abstract(t) {
		if isUntyped(t) {
			c.convertUntyped(a, target)
		}
		return target // na definição genérica, a conversão é checada em cada instância
	}
	if f, ok := c.info.Values[a].(float64); ok && t == UntypedFloat && isInteger(target) {
		// conversão explícita de constante decimal: trunca, como int(x) faz
		// em tempo de execução (int(-2.9) é -2)
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
		// conversão de constante com tipo: calculada aqui
		if f, isF := v.(float64); isF && isInteger(target) {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				c.errorf(a.Pos(), "constante %s não cabe em %s", formatFloat(f), target)
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
