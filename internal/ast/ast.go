// Package ast define a árvore sintática produzida pelo parser.
package ast

import "mlc/internal/token"

type Node interface {
	Pos() token.Pos
}

type File struct {
	Imports    []*Import
	Structs    []*StructDecl
	Enums      []*EnumDecl
	Consts     []*ConstDecl // na ordem do arquivo
	Funcs      []*FuncDecl
	Interfaces []*InterfaceDecl
	Impls      []*ImplDecl
	Links      []*LinkDecl // link "sqlite3": bibliotecas de C a ligar
}

// LinkDecl é "link \"sqlite3\"": a biblioteca de C que o arquivo usa (vira
// -lsqlite3 na ligação).
type LinkDecl struct {
	Lib string
	P   token.Pos
}

func (n *LinkDecl) Pos() token.Pos { return n.P }

// InterfaceDecl é "interface Display { fn write(self &Self, w &Writer) }":
// assinaturas de métodos que um tipo implementa com impl. Nesta versão, uma
// interface só serve como restrição de genérico.
type InterfaceDecl struct {
	Name    string
	Pub     bool
	Methods []*FuncDecl // sem corpo; o primeiro parâmetro é o receptor (self &Self)
	P       token.Pos
}

// ImplDecl declara que um tipo implementa uma interface:
//
//	impl Display for Ponto                       (com os métodos da struct)
//	impl Display for int { fn (x &int) write(...) {...} }
//	impl[T Display] Display for []T { ... }       (genérico)
//	impl auto Display for Ponto                  (write gerado)
type ImplDecl struct {
	TypeParams []*TypeParam
	Auto       bool
	Iface      *TypeExpr // a interface (Display ou io.Display)
	Type       *TypeExpr
	Methods    []*FuncDecl // forma com bloco; nil na forma de uma linha
	Block      bool        // tem bloco { } (mesmo vazio)
	P          token.Pos
}

func (n *InterfaceDecl) Pos() token.Pos { return n.P }
func (n *ImplDecl) Pos() token.Pos      { return n.P }

type StructDecl struct {
	Extern     bool // extern "C" type Banco: tipo opaco de C, só atrás de ponteiro
	Name       string
	TypeParams []*TypeParam // struct Par[A any, B any] { ... }
	Pub        bool
	Fields     []*FieldDecl
	P          token.Pos
}

type FieldDecl struct {
	Name string
	Type *TypeExpr
	P    token.Pos
}

// ConstDecl é "const N T = v", no topo do arquivo ou dentro de um bloco.
type ConstDecl struct {
	Name  string
	Pub   bool
	Type  *TypeExpr // opcional
	Value Expr
	P     token.Pos // posição do nome
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
	Extern     bool // extern fn: implementada no runtime C (só na biblioteca padrão), sem corpo
	ExternC    bool // extern "C" fn: uma função de C de verdade, chamada pelo nome
	Name       string
	TypeParams []*TypeParam // fn maior[T ordered](...)
	Pub        bool         // visível para quem importa o arquivo
	Recv       *Param       // receptor de um método: fn (p &Ponto) area(); nil numa função
	Params     []*Param
	Result     *TypeExpr // nil quando a função não retorna valor
	Body       *Block
	P          token.Pos
}

type Param struct {
	Name string
	Type *TypeExpr
	P    token.Pos
}

// TypeExpr é um tipo escrito no código: um nome (int, Ponto) ou um
// modificador aplicado a Elem: []T, *T, ?*T ou &T.
type TypeExpr struct {
	Name   string
	Module string // qualificador: "geometria" em geometria.Ponto
	Mod    TypeMod
	Elem   *TypeExpr   // não nil quando Mod != TypeNamed
	Len    Expr        // tamanho de [N]T
	Args   []*TypeExpr // argumentos de tipo: Par[int, string]
	Params []*TypeExpr // fn(int, &T) R: os parâmetros; o retorno fica em Elem (nil: nenhum)
	// Constraint: restrição escrita no receptor de um método, como o any de
	// fn (v &Vec[T any]) get(): o método só existe quando T a atende
	Constraint string
	P          token.Pos
}

type TypeMod int

const (
	TypeNamed   TypeMod = iota
	TypeSlice           // []T
	TypePointer         // *T
	TypeNullPtr         // ?*T
	TypeBorrow          // &T
	TypeArray           // [N]T
	TypeFunc            // fn(T, U) R
)

func (n *Import) Pos() token.Pos   { return n.P }
func (n *FuncDecl) Pos() token.Pos { return n.P }
func (n *Param) Pos() token.Pos    { return n.P }
func (n *TypeExpr) Pos() token.Pos { return n.P }

// ---------- Comandos ----------

type Stmt interface {
	Node
	stmtNode()
}

type Block struct {
	Stmts []Stmt
	P     token.Pos
	End   token.Pos // posição do '}'
}

type ExprStmt struct {
	X Expr
}

type ReturnStmt struct {
	Value Expr // nil em "return" sem valor
	P     token.Pos
}

// VarDecl é "var x T = v" ou "x := v" (Short). Type ou Value pode ser nil,
// mas não os dois.
type VarDecl struct {
	Name  string
	Type  *TypeExpr
	Value Expr
	Short bool
	P     token.Pos // posição do nome
}

// AssignStmt é "x = v" ou uma atribuição composta como "x += v".
type AssignStmt struct {
	Op    token.Kind // ASSIGN, ADD_ASSIGN, ...
	Lhs   Expr
	Rhs   Expr
	OpPos token.Pos
}

type IncDecStmt struct {
	Op    token.Kind // INC ou DEC
	X     Expr
	OpPos token.Pos
}

type IfStmt struct {
	Cond Expr
	Then *Block
	Else Stmt // nil, *IfStmt ou *Block
	P    token.Pos
}

// ForStmt cobre as quatro formas: "for { }" (tudo nil), "for cond { }" (só
// Cond), "for init; cond; post { }" e "for x in s { }" (Var e Iter).
type ForStmt struct {
	Init Stmt
	Cond Expr
	Post Stmt
	Var  *Ident // for x in s
	Iter Expr
	Body *Block
	P    token.Pos
}

// MatchStmt é "match x { padrões => corpo ... }". Só um braço executa.
type MatchStmt struct {
	Subject Expr
	Arms    []*MatchArm
	P       token.Pos
}

type MatchArm struct {
	Patterns []*Pattern
	Body     Stmt // comando simples, *Block, *BranchStmt ou *ReturnStmt
	P        token.Pos
}

// Pattern é um literal (Lit: *IntLit, *StringLit, *FloatLit ou *Ident
// true/false) ou o curinga _. Neg marca um inteiro negativo, como em -1.
type Pattern struct {
	// Variante de enum: Circulo(r, _). Binds tem um nome por campo (_ ignora).
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

// BranchStmt é break ou continue.
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

// ---------- Expressões ----------

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

// StringLit guarda o valor já sem escapes.
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

// CompositeLit é "Ponto{x: 1, y: 2}". Fields vazio significa o valor zero.
type CompositeLit struct {
	Type     *Ident
	Module   *Ident // qualificador: geometria em geometria.Ponto{...}
	TypeArgs []Expr // Par[int, string]{...}
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

// TypeArg é um tipo usado como argumento: o []int de alloc([]int, n).
type TypeArg struct {
	Type *TypeExpr
}

func (n *TypeArg) Pos() token.Pos { return n.Type.P }

func (*TypeArg) exprNode() {}

// SliceExpr é s[a:b]. Low e High podem ser nil (s[:b], s[a:], s[:]).
type SliceExpr struct {
	X, Low, High Expr
	Lbrack       token.Pos
}

func (n *SliceExpr) Pos() token.Pos { return n.X.Pos() }

func (*SliceExpr) exprNode() {}

// CharLit é um literal de byte: 'a'.
type CharLit struct {
	Value byte
	P     token.Pos
}

func (n *CharLit) Pos() token.Pos { return n.P }

func (*CharLit) exprNode() {}

// ArrayLit é "[3]int{1, 2, 3}". Elems vazio significa o valor zero.
type ArrayLit struct {
	Type   *TypeExpr
	Elems  []Expr
	Lbrace token.Pos
}

func (n *ArrayLit) Pos() token.Pos { return n.Type.P }

func (*ArrayLit) exprNode() {}

// TypeParam é um parâmetro de tipo: o T de fn maior[T ordered](...).
type TypeParam struct {
	Name       string
	Constraint string // any, comparable, ordered, number, integer
	P          token.Pos
}

// IndexListExpr é f[A, B]: argumentos de tipo de uma função ou struct genérica.
// Com um argumento só, f[int] é um IndexExpr; o checker decide se é índice ou
// instância.
type IndexListExpr struct {
	X       Expr
	Indices []Expr
	Lbrack  token.Pos
}

func (n *IndexListExpr) Pos() token.Pos { return n.X.Pos() }

func (*IndexListExpr) exprNode() {}

// EnumDecl é "enum Forma { Circulo(raio float64); Vazio }".
type EnumDecl struct {
	Name       string
	Pub        bool
	TypeParams []*TypeParam
	Variants   []*VariantDecl
	P          token.Pos
}

// VariantDecl é uma variante de enum, com campos opcionais: Circulo(raio float64).
type VariantDecl struct {
	Name   string
	Fields []*FieldDecl
	P      token.Pos
}

func (n *EnumDecl) Pos() token.Pos { return n.P }
