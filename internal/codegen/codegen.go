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
	for _, fn := range info.Funcs {
		g.raw("static %s;\n", signature(fn))
	}
	g.raw("\n")
	for _, fn := range info.Funcs {
		g.function(fn)
	}
	g.raw("int main(void) {\n    %s();\n    mlc_check_allocs();\n    return 0;\n}\n", info.Main.CName)
	return g.buf.String()
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
	}
	if s, ok := t.(*checker.Struct); ok {
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
		if checker.IsInteger(g.typeOf(s.Subject)) {
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
		return g.expr(s.X)
	case *ast.VarDecl:
		l := g.info.Decls[s]
		value := zeroValue(l.Type)
		if s.Value != nil {
			value = g.expr(s.Value)
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
		return sequence(append(pre, ops...), lhs+" = "+arith(assignOps[s.Op], g.typeOf(s.Lhs), v[0], v[1]))
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

// assignOps liga cada atribuição composta ao seu operador aritmético.
var assignOps = map[token.Kind]token.Kind{
	token.ADD_ASSIGN: token.ADD,
	token.SUB_ASSIGN: token.SUB,
	token.MUL_ASSIGN: token.MUL,
	token.QUO_ASSIGN: token.QUO,
	token.REM_ASSIGN: token.REM,
}

func zeroValue(t checker.Type) string {
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

// ioPrint traduz io.print para uma expressão com vírgulas: argumentos
// separados por espaço e quebra de linha no fim.
//
// Como em qualquer chamada, todos os argumentos são calculados antes de
// imprimir: se algum tem efeito, os que não são literais vão para
// temporárias, e só depois começam as impressões.
func (g *gen) ioPrint(call *ast.CallExpr) string {
	effects := false
	for _, a := range call.Args {
		effects = effects || g.effectful(a)
	}
	var parts []string
	vals := make([]string, len(call.Args))
	for i, a := range call.Args {
		vals[i] = g.expr(a)
		if effects && !g.isLiteral(a) {
			t := g.temp(g.info.Types[a])
			parts = append(parts, t+" = "+vals[i])
			vals[i] = t
		}
	}
	for i, a := range call.Args {
		if i > 0 {
			parts = append(parts, "mlc_io_print_sep()")
		}
		parts = append(parts, printCall(g.info.Types[a], vals[i]))
	}
	parts = append(parts, "mlc_io_print_newline()")
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func (g *gen) expr(e ast.Expr) string {
	// Expressões constantes (literais, constantes, contas entre constantes)
	// já foram calculadas pelo checker: viram um literal do tipo final.
	if v, ok := g.info.Values[e]; ok {
		return constant(v, g.typeOf(e))
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
		if callee.Builtin == checker.IoPrint {
			return g.ioPrint(e)
		}
		switch callee.Builtin {
		case checker.Alloc:
			return g.alloc(e, callee)
		case checker.Free:
			if _, ok := g.info.Types[e.Args[0]].(*checker.Slice); ok {
				return "mlc_free(" + g.expr(e.Args[0]) + ".ptr)"
			}
			return "mlc_free(" + g.expr(e.Args[0]) + ")"
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
		pre, args := g.operands(e.Args)
		if callee.Func.Native != "" {
			// função do runtime: recebe arquivo e linha para os panics
			args = append(args, "__FILE__", "__LINE__")
			return sequence(pre, fmt.Sprintf("%s(%s)", callee.Func.Native, strings.Join(args, ", ")))
		}
		return sequence(pre, fmt.Sprintf("%s(%s)", callee.Func.CName, strings.Join(args, ", ")))
	}
	panic(fmt.Sprintf("codegen: expressão desconhecida %T", e))
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
