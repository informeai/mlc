// Package fuzz attacks the ownership checker on purpose (see
// docs/verification.md). This is phase 2: mutations known to create an
// ownership bug in a program the checker accepts. The checker must
// refuse the mutant; if it accepts it, the mutant runs under the sanitizers and the
// allocation counter, and an error there is a hole in the checker.
package fuzz

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/doc"
	"mlc/internal/token"
)

// Mutant is the program with one mutation applied.
type Mutant struct {
	Kind string // the mutation's name (Kinds)
	Line int    // the line of the original where it was applied
	Desc string // what was done, for the report
	Src  []byte
}

// The mutations. Each one creates an ownership bug if the mutated piece runs.
const (
	DropRelease   = "deletes the free"       // leak (or a lock, a forgotten thread)
	DupRelease    = "duplicates the free"    // double free, use after consuming
	EarlyRelease  = "frees before the use"   // use after free
	UseAfter      = "uses after freeing"     // use after free
	CondRelease   = "frees on only one path" // leak on the other path
	DupMove       = "moves twice"            // use after move
	UseAfterMove  = "uses after moving"      // use after move
	BorrowToMove  = "replaces &x with x"     // a move where there was a borrow
	condName      = "mlc_mutation"
	mutatedMarker = "// mutation: "
)

// Mutants returns the mutants of an already checked program (f is the main
// file's AST, info the checker's result).
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

// block visits the statements of a block and the blocks inside.
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

// stmt applies the mutations that fit statement i of block b.
func (m *mutator) stmt(b *ast.Block, i int, s ast.Stmt) {
	line := s.Pos().Line
	if !m.simpleLine(line) {
		return
	}
	text := strings.TrimSpace(m.lines[line-1])
	if v := m.release(s); v != "" {
		m.add(DropRelease, line, "deletes "+text, m.replace(line))
		m.add(DupRelease, line, "repeats "+text, m.replace(line, m.lines[line-1], m.lines[line-1]))
		indent := indentOf(m.lines[line-1])
		m.add(CondRelease, line, "puts "+text+" inside an if", m.replace(line,
			indent+condName+" := 0",
			indent+"if "+condName+" == 1 {",
			indent+"    "+text,
			indent+"}"))
		if i > 0 {
			prev := b.Stmts[i-1]
			if pl := prev.Pos().Line; pl == line-1 && m.simpleLine(pl) && mentions(prev, v) && !declares(prev) {
				m.add(EarlyRelease, line, "swaps "+text+" with the line above, which uses "+v, m.swap(pl, line))
			}
		}
		// repeats v's last use in the same block after the free
		for j := i - 1; j >= 0; j-- {
			prev := b.Stmts[j]
			pl := prev.Pos().Line
			if m.simpleLine(pl) && mentions(prev, v) && !declares(prev) && m.release(prev) == "" {
				use := strings.TrimSpace(m.lines[pl-1])
				m.add(UseAfter, line, "repeats "+use+" after "+text, m.replace(line, m.lines[line-1], indentOf(m.lines[line-1])+use))
				break
			}
		}
		return
	}
	if v := m.moves(s); v != "" {
		m.add(DupMove, line, "repeats "+text+" (moves "+v+" again)", m.replace(line, m.lines[line-1], m.lines[line-1]))
		// the block's next statement that uses v, before the move
		for j := i + 1; j < len(b.Stmts); j++ {
			next := b.Stmts[j]
			nl := next.Pos().Line
			if m.simpleLine(nl) && mentions(next, v) && !declares(next) {
				use := strings.TrimSpace(m.lines[nl-1])
				m.add(UseAfterMove, line, "puts "+use+" right after "+text, m.replace(line, m.lines[line-1], indentOf(m.lines[line-1])+use))
				break
			}
		}
	}
	if v, ok := m.borrowArg(s); ok {
		mut := strings.Replace(m.lines[line-1], "&"+v, v, 1)
		m.add(BorrowToMove, line, "replaces &"+v+" with "+v+" in "+text, m.replace(line, mut))
	}
}

func (m *mutator) add(kind string, line int, desc string, lines []string) {
	src := mutatedMarker + kind + ", line " + fmt.Sprint(line) + ": " + desc + "\n" + strings.Join(lines, "\n")
	m.out = append(m.out, Mutant{Kind: kind, Line: line, Desc: desc, Src: []byte(src)})
}

// replace returns the program's lines with line n replaced by with
// (none: the line goes away). The mutant's first line is the comment with the
// mutation, so the errors' lines are one below the original's.
func (m *mutator) replace(n int, with ...string) []string {
	out := append([]string{}, m.lines[:n-1]...)
	out = append(out, with...)
	if len(with) == 0 {
		out = append(out, "") // keeps the numbering of the lines below
	}
	return append(out, m.lines[n:]...)
}

func (m *mutator) swap(a, b int) []string {
	out := append([]string{}, m.lines...)
	out[a-1], out[b-1] = out[b-1], out[a-1]
	return out
}

// simpleLine says whether line n has a whole statement, without a block: only those
// are moved, deleted or repeated.
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

// release returns the variable the statement frees or consumes: free(x),
// drop(x), thread.join(t), x.close(), g.unlock(), a method with a *T receiver
// (x.free(), g.wait()). "" if it isn't a release.
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

// moves returns the owning variable the statement moves into a call:
// v.push(x), tx.send(x), f(x) with f's parameter an owner (*T, []T). "" if it doesn't
// move anything (io.print(x) only reads).
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
			params = params[1:] // the receiver
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

// borrowArg finds an argument &x, with x an owner, in a call: replacing it with x
// moves the owner to something that should only borrow.
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

// root is the variable at the root of x, x.field, x[i] or *x.
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

// path writes x, x.a or x.a.b ("" for another expression; *x and x[i] become
// x's path).
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

// mentions says whether the statement uses the path p: itself, something inside it
// (p.field) or the object that contains it (using w whole uses w.results).
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
		return !isSel // a path a.b is looked at whole, without going down to a
	})
	return found
}

// declares says whether the statement declares a variable (repeating it would be a
// repeated-name error, not an ownership one).
func declares(s ast.Stmt) bool {
	_, ok := s.(*ast.VarDecl)
	return ok
}

func indentOf(l string) string {
	return l[:len(l)-len(strings.TrimLeft(l, " \t"))]
}
