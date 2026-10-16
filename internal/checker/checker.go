// Package checker valida nomes e tipos do programa antes da geração de C.
// Tudo que o checker aceita precisa gerar C sem comportamento indefinido.
package checker

import (
	"fmt"
	"sort"
	"strconv"

	"mlc/internal/ast"
	"mlc/internal/token"
)

type Type interface {
	String() string
}

type Basic struct{ name string }

func (b *Basic) String() string { return b.name }

var (
	Invalid = &Basic{"inválido"}
	Void    = &Basic{"void"}
	Int     = &Basic{"int"}
	Bool    = &Basic{"bool"}
	String  = &Basic{"string"}
	Byte    = &Basic{"byte"}
)

var basicTypes = map[string]Type{"int": Int, "bool": Bool, "string": String, "byte": Byte}

// predeclared são nomes que o usuário não pode redeclarar.
var predeclared = map[string]bool{
	"int": true, "bool": true, "string": true, "byte": true,
	"true": true, "false": true, "nil": true,
	"alloc": true, "free": true, "len": true, "realloc": true,
	"take": true, "replace": true, "drain": true,
}

// modules são os módulos que podem ser importados.
var modules = map[string]bool{"io": true, "str": true}

type Local struct {
	Name  string
	CName string // nome no C gerado, único dentro da função
	Type  Type
	Pos   token.Pos
	Param bool
	Used  bool // lida em alguma expressão

	IsConst bool // constante: Const tem o valor (int64, string ou bool)
	Const   any
}

type Func struct {
	Name   string
	Params []*Local
	Result Type
	Decl   *ast.FuncDecl // nil numa função nativa
	Native string        // nome no runtime C, numa função nativa (str.copy)
}

type Builtin int

const (
	NoBuiltin Builtin = iota
	IoPrint           // io.print
	Alloc             // alloc(T) ou alloc(valor)
	Free              // free(p)
	Len               // len(s)
	Realloc           // realloc(s, n)
	Take              // take(&s[i])
	Replace           // replace(&s[i], v)
	Convert           // int(x), byte(x)
)

// Callee é o destino resolvido de uma chamada: uma função do usuário ou um builtin.
type Callee struct {
	Func    *Func
	Builtin Builtin
	Elem    Type // alloc: o tipo alocado
	Zero    bool // alloc: memória zerada (alloc(T)) em vez de valor inicial
	Slice   bool // alloc([]T, n)
}

// Info é o resultado da checagem, consumido pelo gerador de código.
type Info struct {
	Types  map[ast.Expr]Type
	Locals map[*ast.Ident]*Local
	Decls  map[*ast.VarDecl]*Local
	// Patterns guarda o valor constante de cada padrão de match: int64,
	// string ou bool. O curinga _ não aparece.
	Patterns map[*ast.Pattern]any
	// Consts guarda o valor dos identificadores que se referem a constantes.
	Consts  map[*ast.Ident]any
	Structs []*Struct                 // cada struct depois das que ela contém
	Slices  []*Slice                  // tipos slice usados, para o gerador declarar
	ForVars map[*ast.ForStmt]*Local   // a variável de cada for x in s
	Drains  map[*ast.ForStmt]ast.Expr // for x in drain(s): o slice esvaziado
	Calls   map[*ast.CallExpr]Callee
	Funcs   []*Func // na ordem de declaração
}

type scope struct {
	parent *scope
	names  map[string]*Local
	order  []*Local
}

func (s *scope) lookup(name string) *Local {
	for ; s != nil; s = s.parent {
		if l, ok := s.names[name]; ok {
			return l
		}
	}
	return nil
}

type checker struct {
	file    string
	info    *Info
	imports map[string]bool
	funcs   map[string]*Func
	structs map[string]*Struct
	global  *scope // constantes do topo; pai do escopo de cada função

	pendingConsts map[string]bool // constantes do topo ainda não avaliadas
	pointers      map[ptrKey]*Pointer
	litOK         bool // o próximo literal composto pode conter ponteiros donos (dentro de alloc)
	borrows       map[Type]*Borrow
	slices        map[Type]*Slice
	natives       map[string]*Func
	scope         *scope
	fn            *Func
	errs          []token.Error

	cnames map[string]int        // quantas variáveis com cada nome a função já declarou
	loops  []*ast.ForStmt        // laços abertos, o mais interno por último
	breaks map[*ast.ForStmt]bool // laços que têm break direto
}

func Check(file string, f *ast.File) (*Info, []token.Error) {
	c := &checker{
		file: file,
		info: &Info{
			Types:    map[ast.Expr]Type{},
			Locals:   map[*ast.Ident]*Local{},
			Decls:    map[*ast.VarDecl]*Local{},
			Patterns: map[*ast.Pattern]any{},
			Consts:   map[*ast.Ident]any{},
			ForVars:  map[*ast.ForStmt]*Local{},
			Drains:   map[*ast.ForStmt]ast.Expr{},
			Calls:    map[*ast.CallExpr]Callee{},
		},
		imports: map[string]bool{},
		funcs:   map[string]*Func{},
		structs: map[string]*Struct{},
		global:  &scope{names: map[string]*Local{}},

		pendingConsts: map[string]bool{},
		pointers:      map[ptrKey]*Pointer{},
		borrows:       map[Type]*Borrow{},
		slices:        map[Type]*Slice{},
		natives:       map[string]*Func{},
		breaks:        map[*ast.ForStmt]bool{},
	}
	c.collectImports(f)
	c.collectStructs(f)
	c.collectConsts(f)
	c.collectFuncs(f)
	for _, fn := range c.info.Funcs {
		c.checkFunc(fn)
	}
	// A posse só é verificada num programa sem erros de tipo: ela depende
	// dos tipos resolvidos.
	if len(c.errs) == 0 {
		for _, fn := range c.info.Funcs {
			c.checkOwnership(fn)
		}
	}
	sort.SliceStable(c.errs, func(i, j int) bool { return c.errs[i].Pos.Before(c.errs[j].Pos) })
	return c.info, c.errs
}

func (c *checker) pushScope() {
	c.scope = &scope{parent: c.scope, names: map[string]*Local{}}
}

// popScope fecha o escopo atual e reporta variáveis declaradas e nunca lidas.
func (c *checker) popScope() {
	for _, l := range c.scope.order {
		if !l.Used && !l.Param {
			c.errorf(l.Pos, "%s declarada e não usada", l.Name)
		}
	}
	c.scope = c.scope.parent
}

// declare cria uma variável no escopo atual. O nome em C recebe um prefixo
// diferente a cada redeclaração na função (l_x, l2_x, l3_x...), para que uma
// variável interna nunca esconda a externa no C gerado: em "x := x + 1"
// dentro de um bloco, o x da direita é o externo.
func (c *checker) declare(name string, pos token.Pos, t Type, param bool) *Local {
	c.cnames[name]++
	cname := "l_" + name
	if n := c.cnames[name]; n > 1 {
		cname = fmt.Sprintf("l%d_%s", n, name)
	}
	l := &Local{Name: name, CName: cname, Type: t, Pos: pos, Param: param}
	if predeclared[name] || modules[name] {
		c.errorf(pos, "o nome %q é reservado", name)
		l.Used = true
		return l
	}
	if c.structs[name] != nil {
		c.errorf(pos, "o nome %s já é um tipo (struct)", name)
		l.Used = true
		return l
	}
	if prev, ok := c.scope.names[name]; ok {
		// A declaração original continua valendo; a repetida só gera este erro.
		c.errorf(pos, "%s já declarada neste escopo (linha %d)", name, prev.Pos.Line)
		l.Used = true
		return l
	}
	c.scope.names[name] = l
	c.scope.order = append(c.scope.order, l)
	return l
}

func (c *checker) errorf(pos token.Pos, format string, args ...any) {
	c.errs = append(c.errs, token.Error{File: c.file, Pos: pos, Msg: fmt.Sprintf(format, args...)})
}

// ---------- Declarações ----------

func (c *checker) collectImports(f *ast.File) {
	for _, imp := range f.Imports {
		switch {
		case !modules[imp.Path]:
			c.errorf(imp.P, "módulo desconhecido %q (disponíveis: \"io\", \"str\")", imp.Path)
		case c.imports[imp.Path]:
			c.errorf(imp.P, "módulo %q importado duas vezes", imp.Path)
		default:
			c.imports[imp.Path] = true
		}
	}
}

func (c *checker) collectFuncs(f *ast.File) {
	for _, d := range f.Funcs {
		if predeclared[d.Name] || modules[d.Name] {
			c.errorf(d.P, "o nome %q é reservado", d.Name)
			continue
		}
		if c.structs[d.Name] != nil || c.global.names[d.Name] != nil {
			c.errorf(d.P, "o nome %s já é usado por uma struct ou constante", d.Name)
			continue
		}
		if prev, ok := c.funcs[d.Name]; ok {
			c.errorf(d.P, "função %s já declarada na linha %d", d.Name, prev.Decl.P.Line)
			continue
		}
		fn := &Func{Name: d.Name, Result: Void, Decl: d}
		seen := map[string]bool{}
		for _, p := range d.Params {
			switch {
			case predeclared[p.Name] || modules[p.Name]:
				c.errorf(p.P, "o nome %q é reservado", p.Name)
			case seen[p.Name]:
				c.errorf(p.P, "parâmetro %s repetido", p.Name)
			}
			seen[p.Name] = true
			fn.Params = append(fn.Params, &Local{
				Name: p.Name, CName: "l_" + p.Name, Type: c.resolveType(p.Type, ctxParam), Pos: p.P, Param: true,
			})
		}
		if d.Result != nil {
			fn.Result = c.resolveType(d.Result, ctxResult)
		}
		c.funcs[d.Name] = fn
		c.info.Funcs = append(c.info.Funcs, fn)
	}

	main := c.funcs["main"]
	switch {
	case main == nil:
		c.errorf(token.Pos{Line: 1, Col: 1}, "função main não declarada")
	case len(main.Params) > 0 || main.Result != Void:
		c.errorf(main.Decl.P, "main não recebe parâmetros nem retorna valor")
	}
}

// ---------- Comandos ----------

func (c *checker) checkFunc(fn *Func) {
	c.fn = fn
	c.scope = c.global
	c.cnames = map[string]int{}
	c.pushScope()
	for _, p := range fn.Params {
		c.scope.names[p.Name] = p
		c.cnames[p.Name] = 1
	}
	// O corpo compartilha o escopo dos parâmetros, como no Go.
	for _, s := range fn.Decl.Body.Stmts {
		c.checkStmt(s)
	}
	c.popScope()
	if fn.Result != Void && !c.terminates(fn.Decl.Body) {
		c.errorf(fn.Decl.Body.End, "função %s precisa terminar com return", fn.Name)
	}
}

// terminates diz se a execução nunca passa do fim do comando: return, if/else
// com os dois lados terminando, ou "for { }" sem break.
func (c *checker) terminates(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.Block:
		return len(s.Stmts) > 0 && c.terminates(s.Stmts[len(s.Stmts)-1])
	case *ast.IfStmt:
		return s.Else != nil && c.terminates(s.Then) && c.terminates(s.Else)
	case *ast.ForStmt:
		return s.Cond == nil && !c.breaks[s]
	case *ast.MatchStmt:
		// Todo match aceito pelo checker cobre todos os valores, então ele
		// termina se todos os braços terminam.
		for _, arm := range s.Arms {
			if !c.terminates(arm.Body) {
				return false
			}
		}
		return len(s.Arms) > 0
	}
	return false
}

func (c *checker) checkBlock(b *ast.Block) {
	c.pushScope()
	for _, s := range b.Stmts {
		c.checkStmt(s)
	}
	c.popScope()
}

func (c *checker) checkStmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.VarDecl:
		var t Type
		if s.Type != nil {
			t = c.resolveType(s.Type, ctxVar)
		}
		if s.Value != nil {
			// O valor é checado antes de declarar: em "x := x + 1" o x da
			// direita é o de fora.
			vt := c.value(s.Value)
			if t == nil {
				t = vt
			} else {
				c.assignable(vt, t, s.Value.Pos(), "declaração de "+s.Name)
			}
		}
		switch tt := t.(type) {
		case *Borrow:
			c.errorf(s.P, "empréstimos (%s) não podem ser guardados em variáveis", tt)
			t = Invalid
		case *Pointer:
			if s.Value == nil && !tt.Nullable {
				c.errorf(s.P, "var %s %s precisa de valor inicial: um ponteiro *T nunca é nil (use ?*%s para começar com nil)", s.Name, tt, tt.Elem)
			}
		}
		if t == NilType {
			c.errorf(s.P, "não dá para deduzir o tipo de nil: declare com var %s ?*T = nil", s.Name)
			t = Invalid
		}
		if s.Type == nil {
			c.noCopy(t, s.P)
		}
		c.info.Decls[s] = c.declare(s.Name, s.P, t, false)
	case *ast.AssignStmt:
		lt := c.target(s.Lhs)
		rt := c.value(s.Rhs)
		if s.Op == token.ASSIGN {
			c.noCopy(lt, s.OpPos)
			c.assignable(rt, lt, s.Rhs.Pos(), "atribuição")
			return
		}
		if lt == Invalid || rt == Invalid {
			return
		}
		if s.Op == token.ADD_ASSIGN && lt == String {
			c.errorf(s.OpPos, "concatenação de strings exige alocação explícita e ainda não é suportada")
		} else if lt != Int || rt != Int {
			c.errorf(s.OpPos, "operador %s não se aplica a %s e %s", s.Op, lt, rt)
		}
	case *ast.IncDecStmt:
		if t := c.target(s.X); t != Int && t != Invalid {
			c.errorf(s.OpPos, "operador %s não se aplica a %s", s.Op, t)
		}
	case *ast.IfStmt:
		c.condition(s.Cond, "if")
		// Se x pode ser nil em cada ponto é decidido pelo verificador de posse,
		// que acompanha o fluxo (ownership.go).
		c.checkBlock(s.Then)
		if s.Else != nil {
			c.checkStmt(s.Else)
		}
	case *ast.ForStmt:
		if s.Var != nil {
			c.checkForIn(s)
			return
		}
		// O escopo do for envolve o init: "for i := 0; ..." declara i só para o laço.
		c.pushScope()
		if s.Init != nil {
			c.checkStmt(s.Init)
		}
		if s.Cond != nil {
			c.condition(s.Cond, "for")
		}
		if s.Post != nil {
			if _, ok := s.Post.(*ast.VarDecl); ok {
				c.errorf(s.Post.Pos(), "declaração não permitida no pós do for")
			} else {
				c.checkStmt(s.Post)
			}
		}
		c.loops = append(c.loops, s)
		c.checkBlock(s.Body)
		c.loops = c.loops[:len(c.loops)-1]
		c.popScope()
	case *ast.ConstDecl:
		c.constDecl(s)
	case *ast.MatchStmt:
		c.checkMatch(s)
	case *ast.BranchStmt:
		if len(c.loops) == 0 {
			c.errorf(s.P, "%s fora de um for", s.Tok)
		} else if s.Tok == token.BREAK {
			c.breaks[c.loops[len(c.loops)-1]] = true
		}
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		if !ok {
			c.errorf(s.X.Pos(), "expressão calculada mas não usada")
			c.expr(s.X)
			return
		}
		c.expr(call)
	case *ast.ReturnStmt:
		if s.Value == nil {
			if c.fn.Result != Void {
				c.errorf(s.P, "return sem valor em função que retorna %s", c.fn.Result)
			}
			return
		}
		t := c.value(s.Value)
		if c.fn.Result == Void {
			c.errorf(s.Value.Pos(), "função %s não retorna valor", c.fn.Name)
			return
		}
		c.assignable(t, c.fn.Result, s.Value.Pos(), "return")
	case *ast.Block:
		c.checkBlock(s)
	default:
		panic(fmt.Sprintf("checker: comando desconhecido %T", s))
	}
}

// checkMatch valida um match: tipo de cada padrão, padrões repetidos,
// braços inalcançáveis e cobertura de todos os valores.
func (c *checker) checkMatch(s *ast.MatchStmt) {
	t := c.value(s.Subject)
	if t != Int && t != Bool && t != String && t != Byte && t != Invalid {
		c.errorf(s.Subject.Pos(), "match não se aplica a %s (só int, byte, bool e string)", t)
		t = Invalid
	}
	seen := map[any]token.Pos{}
	var wildcard *ast.Pattern
	unreachable := false
	for _, arm := range s.Arms {
		if wildcard != nil && !unreachable {
			c.errorf(arm.P, "braço depois de _ nunca é executado")
			unreachable = true
		}
		for _, p := range arm.Patterns {
			if p.Wildcard {
				if len(arm.Patterns) > 1 {
					c.errorf(p.P, "_ precisa estar sozinho no braço")
				}
				wildcard = p
				continue
			}
			v, pt := c.patternValue(p)
			if pt == Invalid {
				continue
			}
			if t != Invalid && pt != t {
				c.errorf(p.P, "padrão do tipo %s em match sobre %s", pt, t)
				continue
			}
			if prev, dup := seen[v]; dup {
				c.errorf(p.P, "padrão %s repetido (já aparece na linha %d)", formatConst(v), prev.Line)
				continue
			}
			seen[v] = p.P
			c.info.Patterns[p] = v
		}
		c.pushScope()
		c.checkStmt(arm.Body)
		c.popScope()
	}

	coversBool := t == Bool && len(seen) == 2
	switch {
	case t == Invalid:
	case coversBool && wildcard != nil:
		c.errorf(wildcard.P, "braço _ nunca é executado: true e false já foram cobertos")
	case wildcard == nil && t == Bool && !coversBool:
		c.errorf(s.P, "match sobre bool precisa cobrir true e false ou ter um braço _")
	case wildcard == nil && t != Bool:
		c.errorf(s.P, "match sobre %s precisa de um braço _", t)
	}
}

// patternValue devolve o valor constante de um padrão e o seu tipo.
func (c *checker) patternValue(p *ast.Pattern) (any, Type) {
	switch lit := p.Lit.(type) {
	case *ast.IntLit:
		text := lit.Value
		if p.Neg {
			text = "-" + text
		}
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			c.errorf(p.P, "constante %s não cabe em int (64 bits)", text)
			return nil, Invalid
		}
		return v, Int
	case *ast.StringLit:
		return lit.Value, String
	case *ast.CharLit:
		return int64(lit.Value), Byte
	case *ast.Ident:
		if l := c.scope.lookup(lit.Name); l != nil {
			if !l.IsConst {
				c.errorf(p.P, "%s não é constante: padrão precisa ser um literal, uma constante ou _", lit.Name)
				return nil, Invalid
			}
			return l.Const, l.Type
		}
		if lit.Name == "true" || lit.Name == "false" {
			return lit.Name == "true", Bool
		}
		c.errorf(p.P, "identificador não declarado: %s", lit.Name)
	case *ast.FloatLit:
		c.errorf(p.P, "float ainda não é suportado nesta versão")
	}
	return nil, Invalid
}

func formatConst(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	return fmt.Sprint(v)
}

func (c *checker) condition(e ast.Expr, context string) {
	if t := c.value(e); t != Bool && t != Invalid {
		c.errorf(e.Pos(), "a condição do %s precisa ser bool, encontrado %s", context, t)
	}
}

// target checa o lado esquerdo de uma atribuição: uma variável ou um campo
// dela (p.x, r.canto.x). Escrever numa variável não conta como uso dela.
func (c *checker) target(e ast.Expr) Type {
	var t Type
	switch e := e.(type) {
	case *ast.SelectorExpr:
		base := c.targetBase(e.X)
		if _, isPtr := base.(*Pointer); isPtr || isBorrow(base) {
			// Escrever através de um ponteiro é usar o ponteiro.
			c.markUsed(e.X)
		}
		t = c.field(base, e)
	case *ast.Ident:
		t = c.targetVar(e)
	case *ast.IndexExpr:
		t = c.targetIndex(e)
	case *ast.UnaryExpr:
		if e.Op != token.MUL {
			c.errorf(e.Pos(), "não é possível atribuir a esta expressão (só a variáveis, campos e *p)")
			return Invalid
		}
		t, _ = c.deref(c.value(e.X), e.X.Pos(), exprName(e.X))
		if !isPointerType(c.info.Types[e.X]) && t != Invalid {
			c.errorf(e.P, "operador * só se aplica a ponteiros e empréstimos, não a %s", t)
			t = Invalid
		}
	default:
		c.errorf(e.Pos(), "não é possível atribuir a esta expressão (só a variáveis, campos e *p)")
		return Invalid
	}
	c.info.Types[e] = t
	return t
}

// targetBase checa a base de um campo atribuído (p em p.x = 1). Diferente de
// targetVar, a variável em si não é reatribuída, então empréstimos e apelidos
// de "if x != nil" valem como base.
func (c *checker) targetBase(e ast.Expr) Type {
	id, ok := e.(*ast.Ident)
	if !ok {
		return c.target(e)
	}
	if l := c.scope.lookup(id.Name); l != nil && !l.IsConst {
		c.info.Locals[id] = l
		c.info.Types[id] = l.Type
		return l.Type
	}
	return c.target(e)
}

func (c *checker) targetVar(id *ast.Ident) Type {
	l := c.scope.lookup(id.Name)
	switch {
	case l != nil && l.IsConst:
		c.errorf(id.P, "não é possível atribuir à constante %s", id.Name)
		return Invalid
	case l != nil && isBorrow(l.Type):
		c.errorf(id.P, "o empréstimo %s não pode ser reatribuído", id.Name)
		return Invalid
	case l != nil:
		c.info.Locals[id] = l
		return l.Type
	case c.funcs[id.Name] != nil || c.structs[id.Name] != nil || predeclared[id.Name] || modules[id.Name]:
		c.errorf(id.P, "não é possível atribuir a %s", id.Name)
	default:
		c.errorf(id.P, "identificador não declarado: %s", id.Name)
	}
	return Invalid
}

// ---------- Expressões ----------

// value checa uma expressão que precisa produzir valor.
func (c *checker) value(e ast.Expr) Type {
	t := c.expr(e)
	if t == Void {
		c.errorf(e.Pos(), "a chamada não produz valor")
		return Invalid
	}
	return t
}

func (c *checker) expr(e ast.Expr) Type {
	t := c.expr0(e)
	c.info.Types[e] = t
	return t
}

func (c *checker) expr0(e ast.Expr) Type {
	switch e := e.(type) {
	case *ast.IntLit:
		if _, err := strconv.ParseInt(e.Value, 10, 64); err != nil {
			c.errorf(e.P, "constante %s não cabe em int (64 bits)", e.Value)
			return Invalid
		}
		return Int
	case *ast.FloatLit:
		c.errorf(e.P, "float ainda não é suportado nesta versão")
		return Invalid
	case *ast.StringLit:
		return String
	case *ast.CharLit:
		return Byte
	case *ast.Ident:
		return c.ident(e)
	case *ast.UnaryExpr:
		return c.unary(e)
	case *ast.BinaryExpr:
		return c.binary(e)
	case *ast.CallExpr:
		return c.call(e)
	case *ast.SelectorExpr:
		return c.selector(e)
	case *ast.CompositeLit:
		return c.composite(e)
	case *ast.IndexExpr:
		return c.index(e)
	case *ast.TypeArg:
		c.errorf(e.Pos(), "um tipo só pode ser argumento de alloc")
		return Invalid
	case *ast.SliceExpr:
		c.errorf(e.Lbrack, "um sub-slice só pode ser emprestado como argumento de chamada: f(&s[a:b])")
		c.subSlice(e)
		return Invalid
	}
	panic(fmt.Sprintf("checker: expressão desconhecida %T", e))
}

func (c *checker) ident(e *ast.Ident) Type {
	if l := c.scope.lookup(e.Name); l != nil {
		l.Used = true
		if l.IsConst {
			c.info.Consts[e] = l.Const
		} else {
			c.info.Locals[e] = l
		}
		return l.Type
	}
	switch e.Name {
	case "true", "false":
		return Bool
	case "nil":
		return NilType
	case "alloc", "free", "len", "realloc", "take", "replace":
		c.errorf(e.P, "%s precisa ser chamada: %s(...)", e.Name, e.Name)
		return Invalid
	}
	switch {
	case c.funcs[e.Name] != nil:
		c.errorf(e.P, "função %s usada como valor (faltou chamar?)", e.Name)
	case modules[e.Name]:
		c.errorf(e.P, "módulo %s usado como valor", e.Name)
	case basicTypes[e.Name] != nil:
		c.errorf(e.P, "tipo %s usado como valor", e.Name)
	case c.structs[e.Name] != nil:
		c.errorf(e.P, "tipo %s usado como valor (para criar um valor: %s{...}; no cabeçalho de if, for ou match, entre parênteses)", e.Name, e.Name)
	default:
		c.errorf(e.P, "identificador não declarado: %s", e.Name)
	}
	return Invalid
}

func (c *checker) unary(e *ast.UnaryExpr) Type {
	switch e.Op {
	case token.AMP:
		c.errorf(e.P, "& só pode aparecer como argumento de chamada, para emprestar: f(&x)")
		c.value(e.X)
		return Invalid
	case token.MUL:
		t := c.value(e.X)
		if t == Invalid {
			return Invalid
		}
		if !isPointerType(t) {
			c.errorf(e.P, "operador * só se aplica a ponteiros e empréstimos, não a %s", t)
			return Invalid
		}
		elem, _ := c.deref(t, e.X.Pos(), exprName(e.X))
		return elem
	}
	t := c.value(e.X)
	want := Int
	if e.Op == token.NOT {
		want = Bool
	}
	if t == Invalid {
		return Invalid
	}
	if t != want {
		c.errorf(e.P, "operador %s não se aplica a %s", e.Op, t)
		return Invalid
	}
	return want
}

func (c *checker) binary(e *ast.BinaryExpr) Type {
	x, y := c.value(e.X), c.value(e.Y)
	if x == Invalid || y == Invalid {
		return Invalid
	}
	mismatch := func() Type {
		c.errorf(e.OpPos, "operador %s não se aplica a %s e %s%s", e.Op, x, y, byteHint(x, y))
		return Invalid
	}
	switch e.Op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		if e.Op == token.ADD && x == String && y == String {
			c.errorf(e.OpPos, "concatenação de strings exige alocação explícita e ainda não é suportada")
			return Invalid
		}
		if x != Int || y != Int {
			return mismatch()
		}
		return Int
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		if x != y || (x != Int && x != Byte) {
			return mismatch()
		}
		return Bool
	case token.EQL, token.NEQ:
		if x == NilType || y == NilType {
			return c.nilComparison(e, x, y)
		}
		if isPointerType(x) || isPointerType(y) {
			c.errorf(e.OpPos, "ponteiros só podem ser comparados com nil")
			return Invalid
		}
		if c.noSliceCompare(e, x) || c.noSliceCompare(e, y) {
			return Invalid
		}
		if x != y {
			c.errorf(e.OpPos, "comparação entre tipos diferentes: %s e %s", x, y)
			return Invalid
		}
		if isResource(x) {
			c.errorf(e.OpPos, "structs que contêm ponteiros donos não podem ser comparadas com ==")
			return Invalid
		}
		return Bool
	case token.LAND, token.LOR:
		if x != Bool || y != Bool {
			return mismatch()
		}
		return Bool
	}
	panic(fmt.Sprintf("checker: operador binário desconhecido %s", e.Op))
}

func (c *checker) call(e *ast.CallExpr) Type {
	switch fun := e.Fun.(type) {
	case *ast.SelectorExpr:
		id, ok := fun.X.(*ast.Ident)
		if !ok || !modules[id.Name] || c.scope.lookup(id.Name) != nil {
			break
		}
		if !c.imports[id.Name] {
			c.errorf(id.P, "módulo %s não foi importado (adicione: import %q)", id.Name, id.Name)
			c.checkArgs(e)
			return Invalid
		}
		if id.Name == "io" && fun.Sel == "print" {
			for _, a := range e.Args {
				t := c.value(a)
				elem, isSl := sliceElem(t)
				switch {
				case isPointerType(t) || t == NilType:
					c.errorf(a.Pos(), "io.print não imprime ponteiros (%s); imprima o conteúdo: *p ou p.campo", t)
				case isSl && (isPointerType(elem) || isSlicePtrElem(elem)):
					c.errorf(a.Pos(), "io.print não imprime %s: os elementos são ponteiros", t)
				default:
					c.noCopy(t, a.Pos())
				}
			}
			c.info.Calls[e] = Callee{Builtin: IoPrint}
			return Void
		}
		if t, ok := c.moduleFunc(e, id.Name, fun.Sel); ok {
			return t
		}
		c.errorf(fun.SelPos, "%s.%s não existe", id.Name, fun.Sel)
		c.checkArgs(e)
		return Invalid

	case *ast.Ident:
		if t, ok := c.builtinCall(e, fun.Name); ok {
			return t
		}
		if bt, ok := basicTypes[fun.Name]; ok && c.scope.lookup(fun.Name) == nil {
			return c.conversion(e, bt)
		}
		fn := c.funcs[fun.Name]
		if fn == nil || c.scope.lookup(fun.Name) != nil {
			break
		}
		return c.userCall(e, fn)
	}

	if t := c.expr(e.Fun); t != Invalid {
		c.errorf(e.Lparen, "valor do tipo %s não é uma função", t)
	}
	c.checkArgs(e)
	return Invalid
}

// checkArgs checa os argumentos de uma chamada inválida, para reportar
// erros dentro deles também.
func (c *checker) checkArgs(e *ast.CallExpr) {
	for _, a := range e.Args {
		c.value(a)
	}
}

// userCall checa uma chamada a uma função do usuário ou nativa.
func (c *checker) userCall(e *ast.CallExpr, fn *Func) Type {
	if len(e.Args) != len(fn.Params) {
		c.errorf(e.Lparen, "%s espera %d argumento(s), recebeu %d", fn.Name, len(fn.Params), len(e.Args))
	}
	for i, a := range e.Args {
		context := fmt.Sprintf("argumento %d de %s", i+1, fn.Name)
		if i < len(fn.Params) {
			if b, ok := fn.Params[i].Type.(*Borrow); ok {
				c.borrowArg(a, b, context)
				continue
			}
		}
		t := c.value(a)
		if i < len(fn.Params) {
			c.assignable(t, fn.Params[i].Type, a.Pos(), context)
		}
	}
	c.info.Calls[e] = Callee{Func: fn}
	return fn.Result
}
