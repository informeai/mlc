package checker

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
)

// Functions as values
//
//	var less fn(&int, &int) bool = compare
//	sort.sort_by(&s, compare)
//	fn apply(f fn(int) int, x int) int { return f(x) }
//
// An fn value is just the address of a function: it doesn't capture variables (there are no
// closures), so it doesn't allocate and has no owner; it can be copied freely. The
// parameter types say what the call does with each argument (copies,
// borrows or moves), as in a direct call, and the ownership checker
// checks it the same way.
//
// The zero value of an fn (var f fn(int) int, an unfilled field, alloc's
// memory) is a function that panics: there's never a null function pointer.
// Methods, generic functions without the types and extern fn don't become values.

// FuncType is the type of an fn value.
type FuncType struct {
	Params []Type
	Result Type // Void when it returns nothing
	ID     int  // name in C: mlc_fnt_<ID>
}

func (f *FuncType) String() string {
	var ps []string
	for _, p := range f.Params {
		ps = append(ps, p.String())
	}
	s := "fn(" + strings.Join(ps, ", ") + ")"
	if f.Result != Void {
		s += " " + f.Result.String()
	}
	return s
}

// funcType returns the fn type (unique for each signature).
func (c *checker) funcType(params []Type, result Type) *FuncType {
	var keys []string
	for _, p := range params {
		keys = append(keys, typeKey(p))
	}
	key := strings.Join(keys, ",") + "->" + typeKey(result)
	if t, ok := c.funcTypes[key]; ok {
		return t
	}
	t := &FuncType{Params: params, Result: result, ID: len(c.funcTypes) + 1}
	c.funcTypes[key] = t
	if !abstract(t) {
		c.info.FuncTypes = append(c.info.FuncTypes, t)
	}
	return t
}

func (c *checker) resolveFuncType(t *ast.TypeExpr) Type {
	var params []Type
	for _, p := range t.Params {
		pt := c.resolveType(p, ctxParam)
		if pt == Invalid {
			return Invalid
		}
		params = append(params, pt)
	}
	var result Type = Void
	if t.Elem != nil {
		result = c.resolveType(t.Elem, ctxResult)
		if result == Invalid {
			return Invalid
		}
		if isBorrow(result) {
			c.errorf(t.Elem.P, "an fn type can't return a borrow in this version")
			return Invalid
		}
	}
	return c.funcType(params, result)
}

// funcRef checks the use of a function as a value (e is the name, mod.name or
// f[int]).
func (c *checker) funcRef(e ast.Expr, fn *Func) Type {
	if fn.Native != "" {
		c.errorf(e.Pos(), "an extern fn can't be used as a value; wrap it in an ordinary fn")
		return Invalid
	}
	if fn.borrowParam() >= 0 {
		c.errorf(e.Pos(), "%s returns a borrow and can't be used as a value in this version", fn.Name)
		return Invalid
	}
	var params []Type
	for _, p := range fn.Params {
		params = append(params, p.Type)
	}
	c.info.FuncRefs[e] = fn
	return c.funcType(params, fn.Result)
}

// indirectCall checks f(args) with f an fn value.
func (c *checker) indirectCall(e *ast.CallExpr, ft *FuncType) Type {
	fn := &Func{Name: "the function " + ft.String(), Result: ft.Result, mod: c.mod}
	for i, p := range ft.Params {
		fn.Params = append(fn.Params, &Local{Name: fmt.Sprintf("p%d", i+1), Type: p, Param: true})
	}
	c.callArgs(e, fn, fn.Params)
	c.info.Calls[e] = Callee{Func: fn, Indirect: true}
	return ft.Result
}

func isFuncType(t Type) bool { _, ok := t.(*FuncType); return ok }
