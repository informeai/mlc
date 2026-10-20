package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/token"
)

// Evaluation order
//
// MLC guarantees that operands are evaluated left to right:
// call arguments, the two sides of an operator and the fields of a
// literal. C doesn't guarantee that: in f(a(), b()) the C compiler may call
// b() first. When the order can be observed, the generator computes the
// operands into temporaries, sequenced by C's comma operator:
//
//	sum(a(), b())  →  (mlc_t1 = mlc_fn_a(), mlc_fn_sum(mlc_t1, mlc_fn_b()))
//
// The temporaries are declared at the start of the function. The last operand that
// isn't a literal stays in place, because nothing is evaluated after it.

// effectful says whether evaluating e can have an observable effect: calling a function
// or panicking (arithmetic can overflow or divide by zero).
func (g *gen) effectful(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.CallExpr:
		return true
	case *ast.IndexExpr:
		return true // can panic out of bounds
	case *ast.SliceExpr:
		return true // can panic out of bounds
	case *ast.BinaryExpr:
		if _, isArith := opNames[e.Op]; isArith || e.Op == token.SHL || e.Op == token.SHR {
			return true // can panic (overflow, division by zero, shift)
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

// isLiteral says whether e is a fixed value, which can be evaluated at any moment.
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
		return isConst || g.info.Locals[e] == nil // constant, true or false
	case *ast.UnaryExpr:
		_, isOut := g.info.Types[e].(*checker.OutHandle)
		return isOut // &h of a &?*X parameter: an address, without effects
	}
	return false
}

// operands translates the operands of an operation. pre are the assignments to
// temporaries that must run first, in order; vals are the values to use.
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

// sequence joins the assignments of pre and the final expression with the comma operator.
func sequence(pre []string, final string) string {
	if len(pre) == 0 {
		return final
	}
	return "(" + strings.Join(pre, ", ") + ", " + final + ")"
}

// temp creates a temporary of type t, declared at the start of the function.
func (g *gen) temp(t checker.Type) string {
	name := g.newName("mlc_t")
	g.temps = append(g.temps, cType(t)+" "+name)
	return name
}

// binary translates a binary operator. top marks the highest-level
// expression of a condition, which comes out without outer parentheses ("if ((a == b))"
// produces a clang warning).
func (g *gen) binary(e *ast.BinaryExpr, top bool) string {
	if e.Op == token.LAND || e.Op == token.LOR {
		// C already evaluates && and || left to right, with short-circuiting.
		s := fmt.Sprintf("%s %s %s", g.expr(e.X), e.Op, g.expr(e.Y))
		if !top {
			s = "(" + s + ")"
		}
		return s
	}
	pre, v := g.operands([]ast.Expr{e.X, e.Y})
	var core string
	bare := false // core is "x op y", without its own parentheses
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
