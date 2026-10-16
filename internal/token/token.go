// Package token define os tokens da linguagem MLC, posições no código-fonte
// e o tipo de erro de compilação compartilhado pelas fases do compilador.
package token

import "fmt"

type Kind int

const (
	EOF Kind = iota
	ILLEGAL
	IDENT
	INT
	FLOAT
	STRING
	CHAR // literal de byte: 'a'

	ADD        // +
	SUB        // -
	MUL        // *
	QUO        // /
	REM        // %
	ASSIGN     // =
	ADD_ASSIGN // +=
	SUB_ASSIGN // -=
	MUL_ASSIGN // *=
	QUO_ASSIGN // /=
	REM_ASSIGN // %=
	INC        // ++
	DEC        // --
	EQL        // ==
	NEQ        // !=
	LSS        // <
	LEQ        // <=
	GTR        // >
	GEQ        // >=
	LAND       // &&
	LOR        // ||
	NOT        // !
	DEFINE     // :=
	ARROW      // =>
	LPAREN     // (
	RPAREN     // )
	LBRACK     // [
	RBRACK     // ]
	LBRACE     // {
	RBRACE     // }
	COMMA      // ,
	SEMICOLON  // ; (explícito ou inserido no fim da linha)
	PERIOD     // .
	COLON      // :
	AMP        // & (empréstimo)
	QUESTION   // ? (em ?*T)

	// bits e aritmética que dá a volta
	OR              // |
	XOR             // ^ (binário: ou exclusivo; unário: inverte os bits)
	SHL             // <<
	SHR             // >>
	AND_ASSIGN      // &=
	OR_ASSIGN       // |=
	XOR_ASSIGN      // ^=
	SHL_ASSIGN      // <<=
	SHR_ASSIGN      // >>=
	ADD_WRAP        // +%
	SUB_WRAP        // -%
	MUL_WRAP        // *%
	ADD_WRAP_ASSIGN // +%=
	SUB_WRAP_ASSIGN // -%=
	MUL_WRAP_ASSIGN // *%=
	COMMENT         // // até o fim da linha (só no modo com comentários do lexer)

	keywordBeg
	IMPORT
	FN
	STRUCT
	CONST
	VAR
	IF
	ELSE
	MATCH
	FOR
	IN
	RETURN
	BREAK
	CONTINUE
	PUB
	ENUM
	EXTERN
	INTERFACE
	IMPL
	keywordEnd
)

var names = [...]string{
	EOF:     "EOF",
	ILLEGAL: "ILLEGAL",
	IDENT:   "IDENT",
	INT:     "INT",
	FLOAT:   "FLOAT",
	STRING:  "STRING",
	CHAR:    "CHAR",

	ADD:        "+",
	SUB:        "-",
	MUL:        "*",
	QUO:        "/",
	REM:        "%",
	ASSIGN:     "=",
	ADD_ASSIGN: "+=",
	SUB_ASSIGN: "-=",
	MUL_ASSIGN: "*=",
	QUO_ASSIGN: "/=",
	REM_ASSIGN: "%=",
	INC:        "++",
	DEC:        "--",
	EQL:        "==",
	NEQ:        "!=",
	LSS:        "<",
	LEQ:        "<=",
	GTR:        ">",
	GEQ:        ">=",
	LAND:       "&&",
	LOR:        "||",
	NOT:        "!",
	DEFINE:     ":=",
	ARROW:      "=>",
	LPAREN:     "(",
	RPAREN:     ")",
	LBRACK:     "[",
	RBRACK:     "]",
	LBRACE:     "{",
	RBRACE:     "}",
	COMMA:      ",",
	SEMICOLON:  ";",
	PERIOD:     ".",
	COLON:      ":",
	AMP:        "&",
	QUESTION:   "?",

	OR:              "|",
	XOR:             "^",
	SHL:             "<<",
	SHR:             ">>",
	AND_ASSIGN:      "&=",
	OR_ASSIGN:       "|=",
	XOR_ASSIGN:      "^=",
	SHL_ASSIGN:      "<<=",
	SHR_ASSIGN:      ">>=",
	ADD_WRAP:        "+%",
	SUB_WRAP:        "-%",
	MUL_WRAP:        "*%",
	ADD_WRAP_ASSIGN: "+%=",
	SUB_WRAP_ASSIGN: "-%=",
	MUL_WRAP_ASSIGN: "*%=",
	COMMENT:         "comentário",

	IMPORT:    "import",
	FN:        "fn",
	STRUCT:    "struct",
	CONST:     "const",
	VAR:       "var",
	IF:        "if",
	ELSE:      "else",
	MATCH:     "match",
	FOR:       "for",
	IN:        "in",
	RETURN:    "return",
	BREAK:     "break",
	CONTINUE:  "continue",
	PUB:       "pub",
	ENUM:      "enum",
	EXTERN:    "extern",
	INTERFACE: "interface",
	IMPL:      "impl",
}

func (k Kind) String() string {
	if k >= 0 && int(k) < len(names) && names[k] != "" {
		return names[k]
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

var keywords = func() map[string]Kind {
	m := make(map[string]Kind, keywordEnd-keywordBeg)
	for k := keywordBeg + 1; k < keywordEnd; k++ {
		m[names[k]] = k
	}
	return m
}()

// Lookup devolve a palavra-chave correspondente a ident, ou IDENT.
func Lookup(ident string) Kind {
	if k, ok := keywords[ident]; ok {
		return k
	}
	return IDENT
}

type Pos struct {
	Line int
	Col  int
}

func (p Pos) Before(q Pos) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Col < q.Col)
}

type Token struct {
	Kind Kind
	Lit  string
	Pos  Pos
	// Off e End delimitam o texto do token no arquivo (src[Off:End]), como
	// foi escrito: o formatador usa isso para preservar literais.
	Off, End int
}

// Error é um erro de compilação com posição no arquivo-fonte.
type Error struct {
	File string
	Pos  Pos
	Msg  string
}

func (e Error) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Pos.Line, e.Pos.Col, e.Msg)
}
