// Package fuzz ataca o verificador de posse de propósito (veja
// docs/verification.md). Esta é a fase 2: mutações que sabidamente criam um
// bug de posse num programa que o verificador aceita. O verificador precisa
// recusar o mutante; se aceitar, o mutante roda sob os sanitizers e o
// contador de alocações, e um erro ali é um buraco no verificador.
package fuzz

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/doc"
	"mlc/internal/token"
)

// Mutant é o programa com uma mutação aplicada.
type Mutant struct {
	Kind string // o nome da mutação (Kinds)
	Line int    // a linha do original onde ela foi aplicada
	Desc string // o que foi feito, para o relatório
	Src  []byte
}

// As mutações. Cada uma cria um bug de posse se o trecho mutado roda.
const (
	DropRelease   = "apaga a liberação"     // vazamento (ou trava, thread esquecida)
	DupRelease    = "duplica a liberação"   // free duplo, uso depois de consumir
	EarlyRelease  = "libera antes do uso"   // uso depois de free
	UseAfter      = "usa depois de liberar" // uso depois de free
	CondRelease   = "libera só num caminho" // vazamento no outro caminho
	DupMove       = "move duas vezes"       // uso depois de mover
	UseAfterMove  = "usa depois de mover"   // uso depois de mover
	BorrowToMove  = "troca &x por x"        // move onde era empréstimo
	condName      = "mlc_mutacao"
	mutatedMarker = "// mutação: "
)

// Mutants devolve os mutantes de um programa já checado (f é a AST do
// arquivo principal, info o resultado do checker).
func Mutants(src []byte, f *ast.File, info *checker.Info) []Mutant {
	m := &mutator{lines: strings.Split(string(src), "\n"), info: info}
	for _, fn := range f.Funcs {
		if fn.Body != nil {
			m.block(fn.Body)
		}
	}
	return m.out
}

type mutator struct {
	lines []string
	info  *checker.Info
	out   []Mutant
}

// block visita os comandos de um bloco e os blocos de dentro.
func (m *mutator) block(b *ast.Block) {
	for i, s := range b.Stmts {
		m.stmt(b, i, s)
		switch s := s.(type) {
		case *ast.Block:
			m.block(s)
		case *ast.IfStmt:
			m.ifStmt(s)
		case *ast.ForStmt:
			m.block(s.Body)
		case *ast.MatchStmt:
			for _, arm := range s.Arms {
				if body, ok := arm.Body.(*ast.Block); ok {
					m.block(body)
				}
			}
		}
	}
}

func (m *mutator) ifStmt(s *ast.IfStmt) {
	m.block(s.Then)
	switch e := s.Else.(type) {
	case *ast.Block:
		m.block(e)
	case *ast.IfStmt:
		m.ifStmt(e)
	}
}

// stmt aplica as mutações que cabem no comando i do bloco b.
func (m *mutator) stmt(b *ast.Block, i int, s ast.Stmt) {
	line := s.Pos().Line
	if !m.simpleLine(line) {
		return
	}
	text := strings.TrimSpace(m.lines[line-1])
	if v := m.release(s); v != "" {
		m.add(DropRelease, line, "apaga "+text, m.replace(line))
		m.add(DupRelease, line, "repete "+text, m.replace(line, m.lines[line-1], m.lines[line-1]))
		indent := indentOf(m.lines[line-1])
		m.add(CondRelease, line, "põe "+text+" dentro de um if", m.replace(line,
			indent+condName+" := 0",
			indent+"if "+condName+" == 1 {",
			indent+"    "+text,
			indent+"}"))
		if i > 0 {
			prev := b.Stmts[i-1]
			if pl := prev.Pos().Line; pl == line-1 && m.simpleLine(pl) && mentions(prev, v) && !declares(prev) {
				m.add(EarlyRelease, line, "troca "+text+" com a linha de cima, que usa "+v, m.swap(pl, line))
			}
		}
		// repete depois da liberação o último uso de v no mesmo bloco
		for j := i - 1; j >= 0; j-- {
			prev := b.Stmts[j]
			pl := prev.Pos().Line
			if m.simpleLine(pl) && mentions(prev, v) && !declares(prev) && m.release(prev) == "" {
				use := strings.TrimSpace(m.lines[pl-1])
				m.add(UseAfter, line, "repete "+use+" depois de "+text, m.replace(line, m.lines[line-1], indentOf(m.lines[line-1])+use))
				break
			}
		}
		return
	}
	if v := m.moves(s); v != "" {
		m.add(DupMove, line, "repete "+text+" (move "+v+" de novo)", m.replace(line, m.lines[line-1], m.lines[line-1]))
		// o próximo comando do bloco que usa v, antes do move
		for j := i + 1; j < len(b.Stmts); j++ {
			next := b.Stmts[j]
			nl := next.Pos().Line
			if m.simpleLine(nl) && mentions(next, v) && !declares(next) {
				use := strings.TrimSpace(m.lines[nl-1])
				m.add(UseAfterMove, line, "põe "+use+" logo depois de "+text, m.replace(line, m.lines[line-1], indentOf(m.lines[line-1])+use))
				break
			}
		}
	}
	if v, ok := m.borrowArg(s); ok {
		mut := strings.Replace(m.lines[line-1], "&"+v, v, 1)
		m.add(BorrowToMove, line, "troca &"+v+" por "+v+" em "+text, m.replace(line, mut))
	}
}

func (m *mutator) add(kind string, line int, desc string, lines []string) {
	src := mutatedMarker + kind + ", linha " + fmt.Sprint(line) + ": " + desc + "\n" + strings.Join(lines, "\n")
	m.out = append(m.out, Mutant{Kind: kind, Line: line, Desc: desc, Src: []byte(src)})
}

// replace devolve as linhas do programa com a linha n trocada por with
// (nenhuma: a linha some). A primeira linha do mutante é o comentário com a
// mutação, então as linhas dos erros ficam uma abaixo das do original.
func (m *mutator) replace(n int, with ...string) []string {
	out := append([]string{}, m.lines[:n-1]...)
	out = append(out, with...)
	if len(with) == 0 {
		out = append(out, "") // mantém a numeração das linhas de baixo
	}
	return append(out, m.lines[n:]...)
}

func (m *mutator) swap(a, b int) []string {
	out := append([]string{}, m.lines...)
	out[a-1], out[b-1] = out[b-1], out[a-1]
	return out
}

// simpleLine diz se a linha n tem um comando inteiro, sem bloco: só esses
// são movidos, apagados ou repetidos.
func (m *mutator) simpleLine(n int) bool {
	if n < 1 || n > len(m.lines) {
		return false
	}
	l := doc.StripComment(strings.TrimSpace(m.lines[n-1]))
	if l == "" || strings.ContainsAny(l, "{}") || strings.HasSuffix(l, ",") || strings.HasSuffix(l, "(") {
		return false
	}
	return strings.Count(l, "(") == strings.Count(l, ")")
}

// release devolve a variável que o comando libera ou consome: free(x),
// drop(x), thread.join(t), x.close(), g.unlock(), um método com receptor *T
// (x.free(), g.wait()). "" se não é uma liberação.
func (m *mutator) release(s ast.Stmt) string {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return ""
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return ""
	}
	k := m.info.Calls[call]
	switch k.Builtin {
	case checker.Free, checker.Drop, checker.ThreadJoin:
		if len(call.Args) == 1 {
			return path(call.Args[0])
		}
		return ""
	case checker.ChanOp:
		if p := m.info.Chans[call]; p != nil && p.Op == "close" {
			return recvRoot(call)
		}
		return ""
	case checker.MutexOp:
		if p := m.info.Mutexes[call]; p != nil && (p.Op == "close" || p.Op == "unlock") {
			return recvRoot(call)
		}
		return ""
	}
	if fn := k.Func; fn != nil && fn.Method && !k.Indirect && len(fn.Params) > 0 {
		if _, owner := fn.Params[0].Type.(*checker.Pointer); owner {
			return recvRoot(call)
		}
	}
	return ""
}

// moves devolve a variável dona que o comando move para uma chamada:
// v.push(x), tx.send(x), f(x) com o parâmetro de f dono (*T, []T). "" se não
// move nada (io.print(x) só lê).
func (m *mutator) moves(s ast.Stmt) string {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return ""
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return ""
	}
	k := m.info.Calls[call]
	var params []*checker.Local
	switch {
	case k.Builtin == checker.ChanOp:
		if p := m.info.Chans[call]; p == nil || p.Op != "send" {
			return ""
		}
	case k.Builtin != checker.NoBuiltin || k.Func == nil:
		return ""
	default:
		params = k.Func.Params
		if k.Func.Method && !k.Indirect && len(params) > 0 {
			params = params[1:] // o receptor
		}
	}
	for i, a := range call.Args {
		id, ok := a.(*ast.Ident)
		if !ok {
			continue
		}
		if params != nil && (i >= len(params) || !checker.IsOwner(params[i].Type)) {
			continue
		}
		if l := m.info.Locals[id]; l != nil && checker.IsOwner(l.Type) {
			return id.Name
		}
	}
	return ""
}

// borrowArg acha um argumento &x, com x dono, numa chamada: trocar por x
// move o dono para quem só devia emprestar.
func (m *mutator) borrowArg(s ast.Stmt) (string, bool) {
	var name string
	ast.Inspect(s, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || name != "" {
			return name == ""
		}
		for _, a := range call.Args {
			u, ok := a.(*ast.UnaryExpr)
			if !ok || u.Op != token.AMP {
				continue
			}
			id, ok := u.X.(*ast.Ident)
			if !ok {
				continue
			}
			if l := m.info.Locals[id]; l != nil && checker.IsOwner(l.Type) {
				name = id.Name
				return false
			}
		}
		return true
	})
	return name, name != ""
}

// root é a variável na raiz de x, x.campo, x[i] ou *x.
func root(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.UnaryExpr:
			e = x.X
		default:
			return ""
		}
	}
}

func recvRoot(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return path(sel.X)
	}
	return ""
}

// path escreve x, x.a ou x.a.b ("" para outra expressão; *x e x[i] viram
// o caminho de x).
func path(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if p := path(x.X); p != "" {
			return p + "." + x.Sel
		}
	case *ast.UnaryExpr:
		return path(x.X)
	case *ast.IndexExpr:
		return path(x.X)
	}
	return root(e)
}

// mentions diz se o comando usa o caminho p: ele mesmo, algo dentro dele
// (p.campo) ou o objeto que o contém (usar w inteiro usa w.results).
func mentions(s ast.Stmt, p string) bool {
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		if found {
			return false
		}
		var q string
		switch x := n.(type) {
		case *ast.SelectorExpr:
			q = path(x)
		case *ast.Ident:
			q = x.Name
		default:
			return true
		}
		if q == p || strings.HasPrefix(q, p+".") || strings.HasPrefix(p, q+".") {
			found = true
			return false
		}
		_, isSel := n.(*ast.SelectorExpr)
		return !isSel // um caminho a.b é olhado inteiro, sem descer para a
	})
	return found
}

// declares diz se o comando declara uma variável (repeti-lo seria erro de
// nome repetido, não de posse).
func declares(s ast.Stmt) bool {
	_, ok := s.(*ast.VarDecl)
	return ok
}

func indentOf(l string) string {
	return l[:len(l)-len(strings.TrimLeft(l, " \t"))]
}
