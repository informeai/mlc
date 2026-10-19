// Package checker valida nomes e tipos do programa antes da geração de C.
// Tudo que o checker aceita precisa gerar C sem comportamento indefinido.
package checker

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

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

var basicTypes = map[string]Type{
	"int": Int, "bool": Bool, "string": String, "byte": Byte,
	"int8": Int8, "int16": Int16, "int32": Int32,
	"uint16": Uint16, "uint32": Uint32, "uint64": Uint64,
	"float32": Float32, "float64": Float64,
}

// predeclared são nomes que o usuário não pode redeclarar.
var predeclared = map[string]bool{
	"int": true, "bool": true, "string": true, "byte": true,
	"int8": true, "int16": true, "int32": true, "uint16": true, "uint32": true, "uint64": true,
	"float32": true, "float64": true,
	"true": true, "false": true, "nil": true,
	"alloc": true, "free": true, "len": true, "realloc": true,
	"take": true, "replace": true, "drain": true, "drop": true,
}

type Local struct {
	Name  string
	CName string // nome no C gerado, único dentro da função
	Type  Type
	Pos   token.Pos
	Param bool
	Used  bool // lida em alguma expressão

	IsConst bool // constante: Const tem o valor (int64, string ou bool)
	Const   any
	Pub     bool // constante pública (pub const)
}

type Func struct {
	Name     string
	CName    string // nome no C gerado (mlc_fn_...)
	File     string // arquivo da declaração
	Pub      bool
	mod      *module
	Params   []*Local
	Result   Type
	Decl     *ast.FuncDecl // nil numa função nativa
	Native   string        // nome no runtime C, numa função nativa (str.copy)
	Method   bool          // método: Params[0] é o receptor
	typeVars []Type        // função genérica: os parâmetros de tipo (checagem abstrata)
	// método genérico (each[C]): os tipos da struct e os do próprio método
	structBinding, methodBinding map[string]Type
	instances                    map[string]*Func // função genérica: instâncias já criadas
	recv                         recvMode
	queued                       bool   // instância de genérico: checada por checkInstances
	CSym                         string // extern "C" fn: o nome do símbolo em C
	// BorrowFrom: numa função que devolve &T, o parâmetro (1 + índice) de onde
	// o empréstimo vem; 0 se não devolve empréstimo
	BorrowFrom int
}

type Builtin int

const (
	NoBuiltin   Builtin = iota
	IoPrint             // io.print
	Alloc               // alloc(T) ou alloc(valor)
	Free                // free(p)
	Len                 // len(s)
	Realloc             // realloc(s, n)
	Take                // take(&s[i])
	Replace             // replace(&s[i], v)
	Convert             // int(x), byte(x)
	Drop                // drop(x): libera x se for dono (código genérico)
	ThreadSpawn         // thread.spawn(f, ctx, stack)
	ThreadJoin          // thread.join(t)
	ChanOp              // chan.new e os métodos das pontas de canal
	MutexOp             // mutex.new e os métodos de Mutex e Guard
)

// Callee é o destino resolvido de uma chamada: uma função do usuário ou um builtin.
type Callee struct {
	Func     *Func
	Builtin  Builtin
	Elem     Type // alloc: o tipo alocado
	Zero     bool // alloc: memória zerada (alloc(T)) em vez de valor inicial
	Slice    bool // alloc([]T, n)
	Indirect bool // chamada através de um valor fn: f(x), s.campo(x)
}

// Info é o resultado da checagem, consumido pelo gerador de código.
type Info struct {
	Types  map[ast.Expr]Type
	Locals map[*ast.Ident]*Local
	Decls  map[*ast.VarDecl]*Local
	// Patterns guarda o valor constante de cada padrão de match: int64,
	// string ou bool. O curinga _ não aparece.
	Patterns map[*ast.Pattern]any
	// Values guarda o valor das expressões constantes (literais, constantes,
	// contas só com constantes): *big.Int, float64, string ou bool.
	Values       map[ast.Expr]any
	Main         *Func                      // fn main do arquivo principal
	Tests        []*Func                    // fn test_*() de um arquivo de teste, na ordem do código
	Benches      []*Func                    // fn bench_*(b &testing.B) de um arquivo de teste
	Impls        []*Impl                    // os impl de todos os arquivos (para o editor)
	TestMode     bool                       // arquivo de teste: o main roda testes e benchmarks
	Structs      []*Struct                  // cada struct depois das que ela contém
	Slices       []*Slice                   // tipos slice usados, para o gerador declarar
	Arrays       []*Array                   // tipos array usados, para o gerador declarar
	ForVars      map[*ast.ForStmt]*Local    // a variável de cada for x in s
	Recv         map[*ast.CallExpr]ast.Expr // receptor de cada chamada de método (x, *x ou &x)
	EnumCtors    map[ast.Expr]*Variant      // Forma.Circulo(...) e Forma.Vazio
	PatternBinds map[*ast.Pattern][]*Local  // nomes dados aos campos em Circulo(r)
	Enums        []*Enum                    // enums concretos, para o gerador declarar
	Externs      []*Func                    // extern fn da biblioteca padrão, para o gerador declarar
	Drains       map[*ast.ForStmt]ast.Expr  // for x in drain(s): o slice esvaziado
	Calls        map[*ast.CallExpr]Callee
	Prints       map[*ast.CallExpr]*PrintPlan // io.print e io.printf reescritos
	Spawns       map[*ast.CallExpr]*SpawnPlan // thread.spawn
	Chans        map[*ast.CallExpr]*ChanPlan  // chan.new e os métodos das pontas
	Mutexes      map[*ast.CallExpr]*MutexPlan // mutex.new e os métodos de Mutex e Guard
	FuncRefs     map[ast.Expr]*Func           // funções usadas como valor
	BorrowSrc    map[*Local]*Local            // variável emprestada → a origem (raiz)
	BorrowView   map[*Local]bool              // a variável emprestada é uma visão (sub-slice)
	BorrowParent map[*Local]*Local            // variável emprestada derivada de outra
	DropTypes    map[string]Type              // tipos com drop não trivial (por typeKey)
	DropOrder    []Type                       // os mesmos, na ordem em que apareceram
	DropFree     map[*Struct]*Func            // o método free de cada struct liberada por drop
	CExterns     []*Func                      // funções de C (extern "C" fn), para o gerador declarar
	MatchTakes   map[*ast.MatchStmt]bool      // match que consome um enum dono (tira os dados)
	BindBorrows  map[*Local]bool              // nome de padrão que empresta um campo dono
	Links        []string                     // bibliotecas de C pedidas com link "nome"
	FuncTypes    []*FuncType                  // tipos fn concretos, para o gerador declarar
	Funcs        []*Func                      // na ordem de declaração
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
	info    *Info
	mod     *module            // arquivo em checagem
	modules map[string]*module // arquivos importados, pelo caminho do import
	order   map[string]int     // ordem de cada arquivo, para ordenar os erros

	pointers    map[ptrKey]*Pointer
	typeParams  map[string]Type // parâmetros de tipo visíveis (TypeVars na definição, tipos na instância)
	building    map[*Struct]bool
	pending     []pendingInst
	instName    string // instância genérica em checagem, para as mensagens
	abstracts   []abstractCheck
	impls       []*Impl                       // impl de todos os arquivos já checados
	paramIfaces map[*ast.TypeParam]*Interface // interface de cada restrição (resolvida na declaração)
	tvMethods   map[string]*Func
	autos       []autoImpl            // impl auto do arquivo atual, para conferir os campos
	declared    map[declaredImpl]bool // impl do arquivo atual, anunciados antes das assinaturas
	funcTypes   map[string]*FuncType
	outHandles  map[*Pointer]*OutHandle
	optArgs     map[Type]*OptArg
	threads     map[Type]*Struct // thread.Thread[R], por R
	chans       map[chanKey]*Struct
	mutexes     map[mutexKey]*Struct
	inExternC   bool           // resolvendo a assinatura de uma extern "C" fn (aceita &?*X)
	printRecv   ast.Expr       // w.printf: o Writer, enquanto ioPrint checa a chamada
	printLevel  int            // lg.info: o nível, enquanto ioPrint checa a chamada
	probes      map[*Func]bool // instâncias de verificação de posse (T owned = []byte)
	probing     bool           // checando uma delas
	muted       bool           // erros silenciados (tipos de uma instância de verificação)
	mutedErrs   int
	probeBroken map[*Func]bool
	inReceiver  bool       // resolvendo o tipo do receptor de um método
	freeNeeds   []freeNeed // structs com donos dentro de um T owned: precisam de free
	freeMissing map[*Struct]bool
	expected    Type // tipo esperado de expectedFor (para Resultado.Ok sem argumentos de tipo)
	expectedFor ast.Expr
	litOK       bool // o próximo literal composto pode conter ponteiros donos (dentro de alloc)
	borrows     map[Type]*Borrow
	slices      map[Type]*Slice
	arrays      map[arrKey]*Array
	scope       *scope
	fn          *Func
	errs        []token.Error

	cnames map[string]int        // quantas variáveis com cada nome a função já declarou
	loops  []*ast.ForStmt        // laços abertos, o mais interno por último
	breaks map[*ast.ForStmt]bool // laços que têm break direto
}

// Source é um arquivo do programa, já analisado pelo parser.
type Source struct {
	Path string // caminho usado no import ("" para o arquivo principal)
	Std  bool   // arquivo da biblioteca padrão: pode declarar extern fn
	Test bool   // arquivo de teste (_test.mlc): sem main, com fn test_*()
	File string // nome do arquivo, para mensagens e #line
	AST  *ast.File
}

// Check checa um programa de um arquivo só.
func Check(file string, f *ast.File) (*Info, []token.Error) {
	return CheckProgram([]Source{{File: file, AST: f}})
}

// CheckProgram checa um programa de vários arquivos. Cada arquivo vem depois
// dos que ele importa, e o principal (Path vazio) vem por último.
func CheckProgram(srcs []Source) (*Info, []token.Error) {
	c := &checker{
		modules: map[string]*module{},
		order:   map[string]int{},
		info: &Info{
			Types:        map[ast.Expr]Type{},
			Locals:       map[*ast.Ident]*Local{},
			Decls:        map[*ast.VarDecl]*Local{},
			Patterns:     map[*ast.Pattern]any{},
			Values:       map[ast.Expr]any{},
			ForVars:      map[*ast.ForStmt]*Local{},
			Recv:         map[*ast.CallExpr]ast.Expr{},
			EnumCtors:    map[ast.Expr]*Variant{},
			PatternBinds: map[*ast.Pattern][]*Local{},
			Drains:       map[*ast.ForStmt]ast.Expr{},
			Calls:        map[*ast.CallExpr]Callee{},
			Prints:       map[*ast.CallExpr]*PrintPlan{},
			Spawns:       map[*ast.CallExpr]*SpawnPlan{},
			Chans:        map[*ast.CallExpr]*ChanPlan{},
			Mutexes:      map[*ast.CallExpr]*MutexPlan{},
			FuncRefs:     map[ast.Expr]*Func{},
			BorrowSrc:    map[*Local]*Local{},
			BorrowView:   map[*Local]bool{},
			BorrowParent: map[*Local]*Local{},
			DropTypes:    map[string]Type{},
			DropFree:     map[*Struct]*Func{},
			MatchTakes:   map[*ast.MatchStmt]bool{},
			BindBorrows:  map[*Local]bool{},
		},
		pointers: map[ptrKey]*Pointer{},
		borrows:  map[Type]*Borrow{},
		slices:   map[Type]*Slice{},
		arrays:   map[arrKey]*Array{},
		building: map[*Struct]bool{},
		breaks:   map[*ast.ForStmt]bool{},

		paramIfaces: map[*ast.TypeParam]*Interface{},
		funcTypes:   map[string]*FuncType{},
		outHandles:  map[*Pointer]*OutHandle{},
		optArgs:     map[Type]*OptArg{},
		threads:     map[Type]*Struct{},
		chans:       map[chanKey]*Struct{},
		mutexes:     map[mutexKey]*Struct{},
		probes:      map[*Func]bool{},
		probeBroken: map[*Func]bool{},
		freeMissing: map[*Struct]bool{},
		tvMethods:   map[string]*Func{},
	}
	for i, src := range srcs {
		c.order[src.File] = i
		c.mod = c.newModule(src, i)
		c.collectImports(src.AST)
		c.collectLinks(src.AST)
		c.collectConsts(src.AST) // antes das structs: [N]T usa constantes
		c.collectEnumNames(src.AST)
		c.collectStructs(src.AST)
		c.collectEnumVariants(src.AST)
		c.collectInterfaces(src.AST)
		c.declareImpls(src.AST)
		c.collectFuncs(src.AST)
		c.collectImpls(src.AST)
		c.declared = nil
		c.probeOwned(src.AST)
		for _, fn := range c.info.Funcs {
			if fn.mod == c.mod && !fn.queued {
				c.checkFunc(fn)
			}
		}
		c.checkAbstracts()
	}
	// Structs com donos usadas dentro de um T owned precisam de free.
	c.checkFreeNeeds()
	// Corpo das instâncias de genéricos criadas pelos usos.
	c.checkInstances()
	// A posse só é verificada num programa sem erros de tipo: ela depende
	// dos tipos resolvidos.
	if len(c.errs) == 0 {
		for _, fn := range c.info.Funcs {
			c.mod = fn.mod
			c.probing = c.probes[fn]
			c.instName = ""
			if c.probing {
				c.instName = fn.Name
			}
			if !c.probeBroken[fn] {
				c.checkBorrowLifetimes(fn)
				c.checkOwnership(fn)
			}
		}
		c.probing, c.instName = false, ""
	}
	// Erros na ordem dos arquivos e, dentro de cada um, na ordem do código.
	sort.SliceStable(c.errs, func(i, j int) bool {
		a, b := c.errs[i], c.errs[j]
		if a.File != b.File {
			return c.order[a.File] < c.order[b.File]
		}
		return a.Pos.Before(b.Pos)
	})
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
	if predeclared[name] {
		c.errorf(pos, "o nome %q é reservado", name)
		l.Used = true
		return l
	}
	if c.mod.uses[name] != nil {
		c.errorf(pos, "o nome %s é um módulo importado", name)
		l.Used = true
		return l
	}
	if c.mod.enums[name] != nil {
		c.errorf(pos, "o nome %s já é um tipo (enum)", name)
		l.Used = true
		return l
	}
	if c.mod.structs[name] != nil {
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
	if c.muted {
		c.mutedErrs++
		return
	}
	msg := fmt.Sprintf(format, args...)
	switch {
	case c.probing:
		msg += " (verificado com T dono, como " + c.instName + ")"
	case c.instName != "":
		msg += " (na instância " + c.instName + ")"
	}
	c.errs = append(c.errs, token.Error{File: c.mod.file, Pos: pos, Msg: msg})
}

// ---------- Declarações ----------

func (c *checker) collectFuncs(f *ast.File) {
	for _, d := range f.Funcs {
		if d.Recv != nil {
			c.collectMethod(d)
			continue
		}
		if predeclared[d.Name] {
			c.errorf(d.P, "o nome %q é reservado", d.Name)
			continue
		}
		if c.mod.structs[d.Name] != nil || c.mod.global.names[d.Name] != nil {
			c.errorf(d.P, "o nome %s já é usado por uma struct ou constante", d.Name)
			continue
		}
		if prev, ok := c.mod.generics[d.Name]; ok {
			c.errorf(d.P, "função %s já declarada na linha %d", d.Name, prev.Decl.P.Line)
			continue
		}
		if prev, ok := c.mod.funcs[d.Name]; ok {
			c.errorf(d.P, "função %s já declarada na linha %d", d.Name, prev.Decl.P.Line)
			continue
		}
		if len(d.TypeParams) > 0 {
			c.collectGeneric(d)
			continue
		}
		fn := &Func{
			Name: c.qualify(d.Name), Result: Void, Decl: d,
			CName: "mlc_fn_" + c.mod.prefix + d.Name, File: c.mod.file, Pub: d.Pub, mod: c.mod,
		}
		c.inExternC = d.ExternC
		c.signature(fn, d, nil)
		c.inExternC = false
		if d.ExternC {
			c.collectExternC(fn, d) // extern "C" fn: uma função de C de verdade
			continue
		}
		if d.Extern {
			// implementada no runtime C: mlc_std_<módulo>_<nome>, sem corpo
			if !c.mod.std {
				c.errorf(d.P, "extern fn só é permitida na biblioteca padrão; para chamar uma função de C, declare extern \"C\" fn")
			}
			fn.Native = "mlc_std_" + c.mod.name + "_" + d.Name
			c.info.Externs = append(c.info.Externs, fn)
			c.mod.funcs[d.Name] = fn
			continue
		}
		c.mod.funcs[d.Name] = fn
		c.info.Funcs = append(c.info.Funcs, fn)
	}

	main := c.mod.funcs["main"]
	if c.entry() && c.mod.test {
		c.collectTests(f, main)
		return
	}
	switch {
	case !c.entry() && main != nil:
		c.errorf(main.Decl.P, "fn main só pode existir no arquivo principal; %s é importado", c.mod.file)
	case !c.entry():
	case main == nil:
		c.errorf(token.Pos{Line: 1, Col: 1}, "função main não declarada")
	case len(main.Params) > 0 || main.Result != Void:
		c.errorf(main.Decl.P, "main não recebe parâmetros nem retorna valor")
	default:
		c.info.Main = main
	}
}

// collectTests junta as funções test_* de um arquivo de teste: o mlc test
// gera o main que roda cada uma num processo separado.
func (c *checker) collectTests(f *ast.File, main *Func) {
	if main != nil {
		c.errorf(main.Decl.P, "um arquivo de teste não declara fn main: o mlc test gera o main que roda os testes")
	}
	c.info.TestMode = true
	for _, d := range f.Funcs {
		if d.Recv != nil {
			continue
		}
		if strings.HasPrefix(d.Name, "bench_") {
			c.collectBench(d)
			continue
		}
		if !strings.HasPrefix(d.Name, "test_") {
			continue
		}
		if len(d.TypeParams) > 0 {
			c.errorf(d.P, "o teste %s não pode ser genérico", d.Name)
			continue
		}
		fn := c.mod.funcs[d.Name]
		if fn == nil || fn.Decl != d {
			continue // nome repetido ou reservado: já deu erro
		}
		if len(fn.Params) > 0 || fn.Result != Void {
			c.errorf(d.P, "o teste %s não recebe parâmetros nem retorna valor", d.Name)
			continue
		}
		c.info.Tests = append(c.info.Tests, fn)
	}
	if len(c.info.Tests) == 0 && len(c.info.Benches) == 0 && len(c.errs) == 0 {
		c.errorf(token.Pos{Line: 1, Col: 1}, "arquivo de teste sem testes: declare funções fn test_nome() (ou fn bench_nome(b &testing.B))")
	}
}

// collectBench registra um benchmark: fn bench_nome(b &testing.B), que
// repete o trecho medido b.n vezes.
func (c *checker) collectBench(d *ast.FuncDecl) {
	if len(d.TypeParams) > 0 {
		c.errorf(d.P, "o benchmark %s não pode ser genérico", d.Name)
		return
	}
	fn := c.mod.funcs[d.Name]
	if fn == nil || fn.Decl != d {
		return
	}
	ok := len(fn.Params) == 1 && fn.Result == Void
	if ok {
		b, isBorrow := fn.Params[0].Type.(*Borrow)
		s, isStruct := Type(nil), false
		if isBorrow {
			s = b.Elem
			_, isStruct = s.(*Struct)
		}
		ok = isStruct && s.(*Struct).Name == "testing.B"
	}
	if !ok {
		c.errorf(d.P, "o benchmark %s recebe b &testing.B e não retorna valor: fn %s(b &testing.B)", d.Name, d.Name)
		return
	}
	c.info.Benches = append(c.info.Benches, fn)
}

// ---------- Comandos ----------

func (c *checker) checkFunc(fn *Func) {
	c.fn = fn
	c.mod = fn.mod
	c.scope = c.mod.global
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
			u, isAmp := s.Value.(*ast.UnaryExpr)
			isAmp = isAmp && u.Op == token.AMP
			switch b, isB := t.(*Borrow); {
			case isB:
				c.borrowExpr(s.Value, b, "declaração de "+s.Name)
			case t == nil && isAmp:
				t = c.borrowExpr(s.Value, nil, "declaração de "+s.Name) // x := &a.campo
			case t == nil:
				t = c.valueDefault(s.Value) // x := 5 é int; x := 2.5 é float64
			default:
				c.valueAs(s.Value, t, "declaração de "+s.Name)
			}
		}
		switch tt := t.(type) {
		case *Borrow:
			if s.Value == nil {
				c.errorf(s.P, "var %s %s precisa de valor inicial: um empréstimo sempre aponta para algo", s.Name, tt)
				t = Invalid
			}
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
		if isBorrow(t) && s.Value != nil {
			c.declareBorrow(c.info.Decls[s], s.Value)
		}
	case *ast.AssignStmt:
		lt := c.target(s.Lhs)
		if s.Op == token.ASSIGN {
			c.noCopy(lt, s.OpPos)
			c.valueAs(s.Rhs, lt, "atribuição")
			return
		}
		if lt == String && s.Op == token.ADD_ASSIGN {
			c.value(s.Rhs)
			c.errorf(s.OpPos, "concatenação de strings exige alocação explícita: monte o texto num buffer (veja docs/texto.md)")
			return
		}
		rt := c.value(s.Rhs)
		op := compoundOps[s.Op]
		if op == token.SHL || op == token.SHR {
			// x <<= n: n pode ser de qualquer tipo inteiro
			switch {
			case lt == Invalid || rt == Invalid:
			case !intLike(lt):
				c.errorf(s.OpPos, "operador %s só se aplica a inteiros, não a %s", s.Op, lt)
			case !intLike(rt):
				c.errorf(s.OpPos, "a quantidade de bits de %s precisa ser um inteiro, não %s", s.Op, rt)
			default:
				if v, ok := c.info.Values[s.Rhs]; ok && (v.(*big.Int).Sign() < 0 || isInteger(lt) && v.(*big.Int).Cmp(big.NewInt(int64(IntBits(lt)))) >= 0) {
					c.errorf(s.Rhs.Pos(), "deslocamento de %s bits num %s (use de 0 a %d)", v, lt, IntBits(lt)-1)
				}
				if rt == UntypedInt {
					c.convertUntyped(s.Rhs, Int)
				}
			}
			return
		}
		if isUntyped(rt) && numLike(lt) {
			c.convertUntyped(s.Rhs, lt)
			rt = lt
		}
		switch {
		case lt == Invalid || rt == Invalid:
		case lt != rt || !numLike(lt):
			c.errorf(s.OpPos, "operador %s não se aplica a %s e %s", s.Op, lt, rt)
		case s.Op == token.REM_ASSIGN && (isFloat(lt) || (abstract(lt) && !tvHas(lt, "mod"))):
			c.errorf(s.OpPos, "operador %%= não se aplica a %s (só a inteiros)", lt)
		case isBitOp(op) && !intLike(lt):
			c.errorf(s.OpPos, "operador %s só se aplica a inteiros, não a %s", s.Op, lt)
		}
	case *ast.IncDecStmt:
		if t := c.target(s.X); !isInteger(t) && t != Invalid {
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
		if c.fn.Result == Void {
			c.value(s.Value)
			c.errorf(s.Value.Pos(), "função %s não retorna valor", c.fn.Name)
			return
		}
		if b, ok := c.fn.Result.(*Borrow); ok {
			c.checkBorrowReturn(s, b) // return &v.items[i]: de onde vem o empréstimo
			return
		}
		c.valueAs(s.Value, c.fn.Result, "return")
	case *ast.Block:
		c.checkBlock(s)
	default:
		panic(fmt.Sprintf("checker: comando desconhecido %T", s))
	}
}

// checkMatch valida um match: tipo de cada padrão, padrões repetidos,
// braços inalcançáveis e cobertura de todos os valores.
func (c *checker) checkMatch(s *ast.MatchStmt) {
	t := c.valueDefault(s.Subject)
	if b, ok := t.(*Borrow); ok {
		if en, ok := b.Elem.(*Enum); ok {
			c.checkEnumMatch(s, en) // match e, com e &Enum: só lê
			return
		}
	}
	if en, ok := t.(*Enum); ok {
		c.checkEnumMatch(s, en)
		return
	}
	if !isInteger(t) && t != Bool && t != String && t != Invalid {
		c.errorf(s.Subject.Pos(), "match não se aplica a %s (só inteiros, bool e string)", t)
		t = Invalid
	}
	seen := map[string]token.Pos{}
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
			v, pt := c.patternValue(p, t)
			if pt == Invalid || v == nil {
				continue
			}
			if t != Invalid && pt != t {
				c.errorf(p.P, "padrão do tipo %s em match sobre %s", pt, t)
				continue
			}
			key := fmt.Sprintf("%T:%v", v, v)
			if prev, dup := seen[key]; dup {
				c.errorf(p.P, "padrão %s repetido (já aparece na linha %d)", formatConst(v), prev.Line)
				continue
			}
			seen[key] = p.P
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
// patternValue devolve o valor constante de um padrão e o seu tipo. Um
// literal sem tipo assume o tipo do valor do match (subject).
func (c *checker) patternValue(p *ast.Pattern, subject Type) (any, Type) {
	if p.Lit == nil { // padrão de variante (V(x)) fora de match sobre enum
		if subject != Invalid {
			c.errorf(p.P, "padrão de variante em match sobre %s", subject)
		}
		return nil, Invalid
	}
	if _, ok := p.Lit.(*ast.Ident); ok && subject == Invalid {
		return nil, Invalid // o sujeito já deu erro; o nome pode ser uma variante
	}
	switch lit := p.Lit.(type) {
	case *ast.Ident:
		if l := c.scope.lookup(lit.Name); l != nil && !l.IsConst {
			c.errorf(p.P, "%s não é constante: padrão precisa ser um literal, uma constante ou _", lit.Name)
			return nil, Invalid
		}
	case *ast.SelectorExpr:
		if c.userModule(lit.X) == nil {
			c.errorf(p.P, "padrão precisa ser um literal, uma constante ou _")
			return nil, Invalid
		}
	}
	t := c.value(p.Lit)
	if t == Invalid {
		return nil, Invalid
	}
	if p.Neg {
		// -5: o sinal faz parte do padrão
		if v, ok := c.info.Values[p.Lit].(*big.Int); ok {
			c.info.Values[p.Lit] = new(big.Int).Neg(v)
		}
	}
	if isUntyped(t) {
		target := subject
		if !isNumeric(target) {
			target = DefaultType(t)
		}
		c.convertUntyped(p.Lit, target)
		t = target
	}
	if isFloat(t) {
		c.errorf(p.P, "padrões decimais não são aceitos (comparar decimais por igualdade é frágil)")
		return nil, Invalid
	}
	return c.info.Values[p.Lit], t
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
	case c.mod.funcs[id.Name] != nil || c.mod.structs[id.Name] != nil || predeclared[id.Name]:
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
		v := ParseIntLit(e.Value)
		c.info.Values[e] = v
		return UntypedInt
	case *ast.FloatLit:
		f, err := strconv.ParseFloat(strings.ReplaceAll(e.Value, "_", ""), 64)
		if err != nil {
			c.errorf(e.P, "constante decimal %s fora da faixa de float64", e.Value)
			return Invalid
		}
		c.info.Values[e] = f
		return UntypedFloat
	case *ast.StringLit:
		c.info.Values[e] = e.Value
		return String
	case *ast.CharLit:
		c.info.Values[e] = big.NewInt(int64(e.Value))
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
	case *ast.ArrayLit:
		return c.arrayLit(e)
	case *ast.IndexExpr, *ast.IndexListExpr:
		// f[int] como valor: a instância de uma função genérica
		base, targs := splitIndex(e)
		if g := c.genericFuncOf(base); g != nil {
			var args []Type
			for _, a := range targs {
				args = append(args, c.exprType(a))
			}
			if inst := c.instFunc(g, args, e.Pos()); inst != nil {
				return c.funcRef(e, inst)
			}
			return Invalid
		}
		if ix, ok := e.(*ast.IndexExpr); ok {
			return c.index(ix)
		}
		c.errorf(e.Pos(), "argumentos de tipo só valem para funções e tipos genéricos")
		return Invalid
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
			c.info.Values[e] = l.Const
		} else {
			c.info.Locals[e] = l
		}
		return l.Type
	}
	switch e.Name {
	case "true", "false":
		c.info.Values[e] = e.Name == "true"
		return Bool
	case "nil":
		return NilType
	case "alloc", "free", "len", "realloc", "take", "replace":
		c.errorf(e.P, "%s precisa ser chamada: %s(...)", e.Name, e.Name)
		return Invalid
	}
	switch {
	case c.mod.pendingConsts[e.Name]:
		c.errorf(e.P, "constante %s usada antes de ser declarada", e.Name)
	case c.mod.funcs[e.Name] != nil:
		return c.funcRef(e, c.mod.funcs[e.Name])
	case c.mod.generics[e.Name] != nil:
		c.errorf(e.P, "%s é genérica: para usar como valor, escreva os tipos, como %s[int]", e.Name, e.Name)
	case c.typeParams[e.Name] != nil:
		c.errorf(e.P, "tipo %s usado como valor (para converter: %s(x))", e.Name, e.Name)
	case c.mod.uses[e.Name] != nil:
		c.errorf(e.P, "módulo %s usado como valor", e.Name)
	case basicTypes[e.Name] != nil:
		c.errorf(e.P, "tipo %s usado como valor", e.Name)
	case c.mod.enums[e.Name] != nil:
		c.errorf(e.P, "tipo %s usado como valor (para criar um valor: %s.Variante)", e.Name, e.Name)
	case c.mod.structs[e.Name] != nil:
		c.errorf(e.P, "tipo %s usado como valor (para criar um valor: %s{...}; no cabeçalho de if, for ou match, entre parênteses)", e.Name, e.Name)
	case StdModule(e.Name):
		c.errorf(e.P, "módulo %s não foi importado (adicione: import \"std/%s\")", e.Name, e.Name)
	default:
		c.errorf(e.P, "identificador não declarado: %s", e.Name)
	}
	return Invalid
}

func (c *checker) call(e *ast.CallExpr) Type {
	switch fun := e.Fun.(type) {
	case *ast.SelectorExpr:
		if en := c.enumNamed(fun.X); en != nil {
			return c.enumCtor(e, e, en, fun) // Forma.Circulo(...)
		}
		if g := c.genericFuncOf(fun); g != nil {
			return c.genericCall(e, g, nil) // mod.f(args), genérica
		}
		if m := c.userModule(fun.X); m != nil {
			if kind := ioBuiltin(m, fun.Sel); kind != "" {
				return c.ioPrint(e, kind)
			}
			if m.std && m.path == "std/mutex" && fun.Sel == "new" {
				return c.mutexNew(e, nil)
			}
			if m.std && m.path == "std/chan" && fun.Sel == "new" {
				c.errorf(fun.SelPos, "chan.new precisa do tipo dos valores: chan.new[int](16)")
				c.checkArgs(e)
				return Invalid
			}
			switch threadBuiltin(m, fun.Sel) {
			case "spawn":
				return c.threadSpawn(e)
			case "join":
				return c.threadJoin(e)
			}
			return c.moduleCall(e, m, fun)
		}
		if t, ok := c.methodCall(e, fun); ok {
			return t
		}

	case *ast.Ident:
		if t, ok := c.builtinCall(e, fun.Name); ok {
			return t
		}
		if tp := c.typeParams[fun.Name]; tp != nil && c.scope.lookup(fun.Name) == nil {
			return c.conversion(e, tp) // T(x), com T parâmetro de tipo
		}
		if g := c.genericFuncOf(fun); g != nil {
			return c.genericCall(e, g, nil)
		}
		if bt, ok := basicTypes[fun.Name]; ok && c.scope.lookup(fun.Name) == nil {
			return c.conversion(e, bt)
		}
		if l := c.scope.lookup(fun.Name); l != nil {
			if ft, ok := l.Type.(*FuncType); ok {
				c.value(fun)
				return c.indirectCall(e, ft) // f(x), com f uma variável fn
			}
		}
		fn := c.mod.funcs[fun.Name]
		if fn == nil || c.scope.lookup(fun.Name) != nil {
			break
		}
		return c.userCall(e, fn)
	case *ast.IndexExpr, *ast.IndexListExpr:
		// f[int](args): argumentos de tipo explícitos
		base, targs := splitIndex(fun)
		if sel, ok := base.(*ast.SelectorExpr); ok && c.userModule(sel.X) == nil {
			// x.m[T](args): método genérico com os tipos escritos
			t := c.value(sel.X)
			if s := structBehind(t); s != nil {
				m := s.Methods[sel.Sel]
				if m == nil && s.Origin != nil {
					m = c.instMethod(s, sel.Sel)
				}
				if m != nil && m.typeVars != nil {
					return c.genericMethodCall(e, sel, t, s, m, targs)
				}
			}
		}
		if sel, ok := base.(*ast.SelectorExpr); ok && sel.Sel == "new" {
			if m := c.userModule(sel.X); m != nil && m.std && m.path == "std/chan" {
				return c.chanNew(e, targs)
			}
			if m := c.userModule(sel.X); m != nil && m.std && m.path == "std/mutex" {
				return c.mutexNew(e, targs)
			}
		}
		if g := c.genericFuncOf(base); g != nil {
			return c.genericCall(e, g, targs)
		}
	}

	if t := c.expr(e.Fun); t != Invalid {
		if ft, ok := t.(*FuncType); ok {
			return c.indirectCall(e, ft) // fs[i](x), pega()(x)
		}
		c.errorf(e.Lparen, "valor do tipo %s não é uma função", t)
	}
	c.checkArgs(e)
	return Invalid
}

// checkArgs checa os argumentos de uma chamada inválida, para reportar
// erros dentro deles também.
func (c *checker) checkArgs(e *ast.CallExpr) {
	for _, a := range e.Args {
		c.peekType(a) // aceita &x e &s[a:b], como numa chamada válida
	}
}

// userCall checa uma chamada a uma função do usuário ou nativa.
func (c *checker) userCall(e *ast.CallExpr, fn *Func) Type {
	c.callArgs(e, fn, fn.Params)
	c.info.Calls[e] = Callee{Func: fn}
	return fn.Result
}

// ParseIntLit lê um literal inteiro: decimal (com _ entre os dígitos) ou
// com prefixo 0x, 0b, 0o. Um zero à esquerda continua decimal (010 é dez).
func ParseIntLit(s string) *big.Int {
	s = strings.ReplaceAll(s, "_", "")
	base := 10
	if len(s) > 2 && s[0] == '0' {
		switch s[1] {
		case 'x', 'X':
			base, s = 16, s[2:]
		case 'b', 'B':
			base, s = 2, s[2:]
		case 'o', 'O':
			base, s = 8, s[2:]
		}
	}
	v, ok := new(big.Int).SetString(s, base)
	if !ok {
		return new(big.Int)
	}
	return v
}
