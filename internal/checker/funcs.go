package checker

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
)

// Funções como valor
//
//	var menor fn(&int, &int) bool = compara
//	sort.sort_by(&s, compara)
//	fn aplica(f fn(int) int, x int) int { return f(x) }
//
// Um valor fn é só o endereço de uma função: não captura variáveis (não há
// closures), então não aloca e não tem dono; pode ser copiado à vontade. Os
// tipos dos parâmetros dizem o que a chamada faz com cada argumento (copia,
// empresta ou move), como numa chamada direta, e o verificador de posse
// confere do mesmo jeito.
//
// O valor zero de um fn (var f fn(int) int, campo não preenchido, memória de
// alloc) é uma função que dá panic: nunca existe ponteiro de função nulo.
// Métodos, funções genéricas sem os tipos e extern fn não viram valor.

// FuncType é o tipo de um valor fn.
type FuncType struct {
	Params []Type
	Result Type // Void quando não retorna nada
	ID     int  // nome no C: mlc_fnt_<ID>
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

// funcType devolve o tipo fn (único para cada assinatura).
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
	}
	return c.funcType(params, result)
}

// funcRef checa o uso de uma função como valor (e é o nome, mod.nome ou
// f[int]).
func (c *checker) funcRef(e ast.Expr, fn *Func) Type {
	if fn.Native != "" {
		c.errorf(e.Pos(), "extern fn não pode ser usada como valor; envolva-a numa fn comum")
		return Invalid
	}
	var params []Type
	for _, p := range fn.Params {
		params = append(params, p.Type)
	}
	c.info.FuncRefs[e] = fn
	return c.funcType(params, fn.Result)
}

// indirectCall checa f(args) com f um valor fn.
func (c *checker) indirectCall(e *ast.CallExpr, ft *FuncType) Type {
	fn := &Func{Name: "a função " + ft.String(), Result: ft.Result, mod: c.mod}
	for i, p := range ft.Params {
		fn.Params = append(fn.Params, &Local{Name: fmt.Sprintf("p%d", i+1), Type: p, Param: true})
	}
	c.callArgs(e, fn, fn.Params)
	c.info.Calls[e] = Callee{Func: fn, Indirect: true}
	return ft.Result
}

func isFuncType(t Type) bool { _, ok := t.(*FuncType); return ok }
