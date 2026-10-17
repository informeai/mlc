package codegen

import (
	"fmt"
	"sort"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Threads: cada tipo de função de thread (fn(C) R) ganha um registro e uma
// função de entrada:
//
//	typedef struct { mlc_thread base; R result; } mlc_join_K;     // o que o join lê
//	typedef struct { mlc_join_K head; F f; C ctx; } mlc_spawn_N;
//	static void mlc_spawn_N_run(mlc_thread *t) { s->head.result = s->f(s->ctx); }
//
// O registro começa com o mlc_thread do runtime, então o handle
// (*thread.Thread[R], mlc_thread * no C) é o próprio registro. O join só
// conhece R, e por isso lê o resultado pelo mlc_join_K, que é o começo de
// todo registro com aquele R.

// threadDefs escreve os registros e as funções de entrada dos thread.spawn.
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

// joinType devolve (e escreve, na primeira vez) o começo dos registros
// cujo resultado é r.
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

// threadSpawn traduz thread.spawn(f, ctx, stack): calcula os argumentos (da
// esquerda para a direita), confere que f não é nulo, aloca o registro,
// põe f e o contexto nele e cria a thread.
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

// threadJoin traduz thread.join(t): espera a thread, tira o resultado do
// registro e libera o registro.
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

// Canais: o canal fica no runtime (mlc_chan) e cada ponta é um mlc_chan *.
// Os valores passam pelo canal como bytes; um dono que sobra no canal (ou
// que um send sem receptor descarta) é liberado pela função de drop do tipo,
// que o chan.new entrega ao runtime.

// chanDropName é a função void (*)(void *) que libera um valor do canal.
func chanDropName(t checker.Type) string { return "mlc_chan_drop_" + mangle(t) }

// chanDrops escreve as funções de drop dos canais de donos.
func (g *gen) chanDrops() {
	done := map[string]bool{}
	for _, p := range g.info.Chans {
		if p.Op != "new" || !checker.IsOwner(p.Elem) || done[chanDropName(p.Elem)] {
			continue
		}
		done[chanDropName(p.Elem)] = true
		g.raw("static void %s(void *p) { %s(*(%s *)p); }\n", chanDropName(p.Elem), dropName(p.Elem), cType(p.Elem))
	}
}

// chanOp traduz chan.new e os métodos das pontas.
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
	case "recv":
		en := p.Recv.(*checker.Enum)
		got, closed := en.Variants[0], en.Variants[1]
		v := g.temp(p.Elem)
		t := enumCType(en)
		return fmt.Sprintf("(%s, mlc_chan_recv(%s, &%s) ? ((%s){.tag = %d, .u.v_%s = {.f_%s = %s}}) : ((%s){.tag = %d}))",
			set, end, v, t, got.Tag, got.Name, got.Fields[0].Name, v, t, closed.Tag)
	}
	panic("codegen: operação de canal desconhecida " + p.Op)
}
