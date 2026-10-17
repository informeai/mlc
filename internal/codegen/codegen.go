// Package codegen traduz um programa já checado para C11.
//
// Convenções do C gerado:
//   - funções do usuário viram mlc_fn_<nome> (com o prefixo do arquivo, se
//     vier de um import); variáveis viram l_<nome> (ou
//     lN_<nome> quando o nome é redeclarado na função), o que evita colisão
//     com palavras reservadas e com a biblioteca do C;
//   - cada comando é precedido de #line, para que erros e panics do runtime
//     apontem para o arquivo .mlc;
//   - aritmética inteira passa pelas macros MLC_* do runtime, que checam
//     estouro e divisão por zero em vez de cair em comportamento indefinido.
package codegen

import (
	"fmt"
	"math/big"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/token"
)

type gen struct {
	file   string
	info   *checker.Info
	buf    *strings.Builder
	indent int

	loops    []*loopCtx // laços abertos, o mais interno por último
	switches int        // switch do C abertos (match sobre int)
	labels   int        // contador para nomes únicos de labels e temporárias
	// indexOverride troca o índice de s[i] por uma temporária, para que um
	// índice com efeito seja calculado uma vez só em s[f()] += 1.
	indexOverride map[*ast.IndexExpr]string
	temps         []string // declarações das temporárias da função em geração

	joins      map[string]string // thread.join: o começo dos registros, pelo tipo C do resultado
	spawnNames map[int]string    // thread.spawn: o registro, pelo ID do tipo da função
}

// loopCtx acompanha um laço em geração. Um break do MLC dentro de um switch
// do C sairia só do switch, então ele vira "goto label", com o label logo
// depois do laço.
type loopCtx struct {
	switches  int    // valor de gen.switches quando o laço abriu
	label     string // label de saída, criado no primeiro uso
	needLabel bool
}

func Generate(file string, f *ast.File, info *checker.Info) string {
	g := &gen{file: file, info: info, buf: &strings.Builder{}, indexOverride: map[*ast.IndexExpr]string{}}
	g.raw("// Gerado por mlc a partir de %s. Não edite.\n", file)
	g.raw("#include \"mlc_runtime.h\"\n\n")
	g.structs()
	// funções da biblioteca padrão implementadas no runtime C
	for _, fn := range info.Externs {
		params := []string{}
		for _, p := range fn.Params {
			params = append(params, cType(p.Type))
		}
		params = append(params, "const char *", "int")
		g.raw("%s %s(%s);\n", cType(fn.Result), fn.Native, strings.Join(params, ", "))
	}
	// funções de C (extern "C" fn): um nome local ligado ao símbolo de C por
	// rótulo de assembly, para não brigar com o que os cabeçalhos já declaram
	for _, fn := range info.CExterns {
		params := []string{}
		for _, p := range fn.Params {
			params = append(params, cFFIType(p.Type))
		}
		if len(params) == 0 {
			params = append(params, "void")
		}
		g.raw("extern %s %s(%s) __asm__(MLC_SYM(%q));\n", cFFIType(fn.Result), fn.CName, strings.Join(params, ", "), fn.CSym)
	}
	g.threadDefs()
	for _, fn := range info.Funcs {
		g.raw("static MLC_MAYBE_UNUSED %s;\n", signature(fn)) // instâncias podem ficar sem uso
	}
	g.dropHelpers()
	g.raw("\n")
	for _, fn := range info.Funcs {
		g.function(fn)
	}
	if info.Tests != nil {
		g.testMain(info.Tests)
		return g.buf.String()
	}
	g.raw("int main(int argc, char **argv) {\n    mlc_args_init(argc, argv);\n    %s();\n    mlc_check_allocs();\n    return 0;\n}\n", info.Main.CName)
	return g.buf.String()
}

// testMain gera o main de um programa de teste: roda o teste cujo índice
// veio como argumento (o mlc test roda um processo por teste).
func (g *gen) testMain(tests []*checker.Func) {
	g.raw("int main(int argc, char **argv) {\n    int64_t test = mlc_test_selected(argc, argv);\n    mlc_args_init(1, argv);\n    switch (test) {\n")
	for i, fn := range tests {
		g.raw("    case %d: %s(); break;\n", i, fn.CName)
	}
	g.raw("    default: return 2;\n    }\n    mlc_test_finish();\n    mlc_check_allocs();\n    return 0;\n}\n")
}

func (g *gen) raw(format string, args ...any) {
	fmt.Fprintf(g.buf, format, args...)
}

// line escreve uma linha de código na indentação atual.
func (g *gen) line(format string, args ...any) {
	g.buf.WriteString(strings.Repeat("    ", g.indent))
	fmt.Fprintf(g.buf, format, args...)
	g.buf.WriteByte('\n')
}

func (g *gen) lineDirective(pos token.Pos) {
	g.raw("#line %d %s\n", pos.Line, cString(g.file))
}

func cType(t checker.Type) string {
	switch t := t.(type) {
	case *checker.Enum:
		return enumCType(t)
	case *checker.Array:
		return arrayCType(t)
	case *checker.Basic:
		if c, ok := numericC[checker.DefaultType(t)]; ok {
			return c
		}
	case *checker.Slice:
		return sliceCType(t)
	case *checker.Pointer:
		return cType(t.Elem) + " *"
	case *checker.Borrow:
		return cType(t.Elem) + " *"
	case *checker.FuncType:
		return funcCType(t)
	}
	if s, ok := t.(*checker.Struct); ok {
		if s.ThreadResult != nil {
			return "mlc_thread" // thread.Thread[R]: o registro do runtime
		}
		return structCType(s)
	}
	switch t {
	case checker.Int:
		return "int64_t"
	case checker.Bool:
		return "bool"
	case checker.String:
		return "mlc_str"
	case checker.Void:
		return "void"
	}
	panic(fmt.Sprintf("codegen: tipo sem representação em C: %s", t))
}

func signature(fn *checker.Func) string {
	params := make([]string, len(fn.Params))
	for i, p := range fn.Params {
		params[i] = cType(p.Type) + " l_" + p.Name
	}
	list := "void"
	if len(params) > 0 {
		list = strings.Join(params, ", ")
	}
	return fmt.Sprintf("%s %s(%s)", cType(fn.Result), fn.CName, list)
}

func (g *gen) function(fn *checker.Func) {
	g.file = fn.File // cada função aponta para o arquivo dela (vale também para o corpo)
	// O corpo é gerado antes, num buffer à parte, para saber quais
	// temporárias declarar no começo da função.
	g.temps = nil
	outer, body := g.buf, &strings.Builder{}
	g.buf = body
	g.indent++
	for _, s := range fn.Decl.Body.Stmts {
		g.stmt(s)
	}
	g.indent--
	g.buf = outer

	g.lineDirective(fn.Decl.P)
	g.raw("static %s {\n", signature(fn))
	for _, t := range g.temps {
		g.raw("    %s;\n", t)
	}
	g.buf.WriteString(body.String())
	g.raw("}\n\n")
}

func (g *gen) block(b *ast.Block) {
	g.indent++
	for _, s := range b.Stmts {
		g.stmt(s)
	}
	g.indent--
}

func (g *gen) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.ConstDecl:
		// Constantes não existem no C: cada uso vira o valor literal.
	case *ast.ExprStmt, *ast.VarDecl, *ast.AssignStmt, *ast.IncDecStmt:
		g.lineDirective(s.Pos())
		g.line("%s;", g.simple(s))
	case *ast.IfStmt:
		g.lineDirective(s.P)
		g.ifStmt(s)
	case *ast.ForStmt:
		g.lineDirective(s.P)
		switch {
		case s.Var != nil:
			g.forIn(s)
		case s.Init == nil && s.Post == nil && s.Cond == nil:
			g.line("for (;;) {")
		case s.Init == nil && s.Post == nil:
			g.line("while (%s) {", g.cond(s.Cond))
		default:
			var init, cond, post string
			if s.Init != nil {
				init = g.simple(s.Init)
			}
			if s.Cond != nil {
				cond = g.cond(s.Cond)
			}
			if s.Post != nil {
				post = g.simple(s.Post)
			}
			g.line("for (%s; %s; %s) {", init, cond, post)
		}
		loop := &loopCtx{switches: g.switches}
		g.loops = append(g.loops, loop)
		g.block(s.Body)
		g.loops = g.loops[:len(g.loops)-1]
		g.line("}")
		if loop.needLabel {
			g.line("%s:;", loop.label)
		}
	case *ast.MatchStmt:
		g.lineDirective(s.P)
		if isEnum(g.typeOf(s.Subject)) {
			g.matchEnum(s)
		} else if checker.IsInteger(g.typeOf(s.Subject)) {
			g.matchSwitch(s)
		} else {
			g.matchIfChain(s)
		}
	case *ast.BranchStmt:
		g.lineDirective(s.P)
		loop := g.loops[len(g.loops)-1]
		if s.Tok == token.BREAK && g.switches > loop.switches {
			if !loop.needLabel {
				loop.needLabel = true
				loop.label = g.newName("mlc_break")
			}
			g.line("goto %s;", loop.label)
			return
		}
		// continue dentro de um switch do C já vai para o laço, como no MLC.
		g.line("%s;", s.Tok)
	case *ast.ReturnStmt:
		g.lineDirective(s.P)
		if s.Value == nil {
			g.line("return;")
		} else {
			g.line("return %s;", g.expr(s.Value))
		}
	case *ast.Block:
		g.line("{")
		g.indent++
		for _, st := range s.Stmts {
			g.stmt(st)
		}
		g.indent--
		g.line("}")
	default:
		panic(fmt.Sprintf("codegen: comando desconhecido %T", s))
	}
}

func (g *gen) newName(prefix string) string {
	g.labels++
	return fmt.Sprintf("%s_%d", prefix, g.labels)
}

// armBody emite o corpo de um braço de match, já dentro de chaves.
func (g *gen) armBody(s ast.Stmt) {
	if b, ok := s.(*ast.Block); ok {
		g.block(b)
		return
	}
	g.indent++
	g.stmt(s)
	g.indent--
}

// matchSwitch emite match sobre int como switch do C, que o compilador C
// pode transformar em tabela de saltos. Cada braço termina em break: não há
// fallthrough. O checker garante que existe um braço _ (o default).
func (g *gen) matchSwitch(s *ast.MatchStmt) {
	g.line("switch (%s) {", g.expr(s.Subject))
	g.switches++
	for _, arm := range s.Arms {
		var labels []string
		for _, p := range arm.Patterns {
			if p.Wildcard {
				labels = append(labels, "default:")
			} else {
				labels = append(labels, "case "+constant(g.info.Patterns[p], g.typeOf(s.Subject))+":")
			}
		}
		g.line("%s {", strings.Join(labels, " "))
		g.armBody(arm.Body)
		g.indent++
		g.line("break;")
		g.indent--
		g.line("}")
	}
	g.switches--
	g.line("}")
}

// matchIfChain emite match sobre string ou bool como cadeia de if/else. O
// valor é calculado uma vez só, numa temporária.
func (g *gen) matchIfChain(s *ast.MatchStmt) {
	t := g.info.Types[s.Subject]
	if len(s.Arms) == 1 && s.Arms[0].Patterns[0].Wildcard {
		// Só o curinga: nenhuma comparação, mas o valor ainda é calculado.
		g.line("(void)%s;", g.expr(s.Subject))
		g.line("{")
		g.armBody(s.Arms[0].Body)
		g.line("}")
		return
	}
	tmp := g.newName("mlc_subject")
	g.line("{")
	g.indent++
	g.line("%s %s = %s;", cType(t), tmp, g.expr(s.Subject))
	for i, arm := range s.Arms {
		var conds []string
		for _, p := range arm.Patterns {
			if p.Wildcard {
				continue
			}
			switch v := g.info.Patterns[p].(type) {
			case string:
				conds = append(conds, fmt.Sprintf("mlc_string_eq(%s, MLC_STR_LIT(%s, %d))", tmp, cString(v), len(v)))
			case bool:
				if v {
					conds = append(conds, tmp)
				} else {
					conds = append(conds, "!"+tmp)
				}
			}
		}
		switch {
		case len(conds) == 0 || (i > 0 && i == len(s.Arms)-1):
			// O checker garante que o match cobre todos os valores, então o
			// último braço não precisa de condição. Sem isso, num match com
			// true/false o compilador C não vê que todos os caminhos retornam.
			g.line("} else {")
		case i == 0:
			g.line("if (%s) {", strings.Join(conds, " || "))
		default:
			g.line("} else if (%s) {", strings.Join(conds, " || "))
		}
		g.armBody(arm.Body)
	}
	g.line("}")
	g.indent--
	g.line("}")
}

// ifStmt emite if/else; um "else if" vira um if aninhado dentro do else.
func (g *gen) ifStmt(s *ast.IfStmt) {
	g.line("if (%s) {", g.cond(s.Cond))
	g.block(s.Then)
	switch e := s.Else.(type) {
	case nil:
	case *ast.IfStmt:
		g.line("} else {")
		g.indent++
		g.ifStmt(e)
		g.indent--
	case *ast.Block:
		g.line("} else {")
		g.block(e)
	}
	g.line("}")
}

// simple traduz um comando simples para uma expressão C (ou declaração),
// sem o ';' final, para servir tanto como comando quanto no init/pós do for.
func (g *gen) simple(s ast.Stmt) string {
	switch s := s.(type) {
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok && g.info.Calls[call].Builtin != checker.NoBuiltin && g.typeOf(s.X) != checker.Void {
			// thread.join(t) ou str.format_len(...) como comando: o valor
			// (o último da expressão com vírgulas) é descartado
			return "(void)" + g.expr(s.X)
		}
		return g.expr(s.X)
	case *ast.VarDecl:
		l := g.info.Decls[s]
		var value string
		if s.Value != nil {
			value = g.expr(s.Value)
		} else {
			value = zeroValue(l.Type)
		}
		return fmt.Sprintf("%s %s = %s", cType(l.Type), l.CName, value)
	case *ast.AssignStmt:
		pre := g.hoistIndex(s.Lhs, g.effectful(s.Rhs))
		defer g.dropIndex(s.Lhs)
		lhs := g.expr(s.Lhs)
		if s.Op == token.ASSIGN {
			return sequence(pre, lhs+" = "+g.expr(s.Rhs))
		}
		// x += v lê x antes de calcular v, da esquerda para a direita.
		ops, v := g.operands([]ast.Expr{s.Lhs, s.Rhs})
		return sequence(append(pre, ops...), lhs+" = "+binop(checker.CompoundOp(s.Op), g.typeOf(s.Lhs), v[0], v[1]))
	case *ast.IncDecStmt:
		op := token.ADD
		if s.Op == token.DEC {
			op = token.SUB
		}
		pre := g.hoistIndex(s.X, false)
		defer g.dropIndex(s.X)
		x := g.expr(s.X)
		t := g.typeOf(s.X)
		return sequence(pre, x+" = "+arith(op, t, x, constant(big.NewInt(1), t)))
	}
	panic(fmt.Sprintf("codegen: comando simples desconhecido %T", s))
}

func zeroValue(t checker.Type) string {
	if en, ok := t.(*checker.Enum); ok {
		return "((" + enumCType(en) + "){0})" // primeira variante, campos zerados
	}
	if a, ok := t.(*checker.Array); ok {
		return "((" + arrayCType(a) + "){0})"
	}
	if c, ok := numericC[t]; ok {
		return "((" + c + ")0)"
	}
	if s, ok := t.(*checker.Slice); ok {
		return "((" + sliceCType(s) + "){0})" // slice vazio
	}
	if _, ok := t.(*checker.Pointer); ok {
		return "NULL" // só ?*T tem valor zero; *T sempre é inicializado
	}
	if _, ok := t.(*checker.FuncType); ok {
		return "NULL" // chamar dá panic: toda chamada indireta confere
	}
	if s, ok := t.(*checker.Struct); ok {
		return "((" + structCType(s) + "){0})"
	}
	switch t {
	case checker.Int:
		return "INT64_C(0)"
	case checker.Bool:
		return "false"
	case checker.String:
		return `MLC_STR_LIT("", 0)`
	}
	panic(fmt.Sprintf("codegen: tipo sem valor zero: %s", t))
}

// cond traduz a condição de if/while/for.
func (g *gen) cond(e ast.Expr) string {
	if b, ok := e.(*ast.BinaryExpr); ok {
		return g.binary(b, true)
	}
	return g.expr(e)
}

// ioPrint traduz io.print, io.printf, str.format e str.format_len conforme
// o plano do checker: os valores vão para temporárias (da esquerda para a
// direita, antes de escrever qualquer coisa) e cada um é escrito pelo seu
// método write, sobre um io.Writer na pilha. No str.format, o Writer
// escreve num destino registrado no runtime (mlc_sink_push), e o resultado
// é quantos bytes foram escritos.
func (g *gen) ioPrint(call *ast.CallExpr) string {
	plan := g.info.Prints[call]
	w := g.temp(plan.Writer)
	fd := plan.Fd
	switch plan.Mode {
	case checker.PrintSink:
		fd = -1 // io.SINK
	case checker.PrintCount:
		fd = -2 // io.COUNT
	}
	parts := []string{
		fmt.Sprintf("%s = %s", w, zeroValue(plan.Writer)),
		fmt.Sprintf("%s.f_fd = INT64_C(%d)", w, fd),
	}
	if plan.Mode == checker.PrintSink {
		// o destino é calculado primeiro, como o primeiro argumento
		parts = append(parts, fmt.Sprintf("%s.f_sink = mlc_sink_push(%s, __FILE__, __LINE__)", w, g.expr(plan.Dst)))
	}
	vals := make([]string, len(plan.Args))
	for i, a := range plan.Args {
		vals[i] = g.temp(plan.ArgTypes[i])
		parts = append(parts, vals[i]+" = "+g.expr(a))
	}
	for _, p := range plan.Parts {
		if p.Arg < 0 {
			parts = append(parts, fmt.Sprintf("%s(&%s, %s)", plan.PutStr.CName, w, constant(p.Lit, checker.String)))
			continue
		}
		v, t := vals[p.Arg], plan.ArgTypes[p.Arg]
		recv := "&" + v
		if p.Ptr {
			recv, v, t = v, "(*"+v+")", t.(*checker.Borrow).Elem // o empréstimo já é o ponteiro
		}
		if p.View {
			at := t.(*checker.Array)
			recv = fmt.Sprintf("&(%s){%s.a, INT64_C(%d)}", sliceCType(&checker.Slice{Elem: at.Elem}), v, at.Len)
		}
		write := func(writer string) string { return fmt.Sprintf("%s(%s, &%s)", p.Write.CName, recv, writer) }
		if p.Spec != nil && p.Spec.Text {
			write = func(writer string) string { return fmt.Sprintf("%s(&%s, %s)", plan.PutBytes.CName, writer, recv) }
		}
		if p.Spec == nil {
			parts = append(parts, write(w))
			continue
		}
		parts = append(parts, g.specWrite(plan, p.Spec, w, write)...)
	}
	parts = append(parts, fmt.Sprintf("%s(&%s)", plan.Flush.CName, w))
	switch plan.Mode {
	case checker.PrintSink:
		parts = append(parts, fmt.Sprintf("mlc_sink_pop(%s.f_sink, __FILE__, __LINE__)", w))
	case checker.PrintCount:
		parts = append(parts, w+".f_total")
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// specWrite escreve um valor com especificação ({:>8.2}): os campos do
// Writer valem durante o write, e o alinhamento é feito em volta dele (para
// alinhar à direita ou no centro, o valor é escrito antes num Writer que só
// conta, para saber o tamanho).
func (g *gen) specWrite(plan *checker.PrintPlan, spec *checker.FormatSpec, w string, write func(string) string) []string {
	var parts []string
	prec := 0
	if spec.Prec >= 0 {
		prec = spec.Prec + 1
	}
	numeric := spec.Zero || spec.Prec >= 0 || spec.Base != 0
	if numeric {
		parts = append(parts, fmt.Sprintf("%s(&%s, INT64_C(%d), %t, INT64_C(%d), INT64_C(%d), %t, %t)",
			plan.SpecBegin.CName, w, spec.Width, spec.Zero, prec, spec.Base, spec.Upper, spec.Alt))
	}
	if spec.Width > 0 && !spec.Zero {
		fill, align := fmt.Sprintf("(uint8_t)%d", spec.Fill), fmt.Sprintf("(uint8_t)%d", spec.Align)
		if spec.Align != '<' {
			m := g.temp(plan.Writer)
			parts = append(parts,
				fmt.Sprintf("%s = %s(&%s)", m, plan.Measure.CName, w),
				write(m),
				fmt.Sprintf("%s(&%s, %s, %s, INT64_C(%d), %s.f_chars)", plan.PadBefore.CName, w, fill, align, spec.Width, m))
		}
		start := g.temp(checker.Int)
		parts = append(parts,
			fmt.Sprintf("%s = %s.f_chars", start, w),
			write(w),
			fmt.Sprintf("%s(&%s, %s, %s, INT64_C(%d), %s.f_chars - %s)", plan.PadAfter.CName, w, fill, align, spec.Width, w, start))
	} else {
		parts = append(parts, write(w))
	}
	if numeric {
		parts = append(parts, fmt.Sprintf("%s(&%s)", plan.SpecEnd.CName, w))
	}
	return parts
}

func (g *gen) expr(e ast.Expr) string {
	// Expressões constantes (literais, constantes, contas entre constantes)
	// já foram calculadas pelo checker: viram um literal do tipo final.
	if v, ok := g.info.Values[e]; ok {
		return constant(v, g.typeOf(e))
	}
	if v, ok := g.info.EnumCtors[e]; ok {
		return g.enumCtor(e, v)
	}
	if fn, ok := g.info.FuncRefs[e]; ok {
		return fn.CName // a função usada como valor: o endereço dela
	}
	if _, ok := g.info.Types[e].(*checker.OutHandle); ok {
		return "&" + g.expr(e.(*ast.UnaryExpr).X) // &h para um parâmetro &?*X: o lugar do handle
	}
	switch e := e.(type) {
	case *ast.Ident:
		if l := g.info.Locals[e]; l != nil {
			return l.CName
		}
		if e.Name == "nil" {
			return "NULL"
		}
		return e.Name // true / false
	case *ast.UnaryExpr:
		switch e.Op {
		case token.SUB:
			return neg(g.typeOf(e.X), g.expr(e.X))
		case token.MUL:
			return "(*" + g.expr(e.X) + ")"
		case token.XOR:
			return fmt.Sprintf("((%s)~(%s))", numericC[g.typeOf(e)], g.expr(e.X))
		case token.AMP:
			if _, at := g.arrayOf(e.X); at != nil {
				if _, view := g.info.Types[e].(*checker.Borrow); view && !isArrayBorrow(g.info.Types[e]) {
					return g.arrayView(e.X) // &a como &[]T
				}
			}
			return g.borrow(e.X)
		}
		return "(!" + g.expr(e.X) + ")"
	case *ast.BinaryExpr:
		return g.binary(e, false)
	case *ast.IndexExpr:
		return g.index(e)
	case *ast.SelectorExpr:
		if isPointer(g.info.Types[e.X]) {
			return g.expr(e.X) + "->f_" + e.Sel
		}
		return g.expr(e.X) + ".f_" + e.Sel
	case *ast.CompositeLit:
		return g.composite(e)
	case *ast.ArrayLit:
		return g.arrayLit(e)
	case *ast.CallExpr:
		callee := g.info.Calls[e]
		switch callee.Builtin {
		case checker.IoPrint:
			return g.ioPrint(e)
		case checker.ThreadSpawn:
			return g.threadSpawn(e)
		case checker.ThreadJoin:
			return g.threadJoin(e)
		}
		switch callee.Builtin {
		case checker.Alloc:
			return g.alloc(e, callee)
		case checker.Free:
			if _, ok := g.info.Types[e.Args[0]].(*checker.Slice); ok {
				return "mlc_free(" + g.expr(e.Args[0]) + ".ptr)"
			}
			if t, ok := g.info.Types[e.Args[0]].(*checker.Enum); ok {
				return dropName(t) + "(" + g.expr(e.Args[0]) + ")" // os donos da variante
			}
			return "mlc_free(" + g.expr(e.Args[0]) + ")"
		case checker.Drop:
			// drop(x): libera x conforme o tipo (mlc_drop_*); nada se não for
			// dono (T = int numa instância)
			if t := g.info.Types[e.Args[0]]; checker.IsOwner(t) {
				return dropName(t) + "(" + g.expr(e.Args[0]) + ")"
			}
			return "((void)(" + g.expr(e.Args[0]) + "))"
		case checker.Realloc:
			return g.realloc(e, callee)
		case checker.Take, checker.Replace:
			return g.takeReplace(e, callee)
		case checker.Convert:
			return g.convert(e, callee)
		case checker.Len:
			if a, at := g.arrayOf(e.Args[0]); at != nil {
				return fmt.Sprintf("((void)%s, INT64_C(%d))", a, at.Len) // o tamanho é fixo; o array ainda é avaliado
			}
			return g.sliceVal(e.Args[0]) + ".len"
		}
		if callee.Indirect {
			return g.indirectCall(e)
		}
		if callee.Func != nil && callee.Func.CSym != "" {
			return g.cCall(e, callee.Func)
		}
		pre, args := g.operands(g.info.CallArgs(e)) // num método, o receptor vem primeiro
		if callee.Func.Native != "" {
			// função do runtime: recebe arquivo e linha para os panics
			args = append(args, "__FILE__", "__LINE__")
			return sequence(pre, fmt.Sprintf("%s(%s)", callee.Func.Native, strings.Join(args, ", ")))
		}
		return sequence(pre, fmt.Sprintf("%s(%s)", callee.Func.CName, strings.Join(args, ", ")))
	}
	panic(fmt.Sprintf("codegen: expressão desconhecida %T", e))
}

// indirectCall traduz f(args) com f um valor fn: f é calculado primeiro,
// depois os argumentos (da esquerda para a direita), e a chamada confere que
// f não é nulo (o valor zero de um fn).
func (g *gen) indirectCall(e *ast.CallExpr) string {
	fun := g.temp(g.info.Types[e.Fun])
	pre := []string{fun + " = " + g.expr(e.Fun)}
	more, args := g.operands(e.Args)
	pre = append(pre, more...)
	pre = append(pre, fmt.Sprintf("(%s == NULL ? mlc_null_fn(__FILE__, __LINE__) : (void)0)", fun))
	return sequence(pre, fmt.Sprintf("%s(%s)", fun, strings.Join(args, ", ")))
}

// cString escreve s como literal de string C. Bytes fora do ASCII imprimível
// viram escapes octais, e '?' é escapado para não formar trígrafos.
func cString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\' || c == '?':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, "\\%03o", c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// dropName é a função que libera um valor do tipo t (drop em código genérico).
func dropName(t checker.Type) string { return "mlc_drop_" + mangle(t) }

// dropHelpers escreve uma função de drop para cada tipo dono liberado por
// drop: free de um dono simples, o método free de uma struct com donos, ou
// cada elemento e depois o slice.
func (g *gen) dropHelpers() {
	for _, t := range g.info.DropOrder {
		g.raw("static MLC_MAYBE_UNUSED void %s(%s v);\n", dropName(t), cType(t))
	}
	for _, t := range g.info.DropOrder {
		g.raw("static void %s(%s v) {\n", dropName(t), cType(t))
		switch tt := t.(type) {
		case *checker.Pointer:
			if s, ok := tt.Elem.(*checker.Struct); ok && s.Resource {
				g.raw("    if (v != NULL) %s(v);\n", g.info.DropFree[s].CName)
			} else {
				g.raw("    mlc_free(v);\n")
			}
		case *checker.Slice:
			if checker.IsOwner(tt.Elem) {
				g.raw("    for (int64_t i = 0; i < v.len; i++) %s(v.ptr[i]);\n", dropName(tt.Elem))
			}
			g.raw("    mlc_free(v.ptr);\n")
		case *checker.Enum:
			// libera os donos da variante que estiver lá
			g.raw("    switch (v.tag) {\n")
			for _, vr := range tt.Variants {
				var frees []string
				for _, f := range vr.Fields {
					if checker.IsOwner(f.Type) {
						frees = append(frees, fmt.Sprintf("%s(v.u.v_%s.f_%s);", dropName(f.Type), vr.Name, f.Name))
					}
				}
				if len(frees) > 0 {
					g.raw("    case %d: %s break;\n", vr.Tag, strings.Join(frees, " "))
				}
			}
			g.raw("    default: break;\n    }\n")
		}
		g.raw("}\n")
	}
}

// cFFIType é o tipo C de um parâmetro ou retorno de uma função de C: string
// vira const char * (os literais do MLC terminam em zero) e &[]T vira o
// ponteiro para os dados.
func cFFIType(t checker.Type) string {
	if t == checker.String {
		return "const char *"
	}
	if b, ok := t.(*checker.Borrow); ok {
		if s, ok := b.Elem.(*checker.Slice); ok {
			return cType(s.Elem) + " *"
		}
	}
	if oh, ok := t.(*checker.OutHandle); ok {
		return cType(oh.Ptr) + "*" // &?*X: o endereço da variável que recebe o handle
	}
	return cType(t)
}

// cCall traduz a chamada de uma função de C, convertendo os argumentos.
func (g *gen) cCall(e *ast.CallExpr, fn *checker.Func) string {
	pre, args := g.operands(e.Args)
	for i, p := range fn.Params {
		if i >= len(args) {
			break
		}
		switch {
		case p.Type == checker.String:
			args[i] = "(" + args[i] + ").ptr"
		case isSliceBorrow(p.Type):
			args[i] = "(" + args[i] + ")->ptr"
		}
	}
	return sequence(pre, fmt.Sprintf("%s(%s)", fn.CName, strings.Join(args, ", ")))
}

func isSliceBorrow(t checker.Type) bool {
	b, ok := t.(*checker.Borrow)
	if !ok {
		return false
	}
	_, ok = b.Elem.(*checker.Slice)
	return ok
}
