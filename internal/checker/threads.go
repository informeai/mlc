package checker

import (
	"mlc/internal/ast"
)

// Threads (std/thread)
//
//	t := thread.spawn(soma, metade, thread.STACK_64KB)   // metade é movida para a thread
//	r := thread.join(t)                                  // o resultado volta por posse
//
// spawn e join são embutidas no compilador, como io.print: spawn recebe a
// função da thread (fn(C) ou fn(C) R, sem closure), o contexto (movido) e o
// tamanho da pilha em bytes, e devolve um *thread.Thread[R] (*thread.Thread
// quando a função não devolve nada). O handle se comporta como um handle de
// C: não aceita free nem alloc, não vai para um Vec genérico, e o verificador
// cobra que ele seja passado a thread.join, que espera a thread terminar,
// devolve o resultado e libera o registro. Assim nenhuma thread fica solta.
//
// Dados só atravessam por movimento (o contexto entra, o resultado sai):
// a função da thread não recebe empréstimos, e não há variáveis globais
// mutáveis, então não há como duas threads mexerem na mesma memória.

// SpawnPlan é o que o gerador precisa para um thread.spawn.
type SpawnPlan struct {
	Fn     *FuncType
	Ctx    Type
	Result Type // Void se a função não devolve nada
}

// threadBuiltin diz se m.sel é thread.spawn ou thread.join.
func threadBuiltin(m *module, sel string) string {
	if m.std && m.path == "std/thread" && (sel == "spawn" || sel == "join") {
		return sel
	}
	return ""
}

// threadType devolve o handle thread.Thread[R] (thread.Thread com R Void).
// Todas as instâncias são o mesmo tipo no C (mlc_thread, do runtime).
func (c *checker) threadType(r Type) *Struct {
	if s, ok := c.threads[r]; ok {
		return s
	}
	name := "thread.Thread"
	if r != Void {
		name += "[" + r.String() + "]"
	}
	s := &Struct{Name: name, CName: "mlc_thread", Methods: map[string]*Func{}, Pub: true, Extern: true,
		ThreadResult: r, mod: c.modules["std/thread"]}
	c.threads[r] = s
	return s
}

// threadTypeExpr resolve thread.Thread e thread.Thread[R] escritos no código.
func (c *checker) threadTypeExpr(t *ast.TypeExpr) Type {
	switch len(t.Args) {
	case 0:
		return c.threadType(Void)
	case 1:
		r := c.resolveType(t.Args[0], ctxVar)
		if r == Invalid {
			return Invalid
		}
		if isBorrow(r) {
			c.errorf(t.P, "uma thread não devolve empréstimos")
			return Invalid
		}
		return c.threadType(r)
	}
	c.errorf(t.P, "thread.Thread recebe um tipo, o do resultado: thread.Thread[int] (ou thread.Thread, sem resultado)")
	return Invalid
}

func isThread(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	s, ok := t.(*Struct)
	return ok && s.ThreadResult != nil
}

// threadSpawn checa thread.spawn(f, ctx, stack).
func (c *checker) threadSpawn(e *ast.CallExpr) Type {
	if len(e.Args) != 3 {
		c.errorf(e.Lparen, "thread.spawn recebe a função da thread, o contexto e o tamanho da pilha: thread.spawn(f, ctx, thread.STACK_64KB)")
		c.checkArgs(e)
		return Invalid
	}
	ft, ok := c.value(e.Args[0]).(*FuncType)
	if !ok || len(ft.Params) != 1 {
		if c.info.Types[e.Args[0]] != Invalid {
			c.errorf(e.Args[0].Pos(), "a função da thread recebe um parâmetro, o contexto (fn(C) ou fn(C) R); para levar várias coisas, junte-as numa struct")
		}
		c.value(e.Args[1])
		c.value(e.Args[2])
		return Invalid
	}
	ctx := ft.Params[0]
	switch {
	case isBorrow(ctx):
		c.errorf(e.Args[0].Pos(), "a função da thread não pode receber um empréstimo (%s): o contexto é movido para a thread, que passa a ser dona dele", ctx)
		ctx = Invalid
	case isBorrow(ft.Result):
		c.errorf(e.Args[0].Pos(), "a função da thread não pode devolver um empréstimo (%s)", ft.Result)
	}
	if ctx == Invalid {
		c.value(e.Args[1])
	} else {
		c.valueAs(e.Args[1], ctx, "o contexto de thread.spawn")
	}
	c.valueAs(e.Args[2], Int, "o tamanho da pilha de thread.spawn")
	c.info.Spawns[e] = &SpawnPlan{Fn: ft, Ctx: ctx, Result: ft.Result}
	c.info.Calls[e] = Callee{Builtin: ThreadSpawn}
	return c.pointerTo(c.threadType(ft.Result), false)
}

// threadJoin checa thread.join(t).
func (c *checker) threadJoin(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "thread.join recebe o handle da thread: thread.join(t)")
		c.checkArgs(e)
		return Invalid
	}
	t := c.value(e.Args[0])
	if t == Invalid {
		return Invalid
	}
	if !isThread(t) || isBorrow(t) {
		c.errorf(e.Args[0].Pos(), "thread.join recebe o handle devolvido por thread.spawn (*thread.Thread), e o valor é %s", t)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: ThreadJoin}
	return t.(*Pointer).Elem.(*Struct).ThreadResult
}
