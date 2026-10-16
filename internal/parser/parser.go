// Package parser implementa um parser recursive descent para a gramática
// de docs/gramatica.md. Cobre o subconjunto dos marcos já implementados e para
// no primeiro erro de sintaxe.
package parser

import (
	"fmt"
	"sort"

	"mlc/internal/ast"
	"mlc/internal/lexer"
	"mlc/internal/token"
)

// bailout interrompe o parse no primeiro erro; é recuperado em ParseFile.
type bailout struct{}

type Parser struct {
	file string
	lx   *lexer.Lexer
	tok  token.Token
	peek token.Token
	errs []token.Error

	// noLit desliga literais compostos no nível de cima do cabeçalho de
	// if/for/match: em "if x == P {", o '{' abre o bloco. Dentro de
	// parênteses, chamadas e índices eles voltam a valer.
	noLit bool
}

const litInHeader = "literal de struct no cabeçalho de if, for ou match precisa de parênteses: (Tipo{...})"

// withLit executa f com literais compostos ligados ou desligados.
func (p *Parser) withLit(allowed bool, f func()) {
	old := p.noLit
	p.noLit = !allowed
	defer func() { p.noLit = old }()
	f()
}

func ParseFile(file string, src []byte) (f *ast.File, errs []token.Error) {
	p := &Parser{file: file, lx: lexer.New(file, src)}
	p.tok = p.lx.Next()
	p.peek = p.lx.Next()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				panic(r)
			}
		}
		errs = append(append([]token.Error{}, p.lx.Errors()...), p.errs...)
		sort.SliceStable(errs, func(i, j int) bool { return errs[i].Pos.Before(errs[j].Pos) })
	}()
	return p.parseFile(), nil
}

func (p *Parser) next() {
	p.tok = p.peek
	p.peek = p.lx.Next()
}

func (p *Parser) errorf(pos token.Pos, format string, args ...any) {
	p.errs = append(p.errs, token.Error{File: p.file, Pos: pos, Msg: fmt.Sprintf(format, args...)})
	panic(bailout{})
}

func (p *Parser) expect(k token.Kind) token.Token {
	if p.tok.Kind != k {
		p.unexpected("esperado " + describeKind(k))
	}
	t := p.tok
	p.next()
	return t
}

// unexpected reporta o token atual. Tokens ILLEGAL já foram reportados pelo lexer.
func (p *Parser) unexpected(what string) {
	if p.tok.Kind == token.ILLEGAL {
		panic(bailout{})
	}
	p.errorf(p.tok.Pos, "%s, encontrado %s", what, describe(p.tok))
}

// expectEOS consome o fim de comando. Antes de '}' ele pode ser omitido.
func (p *Parser) expectEOS() {
	switch p.tok.Kind {
	case token.SEMICOLON:
		p.next()
	case token.RBRACE, token.EOF:
	default:
		p.unexpected("esperado fim de linha ou ';'")
	}
}

func describeKind(k token.Kind) string {
	switch k {
	case token.IDENT:
		return "identificador"
	case token.STRING:
		return "string"
	case token.SEMICOLON:
		return "';' ou fim de linha"
	}
	return fmt.Sprintf("'%s'", k)
}

func describe(t token.Token) string {
	switch t.Kind {
	case token.EOF:
		return "fim do arquivo"
	case token.SEMICOLON:
		if t.Lit == "\n" {
			return "fim de linha"
		}
	case token.IDENT:
		return fmt.Sprintf("identificador %s", t.Lit)
	case token.INT, token.FLOAT:
		return fmt.Sprintf("número %s", t.Lit)
	case token.STRING:
		return fmt.Sprintf("string %q", t.Lit)
	}
	return fmt.Sprintf("'%s'", t.Kind)
}

// ---------- Declarações ----------

func (p *Parser) parseFile() *ast.File {
	f := &ast.File{}
	for p.tok.Kind != token.EOF {
		switch p.tok.Kind {
		case token.SEMICOLON:
			p.next()
		case token.IMPORT:
			f.Imports = append(f.Imports, p.parseImport())
		case token.PUB:
			p.parsePub(f)
		case token.FN:
			f.Funcs = append(f.Funcs, p.parseFunc())
		case token.STRUCT:
			f.Structs = append(f.Structs, p.parseStruct())
		case token.CONST:
			f.Consts = append(f.Consts, p.parseConst())
		default:
			p.unexpected("esperado declaração (import, fn, struct ou const)")
		}
	}
	return f
}

func (p *Parser) parseImport() *ast.Import {
	pos := p.expect(token.IMPORT).Pos
	path := p.expect(token.STRING).Lit
	p.expectEOS()
	return &ast.Import{Path: path, P: pos}
}

func (p *Parser) parseFunc() *ast.FuncDecl {
	pos := p.expect(token.FN).Pos
	fn := &ast.FuncDecl{Name: p.expect(token.IDENT).Lit, P: pos}
	p.expect(token.LPAREN)
	for p.tok.Kind != token.RPAREN {
		name := p.expect(token.IDENT)
		fn.Params = append(fn.Params, &ast.Param{Name: name.Lit, Type: p.parseType(), P: name.Pos})
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
	}
	p.expect(token.RPAREN)
	if p.startsType() {
		fn.Result = p.parseType()
	}
	fn.Body = p.parseBlock()
	return fn
}

func (p *Parser) parseStruct() *ast.StructDecl {
	pos := p.expect(token.STRUCT).Pos
	s := &ast.StructDecl{Name: p.expect(token.IDENT).Lit, P: pos}
	p.expect(token.LBRACE)
	for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
		if p.tok.Kind == token.SEMICOLON {
			p.next()
			continue
		}
		name := p.expect(token.IDENT)
		s.Fields = append(s.Fields, &ast.FieldDecl{Name: name.Lit, Type: p.parseType(), P: name.Pos})
		p.expectEOS()
	}
	p.expect(token.RBRACE)
	return s
}

func (p *Parser) parseConst() *ast.ConstDecl {
	p.expect(token.CONST)
	name := p.expect(token.IDENT)
	d := &ast.ConstDecl{Name: name.Lit, P: name.Pos}
	if p.startsType() {
		d.Type = p.parseType()
	}
	p.expect(token.ASSIGN)
	d.Value = p.parseExpr()
	p.expectEOS()
	return d
}

// startsType diz se o token atual pode começar um tipo.
func (p *Parser) startsType() bool {
	switch p.tok.Kind {
	case token.IDENT, token.LBRACK, token.MUL, token.QUESTION, token.AMP:
		return true
	}
	return false
}

func (p *Parser) parseType() *ast.TypeExpr {
	pos := p.tok.Pos
	switch p.tok.Kind {
	case token.IDENT:
		name := p.tok.Lit
		p.next()
		if p.tok.Kind == token.PERIOD && p.peek.Kind == token.IDENT {
			// tipo de outro arquivo: geometria.Ponto
			p.next()
			qualified := &ast.TypeExpr{Module: name, Name: p.tok.Lit, P: pos}
			p.next()
			return qualified
		}
		return &ast.TypeExpr{Name: name, P: pos}
	case token.LBRACK:
		p.next()
		p.expect(token.RBRACK)
		return &ast.TypeExpr{Mod: ast.TypeSlice, Elem: p.parseType(), P: pos}
	case token.MUL:
		p.next()
		return &ast.TypeExpr{Mod: ast.TypePointer, Elem: p.parseType(), P: pos}
	case token.QUESTION:
		p.next()
		p.expect(token.MUL)
		return &ast.TypeExpr{Mod: ast.TypeNullPtr, Elem: p.parseType(), P: pos}
	case token.AMP:
		p.next()
		return &ast.TypeExpr{Mod: ast.TypeBorrow, Elem: p.parseType(), P: pos}
	}
	p.unexpected("esperado tipo")
	return nil
}

// ---------- Comandos ----------

func (p *Parser) parseBlock() *ast.Block {
	b := &ast.Block{P: p.expect(token.LBRACE).Pos}
	for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
		if p.tok.Kind == token.SEMICOLON {
			p.next()
			continue
		}
		b.Stmts = append(b.Stmts, p.parseStmt())
	}
	b.End = p.expect(token.RBRACE).Pos
	return b
}

func (p *Parser) parseStmt() ast.Stmt {
	if p.tok.Kind == token.IDENT && p.peek.Kind == token.COLON {
		// Não existem labels, então "nome:" no começo de um comando quase
		// sempre é um literal de struct que o cabeçalho de if/for/match cortou.
		p.errorf(p.tok.Pos, litInHeader)
	}
	switch p.tok.Kind {
	case token.LBRACE:
		return p.parseBlock()
	case token.RETURN:
		s := &ast.ReturnStmt{P: p.tok.Pos}
		p.next()
		if p.tok.Kind != token.SEMICOLON && p.tok.Kind != token.RBRACE {
			s.Value = p.parseExpr()
		}
		p.expectEOS()
		return s
	case token.VAR:
		return p.parseVarDecl()
	case token.IF:
		return p.parseIf()
	case token.FOR:
		return p.parseFor()
	case token.BREAK, token.CONTINUE:
		s := &ast.BranchStmt{Tok: p.tok.Kind, P: p.tok.Pos}
		p.next()
		p.expectEOS()
		return s
	case token.MATCH:
		return p.parseMatch()
	case token.CONST:
		return p.parseConst()
	case token.ELSE:
		p.errorf(p.tok.Pos, "else precisa ficar na mesma linha do '}' que fecha o if")
	}
	s := p.parseSimpleStmt()
	p.expectEOS()
	return s
}

// parseSimpleStmt lê x := v, atribuições, x++/x-- ou uma expressão.
func (p *Parser) parseSimpleStmt() ast.Stmt {
	x := p.parseExpr()
	switch p.tok.Kind {
	case token.DEFINE:
		id, ok := x.(*ast.Ident)
		if !ok {
			p.errorf(x.Pos(), "o lado esquerdo de := precisa ser um nome")
		}
		p.next()
		return &ast.VarDecl{Name: id.Name, Value: p.parseExpr(), Short: true, P: id.P}
	case token.ASSIGN, token.ADD_ASSIGN, token.SUB_ASSIGN,
		token.MUL_ASSIGN, token.QUO_ASSIGN, token.REM_ASSIGN:
		op := p.tok
		p.next()
		return &ast.AssignStmt{Op: op.Kind, Lhs: x, Rhs: p.parseExpr(), OpPos: op.Pos}
	case token.INC, token.DEC:
		op := p.tok
		p.next()
		return &ast.IncDecStmt{Op: op.Kind, X: x, OpPos: op.Pos}
	}
	return &ast.ExprStmt{X: x}
}

func (p *Parser) parseVarDecl() *ast.VarDecl {
	p.expect(token.VAR)
	name := p.expect(token.IDENT)
	d := &ast.VarDecl{Name: name.Lit, P: name.Pos}
	if p.startsType() {
		d.Type = p.parseType()
	}
	if p.tok.Kind == token.ASSIGN {
		p.next()
		d.Value = p.parseExpr()
	}
	if d.Type == nil && d.Value == nil {
		p.errorf(name.Pos, "var %s precisa de tipo ou valor inicial", name.Lit)
	}
	p.expectEOS()
	return d
}

func (p *Parser) parseIf() *ast.IfStmt {
	s := &ast.IfStmt{P: p.expect(token.IF).Pos}
	p.withLit(false, func() { s.Cond = p.parseExpr() })
	s.Then = p.parseBlock()
	if p.tok.Kind != token.ELSE {
		return s
	}
	p.next()
	switch p.tok.Kind {
	case token.IF:
		s.Else = p.parseIf()
	case token.LBRACE:
		s.Else = p.parseBlock()
	default:
		p.unexpected("esperado if ou '{' depois de else")
	}
	return s
}

func (p *Parser) parseMatch() *ast.MatchStmt {
	s := &ast.MatchStmt{P: p.expect(token.MATCH).Pos}
	p.withLit(false, func() { s.Subject = p.parseExpr() })
	p.expect(token.LBRACE)
	for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
		if p.tok.Kind == token.SEMICOLON {
			p.next()
			continue
		}
		s.Arms = append(s.Arms, p.parseMatchArm())
	}
	p.expect(token.RBRACE)
	if len(s.Arms) == 0 {
		if p.tok.Kind == token.LBRACE {
			p.errorf(s.P, litInHeader)
		}
		p.errorf(s.P, "match sem braços")
	}
	return s
}

func (p *Parser) parseMatchArm() *ast.MatchArm {
	arm := &ast.MatchArm{P: p.tok.Pos}
	for {
		arm.Patterns = append(arm.Patterns, p.parsePattern())
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
	}
	p.expect(token.ARROW)
	switch p.tok.Kind {
	case token.LBRACE:
		arm.Body = p.parseBlock()
		p.expectEOS()
	case token.VAR, token.CONST, token.IF, token.FOR, token.MATCH:
		p.errorf(p.tok.Pos, "o braço do match aceita um comando simples, um bloco, break, continue ou return (use { } para mais)")
	default:
		arm.Body = p.parseStmt() // já consome o fim de comando
	}
	return arm
}

func (p *Parser) parsePattern() *ast.Pattern {
	t := p.tok
	pat := &ast.Pattern{P: t.Pos}
	switch t.Kind {
	case token.IDENT:
		switch t.Lit {
		case "_":
			pat.Wildcard = true
		default: // true, false ou uma constante; o checker valida
			pat.Lit = &ast.Ident{Name: t.Lit, P: t.Pos}
		}
		p.next()
		if id, ok := pat.Lit.(*ast.Ident); ok && p.tok.Kind == token.PERIOD && p.peek.Kind == token.IDENT {
			// constante de outro arquivo: geometria.MAX
			p.next()
			pat.Lit = &ast.SelectorExpr{X: id, Sel: p.tok.Lit, SelPos: p.tok.Pos}
			p.next()
		}
		return pat
	case token.SUB:
		p.next()
		if p.tok.Kind != token.INT {
			p.unexpected("esperado número depois de '-' no padrão")
		}
		pat.Neg = true
	}
	switch p.tok.Kind {
	case token.INT, token.FLOAT, token.STRING, token.CHAR:
		pat.Lit = p.parsePrimary()
		return pat
	}
	p.unexpected("esperado padrão (literal ou _)")
	return nil
}

func (p *Parser) parseFor() *ast.ForStmt {
	s := &ast.ForStmt{P: p.expect(token.FOR).Pos}
	if p.tok.Kind == token.LBRACE { // for { }
		s.Body = p.parseBlock()
		return s
	}
	if p.tok.Kind == token.IDENT && p.peek.Kind == token.IN { // for x in s { }
		s.Var = &ast.Ident{Name: p.tok.Lit, P: p.tok.Pos}
		p.next()
		p.next()
		p.withLit(false, func() { s.Iter = p.parseExpr() })
		s.Body = p.parseBlock()
		return s
	}

	p.withLit(false, func() { p.parseForHeader(s) })
	s.Body = p.parseBlock()
	return s
}

func (p *Parser) parseForHeader(s *ast.ForStmt) {
	var init ast.Stmt
	if p.tok.Kind != token.SEMICOLON {
		init = p.parseSimpleStmt()
	}
	if p.tok.Kind == token.SEMICOLON { // for init; cond; post { }
		if p.tok.Lit == "\n" {
			p.errorf(p.tok.Pos, "o '{' do for precisa ficar na mesma linha")
		}
		p.next()
		s.Init = init
		if p.tok.Kind != token.SEMICOLON {
			s.Cond = p.parseExpr()
		}
		p.expect(token.SEMICOLON)
		if p.tok.Kind != token.LBRACE {
			s.Post = p.parseSimpleStmt()
		}
		return
	}
	// for cond { }
	es, ok := init.(*ast.ExprStmt)
	if !ok {
		p.errorf(init.Pos(), "a condição do for precisa ser uma expressão")
	}
	s.Cond = es.X
}

// parseComposite lê "Tipo{campo: valor, ...}" depois do nome do tipo.
func (p *Parser) parseComposite(mod, typ *ast.Ident) *ast.CompositeLit {
	lit := &ast.CompositeLit{Module: mod, Type: typ, Lbrace: p.expect(token.LBRACE).Pos}
	p.withLit(true, func() {
		for p.tok.Kind != token.RBRACE {
			name := p.expect(token.IDENT)
			p.expect(token.COLON)
			lit.Fields = append(lit.Fields, &ast.FieldInit{Name: name.Lit, Value: p.parseExpr(), P: name.Pos})
			if p.tok.Kind == token.SEMICOLON && p.tok.Lit == "\n" {
				p.errorf(p.tok.Pos, "falta ',' depois do campo %s (num literal em várias linhas, todo campo termina com vírgula)", name.Lit)
			}
			if p.tok.Kind != token.COMMA {
				break
			}
			p.next()
		}
	})
	p.expect(token.RBRACE)
	return lit
}

// ---------- Expressões ----------

// precedence devolve a precedência de um operador binário (0 se não for um).
func precedence(k token.Kind) int {
	switch k {
	case token.LOR:
		return 1
	case token.LAND:
		return 2
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return 3
	case token.ADD, token.SUB:
		return 4
	case token.MUL, token.QUO, token.REM:
		return 5
	}
	return 0
}

func (p *Parser) parseExpr() ast.Expr { return p.parseBinary(1) }

func (p *Parser) parseBinary(minPrec int) ast.Expr {
	x := p.parseUnary()
	for {
		prec := precedence(p.tok.Kind)
		if prec == 0 || prec < minPrec {
			return x
		}
		op := p.tok
		p.next()
		y := p.parseBinary(prec + 1)
		x = &ast.BinaryExpr{Op: op.Kind, X: x, Y: y, OpPos: op.Pos}
	}
}

func (p *Parser) parseUnary() ast.Expr {
	switch p.tok.Kind {
	case token.SUB, token.NOT, token.MUL, token.AMP: // -x, !x, *p (conteúdo), &x (empréstimo)
		op := p.tok
		p.next()
		return &ast.UnaryExpr{Op: op.Kind, X: p.parseUnary(), P: op.Pos}
	}
	return p.parsePostfix()
}

func (p *Parser) parsePostfix() ast.Expr {
	x := p.parsePrimary()
	for {
		switch p.tok.Kind {
		case token.LPAREN:
			call := &ast.CallExpr{Fun: x, Lparen: p.tok.Pos}
			p.next()
			if id, ok := x.(*ast.Ident); ok && id.Name == "alloc" && p.tok.Kind == token.LBRACK {
				// alloc([]T, n): o primeiro argumento é um tipo
				call.Args = append(call.Args, &ast.TypeArg{Type: p.parseType()})
				if p.tok.Kind == token.COMMA {
					p.next()
				}
			}
			for p.tok.Kind != token.RPAREN {
				p.withLit(true, func() { call.Args = append(call.Args, p.parseExpr()) })
				if p.tok.Kind != token.COMMA {
					break
				}
				p.next()
			}
			p.expect(token.RPAREN)
			x = call
		case token.PERIOD:
			p.next()
			sel := p.expect(token.IDENT)
			x = &ast.SelectorExpr{X: x, Sel: sel.Lit, SelPos: sel.Pos}
		case token.LBRACE:
			if p.noLit {
				return x
			}
			switch t := x.(type) {
			case *ast.Ident:
				x = p.parseComposite(nil, t)
			case *ast.SelectorExpr:
				// literal de um tipo de outro arquivo: geometria.Ponto{...}
				mod, ok := t.X.(*ast.Ident)
				if !ok {
					return x
				}
				x = p.parseComposite(mod, &ast.Ident{Name: t.Sel, P: t.SelPos})
			default:
				return x
			}
		case token.LBRACK:
			pos := p.tok.Pos
			p.next()
			// s[i], ou o sub-slice s[a:b] com limites opcionais
			var lo, hi ast.Expr
			isSlice := false
			p.withLit(true, func() {
				if p.tok.Kind != token.COLON {
					lo = p.parseExpr()
				}
				if p.tok.Kind == token.COLON {
					isSlice = true
					p.next()
					if p.tok.Kind != token.RBRACK {
						hi = p.parseExpr()
					}
				}
			})
			p.expect(token.RBRACK)
			if isSlice {
				x = &ast.SliceExpr{X: x, Low: lo, High: hi, Lbrack: pos}
			} else {
				x = &ast.IndexExpr{X: x, Index: lo, Lbrack: pos}
			}
		default:
			return x
		}
	}
}

func (p *Parser) parsePrimary() ast.Expr {
	t := p.tok
	switch t.Kind {
	case token.IDENT:
		p.next()
		return &ast.Ident{Name: t.Lit, P: t.Pos}
	case token.INT:
		p.next()
		return &ast.IntLit{Value: t.Lit, P: t.Pos}
	case token.FLOAT:
		p.next()
		return &ast.FloatLit{Value: t.Lit, P: t.Pos}
	case token.STRING:
		p.next()
		return &ast.StringLit{Value: t.Lit, P: t.Pos}
	case token.CHAR:
		p.next()
		return &ast.CharLit{Value: t.Lit[0], P: t.Pos}
	case token.LPAREN:
		p.next()
		var x ast.Expr
		p.withLit(true, func() { x = p.parseExpr() })
		p.expect(token.RPAREN)
		return x
	}
	p.unexpected("esperado expressão")
	return nil
}

// parsePub lê uma declaração pública: pub fn, pub struct ou pub const.
func (p *Parser) parsePub(f *ast.File) {
	pos := p.expect(token.PUB).Pos
	switch p.tok.Kind {
	case token.FN:
		fn := p.parseFunc()
		fn.Pub = true
		f.Funcs = append(f.Funcs, fn)
	case token.STRUCT:
		s := p.parseStruct()
		s.Pub = true
		f.Structs = append(f.Structs, s)
	case token.CONST:
		c := p.parseConst()
		c.Pub = true
		f.Consts = append(f.Consts, c)
	default:
		p.errorf(pos, "pub só vale antes de fn, struct ou const")
	}
}
