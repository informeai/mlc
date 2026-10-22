// Package parser implements a recursive descent parser for the grammar
// of docs/grammar.md. It covers the subset of the milestones already implemented and stops
// at the first syntax error.
package parser

import (
	"fmt"
	"sort"

	"mlc/internal/ast"
	"mlc/internal/lexer"
	"mlc/internal/token"
)

// bailout interrupts the parse at the first error; it's recovered in ParseFile.
type bailout struct{}

type Parser struct {
	file string
	lx   *lexer.Lexer
	tok  token.Token
	peek token.Token
	errs []token.Error

	// noLit turns off composite literals at the top level of the header of
	// if/for/match: in "if x == P {", the '{' opens the block. Inside
	// parentheses, calls and indices they're valid again.
	noLit  bool
	extern bool // reading an extern fn: no body
}

const litInHeader = "a struct literal in the header of if, for or match needs parentheses: (Type{...})"

// withLit runs f with composite literals turned on or off.
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
		p.unexpected("expected " + describeKind(k))
	}
	t := p.tok
	p.next()
	return t
}

// unexpected reports the current token. ILLEGAL tokens were already reported by the lexer.
func (p *Parser) unexpected(what string) {
	if p.tok.Kind == token.ILLEGAL {
		panic(bailout{})
	}
	p.errorf(p.tok.Pos, "%s, found %s", what, describe(p.tok))
}

// expectEOS consumes the end of statement. Before '}' it can be omitted.
func (p *Parser) expectEOS() {
	switch p.tok.Kind {
	case token.SEMICOLON:
		p.next()
	case token.RBRACE, token.EOF:
	default:
		p.unexpected("expected end of line or ';'")
	}
}

func describeKind(k token.Kind) string {
	switch k {
	case token.IDENT:
		return "identifier"
	case token.STRING:
		return "string"
	case token.SEMICOLON:
		return "';' or end of line"
	}
	return fmt.Sprintf("'%s'", k)
}

func describe(t token.Token) string {
	switch t.Kind {
	case token.EOF:
		return "end of file"
	case token.SEMICOLON:
		if t.Lit == "\n" {
			return "end of line"
		}
	case token.IDENT:
		return fmt.Sprintf("identifier %s", t.Lit)
	case token.INT, token.FLOAT:
		return fmt.Sprintf("number %s", t.Lit)
	case token.STRING:
		return fmt.Sprintf("string %q", t.Lit)
	}
	return fmt.Sprintf("'%s'", t.Kind)
}

// ---------- Declarations ----------

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
		case token.EXTERN:
			switch p.externKind() {
			case token.IDENT:
				f.Structs = append(f.Structs, p.parseExternType())
			case token.STRUCT:
				f.Structs = append(f.Structs, p.parseExternStruct())
			case token.CONST, token.VAR:
				f.Consts = append(f.Consts, p.parseExternConst())
			default:
				f.Funcs = append(f.Funcs, p.parseExtern())
			}
		case token.STRUCT:
			f.Structs = append(f.Structs, p.parseStruct())
		case token.ENUM:
			f.Enums = append(f.Enums, p.parseEnum())
		case token.CONST:
			f.Consts = append(f.Consts, p.parseConst())
		case token.INTERFACE:
			f.Interfaces = append(f.Interfaces, p.parseInterface())
		case token.IMPL:
			f.Impls = append(f.Impls, p.parseImpl())
		case token.IDENT:
			if p.tok.Lit != "link" || p.peek.Kind != token.STRING {
				p.unexpected("expected a declaration (import, fn, struct, enum, const, interface, impl or link)")
			}
			pos := p.tok.Pos
			p.next()
			f.Links = append(f.Links, &ast.LinkDecl{Lib: p.tok.Lit, P: pos})
			p.next()
			p.expectEOS()
		default:
			p.unexpected("expected a declaration (import, fn, struct, enum, const, interface or impl)")
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
	var recv *ast.Param
	if p.tok.Kind == token.LPAREN {
		// method: fn (p &Point) area()
		p.next()
		name := p.expect(token.IDENT)
		recv = &ast.Param{Name: name.Lit, Type: p.parseType(), P: name.Pos}
		p.expect(token.RPAREN)
	}
	fn := &ast.FuncDecl{Name: p.expect(token.IDENT).Lit, Recv: recv, P: pos}
	fn.Extern = p.extern
	if p.tok.Kind == token.LBRACK {
		fn.TypeParams = p.parseTypeParams()
	}
	p.expect(token.LPAREN)
	for p.tok.Kind != token.RPAREN {
		name := p.expect(token.IDENT)
		param := &ast.Param{Name: name.Lit, P: name.Pos}
		// "kept" is a word only here, before a type: a type named kept
		// (f(x kept)) is still a type
		if p.tok.Kind == token.IDENT && p.tok.Lit == "kept" && p.peek.Kind != token.COMMA && p.peek.Kind != token.RPAREN {
			param.Kept = true
			p.next()
		}
		param.Type = p.parseType()
		fn.Params = append(fn.Params, param)
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
	}
	p.expect(token.RPAREN)
	// in an extern fn without a result, "from" starts the header, not a type
	if p.startsType() && !(fn.Extern && p.tok.Kind == token.IDENT && p.tok.Lit == "from") {
		fn.Result = p.parseType()
	}
	if fn.Extern {
		if p.tok.Kind == token.LBRACE {
			p.errorf(p.tok.Pos, "extern fn has no body: the implementation lives in the C runtime")
			p.parseBlock()
			return fn
		}
		if p.tok.Kind == token.IDENT && p.tok.Lit == "from" {
			return fn // extern "C" fn ... from "h.h": parseExtern reads it
		}
		p.expectEOS()
		return fn
	}
	fn.Body = p.parseBlock()
	return fn
}

// parseExtern reads "extern fn sqrt(x float64) float64": a function without a body,
// implemented in the C runtime.
func (p *Parser) parseExtern() *ast.FuncDecl {
	p.expect(token.EXTERN)
	externC := false
	if p.tok.Kind == token.STRING {
		// extern "C" fn: a function from a C library
		if p.tok.Lit != "C" {
			p.errorf(p.tok.Pos, "the only calling convention is \"C\": extern \"C\" fn")
		}
		externC = true
		p.next()
	}
	if p.tok.Kind != token.FN {
		p.unexpected("expected fn after extern")
	}
	p.extern = true
	defer func() { p.extern = false }()
	fn := p.parseFunc()
	fn.ExternC = externC
	if externC && p.tok.Kind == token.IDENT && p.tok.Lit == "from" {
		fn.Header = p.parseFrom()
		p.expectEOS()
	}
	return fn
}

// parseExternType reads "extern \"C\" type Db": an opaque C type.
func (p *Parser) parseExternType() *ast.StructDecl {
	pos := p.expect(token.EXTERN).Pos
	p.expect(token.STRING)
	p.next() // type
	name := p.expect(token.IDENT)
	p.expectEOS()
	return &ast.StructDecl{Extern: true, Name: name.Lit, P: pos}
}

// externKind says what comes after extern "C": IDENT for type Name (an
// opaque type), STRUCT, CONST, or FN (also for extern without "C").
func (p *Parser) externKind() token.Kind {
	if p.tok.Kind != token.EXTERN || p.peek.Kind != token.STRING {
		return token.FN
	}
	if p.isExternType() {
		return token.IDENT
	}
	save := *p.lx
	tok, peek := p.tok, p.peek
	p.next()
	p.next()
	k := p.tok.Kind
	*p.lx, p.tok, p.peek = save, tok, peek
	if k == token.STRUCT || k == token.CONST || k == token.VAR {
		return k
	}
	return token.FN
}

// parseFrom reads the from "header.h" of an extern "C" struct or const.
func (p *Parser) parseFrom() string {
	if p.tok.Kind != token.IDENT || p.tok.Lit != "from" {
		p.unexpected("expected from \"header.h\": the C header that declares it")
		return ""
	}
	p.next()
	pos := p.tok.Pos
	h := p.expect(token.STRING).Lit
	if h == "" {
		p.errorf(pos, "the header's name is empty")
	}
	return h
}

// parseExternStruct reads extern "C" struct T from "h.h" { fields }: C's
// struct, with the fields the program uses.
func (p *Parser) parseExternStruct() *ast.StructDecl {
	pos := p.expect(token.EXTERN).Pos
	if p.tok.Lit != "C" {
		p.errorf(p.tok.Pos, "the only calling convention is \"C\": extern \"C\" struct")
	}
	p.expect(token.STRING)
	p.expect(token.STRUCT)
	tag := false
	if p.tok.Kind == token.IDENT && p.tok.Lit == "tag" && p.peek.Kind == token.IDENT {
		tag = true // struct tag T: C's type is struct T
		p.next()
	}
	name := p.expect(token.IDENT)
	s := &ast.StructDecl{Name: name.Lit, Tag: tag, P: pos}
	s.Header = p.parseFrom()
	s.Fields = p.parseCFields()
	p.expectEOS()
	return s
}

// parseCFields reads the { ... } of an extern "C" struct: name type,
// name kept *T, or name { ... } for an anonymous nested struct.
func (p *Parser) parseCFields() []*ast.FieldDecl {
	var fields []*ast.FieldDecl
	p.expect(token.LBRACE)
	for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
		if p.tok.Kind == token.SEMICOLON {
			p.next()
			continue
		}
		name := p.expect(token.IDENT)
		fd := &ast.FieldDecl{Name: name.Lit, P: name.Pos}
		switch {
		case p.tok.Kind == token.LBRACE:
			fd.Inner = p.parseCFields()
		case p.tok.Kind == token.IDENT && p.tok.Lit == "kept" && p.peek.Kind != token.SEMICOLON && p.peek.Kind != token.RBRACE:
			fd.Kept = true
			p.next()
			fd.Type = p.parseType()
		default:
			fd.Type = p.parseType()
		}
		fields = append(fields, fd)
		p.expectEOS()
	}
	p.expect(token.RBRACE)
	return fields
}

// parseExternConst reads extern "C" const N T from "h.h": a constant of a C
// header, whose value C gives; or extern "C" var N T from "h.h", a global
// variable of the C library.
func (p *Parser) parseExternConst() *ast.ConstDecl {
	p.expect(token.EXTERN)
	p.expect(token.STRING)
	isVar := p.tok.Kind == token.VAR
	if isVar {
		p.next()
	} else {
		p.expect(token.CONST)
	}
	name := p.expect(token.IDENT)
	d := &ast.ConstDecl{Name: name.Lit, Var: isVar, P: name.Pos}
	if !p.startsType() {
		p.unexpected("expected the type: extern \"C\" const N int32 from \"h.h\"")
		return d
	}
	d.Type = p.parseType()
	d.Header = p.parseFrom()
	p.expectEOS()
	return d
}

// isExternType says whether what comes is extern "C" type Name.
func (p *Parser) isExternType() bool {
	if p.tok.Kind != token.EXTERN || p.peek.Kind != token.STRING {
		return false
	}
	save := *p.lx
	tok, peek := p.tok, p.peek
	p.next()
	p.next()
	is := p.tok.Kind == token.IDENT && p.tok.Lit == "type"
	*p.lx, p.tok, p.peek = save, tok, peek
	return is
}

func (p *Parser) parseStruct() *ast.StructDecl {
	pos := p.expect(token.STRUCT).Pos
	s := &ast.StructDecl{Name: p.expect(token.IDENT).Lit, P: pos}
	if p.tok.Kind == token.LBRACK {
		s.TypeParams = p.parseTypeParams()
	}
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

// startsType says whether the current token can start a type.
func (p *Parser) startsType() bool {
	switch p.tok.Kind {
	case token.IDENT, token.LBRACK, token.MUL, token.QUESTION, token.AMP, token.FN:
		return true
	}
	return false
}

func (p *Parser) parseType() *ast.TypeExpr {
	pos := p.tok.Pos
	switch p.tok.Kind {
	case token.FN:
		// fn(int, &T) bool: the type of a function value
		p.next()
		t := &ast.TypeExpr{Mod: ast.TypeFunc, P: pos}
		p.expect(token.LPAREN)
		for p.tok.Kind != token.RPAREN {
			t.Params = append(t.Params, p.parseType())
			if p.tok.Kind != token.COMMA {
				break
			}
			p.next()
		}
		p.expect(token.RPAREN)
		if p.startsType() {
			t.Elem = p.parseType()
		}
		return t
	case token.IDENT:
		name := p.tok.Lit
		p.next()
		if p.tok.Kind == token.PERIOD && p.peek.Kind == token.IDENT {
			// a type from another file: geometry.Point
			p.next()
			qualified := &ast.TypeExpr{Module: name, Name: p.tok.Lit, P: pos}
			p.next()
			qualified.Args = p.parseTypeArgs()
			return qualified
		}
		return &ast.TypeExpr{Name: name, Args: p.parseTypeArgs(), P: pos}
	case token.LBRACK:
		p.next()
		if p.tok.Kind == token.RBRACK {
			p.next()
			return &ast.TypeExpr{Mod: ast.TypeSlice, Elem: p.parseType(), P: pos}
		}
		// [N]T: the size is a constant expression
		var n ast.Expr
		p.withLit(true, func() { n = p.parseExpr() })
		p.expect(token.RBRACK)
		return &ast.TypeExpr{Mod: ast.TypeArray, Len: n, Elem: p.parseType(), P: pos}
	case token.MUL:
		p.next()
		return &ast.TypeExpr{Mod: ast.TypePointer, Elem: p.parseType(), P: pos}
	case token.QUESTION:
		p.next()
		if p.tok.Kind == token.AMP {
			p.next()
			return &ast.TypeExpr{Mod: ast.TypeOptBorrow, Elem: p.parseType(), P: pos}
		}
		p.expect(token.MUL)
		return &ast.TypeExpr{Mod: ast.TypeNullPtr, Elem: p.parseType(), P: pos}
	case token.AMP:
		p.next()
		return &ast.TypeExpr{Mod: ast.TypeBorrow, Elem: p.parseType(), P: pos}
	}
	p.unexpected("expected a type")
	return nil
}

// ---------- Statements ----------

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
		// There are no labels, so "name:" at the start of a statement is almost
		// always a struct literal the header of if/for/match cut off.
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
		p.errorf(p.tok.Pos, "else must be on the same line as the '}' that closes the if")
	}
	s := p.parseSimpleStmt()
	p.expectEOS()
	return s
}

// parseSimpleStmt reads x := v, assignments, x++/x-- or an expression.
func (p *Parser) parseSimpleStmt() ast.Stmt {
	x := p.parseExpr()
	switch p.tok.Kind {
	case token.DEFINE:
		id, ok := x.(*ast.Ident)
		if !ok {
			p.errorf(x.Pos(), "the left side of := must be a name")
		}
		p.next()
		return &ast.VarDecl{Name: id.Name, Value: p.parseExpr(), Short: true, P: id.P}
	case token.ASSIGN, token.ADD_ASSIGN, token.SUB_ASSIGN,
		token.MUL_ASSIGN, token.QUO_ASSIGN, token.REM_ASSIGN,
		token.AND_ASSIGN, token.OR_ASSIGN, token.XOR_ASSIGN, token.SHL_ASSIGN, token.SHR_ASSIGN,
		token.ADD_WRAP_ASSIGN, token.SUB_WRAP_ASSIGN, token.MUL_WRAP_ASSIGN:
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
		p.errorf(name.Pos, "var %s needs a type or an initial value", name.Lit)
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
		p.unexpected("expected if or '{' after else")
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
		p.errorf(s.P, "match without arms")
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
		p.errorf(p.tok.Pos, "a match arm accepts a simple statement, a block, break, continue or return (use { } for more)")
	default:
		arm.Body = p.parseStmt() // already consumes the end of statement
	}
	return arm
}

func (p *Parser) parsePattern() *ast.Pattern {
	t := p.tok
	pat := &ast.Pattern{P: t.Pos}
	switch t.Kind {
	case token.IDENT:
		if t.Lit != "_" && p.peek.Kind == token.LPAREN {
			return p.variantPattern(pat)
		}
		switch t.Lit {
		case "_":
			pat.Wildcard = true
		default: // true, false or a constant; the checker validates
			pat.Lit = &ast.Ident{Name: t.Lit, P: t.Pos}
		}
		p.next()
		if id, ok := pat.Lit.(*ast.Ident); ok && p.tok.Kind == token.PERIOD && p.peek.Kind == token.IDENT {
			// a constant from another file: geometry.MAX
			p.next()
			pat.Lit = &ast.SelectorExpr{X: id, Sel: p.tok.Lit, SelPos: p.tok.Pos}
			p.next()
		}
		return pat
	case token.SUB:
		p.next()
		if p.tok.Kind != token.INT {
			p.unexpected("expected a number after '-' in the pattern")
		}
		pat.Neg = true
	}
	switch p.tok.Kind {
	case token.INT, token.FLOAT, token.STRING, token.CHAR:
		pat.Lit = p.parsePrimary()
		return pat
	}
	p.unexpected("expected a pattern (literal or _)")
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
			p.errorf(p.tok.Pos, "the '{' of the for must be on the same line")
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
		p.errorf(init.Pos(), "the for's condition must be an expression")
	}
	s.Cond = es.X
}

// parseComposite reads "Type{field: value, ...}" after the type's name.
func (p *Parser) parseComposite(mod, typ *ast.Ident) *ast.CompositeLit {
	lit := &ast.CompositeLit{Module: mod, Type: typ, Lbrace: p.expect(token.LBRACE).Pos}
	p.withLit(true, func() {
		for p.tok.Kind != token.RBRACE {
			name := p.expect(token.IDENT)
			p.expect(token.COLON)
			lit.Fields = append(lit.Fields, &ast.FieldInit{Name: name.Lit, Value: p.parseExpr(), P: name.Pos})
			if p.tok.Kind == token.SEMICOLON && p.tok.Lit == "\n" {
				p.errorf(p.tok.Pos, "missing ',' after the field %s (in a multi-line literal, every field ends with a comma)", name.Lit)
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

// ---------- Expressions ----------

// precedence returns the precedence of a binary operator (0 if it isn't one).
func precedence(k token.Kind) int {
	switch k {
	case token.LOR:
		return 1
	case token.LAND:
		return 2
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return 3
	case token.ADD, token.SUB, token.OR, token.XOR, token.ADD_WRAP, token.SUB_WRAP:
		return 4
	case token.MUL, token.QUO, token.REM, token.SHL, token.SHR, token.AMP, token.MUL_WRAP:
		return 5 // as in Go: a & b == c is (a & b) == c
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
	case token.SUB, token.NOT, token.MUL, token.AMP, token.XOR: // -x, !x, *p (pointee), &x (borrow), ^x (inverts the bits)
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
				// alloc([]T, n): the first argument is a type; alloc([3]int{...}),
				// an array literal
				typ := p.parseType()
				if typ.Mod == ast.TypeArray && p.tok.Kind == token.LBRACE {
					call.Args = append(call.Args, p.arrayLitBody(typ))
				} else {
					call.Args = append(call.Args, &ast.TypeArg{Type: typ})
				}
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
			case *ast.IndexExpr, *ast.IndexListExpr:
				// a generic struct literal: Pair[int, string]{...}
				if lit := p.genericComposite(t); lit != nil {
					x = lit
				} else {
					return x
				}
			case *ast.SelectorExpr:
				// a literal of a type from another file: geometry.Point{...}
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
			// s[i], or the sub-slice s[a:b] with optional bounds
			var lo, hi ast.Expr
			var more []ast.Expr
			isSlice := false
			p.withLit(true, func() {
				if p.tok.Kind != token.COLON {
					lo = p.parseExpr()
				}
				for p.tok.Kind == token.COMMA {
					p.next()
					more = append(more, p.parseExpr())
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
			switch {
			case isSlice:
				x = &ast.SliceExpr{X: x, Low: lo, High: hi, Lbrack: pos}
			case len(more) > 0:
				// f[A, B]: type arguments
				x = &ast.IndexListExpr{X: x, Indices: append([]ast.Expr{lo}, more...), Lbrack: pos}
			default:
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
	case token.QUESTION, token.FN:
		// ?*T and fn(T) R don't start expressions: they're type arguments,
		// as in vec.new[?*Node](8)
		return &ast.TypeArg{Type: p.parseType()}
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
	case token.LBRACK:
		return p.parseArrayLit()
	case token.LPAREN:
		p.next()
		var x ast.Expr
		p.withLit(true, func() { x = p.parseExpr() })
		p.expect(token.RPAREN)
		return x
	}
	p.unexpected("expected an expression")
	return nil
}

// parsePub reads a public declaration: pub fn, pub struct or pub const.
func (p *Parser) parsePub(f *ast.File) {
	pos := p.expect(token.PUB).Pos
	switch p.tok.Kind {
	case token.EXTERN:
		switch p.externKind() {
		case token.IDENT:
			s := p.parseExternType()
			s.Pub = true
			f.Structs = append(f.Structs, s)
		case token.STRUCT:
			s := p.parseExternStruct()
			s.Pub = true
			f.Structs = append(f.Structs, s)
		case token.CONST, token.VAR:
			c := p.parseExternConst()
			c.Pub = true
			f.Consts = append(f.Consts, c)
		default:
			fn := p.parseExtern()
			fn.Pub = true
			f.Funcs = append(f.Funcs, fn)
		}
	case token.FN:
		fn := p.parseFunc()
		fn.Pub = true
		f.Funcs = append(f.Funcs, fn)
	case token.STRUCT:
		s := p.parseStruct()
		s.Pub = true
		f.Structs = append(f.Structs, s)
	case token.ENUM:
		en := p.parseEnum()
		en.Pub = true
		f.Enums = append(f.Enums, en)
	case token.CONST:
		c := p.parseConst()
		c.Pub = true
		f.Consts = append(f.Consts, c)
	case token.INTERFACE:
		in := p.parseInterface()
		in.Pub = true
		f.Interfaces = append(f.Interfaces, in)
	default:
		p.errorf(pos, "pub only applies before fn, struct, enum, const or interface")
	}
}

// parseArrayLit reads "[3]int{1, 2, 3}". Since an array type alone is never an
// expression, the '{' after it always opens the literal, even in the header of
// if, for and match.
func (p *Parser) parseArrayLit() ast.Expr {
	typ := p.parseType()
	if p.tok.Kind != token.LBRACE || typ.Mod != ast.TypeArray {
		// a type used as a type argument: f[[]int](...)
		return &ast.TypeArg{Type: typ}
	}
	return p.arrayLitBody(typ)
}

// arrayLitBody reads the elements {a, b, c} of an array literal.
func (p *Parser) arrayLitBody(typ *ast.TypeExpr) *ast.ArrayLit {
	lit := &ast.ArrayLit{Type: typ, Lbrace: p.expect(token.LBRACE).Pos}
	p.withLit(true, func() {
		for p.tok.Kind != token.RBRACE {
			lit.Elems = append(lit.Elems, p.parseExpr())
			if p.tok.Kind == token.SEMICOLON && p.tok.Lit == "\n" {
				p.errorf(p.tok.Pos, "missing ',' after the element (in a multi-line literal, every element ends with a comma)")
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

// parseTypeParams reads [T any, U ordered] after the name of an fn or struct.
func (p *Parser) parseTypeParams() []*ast.TypeParam {
	p.expect(token.LBRACK)
	var params []*ast.TypeParam
	for {
		name := p.expect(token.IDENT)
		if p.tok.Kind != token.IDENT {
			p.unexpected("expected the type parameter's constraint (any, comparable, ordered, number, integer, owned, ref or an interface)")
		}
		constraint := ""
		for {
			if constraint != "" {
				p.next() // the + between the constraints: K owned + hash.Hash
				if p.tok.Kind != token.IDENT {
					p.unexpected("expected another constraint after +")
				}
				constraint += "+"
			}
			constraint += p.tok.Lit
			p.next()
			if p.tok.Kind == token.PERIOD && p.peek.Kind == token.IDENT {
				// an interface from another file: T io.Display
				p.next()
				constraint += "." + p.tok.Lit
				p.next()
			}
			if p.tok.Kind != token.ADD {
				break
			}
		}
		params = append(params, &ast.TypeParam{Name: name.Lit, Constraint: constraint, P: name.Pos})
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
	}
	p.expect(token.RBRACK)
	return params
}

// parseTypeArgs reads [int, string] after the name of a generic type, if any.
func (p *Parser) parseTypeArgs() []*ast.TypeExpr {
	if p.tok.Kind != token.LBRACK {
		return nil
	}
	p.next()
	var args []*ast.TypeExpr
	for {
		arg := p.parseType()
		if p.tok.Kind == token.IDENT && arg.Mod == ast.TypeNamed && arg.Module == "" && len(arg.Args) == 0 {
			// Vec[T any] in a method's receiver: the method's constraint
			arg.Constraint = p.tok.Lit
			p.next()
			if p.tok.Kind == token.PERIOD && p.peek.Kind == token.IDENT {
				p.next()
				arg.Constraint += "." + p.tok.Lit
				p.next()
			}
		}
		args = append(args, arg)
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
	}
	p.expect(token.RBRACK)
	return args
}

// genericComposite reads the literal of Pair[int, string]{...}, if x is Pair[...]
// (or mod.Pair[...]).
func (p *Parser) genericComposite(x ast.Expr) *ast.CompositeLit {
	var base ast.Expr
	var args []ast.Expr
	switch t := x.(type) {
	case *ast.IndexExpr:
		base, args = t.X, []ast.Expr{t.Index}
	case *ast.IndexListExpr:
		base, args = t.X, t.Indices
	}
	var lit *ast.CompositeLit
	switch b := base.(type) {
	case *ast.Ident:
		lit = p.parseComposite(nil, b)
	case *ast.SelectorExpr:
		mod, ok := b.X.(*ast.Ident)
		if !ok {
			return nil
		}
		lit = p.parseComposite(mod, &ast.Ident{Name: b.Sel, P: b.SelPos})
	default:
		return nil
	}
	lit.TypeArgs = args
	return lit
}

// parseInterface reads "interface Display { fn write(self &Self, w &Writer) }":
// method signatures, without bodies.
func (p *Parser) parseInterface() *ast.InterfaceDecl {
	pos := p.expect(token.INTERFACE).Pos
	in := &ast.InterfaceDecl{Name: p.expect(token.IDENT).Lit, P: pos}
	p.expect(token.LBRACE)
	for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
		if p.tok.Kind == token.SEMICOLON {
			p.next()
			continue
		}
		if p.tok.Kind != token.FN {
			p.unexpected("expected fn: an interface only has method signatures")
		}
		p.extern = true // without a body, like an extern fn
		m := p.parseFunc()
		p.extern = false
		m.Extern = false
		in.Methods = append(in.Methods, m)
	}
	p.expect(token.RBRACE)
	p.expectEOS()
	return in
}

// parseImpl reads "impl [T C] [auto] Interface for Type [{ methods }]".
func (p *Parser) parseImpl() *ast.ImplDecl {
	pos := p.expect(token.IMPL).Pos
	d := &ast.ImplDecl{P: pos}
	if p.tok.Kind == token.LBRACK {
		d.TypeParams = p.parseTypeParams()
	}
	if p.tok.Kind == token.IDENT && p.tok.Lit == "auto" && p.peek.Kind == token.IDENT {
		d.Auto = true
		p.next()
	}
	d.Iface = p.parseType()
	if p.tok.Kind != token.FOR {
		p.unexpected("expected for: impl Interface for Type")
	}
	p.next()
	d.Type = p.parseType()
	if p.tok.Kind == token.LBRACE {
		d.Block = true
		p.next()
		for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
			if p.tok.Kind == token.SEMICOLON {
				p.next()
				continue
			}
			if p.tok.Kind != token.FN {
				p.unexpected("expected fn: an impl's block only has the interface's methods")
			}
			d.Methods = append(d.Methods, p.parseFunc())
		}
		p.expect(token.RBRACE)
	}
	p.expectEOS()
	return d
}

// parseEnum reads "enum Shape { Circle(radius float64); Empty }".
func (p *Parser) parseEnum() *ast.EnumDecl {
	pos := p.expect(token.ENUM).Pos
	en := &ast.EnumDecl{Name: p.expect(token.IDENT).Lit, P: pos}
	if p.tok.Kind == token.LBRACK {
		en.TypeParams = p.parseTypeParams()
	}
	p.expect(token.LBRACE)
	for p.tok.Kind != token.RBRACE && p.tok.Kind != token.EOF {
		if p.tok.Kind == token.SEMICOLON {
			p.next()
			continue
		}
		name := p.expect(token.IDENT)
		v := &ast.VariantDecl{Name: name.Lit, P: name.Pos}
		if p.tok.Kind == token.LPAREN {
			p.next()
			for p.tok.Kind != token.RPAREN {
				fname := p.expect(token.IDENT)
				v.Fields = append(v.Fields, &ast.FieldDecl{Name: fname.Lit, Type: p.parseType(), P: fname.Pos})
				if p.tok.Kind != token.COMMA {
					break
				}
				p.next()
			}
			p.expect(token.RPAREN)
		}
		en.Variants = append(en.Variants, v)
		p.expectEOS()
	}
	p.expect(token.RBRACE)
	return en
}

// variantPattern reads the pattern Circle(r, _) of a match on an enum.
func (p *Parser) variantPattern(pat *ast.Pattern) *ast.Pattern {
	name := p.expect(token.IDENT)
	pat.Variant = &ast.Ident{Name: name.Lit, P: name.Pos}
	p.expect(token.LPAREN)
	for p.tok.Kind != token.RPAREN {
		b := p.expect(token.IDENT)
		pat.Binds = append(pat.Binds, &ast.Ident{Name: b.Lit, P: b.Pos})
		if p.tok.Kind != token.COMMA {
			break
		}
		p.next()
	}
	p.expect(token.RPAREN)
	return pat
}
