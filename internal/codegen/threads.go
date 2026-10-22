package codegen

import (
	"fmt"
	"sort"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Threads: each thread function type (fn(C) R) gets a record and an
// entry function:
//
//	typedef struct { mlc_thread base; R result; } mlc_join_K;     // what join reads
//	typedef struct { mlc_join_K head; F f; C ctx; } mlc_spawn_N;
//	static void mlc_spawn_N_run(mlc_thread *t) { s->head.result = s->f(s->ctx); }
//
// The record starts with the runtime's mlc_thread, so the handle
// (*thread.Thread[R], mlc_thread * in C) is the record itself. join only
// knows R, and so it reads the result through mlc_join_K, which is the start of
// every record with that R.

// threadDefs writes the records and the entry functions of the thread.spawn calls.
func (g *gen) threadDefs() {
	g.joins, g.spawnNames = map[string]string{}, map[int]string{}
	var plans []*checker.SpawnPlan
	for _, p := range g.info.Spawns {
		plans = append(plans, p)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Fn.ID < plans[j].Fn.ID })
	for _, p := range plans {
		if _, done := g.spawnNames[p.Fn.ID]; done {
			continue
		}
		head := "mlc_thread base;"
		if p.Result != checker.Void {
			head = g.joinType(p.Result) + " head;"
		}
		name := fmt.Sprintf("mlc_spawn_%d", p.Fn.ID)
		g.spawnNames[p.Fn.ID] = name
		g.raw("typedef struct { %s %s f; %s ctx; } %s;\n", head, funcCType(p.Fn), cType(p.Ctx), name)
		call := "s->f(s->ctx)"
		if p.Result != checker.Void {
			call = "s->head.result = " + call
		}
		g.raw("static void %s_run(mlc_thread *t) {\n    %s *s = (%s *)t;\n    %s;\n}\n", name, name, name, call)
	}
}

// joinType returns (and writes, the first time) the start of the records
// whose result is r.
func (g *gen) joinType(r checker.Type) string {
	key := cType(r)
	if name, ok := g.joins[key]; ok {
		return name
	}
	name := fmt.Sprintf("mlc_join_%d", len(g.joins))
	g.joins[key] = name
	g.raw("typedef struct { mlc_thread base; %s result; } %s;\n", key, name)
	return name
}

// threadSpawn translates thread.spawn(f, ctx, stack): computes the arguments (left
// to right), checks that f isn't null, allocates the record,
// puts f and the context in it and creates the thread.
func (g *gen) threadSpawn(e *ast.CallExpr) string {
	p := g.info.Spawns[e]
	name := g.spawnNames[p.Fn.ID]
	f, ctx, stack := g.temp(p.Fn), g.temp(p.Ctx), g.temp(checker.Int)
	rec := g.newName("mlc_t")
	g.temps = append(g.temps, name+" *"+rec)
	return fmt.Sprintf("(%s = %s, %s = %s, %s = %s, (%s == NULL ? mlc_null_fn(__FILE__, __LINE__) : (void)0), "+
		"%s = (%s *)mlc_alloc(sizeof(%s), __FILE__, __LINE__), %s->f = %s, %s->ctx = %s, "+
		"mlc_thread_start((mlc_thread *)%s, %s_run, %s, __FILE__, __LINE__), (mlc_thread *)%s)",
		f, g.expr(e.Args[0]), ctx, g.expr(e.Args[1]), stack, g.expr(e.Args[2]), f,
		rec, name, name, rec, f, rec, ctx,
		rec, name, stack, rec)
}

// threadJoin translates thread.join(t): waits for the thread, takes the result out of the
// record and frees the record.
func (g *gen) threadJoin(e *ast.CallExpr) string {
	r := g.info.Types[e.Args[0]].(*checker.Pointer).Elem.(*checker.Struct).ThreadResult
	t := g.temp(g.info.Types[e.Args[0]])
	parts := fmt.Sprintf("%s = %s, mlc_thread_join(%s, __FILE__, __LINE__)", t, g.expr(e.Args[0]), t)
	if r == checker.Void {
		return fmt.Sprintf("(%s, mlc_free(%s))", parts, t)
	}
	res := g.temp(r)
	return fmt.Sprintf("(%s, %s = ((%s *)%s)->result, mlc_free(%s), %s)", parts, res, g.joins[cType(r)], t, t, res)
}

// Channels: the channel lives in the runtime (mlc_chan) and each end is an mlc_chan *.
// Values go through the channel as bytes; an owner left in the channel (or
// one a send without a receiver discards) is freed by the type's drop function,
// which chan.new hands to the runtime.

// chanDropName is the void (*)(void *) function that frees a channel value.
func chanDropName(t checker.Type) string { return "mlc_chan_drop_" + mangle(t) }

// chanDrops writes the drop functions of the owners kept in channels and
// mutexes (the runtime calls them on the last close).
func (g *gen) chanDrops() {
	var elems []checker.Type
	for _, p := range g.info.Chans {
		if p.Op == "new" {
			elems = append(elems, p.Elem)
		}
	}
	for _, p := range g.info.Mutexes {
		if p.Op == "new" {
			elems = append(elems, p.Elem)
		}
	}
	sort.Slice(elems, func(i, j int) bool { return chanDropName(elems[i]) < chanDropName(elems[j]) })
	done := map[string]bool{}
	for _, t := range elems {
		if !checker.IsOwner(t) || done[chanDropName(t)] {
			continue
		}
		done[chanDropName(t)] = true
		g.raw("static void %s(void *p) { %s(*(%s *)p); }\n", chanDropName(t), dropName(t), cType(t))
	}
}

// chanOp translates chan.new and the ends' methods.
func (g *gen) chanOp(e *ast.CallExpr) string {
	p := g.info.Chans[e]
	if p.Op == "new" {
		drop := "NULL"
		if checker.IsOwner(p.Elem) {
			drop = chanDropName(p.Elem)
		}
		return fmt.Sprintf("mlc_chan_new(%s, sizeof(%s), %s, __FILE__, __LINE__)", g.expr(e.Args[0]), cType(p.Elem), drop)
	}
	recvExpr := e.Fun.(*ast.SelectorExpr).X
	end := g.temp(&checker.Pointer{Elem: &checker.Struct{ChanEnd: p.End}})
	set := fmt.Sprintf("%s = (mlc_chan *)MLC_NONNULL(%s)", end, g.expr(recvExpr))
	switch p.Op {
	case "close":
		return fmt.Sprintf("(%s, mlc_chan_close(%s, %d))", set, end, p.End)
	case "sender":
		return fmt.Sprintf("(%s, mlc_chan_add(%s, %d))", set, end, checker.ChanSender)
	case "clone":
		return fmt.Sprintf("(%s, mlc_chan_add(%s, %d))", set, end, p.End)
	case "send":
		v := g.temp(p.Elem)
		return fmt.Sprintf("(%s, %s = %s, mlc_chan_send(%s, &%s))", set, v, g.expr(e.Args[0]), end, v)
	case "try_send":
		// 0: it went in; 1: full and 2: no Receiver, with the value back in the variant
		en := p.Recv.(*checker.Enum)
		sent, full, closed := en.Variants[0], en.Variants[1], en.Variants[2]
		v := g.temp(p.Elem)
		r := g.temp(checker.Int32)
		t := enumCType(en)
		return fmt.Sprintf("(%s, %s = %s, %s = mlc_chan_try_send(%s, &%s), %s == 0 ? ((%s){.tag = %d}) : %s == 1 ? ((%s){.tag = %d, .u.v_%s = {.f_%s = %s}}) : ((%s){.tag = %d, .u.v_%s = {.f_%s = %s}}))",
			set, v, g.expr(e.Args[0]), r, end, v, r, t, sent.Tag, r, t, full.Tag, full.Name, full.Fields[0].Name, v, t, closed.Tag, closed.Name, closed.Fields[0].Name, v)
	case "recv":
		en := p.Recv.(*checker.Enum)
		got, closed := en.Variants[0], en.Variants[1]
		v := g.temp(p.Elem)
		t := enumCType(en)
		return fmt.Sprintf("(%s, mlc_chan_recv(%s, &%s) ? ((%s){.tag = %d, .u.v_%s = {.f_%s = %s}}) : ((%s){.tag = %d}))",
			set, end, v, t, got.Tag, got.Name, got.Fields[0].Name, v, t, closed.Tag)
	}
	panic("codegen: unknown channel operation " + p.Op)
}

// Mutex: the mutex lives in the runtime (mlc_mutex), with the value inside; the handle
// and the guard are mlc_mutex *. get borrows the value that's in the mutex.

// mutexOp translates mutex.new and the methods of Mutex and Guard.
func (g *gen) mutexOp(e *ast.CallExpr) string {
	p := g.info.Mutexes[e]
	if p.Op == "new" {
		v := g.temp(p.Elem)
		drop := "NULL"
		if checker.IsOwner(p.Elem) {
			drop = chanDropName(p.Elem)
		}
		return fmt.Sprintf("(%s = %s, mlc_mutex_new(&%s, sizeof(%s), %s, __FILE__, __LINE__))", v, g.expr(e.Args[0]), v, cType(p.Elem), drop)
	}
	m := g.temp(&checker.Pointer{Elem: &checker.Struct{MutexKind: checker.MutexHandle}})
	set := fmt.Sprintf("%s = (mlc_mutex *)MLC_NONNULL(%s)", m, g.expr(e.Fun.(*ast.SelectorExpr).X))
	switch p.Op {
	case "lock":
		return fmt.Sprintf("(%s, mlc_mutex_lock(%s, __FILE__, __LINE__))", set, m)
	case "clone":
		return fmt.Sprintf("(%s, mlc_mutex_clone(%s))", set, m)
	case "close":
		return fmt.Sprintf("(%s, mlc_mutex_close(%s))", set, m)
	case "unlock":
		return fmt.Sprintf("(%s, mlc_mutex_unlock(%s))", set, m)
	case "wait":
		return fmt.Sprintf("(%s, mlc_mutex_wait(%s))", set, m)
	case "notify", "notify_all":
		return fmt.Sprintf("(%s, mlc_mutex_notify(%s, %t))", set, m, p.Op == "notify_all")
	case "get":
		value := fmt.Sprintf("((%s *)mlc_mutex_value(%s))", cType(p.Elem), m)
		if _, isPtr := p.Elem.(*checker.Pointer); isPtr {
			// a pointer value: the borrow is of the pointed-to object (nil panics)
			return fmt.Sprintf("(%s, (%s)MLC_NONNULL(*%s))", set, cType(p.Elem), value)
		}
		return fmt.Sprintf("(%s, %s)", set, value)
	}
	panic("codegen: unknown mutex operation " + p.Op)
}
