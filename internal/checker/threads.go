package checker

import (
	"mlc/internal/ast"
)

// Threads (std/thread)
//
//	t := thread.spawn(sum, half, thread.STACK_64KB)      // half is moved to the thread
//	r := thread.join(t)                                  // the result comes back by ownership
//
// spawn and join are built into the compiler, like io.print: spawn takes the
// thread's function (fn(C) or fn(C) R, no closure), the context (moved) and the
// stack size in bytes, and returns a *thread.Thread[R] (*thread.Thread
// when the function returns nothing). The handle behaves like a C
// handle: it doesn't accept free or alloc, doesn't go into a generic Vec, and the checker
// requires that it be passed to thread.join, which waits for the thread to finish,
// returns the result and frees the record. This way no thread is left loose.
//
// Data only crosses by move (the context goes in, the result comes out):
// the thread's function doesn't take borrows, and there are no mutable global
// variables, so there's no way for two threads to touch the same memory.

// SpawnPlan is what the generator needs for a thread.spawn.
type SpawnPlan struct {
	Fn     *FuncType
	Ctx    Type
	Result Type // Void if the function returns nothing
}

// threadBuiltin says whether m.sel is thread.spawn or thread.join.
func threadBuiltin(m *module, sel string) string {
	if m.std && m.path == "std/thread" && (sel == "spawn" || sel == "join") {
		return sel
	}
	return ""
}

// threadType returns the handle thread.Thread[R] (thread.Thread with R Void).
// Every instance is the same type in C (mlc_thread, from the runtime).
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

// threadTypeExpr resolves thread.Thread and thread.Thread[R] written in the code.
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
			c.errorf(t.P, "a thread doesn't return borrows")
			return Invalid
		}
		return c.threadType(r)
	}
	c.errorf(t.P, "thread.Thread takes one type, the result's: thread.Thread[int] (or thread.Thread, without a result)")
	return Invalid
}

func isThread(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	s, ok := t.(*Struct)
	return ok && s.ThreadResult != nil
}

// threadSpawn checks thread.spawn(f, ctx, stack).
func (c *checker) threadSpawn(e *ast.CallExpr) Type {
	if len(e.Args) != 3 {
		c.errorf(e.Lparen, "thread.spawn takes the thread's function, the context and the stack size: thread.spawn(f, ctx, thread.STACK_64KB)")
		c.checkArgs(e)
		return Invalid
	}
	ft, ok := c.value(e.Args[0]).(*FuncType)
	if !ok || len(ft.Params) != 1 {
		if c.info.Types[e.Args[0]] != Invalid {
			c.errorf(e.Args[0].Pos(), "the thread's function takes one parameter, the context (fn(C) or fn(C) R); to carry several things, gather them in a struct")
		}
		c.value(e.Args[1])
		c.value(e.Args[2])
		return Invalid
	}
	ctx := ft.Params[0]
	switch {
	case isBorrow(ctx):
		c.errorf(e.Args[0].Pos(), "the thread's function can't take a borrow (%s): the context is moved to the thread, which becomes its owner", ctx)
		ctx = Invalid
	case isBorrow(ft.Result):
		c.errorf(e.Args[0].Pos(), "the thread's function can't return a borrow (%s)", ft.Result)
	case isGuard(ctx) || isGuard(ft.Result):
		c.errorf(e.Args[0].Pos(), "a guard can't go to another thread: it must be unlocked on the thread that locked the mutex")
		ctx = Invalid
	}
	if ctx == Invalid {
		c.value(e.Args[1])
	} else {
		c.valueAs(e.Args[1], ctx, "the context of thread.spawn")
	}
	c.valueAs(e.Args[2], Int, "the stack size of thread.spawn")
	c.spawnSites = append(c.spawnSites, spawnSite{e, c.mod})
	c.info.Spawns[e] = &SpawnPlan{Fn: ft, Ctx: ctx, Result: ft.Result}
	c.info.Calls[e] = Callee{Builtin: ThreadSpawn}
	return c.pointerTo(c.threadType(ft.Result), false)
}

// threadJoin checks thread.join(t).
func (c *checker) threadJoin(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "thread.join takes the thread's handle: thread.join(t)")
		c.checkArgs(e)
		return Invalid
	}
	t := c.value(e.Args[0])
	if t == Invalid {
		return Invalid
	}
	if !isThread(t) || isBorrow(t) {
		c.errorf(e.Args[0].Pos(), "thread.join takes the handle returned by thread.spawn (*thread.Thread), and the value is %s", t)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: ThreadJoin}
	return t.(*Pointer).Elem.(*Struct).ThreadResult
}
