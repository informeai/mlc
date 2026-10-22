// Package checker validates the program's names and types before generating C.
// Everything the checker accepts must generate C without undefined behavior.
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
	Invalid = &Basic{"invalid"}
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

// predeclared are names the user can't redeclare.
var predeclared = map[string]bool{
	"int": true, "bool": true, "string": true, "byte": true,
	"int8": true, "int16": true, "int32": true, "uint16": true, "uint32": true, "uint64": true,
	"float32": true, "float64": true,
	"true": true, "false": true, "nil": true,
	"alloc": true, "free": true, "len": true, "realloc": true,
	"take": true, "replace": true, "drain": true, "drop": true, "keep": true,
}

type Local struct {
	Name  string
	CName string // name in the generated C, unique within the function
	Type  Type
	Pos   token.Pos
	Param bool
	Kept  bool // a parameter of an extern "C" fn that C keeps after the call (kept)
	Used  bool // read in some expression

	IsConst bool // constant: Const has the value (int64, string or bool)
	Const   any
	CConst  string // extern "C" const: the C name; the value is C's (Const is nil)
	Pub     bool   // public constant (pub const)
}

type Func struct {
	Name     string
	CName    string // name in the generated C (mlc_fn_...)
	File     string // the declaration's file
	Pub      bool
	mod      *module
	Params   []*Local
	Result   Type
	Decl     *ast.FuncDecl // nil in a native function
	Native   string        // name in the C runtime, in a native function (str.copy)
	Method   bool          // method: Params[0] is the receiver
	typeVars []Type        // generic function: the type parameters (abstract check)
	// generic method (each[C]): the struct's types and the method's own
	structBinding, methodBinding map[string]Type
	instances                    map[string]*Func // generic function: instances already created
	recv                         recvMode
	queued                       bool   // generic instance: checked by checkInstances
	CSym                         string // extern "C" fn: the symbol's name in C
	// BorrowFrom: in a function that returns &T, the parameter (1 + index) the
	// borrow comes from; 0 if it doesn't return a borrow
	BorrowFrom int
	// Frame: the function's estimated frame on the stack, in bytes (stack.go)
	Frame    int64
	frameTop *stackItem
}

type Builtin int

const (
	NoBuiltin   Builtin = iota
	IoPrint             // io.print
	Alloc               // alloc(T) or alloc(value)
	Free                // free(p)
	Len                 // len(s)
	Realloc             // realloc(s, n)
	Take                // take(&s[i])
	Keep                // keep(x): x moves to C for good (a kept *T)
	Replace             // replace(&s[i], v)
	Convert             // int(x), byte(x)
	Drop                // drop(x): frees x if it's an owner (generic code)
	ThreadSpawn         // thread.spawn(f, ctx, stack)
	ThreadJoin          // thread.join(t)
	ChanOp              // chan.new and the methods of the channel ends
	MutexOp             // mutex.new and the methods of Mutex and Guard
)

// Callee is the resolved target of a call: a user function or a builtin.
type Callee struct {
	Func     *Func
	Builtin  Builtin
	Elem     Type // alloc: the allocated type
	Zero     bool // alloc: zeroed memory (alloc(T)) instead of an initial value
	Slice    bool // alloc([]T, n)
	Indirect bool // a call through an fn value: f(x), s.field(x)
}

// Info is the check's result, consumed by the code generator.
type Info struct {
	Types  map[ast.Expr]Type
	Locals map[*ast.Ident]*Local
	Decls  map[*ast.VarDecl]*Local
	// Patterns keeps the constant value of each match pattern: int64,
	// string or bool. The wildcard _ doesn't appear.
	Patterns map[*ast.Pattern]any
	// Values keeps the value of the constant expressions (literals, constants,
	// arithmetic with constants only): *big.Int, float64, string or bool.
	Values       map[ast.Expr]any
	Main         *Func                      // the main file's fn main
	Tests        []*Func                    // a test file's fn test_*(), in code order
	Benches      []*Func                    // a test file's fn bench_*(b &testing.B)
	Impls        []*Impl                    // the impls of all files (for the editor)
	TestMode     bool                       // test file: main runs tests and benchmarks
	Structs      []*Struct                  // each struct after the ones it contains
	Slices       []*Slice                   // slice types used, for the generator to declare
	Arrays       []*Array                   // array types used, for the generator to declare
	ForVars      map[*ast.ForStmt]*Local    // the variable of each for x in s
	Recv         map[*ast.CallExpr]ast.Expr // the receiver of each method call (x, *x or &x)
	EnumCtors    map[ast.Expr]*Variant      // Shape.Circle(...) and Shape.Empty
	PatternBinds map[*ast.Pattern][]*Local  // names given to the fields in Circle(r)
	Enums        []*Enum                    // concrete enums, for the generator to declare
	Externs      []*Func                    // the standard library's extern fn, for the generator to declare
	Drains       map[*ast.ForStmt]ast.Expr  // for x in drain(s): the emptied slice
	Calls        map[*ast.CallExpr]Callee
	Prints       map[*ast.CallExpr]*PrintPlan // io.print and io.printf rewritten
	Spawns       map[*ast.CallExpr]*SpawnPlan // thread.spawn
	Chans        map[*ast.CallExpr]*ChanPlan  // chan.new and the methods of the ends
	Mutexes      map[*ast.CallExpr]*MutexPlan // mutex.new and the methods of Mutex and Guard
	FuncRefs     map[ast.Expr]*Func           // functions used as values
	BorrowSrc    map[*Local]*Local            // borrowed variable → the source (root)
	BorrowView   map[*Local]bool              // the borrowed variable is a view (sub-slice)
	BorrowParent map[*Local]*Local            // a borrowed variable derived from another
	DropTypes    map[string]Type              // types with a non-trivial drop (by typeKey)
	DropOrder    []Type                       // the same, in the order they appeared
	DropFree     map[*Struct]*Func            // the free method of each struct freed by drop
	CExterns     []*Func                      // C functions (extern "C" fn), for the generator to declare
	Callbacks    []*Func                      // MLC functions passed to C, which get a C wrapper (mlc_fn_x__cb)
	CHeaders     []string                     // C headers to include (extern "C" struct and const ... from)
	CConsts      map[ast.Expr]*Local          // uses of C constants (extern "C" const), named in the C
	MatchTakes   map[*ast.MatchStmt]bool      // a match that consumes an owning enum (takes the data out)
	BindBorrows  map[*Local]bool              // a pattern name that borrows an owning field
	Links        []string                     // C libraries asked for with link "name"
	FuncTypes    []*FuncType                  // concrete fn types, for the generator to declare
	Funcs        []*Func                      // in declaration order
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
	mod     *module            // the file being checked
	modules map[string]*module // imported files, by import path
	order   map[string]int     // order of each file, to sort the errors

	pointers    map[ptrKey]*Pointer
	typeParams  map[string]Type // visible type parameters (TypeVars in the definition, types in the instance)
	building    map[*Struct]bool
	pending     []pendingInst
	instName    string // the generic instance being checked, for the messages
	abstracts   []abstractCheck
	impls       []*Impl                       // impls of all the files already checked
	paramIfaces map[*ast.TypeParam]*Interface // the interface of each constraint (resolved at the declaration)
	tvMethods   map[string]*Func
	autos       []autoImpl            // impl auto of the current file, to check the fields
	declared    map[declaredImpl]bool // impls of the current file, announced before the signatures
	funcTypes   map[string]*FuncType
	outHandles  map[*Pointer]*OutHandle
	optArgs     map[Type]*OptArg
	callbacks   map[*Func]bool // already in info.Callbacks
	kepts       map[*Struct]*Kept
	anonOK      bool             // checking the base of a selector: an anonymous C struct is allowed as a path
	threads     map[Type]*Struct // thread.Thread[R], by R
	spawnSites  []spawnSite      // the thread.spawn calls, to check the stack
	chans       map[chanKey]*Struct
	mutexes     map[mutexKey]*Struct
	inExternC   bool           // resolving the signature of an extern "C" fn (accepts &?*X)
	printRecv   ast.Expr       // w.printf: the Writer, while ioPrint checks the call
	printLevel  int            // lg.info: the level, while ioPrint checks the call
	probes      map[*Func]bool // ownership-checking instances (owned T = []byte)
	probing     bool           // checking one of them
	muted       bool           // muted errors (types of a checking instance)
	mutedErrs   int
	probeBroken map[*Func]bool
	inReceiver  bool       // resolving the type of a method's receiver
	freeNeeds   []freeNeed // structs with owners inside an owned T: they need free
	freeMissing map[*Struct]bool
	expected    Type // expected type of expectedFor (for Result.Ok without type arguments)
	expectedFor ast.Expr
	litOK       bool // the next composite literal can contain owning pointers (inside alloc)
	borrows     map[Type]*Borrow
	slices      map[Type]*Slice
	arrays      map[arrKey]*Array
	scope       *scope
	fn          *Func
	errs        []token.Error

	cnames map[string]int        // how many variables with each name the function already declared
	loops  []*ast.ForStmt        // open loops, the innermost last
	breaks map[*ast.ForStmt]bool // loops that have a direct break
}

// Source is a program file, already parsed.
type Source struct {
	Path string // path used in the import ("" for the main file)
	Std  bool   // a standard library file: can declare extern fn
	Test bool   // a test file (_test.mlc): without main, with fn test_*()
	File string // the file's name, for messages and #line
	AST  *ast.File
}

// Check checks a single-file program.
func Check(file string, f *ast.File) (*Info, []token.Error) {
	return CheckProgram([]Source{{File: file, AST: f}})
}

// CheckProgram checks a multi-file program. Each file comes after
// the ones it imports, and the main one (empty Path) comes last.
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
			CConsts:      map[ast.Expr]*Local{},
			BorrowSrc:    map[*Local]*Local{},
			BorrowView:   map[*Local]bool{},
			BorrowParent: map[*Local]*Local{},
			DropTypes:    map[string]Type{},
			DropFree:     map[*Struct]*Func{},
			MatchTakes:   map[*ast.MatchStmt]bool{},
			BindBorrows:  map[*Local]bool{},
		},
		callbacks: map[*Func]bool{},
		kepts:     map[*Struct]*Kept{},
		pointers:  map[ptrKey]*Pointer{},
		borrows:   map[Type]*Borrow{},
		slices:    map[Type]*Slice{},
		arrays:    map[arrKey]*Array{},
		building:  map[*Struct]bool{},
		breaks:    map[*ast.ForStmt]bool{},

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
		c.collectConsts(src.AST) // before the structs: [N]T uses constants
		c.collectEnumNames(src.AST)
		c.collectInterfaces(src.AST) // before the structs: a generic struct can use an interface of the file as a constraint
		c.collectStructs(src.AST)
		c.collectEnumVariants(src.AST)
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
	// Structs with owners used inside an owned T need free.
	c.checkFreeNeeds()
	// Body of the generic instances created by the uses.
	c.checkInstances()
	// Ownership is only checked in a program without type errors: it depends
	// on the resolved types.
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
	if len(c.errs) == 0 {
		c.checkStacks()
	}
	// Errors in file order and, inside each one, in code order.
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

// popScope closes the current scope and reports variables declared and never read.
func (c *checker) popScope() {
	for _, l := range c.scope.order {
		if !l.Used && !l.Param {
			c.errorf(l.Pos, "%s declared and not used", l.Name)
		}
	}
	c.scope = c.scope.parent
}

// declare creates a variable in the current scope. The C name gets a different
// prefix on each redeclaration in the function (l_x, l2_x, l3_x...), so that an
// inner variable never hides the outer one in the generated C: in "x := x + 1"
// inside a block, the x on the right is the outer one.
func (c *checker) declare(name string, pos token.Pos, t Type, param bool) *Local {
	c.cnames[name]++
	cname := "l_" + name
	if n := c.cnames[name]; n > 1 {
		cname = fmt.Sprintf("l%d_%s", n, name)
	}
	l := &Local{Name: name, CName: cname, Type: t, Pos: pos, Param: param}
	if predeclared[name] {
		c.errorf(pos, "the name %q is reserved", name)
		l.Used = true
		return l
	}
	if c.mod.uses[name] != nil {
		c.errorf(pos, "the name %s is an imported module", name)
		l.Used = true
		return l
	}
	if c.mod.enums[name] != nil {
		c.errorf(pos, "the name %s is already a type (enum)", name)
		l.Used = true
		return l
	}
	if c.mod.structs[name] != nil {
		c.errorf(pos, "the name %s is already a type (struct)", name)
		l.Used = true
		return l
	}
	if prev, ok := c.scope.names[name]; ok {
		// The original declaration still applies; the repeated one only produces this error.
		c.errorf(pos, "%s already declared in this scope (line %d)", name, prev.Pos.Line)
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
		msg += " (checked with an owning T, like " + c.instName + ")"
	case c.instName != "":
		msg += " (in the instance " + c.instName + ")"
	}
	c.errs = append(c.errs, token.Error{File: c.mod.file, Pos: pos, Msg: msg})
}

// ---------- Declarations ----------

func (c *checker) collectFuncs(f *ast.File) {
	for _, d := range f.Funcs {
		if d.Recv != nil {
			c.collectMethod(d)
			continue
		}
		if predeclared[d.Name] {
			c.errorf(d.P, "the name %q is reserved", d.Name)
			continue
		}
		if c.mod.structs[d.Name] != nil || c.mod.global.names[d.Name] != nil {
			c.errorf(d.P, "the name %s is already used by a struct or constant", d.Name)
			continue
		}
		if prev, ok := c.mod.generics[d.Name]; ok {
			c.errorf(d.P, "function %s already declared on line %d", d.Name, prev.Decl.P.Line)
			continue
		}
		if prev, ok := c.mod.funcs[d.Name]; ok {
			c.errorf(d.P, "function %s already declared on line %d", d.Name, prev.Decl.P.Line)
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
			c.collectExternC(fn, d) // extern "C" fn: a real C function
			continue
		}
		if d.Extern {
			// implemented in the C runtime: mlc_std_<module>_<name>, without a body
			if !c.mod.std {
				c.errorf(d.P, "extern fn is only allowed in the standard library; to call a C function, declare extern \"C\" fn")
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
		c.errorf(main.Decl.P, "fn main can only exist in the main file; %s is imported", c.mod.file)
	case !c.entry():
	case main == nil:
		c.errorf(token.Pos{Line: 1, Col: 1}, "function main not declared")
	case len(main.Params) > 0 || main.Result != Void:
		c.errorf(main.Decl.P, "main takes no parameters and returns no value")
	default:
		c.info.Main = main
	}
}

// collectTests gathers the test_* functions of a test file: mlc test
// generates the main that runs each one in a separate process.
func (c *checker) collectTests(f *ast.File, main *Func) {
	if main != nil {
		c.errorf(main.Decl.P, "a test file doesn't declare fn main: mlc test generates the main that runs the tests")
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
			c.errorf(d.P, "the test %s can't be generic", d.Name)
			continue
		}
		fn := c.mod.funcs[d.Name]
		if fn == nil || fn.Decl != d {
			continue // repeated or reserved name: it already gave an error
		}
		if len(fn.Params) > 0 || fn.Result != Void {
			c.errorf(d.P, "the test %s takes no parameters and returns no value", d.Name)
			continue
		}
		c.info.Tests = append(c.info.Tests, fn)
	}
	if len(c.info.Tests) == 0 && len(c.info.Benches) == 0 && len(c.errs) == 0 {
		c.errorf(token.Pos{Line: 1, Col: 1}, "test file without tests: declare functions fn test_name() (or fn bench_name(b &testing.B))")
	}
}

// collectBench registers a benchmark: fn bench_name(b &testing.B), which
// repeats the measured section b.n times.
func (c *checker) collectBench(d *ast.FuncDecl) {
	if len(d.TypeParams) > 0 {
		c.errorf(d.P, "the benchmark %s can't be generic", d.Name)
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
		c.errorf(d.P, "the benchmark %s takes b &testing.B and returns no value: fn %s(b &testing.B)", d.Name, d.Name)
		return
	}
	c.info.Benches = append(c.info.Benches, fn)
}

// ---------- Statements ----------

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
	// The body shares the parameters' scope, as in Go.
	for _, s := range fn.Decl.Body.Stmts {
		c.checkStmt(s)
	}
	c.popScope()
	if fn.Result != Void && !c.terminates(fn.Decl.Body) {
		c.errorf(fn.Decl.Body.End, "function %s must end with return", fn.Name)
	}
}

// terminates says whether execution never goes past the end of the statement: return, if/else
// with both sides terminating, or "for { }" without break.
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
		// Every match the checker accepts covers every value, so it
		// terminates if every arm terminates.
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
			// The value is checked before declaring: in "x := x + 1" the x on the
			// right is the outer one.
			u, isAmp := s.Value.(*ast.UnaryExpr)
			isAmp = isAmp && u.Op == token.AMP
			switch b, isB := t.(*Borrow); {
			case isB:
				c.borrowExpr(s.Value, b, "declaration of "+s.Name)
			case t == nil && isAmp:
				t = c.borrowExpr(s.Value, nil, "declaration of "+s.Name) // x := &a.field
			case t == nil:
				t = c.valueDefault(s.Value) // x := 5 is int; x := 2.5 is float64
			default:
				c.valueAs(s.Value, t, "declaration of "+s.Name)
			}
		}
		switch tt := t.(type) {
		case *Borrow:
			if s.Value == nil {
				c.errorf(s.P, "var %s %s needs an initial value: a borrow always points to something", s.Name, tt)
				t = Invalid
			}
		case *Pointer:
			if s.Value == nil && !tt.Nullable {
				c.errorf(s.P, "var %s %s needs an initial value: a *T pointer is never nil (use ?*%s to start with nil)", s.Name, tt, tt.Elem)
			}
		}
		if t == NilType {
			c.errorf(s.P, "can't infer the type of nil: declare it with var %s ?*T = nil", s.Name)
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
		if sel, ok := s.Lhs.(*ast.SelectorExpr); ok && isCStruct(c.structBase(sel.X)) {
			// a C struct's callback field takes a function's name; an anonymous struct only its fields
			if ft, ok := lt.(*FuncType); ok && s.Op == token.ASSIGN {
				c.callbackArg(s.Rhs, ft, "field "+sel.Sel)
				return
			}
			if isAnonCStruct(lt) {
				c.value(s.Rhs)
				c.errorf(sel.SelPos, "%s is an anonymous C struct: assign its fields (%s.%s.field = ...)", sel.Sel, exprName(sel.X), sel.Sel)
				return
			}
		}
		if s.Op == token.ASSIGN {
			c.noCopy(lt, s.OpPos)
			c.valueAs(s.Rhs, lt, "assignment")
			return
		}
		if lt == String && s.Op == token.ADD_ASSIGN {
			c.value(s.Rhs)
			c.errorf(s.OpPos, "string concatenation requires explicit allocation: build the text in a buffer (see docs/text.md)")
			return
		}
		rt := c.value(s.Rhs)
		op := compoundOps[s.Op]
		if op == token.SHL || op == token.SHR {
			// x <<= n: n can be of any integer type
			switch {
			case lt == Invalid || rt == Invalid:
			case !intLike(lt):
				c.errorf(s.OpPos, "operator %s only applies to integers, not to %s", s.Op, lt)
			case !intLike(rt):
				c.errorf(s.OpPos, "the number of bits of %s must be an integer, not %s", s.Op, rt)
			default:
				if v, ok := c.info.Values[s.Rhs]; ok && (v.(*big.Int).Sign() < 0 || isInteger(lt) && v.(*big.Int).Cmp(big.NewInt(int64(IntBits(lt)))) >= 0) {
					c.errorf(s.Rhs.Pos(), "shift of %s bits on a %s (use 0 to %d)", v, lt, IntBits(lt)-1)
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
			c.errorf(s.OpPos, "operator %s doesn't apply to %s and %s", s.Op, lt, rt)
		case s.Op == token.REM_ASSIGN && (isFloat(lt) || (abstract(lt) && !tvHas(lt, "mod"))):
			c.errorf(s.OpPos, "operator %%= doesn't apply to %s (only to integers)", lt)
		case isBitOp(op) && !intLike(lt):
			c.errorf(s.OpPos, "operator %s only applies to integers, not to %s", s.Op, lt)
		}
	case *ast.IncDecStmt:
		if t := c.target(s.X); !isInteger(t) && t != Invalid {
			c.errorf(s.OpPos, "operator %s doesn't apply to %s", s.Op, t)
		}
	case *ast.IfStmt:
		c.condition(s.Cond, "if")
		// Whether x can be nil at each point is decided by the ownership checker,
		// which follows the flow (ownership.go).
		c.checkBlock(s.Then)
		if s.Else != nil {
			c.checkStmt(s.Else)
		}
	case *ast.ForStmt:
		if s.Var != nil {
			c.checkForIn(s)
			return
		}
		// The for's scope wraps the init: "for i := 0; ..." declares i only for the loop.
		c.pushScope()
		if s.Init != nil {
			c.checkStmt(s.Init)
		}
		if s.Cond != nil {
			c.condition(s.Cond, "for")
		}
		if s.Post != nil {
			if _, ok := s.Post.(*ast.VarDecl); ok {
				c.errorf(s.Post.Pos(), "declaration not allowed in the post statement of for")
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
			c.errorf(s.P, "%s outside a for", s.Tok)
		} else if s.Tok == token.BREAK {
			c.breaks[c.loops[len(c.loops)-1]] = true
		}
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		if !ok {
			c.errorf(s.X.Pos(), "expression computed but not used")
			c.expr(s.X)
			return
		}
		c.expr(call)
	case *ast.ReturnStmt:
		if s.Value == nil {
			if c.fn.Result != Void {
				c.errorf(s.P, "return without a value in a function that returns %s", c.fn.Result)
			}
			return
		}
		if c.fn.Result == Void {
			c.value(s.Value)
			c.errorf(s.Value.Pos(), "function %s doesn't return a value", c.fn.Name)
			return
		}
		if b, ok := c.fn.Result.(*Borrow); ok {
			c.checkBorrowReturn(s, b) // return &v.items[i]: where the borrow comes from
			return
		}
		c.valueAs(s.Value, c.fn.Result, "return")
	case *ast.Block:
		c.checkBlock(s)
	default:
		panic(fmt.Sprintf("checker: unknown statement %T", s))
	}
}

// checkMatch validates a match: each pattern's type, repeated patterns,
// unreachable arms and coverage of every value.
func (c *checker) checkMatch(s *ast.MatchStmt) {
	t := c.valueDefault(s.Subject)
	if b, ok := t.(*Borrow); ok {
		if en, ok := b.Elem.(*Enum); ok {
			c.checkEnumMatch(s, en) // match e, with e &Enum: only reads
			return
		}
	}
	if en, ok := t.(*Enum); ok {
		c.checkEnumMatch(s, en)
		return
	}
	if !isInteger(t) && t != Bool && t != String && t != Invalid {
		c.errorf(s.Subject.Pos(), "match doesn't apply to %s (only integers, bool and string)", t)
		t = Invalid
	}
	seen := map[string]token.Pos{}
	var wildcard *ast.Pattern
	unreachable := false
	for _, arm := range s.Arms {
		if wildcard != nil && !unreachable {
			c.errorf(arm.P, "an arm after _ is never executed")
			unreachable = true
		}
		for _, p := range arm.Patterns {
			if p.Wildcard {
				if len(arm.Patterns) > 1 {
					c.errorf(p.P, "_ must be alone in the arm")
				}
				wildcard = p
				continue
			}
			v, pt := c.patternValue(p, t)
			if pt == Invalid || v == nil {
				continue
			}
			if t != Invalid && pt != t {
				c.errorf(p.P, "pattern of type %s in a match on %s", pt, t)
				continue
			}
			key := fmt.Sprintf("%T:%v", v, v)
			if prev, dup := seen[key]; dup {
				c.errorf(p.P, "repeated pattern %s (already appears on line %d)", formatConst(v), prev.Line)
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
		c.errorf(wildcard.P, "the _ arm is never executed: true and false are already covered")
	case wildcard == nil && t == Bool && !coversBool:
		c.errorf(s.P, "a match on bool must cover true and false or have a _ arm")
	case wildcard == nil && t != Bool:
		c.errorf(s.P, "a match on %s needs a _ arm", t)
	}
}

// patternValue returns a pattern's constant value and its type.
// patternValue returns a pattern's constant value and its type. An
// untyped literal takes the type of the match's value (subject).
func (c *checker) patternValue(p *ast.Pattern, subject Type) (any, Type) {
	if p.Lit == nil { // a variant pattern (V(x)) outside a match on an enum
		if subject != Invalid {
			c.errorf(p.P, "variant pattern in a match on %s", subject)
		}
		return nil, Invalid
	}
	if _, ok := p.Lit.(*ast.Ident); ok && subject == Invalid {
		return nil, Invalid // the subject already gave an error; the name may be a variant
	}
	switch lit := p.Lit.(type) {
	case *ast.Ident:
		if l := c.scope.lookup(lit.Name); l != nil && !l.IsConst {
			c.errorf(p.P, "%s isn't a constant: a pattern must be a literal, a constant or _", lit.Name)
			return nil, Invalid
		}
		if l := c.scope.lookup(lit.Name); l != nil && l.CConst != "" {
			c.errorf(p.P, "%s is a C constant, whose value only C knows: compare it with == instead of a pattern", lit.Name)
			return nil, Invalid
		}
	case *ast.SelectorExpr:
		if c.userModule(lit.X) == nil {
			c.errorf(p.P, "a pattern must be a literal, a constant or _")
			return nil, Invalid
		}
	}
	t := c.value(p.Lit)
	if t == Invalid {
		return nil, Invalid
	}
	if p.Neg {
		// -5: the sign is part of the pattern
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
		c.errorf(p.P, "decimal patterns aren't accepted (comparing decimals for equality is fragile)")
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
		c.errorf(e.Pos(), "the condition of %s must be bool, found %s", context, t)
	}
}

// target checks the left side of an assignment: a variable or one of its
// fields (p.x, r.corner.x). Writing to a variable doesn't count as using it.
func (c *checker) target(e ast.Expr) Type {
	var t Type
	switch e := e.(type) {
	case *ast.SelectorExpr:
		base := c.targetBase(e.X)
		if _, isPtr := base.(*Pointer); isPtr || isBorrow(base) {
			// Writing through a pointer is using the pointer.
			c.markUsed(e.X)
		}
		t = c.field(base, e)
	case *ast.Ident:
		t = c.targetVar(e)
	case *ast.IndexExpr:
		t = c.targetIndex(e)
	case *ast.UnaryExpr:
		if e.Op != token.MUL {
			c.errorf(e.Pos(), "can't assign to this expression (only to variables, fields and *p)")
			return Invalid
		}
		t, _ = c.deref(c.value(e.X), e.X.Pos(), exprName(e.X))
		if !isPointerType(c.info.Types[e.X]) && t != Invalid {
			c.errorf(e.P, "operator * only applies to pointers and borrows, not to %s", t)
			t = Invalid
		}
	default:
		c.errorf(e.Pos(), "can't assign to this expression (only to variables, fields and *p)")
		return Invalid
	}
	c.info.Types[e] = t
	return t
}

// targetBase checks the base of an assigned field (p in p.x = 1). Unlike
// targetVar, the variable itself isn't reassigned, so borrows and the aliases
// of "if x != nil" are valid as a base.
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
		c.errorf(id.P, "can't assign to the constant %s", id.Name)
		return Invalid
	case l != nil && isBorrow(l.Type):
		c.errorf(id.P, "the borrow %s can't be reassigned", id.Name)
		return Invalid
	case l != nil:
		c.info.Locals[id] = l
		return l.Type
	case c.mod.funcs[id.Name] != nil || c.mod.structs[id.Name] != nil || predeclared[id.Name]:
		c.errorf(id.P, "can't assign to %s", id.Name)
	default:
		c.errorf(id.P, "undeclared identifier: %s", id.Name)
	}
	return Invalid
}

// ---------- Expressions ----------

// value checks an expression that must produce a value.
func (c *checker) value(e ast.Expr) Type {
	t := c.expr(e)
	if t == Void {
		c.errorf(e.Pos(), "the call produces no value")
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
			c.errorf(e.P, "decimal constant %s out of the range of float64", e.Value)
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
		// f[int] as a value: the instance of a generic function
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
		c.errorf(e.Pos(), "type arguments only apply to generic functions and types")
		return Invalid
	case *ast.TypeArg:
		c.errorf(e.Pos(), "a type can only be an argument of alloc")
		return Invalid
	case *ast.SliceExpr:
		c.errorf(e.Lbrack, "a sub-slice can only be borrowed as a call argument: f(&s[a:b])")
		c.subSlice(e)
		return Invalid
	}
	panic(fmt.Sprintf("checker: unknown expression %T", e))
}

func (c *checker) ident(e *ast.Ident) Type {
	if l := c.scope.lookup(e.Name); l != nil {
		l.Used = true
		if c.cConstUse(e, l) {
			return l.Type
		}
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
	case "alloc", "free", "len", "realloc", "take", "replace", "keep":
		c.errorf(e.P, "%s must be called: %s(...)", e.Name, e.Name)
		return Invalid
	}
	switch {
	case c.mod.pendingConsts[e.Name]:
		c.errorf(e.P, "constant %s used before being declared", e.Name)
	case c.mod.funcs[e.Name] != nil:
		return c.funcRef(e, c.mod.funcs[e.Name])
	case c.mod.generics[e.Name] != nil:
		c.errorf(e.P, "%s is generic: to use it as a value, write the types, like %s[int]", e.Name, e.Name)
	case c.typeParams[e.Name] != nil:
		c.errorf(e.P, "type %s used as a value (to convert: %s(x))", e.Name, e.Name)
	case c.mod.uses[e.Name] != nil:
		c.errorf(e.P, "module %s used as a value", e.Name)
	case basicTypes[e.Name] != nil:
		c.errorf(e.P, "type %s used as a value", e.Name)
	case c.mod.enums[e.Name] != nil:
		c.errorf(e.P, "type %s used as a value (to create a value: %s.Variant)", e.Name, e.Name)
	case c.mod.structs[e.Name] != nil:
		c.errorf(e.P, "type %s used as a value (to create a value: %s{...}; in the header of if, for or match, in parentheses)", e.Name, e.Name)
	case StdModule(e.Name):
		c.errorf(e.P, "module %s wasn't imported (add: import \"std/%s\")", e.Name, e.Name)
	default:
		c.errorf(e.P, "undeclared identifier: %s", e.Name)
	}
	return Invalid
}

func (c *checker) call(e *ast.CallExpr) Type {
	switch fun := e.Fun.(type) {
	case *ast.SelectorExpr:
		if en := c.enumNamed(fun.X); en != nil {
			return c.enumCtor(e, e, en, fun) // Shape.Circle(...)
		}
		if g := c.genericFuncOf(fun); g != nil {
			return c.genericCall(e, g, nil) // mod.f(args), generic
		}
		if m := c.userModule(fun.X); m != nil {
			if kind := ioBuiltin(m, fun.Sel); kind != "" {
				return c.ioPrint(e, kind)
			}
			if m.std && m.path == "std/mutex" && fun.Sel == "new" {
				return c.mutexNew(e, nil)
			}
			if m.std && m.path == "std/chan" && fun.Sel == "new" {
				c.errorf(fun.SelPos, "chan.new needs the type of the values: chan.new[int](16)")
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
			return c.conversion(e, tp) // T(x), with T a type parameter
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
				return c.indirectCall(e, ft) // f(x), with f an fn variable
			}
		}
		fn := c.mod.funcs[fun.Name]
		if fn == nil || c.scope.lookup(fun.Name) != nil {
			break
		}
		return c.userCall(e, fn)
	case *ast.IndexExpr, *ast.IndexListExpr:
		// f[int](args): explicit type arguments
		base, targs := splitIndex(fun)
		if sel, ok := base.(*ast.SelectorExpr); ok && c.userModule(sel.X) == nil {
			// x.m[T](args): a generic method with the types written
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
			return c.indirectCall(e, ft) // fs[i](x), get()(x)
		}
		c.errorf(e.Lparen, "a value of type %s isn't a function", t)
	}
	c.checkArgs(e)
	return Invalid
}

// checkArgs checks the arguments of an invalid call, to report
// errors inside them too.
func (c *checker) checkArgs(e *ast.CallExpr) {
	for _, a := range e.Args {
		c.peekType(a) // accepts &x and &s[a:b], as in a valid call
	}
}

// userCall checks a call to a user or native function.
func (c *checker) userCall(e *ast.CallExpr, fn *Func) Type {
	c.callArgs(e, fn, fn.Params)
	c.info.Calls[e] = Callee{Func: fn}
	return fn.Result
}

// ParseIntLit reads an integer literal: decimal (with _ between the digits) or
// with the prefix 0x, 0b, 0o. A leading zero stays decimal (010 is ten).
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
