// Package ast defines the syntax tree produced by the parser.
package ast

import "mlc/internal/token"

type Node interface {
	Pos() token.Pos
}

type File struct {
	Imports    []*Import
	Structs    []*StructDecl
	Enums      []*EnumDecl
	Consts     []*ConstDecl // in file order
	Funcs      []*FuncDecl
	Interfaces []*InterfaceDecl
	Impls      []*ImplDecl
	Links      []*LinkDecl // link "sqlite3": C libraries to link
}

// LinkDecl is "link \"sqlite3\"": the C library the file uses (it becomes
// -lsqlite3 when linking).
type LinkDecl struct {
	Lib string
	P   token.Pos
}

func (n *LinkDecl) Pos() token.Pos { return n.P }

// InterfaceDecl is "interface Display { fn write(self &Self, w &Writer) }":
// method signatures a type implements with impl. In this version, an
// interface only serves as a generic constraint.
type InterfaceDecl struct {
	Name    string
	Pub     bool
	Methods []*FuncDecl // no body; the first parameter is the receiver (self &Self)
	P       token.Pos
}

// ImplDecl declares that a type implements an interface:
//
//	impl Display for Point                       (with the struct's methods)
//	impl Display for int { fn (x &int) write(...) {...} }
//	impl[T Display] Display for []T { ... }       (generic)
//	impl auto Display for Point                  (write generated)
type ImplDecl struct {
	TypeParams []*TypeParam
	Auto       bool
	Iface      *TypeExpr // the interface (Display or io.Display)
	Type       *TypeExpr
	Methods    []*FuncDecl // the form with a block; nil in the one-line form
	Block      bool        // has a block { } (even empty)
	P          token.Pos
}

func (n *InterfaceDecl) Pos() token.Pos { return n.P }
func (n *ImplDecl) Pos() token.Pos      { return n.P }

type StructDecl struct {
	Extern     bool // extern "C" type Db: an opaque C type, only behind a pointer
	Name       string
	TypeParams []*TypeParam // struct Pair[A any, B any] { ... }
	Pub        bool
	Fields     []*FieldDecl
	P          token.Pos
}

type FieldDecl struct {
	Name string
	Type *TypeExpr
	P    token.Pos
}

// ConstDecl is "const N T = v", at the top of the file or inside a block.
type ConstDecl struct {
	Name  string
	Pub   bool
	Type  *TypeExpr // optional
	Value Expr
	P     token.Pos // position of the name
}

func (n *StructDecl) Pos() token.Pos { return n.P }
func (n *FieldDecl) Pos() token.Pos  { return n.P }
func (n *ConstDecl) Pos() token.Pos  { return n.P }

func (*ConstDecl) stmtNode() {}

type Import struct {
	Path string
	P    token.Pos
}

type FuncDecl struct {
	Extern     bool // extern fn: implemented in the C runtime (only in the standard library), without a body
	ExternC    bool // extern "C" fn: a real C function, called by name
	Name       string
	TypeParams []*TypeParam // fn larger[T ordered](...)
	Pub        bool         // visible to whoever imports the file
	Recv       *Param       // a method's receiver: fn (p &Point) area(); nil in a function
	Params     []*Param
	Result     *TypeExpr // nil when the function returns no value
	Body       *Block
	P          token.Pos
}

type Param struct {
	Name string
	Type *TypeExpr
	// Kept: "kept" before the type, in an extern "C" fn: C keeps the
	// callback (fn type) or its context (*T) after the call returns
	Kept bool
	P    token.Pos
}

// TypeExpr is a type written in the code: a name (int, Point) or a
// modifier applied to Elem: []T, *T, ?*T or &T.
type TypeExpr struct {
	Name   string
	Module string // qualifier: "geometry" in geometry.Point
	Mod    TypeMod
	Elem   *TypeExpr   // not nil when Mod != TypeNamed
	Len    Expr        // size of [N]T
	Args   []*TypeExpr // type arguments: Pair[int, string]
	Params []*TypeExpr // fn(int, &T) R: the parameters; the result is in Elem (nil: none)
	// Constraint: a constraint written in a method's receiver, like the any of
	// fn (v &Vec[T any]) get(): the method only exists when T satisfies it
	Constraint string
	P          token.Pos
}

type TypeMod int

const (
	TypeNamed     TypeMod = iota
	TypeSlice             // []T
	TypePointer           // *T
	TypeNullPtr           // ?*T
	TypeBorrow            // &T
	TypeArray             // [N]T
	TypeFunc              // fn(T, U) R
	TypeOptBorrow         // ?&T (only in parameters of extern "C" fn)
)

func (n *Import) Pos() token.Pos   { return n.P }
func (n *FuncDecl) Pos() token.Pos { return n.P }
func (n *Param) Pos() token.Pos    { return n.P }
func (n *TypeExpr) Pos() token.Pos { return n.P }

// ---------- Statements ----------

type Stmt interface {
	Node
	stmtNode()
}

type Block struct {
	Stmts []Stmt
	P     token.Pos
	End   token.Pos // position of the '}'
}

type ExprStmt struct {
	X Expr
}

type ReturnStmt struct {
	Value Expr // nil in a "return" without a value
	P     token.Pos
}

// VarDecl is "var x T = v" or "x := v" (Short). Type or Value can be nil,
// but not both.
type VarDecl struct {
	Name  string
	Type  *TypeExpr
	Value Expr
	Short bool
	P     token.Pos // position of the name
}

// AssignStmt is "x = v" or a compound assignment like "x += v".
type AssignStmt struct {
	Op    token.Kind // ASSIGN, ADD_ASSIGN, ...
	Lhs   Expr
	Rhs   Expr
	OpPos token.Pos
}

type IncDecStmt struct {
	Op    token.Kind // INC or DEC
	X     Expr
	OpPos token.Pos
}

type IfStmt struct {
	Cond Expr
	Then *Block
	Else Stmt // nil, *IfStmt or *Block
	P    token.Pos
}

// ForStmt covers the four forms: "for { }" (all nil), "for cond { }" (only
// Cond), "for init; cond; post { }" and "for x in s { }" (Var and Iter).
type ForStmt struct {
	Init Stmt
	Cond Expr
	Post Stmt
	Var  *Ident // for x in s
	Iter Expr
	Body *Block
	P    token.Pos
}

// MatchStmt is "match x { patterns => body ... }". Only one arm runs.
type MatchStmt struct {
	Subject Expr
	Arms    []*MatchArm
	P       token.Pos
}

type MatchArm struct {
	Patterns []*Pattern
	Body     Stmt // a simple statement, *Block, *BranchStmt or *ReturnStmt
	P        token.Pos
}

// Pattern is a literal (Lit: *IntLit, *StringLit, *FloatLit or *Ident
// true/false) or the wildcard _. Neg marks a negative integer, as in -1.
type Pattern struct {
	// An enum variant: Circle(r, _). Binds has one name per field (_ ignores).
	Variant  *Ident
	Binds    []*Ident
	Wildcard bool
	Lit      Expr
	Neg      bool
	P        token.Pos
}

func (n *MatchStmt) Pos() token.Pos { return n.P }
func (n *MatchArm) Pos() token.Pos  { return n.P }
func (n *Pattern) Pos() token.Pos   { return n.P }

func (*MatchStmt) stmtNode() {}

// BranchStmt is break or continue.
type BranchStmt struct {
	Tok token.Kind
	P   token.Pos
}

func (n *Block) Pos() token.Pos      { return n.P }
func (n *ExprStmt) Pos() token.Pos   { return n.X.Pos() }
func (n *ReturnStmt) Pos() token.Pos { return n.P }
func (n *VarDecl) Pos() token.Pos    { return n.P }
func (n *AssignStmt) Pos() token.Pos { return n.Lhs.Pos() }
func (n *IncDecStmt) Pos() token.Pos { return n.X.Pos() }
func (n *IfStmt) Pos() token.Pos     { return n.P }
func (n *ForStmt) Pos() token.Pos    { return n.P }
func (n *BranchStmt) Pos() token.Pos { return n.P }

func (*Block) stmtNode()      {}
func (*ExprStmt) stmtNode()   {}
func (*ReturnStmt) stmtNode() {}
func (*VarDecl) stmtNode()    {}
func (*AssignStmt) stmtNode() {}
func (*IncDecStmt) stmtNode() {}
func (*IfStmt) stmtNode()     {}
func (*ForStmt) stmtNode()    {}
func (*BranchStmt) stmtNode() {}

// ---------- Expressions ----------

type Expr interface {
	Node
	exprNode()
}

type Ident struct {
	Name string
	P    token.Pos
}

type IntLit struct {
	Value string
	P     token.Pos
}

type FloatLit struct {
	Value string
	P     token.Pos
}

// StringLit keeps the value already without escapes.
type StringLit struct {
	Value string
	P     token.Pos
}

type UnaryExpr struct {
	Op token.Kind
	X  Expr
	P  token.Pos
}

type BinaryExpr struct {
	Op    token.Kind
	X, Y  Expr
	OpPos token.Pos
}

type CallExpr struct {
	Fun    Expr
	Args   []Expr
	Lparen token.Pos
}

type SelectorExpr struct {
	X      Expr
	Sel    string
	SelPos token.Pos
}

type IndexExpr struct {
	X, Index Expr
	Lbrack   token.Pos
}

func (n *Ident) Pos() token.Pos        { return n.P }
func (n *IntLit) Pos() token.Pos       { return n.P }
func (n *FloatLit) Pos() token.Pos     { return n.P }
func (n *StringLit) Pos() token.Pos    { return n.P }
func (n *UnaryExpr) Pos() token.Pos    { return n.P }
func (n *BinaryExpr) Pos() token.Pos   { return n.X.Pos() }
func (n *CallExpr) Pos() token.Pos     { return n.Fun.Pos() }
func (n *SelectorExpr) Pos() token.Pos { return n.X.Pos() }
func (n *IndexExpr) Pos() token.Pos    { return n.X.Pos() }

func (*Ident) exprNode()        {}
func (*IntLit) exprNode()       {}
func (*FloatLit) exprNode()     {}
func (*StringLit) exprNode()    {}
func (*UnaryExpr) exprNode()    {}
func (*BinaryExpr) exprNode()   {}
func (*CallExpr) exprNode()     {}
func (*SelectorExpr) exprNode() {}
func (*IndexExpr) exprNode()    {}

// CompositeLit is "Point{x: 1, y: 2}". Empty Fields means the zero value.
type CompositeLit struct {
	Type     *Ident
	Module   *Ident // qualifier: geometry in geometry.Point{...}
	TypeArgs []Expr // Pair[int, string]{...}
	Fields   []*FieldInit
	Lbrace   token.Pos
}

type FieldInit struct {
	Name  string
	Value Expr
	P     token.Pos
}

func (n *CompositeLit) Pos() token.Pos { return n.Type.P }

func (*CompositeLit) exprNode() {}

// TypeArg is a type used as an argument: the []int of alloc([]int, n).
type TypeArg struct {
	Type *TypeExpr
}

func (n *TypeArg) Pos() token.Pos { return n.Type.P }

func (*TypeArg) exprNode() {}

// SliceExpr is s[a:b]. Low and High can be nil (s[:b], s[a:], s[:]).
type SliceExpr struct {
	X, Low, High Expr
	Lbrack       token.Pos
}

func (n *SliceExpr) Pos() token.Pos { return n.X.Pos() }

func (*SliceExpr) exprNode() {}

// CharLit is a byte literal: 'a'.
type CharLit struct {
	Value byte
	P     token.Pos
}

func (n *CharLit) Pos() token.Pos { return n.P }

func (*CharLit) exprNode() {}

// ArrayLit is "[3]int{1, 2, 3}". Empty Elems means the zero value.
type ArrayLit struct {
	Type   *TypeExpr
	Elems  []Expr
	Lbrace token.Pos
}

func (n *ArrayLit) Pos() token.Pos { return n.Type.P }

func (*ArrayLit) exprNode() {}

// TypeParam is a type parameter: the T of fn larger[T ordered](...).
type TypeParam struct {
	Name       string
	Constraint string // any, comparable, ordered, number, integer
	P          token.Pos
}

// IndexListExpr is f[A, B]: type arguments of a generic function or struct.
// With a single argument, f[int] is an IndexExpr; the checker decides whether it's an index or
// an instance.
type IndexListExpr struct {
	X       Expr
	Indices []Expr
	Lbrack  token.Pos
}

func (n *IndexListExpr) Pos() token.Pos { return n.X.Pos() }

func (*IndexListExpr) exprNode() {}

// EnumDecl is "enum Shape { Circle(radius float64); Empty }".
type EnumDecl struct {
	Name       string
	Pub        bool
	TypeParams []*TypeParam
	Variants   []*VariantDecl
	P          token.Pos
}

// VariantDecl is an enum variant, with optional fields: Circle(radius float64).
type VariantDecl struct {
	Name   string
	Fields []*FieldDecl
	P      token.Pos
}

func (n *EnumDecl) Pos() token.Pos { return n.P }
