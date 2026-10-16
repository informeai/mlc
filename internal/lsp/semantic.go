package lsp

import (
	"path"
	"strings"
	"unicode"

	"mlc/internal/lexer"
	"mlc/internal/token"
)

// Semantic tokens: o editor colore cada token pelo tipo informado aqui. A
// classificação é feita sobre os tokens, sem checar o programa, para
// funcionar mesmo com o arquivo incompleto enquanto se digita.

var tokenTypes = []string{
	"keyword", "type", "function", "method", "property", "variable",
	"namespace", "enumMember", "string", "number", "comment",
}

var tokenModifiers = []string{"declaration", "readonly", "defaultLibrary"}

const (
	tKeyword = iota
	tType
	tFunction
	tMethod
	tProperty
	tVariable
	tNamespace
	tEnumMember
	tString
	tNumber
	tComment
)

const (
	mDeclaration = 1 << iota
	mReadonly
	mDefaultLibrary
)

var basicTypes = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint8": true, "uint16": true, "uint32": true, "uint64": true, "byte": true,
	"float32": true, "float64": true, "bool": true, "string": true,
	"any": true, "comparable": true, "ordered": true, "number": true, "integer": true,
}

var builtins = map[string]bool{
	"alloc": true, "free": true, "realloc": true, "len": true,
	"take": true, "replace": true, "drain": true,
}

// semanticTokens devolve os tokens de src no formato do LSP: para cada um,
// (linha, coluna) relativas ao anterior, tamanho, tipo e modificadores.
func semanticTokens(src []byte) []int {
	lx := lexer.NewWithComments("", src)
	var toks []token.Token
	for {
		t := lx.Next()
		if t.Kind == token.EOF {
			break
		}
		if t.Kind == token.SEMICOLON && t.Lit == "\n" {
			continue
		}
		toks = append(toks, t)
	}
	modules := map[string]bool{}
	for i, t := range toks {
		if t.Kind == token.IMPORT && i+1 < len(toks) && toks[i+1].Kind == token.STRING {
			modules[path.Base(toks[i+1].Lit)] = true
		}
	}
	lines := newLineIndex(src)
	data := []int{}
	prevLine, prevChar := 0, 0
	emit := func(t token.Token, typ, mods int) {
		// um token nunca atravessa linhas (strings e comentários vão até o fim da linha)
		start := lines.position(src, t.Off)
		length := utf16Len(src[t.Off:t.End])
		if start.Line != prevLine {
			prevChar = 0
		}
		data = append(data, start.Line-prevLine, start.Character-prevChar, length, typ, mods)
		prevLine, prevChar = start.Line, start.Character
	}
	at := func(i int) token.Kind {
		if i < 0 || i >= len(toks) || toks[i].Kind == token.COMMENT {
			return token.ILLEGAL
		}
		return toks[i].Kind
	}
	for i, t := range toks {
		switch {
		case t.Kind == token.COMMENT:
			emit(t, tComment, 0)
		case t.Kind == token.STRING || t.Kind == token.CHAR:
			emit(t, tString, 0)
		case t.Kind == token.INT || t.Kind == token.FLOAT:
			emit(t, tNumber, 0)
		case t.Kind > token.COMMENT && t.Kind != token.IDENT && isKeyword(t):
			emit(t, tKeyword, 0)
		case t.Kind == token.IDENT:
			typ, mods := classifyIdent(toks, i, at, modules)
			emit(t, typ, mods)
		}
	}
	return data
}

func isKeyword(t token.Token) bool {
	return token.Lookup(t.Lit) == t.Kind && t.Kind != token.IDENT
}

// classifyIdent decide a cor de um identificador olhando os vizinhos.
func classifyIdent(toks []token.Token, i int, at func(int) token.Kind, modules map[string]bool) (int, int) {
	name := toks[i].Lit
	prev, next := at(i-1), at(i+1)
	afterDot := prev == token.PERIOD
	onModule := afterDot && i >= 2 && toks[i-2].Kind == token.IDENT && modules[toks[i-2].Lit]
	switch {
	case name == "true" || name == "false" || name == "nil":
		return tKeyword, 0
	case basicTypes[name] && !afterDot:
		return tType, mDefaultLibrary
	case next == token.PERIOD && modules[name] && !afterDot:
		return tNamespace, 0
	case builtins[name] && next == token.LPAREN && !afterDot:
		return tFunction, mDefaultLibrary
	case prev == token.FN:
		return tFunction, mDeclaration
	case prev == token.RPAREN && next == token.LPAREN && i >= 2 && funcHeader(toks, i):
		return tMethod, mDeclaration // fn (p &Ponto) area()
	case allCaps(name):
		return tVariable, mReadonly // constante: PI, NOT_FOUND
	case capitalized(name) && (next == token.LPAREN || next == token.ARROW || next == token.COMMA && afterDot):
		return tEnumMember, 0 // Circulo(r) =>, Forma.Circulo(...), Vazio =>
	case capitalized(name) && afterDot && !onModule:
		return tEnumMember, 0 // Forma.Vazio
	case capitalized(name):
		return tType, 0
	case next == token.LPAREN || next == token.LBRACK && genericCall(toks, i):
		if afterDot && !onModule {
			return tMethod, 0
		}
		return tFunction, 0
	case afterDot && !onModule:
		return tProperty, 0
	}
	return tVariable, 0
}

// funcHeader diz se o identificador em i vem logo depois do receptor de um
// método: fn (p &Ponto) nome(.
func funcHeader(toks []token.Token, i int) bool {
	depth := 0
	for j := i - 1; j >= 0; j-- {
		switch toks[j].Kind {
		case token.RPAREN:
			depth++
		case token.LPAREN:
			depth--
			if depth == 0 {
				return j > 0 && toks[j-1].Kind == token.FN
			}
		}
	}
	return false
}

// genericCall diz se f[...] em i é seguido de '(' (chamada genérica explícita).
func genericCall(toks []token.Token, i int) bool {
	depth := 0
	for j := i + 1; j < len(toks); j++ {
		switch toks[j].Kind {
		case token.LBRACK:
			depth++
		case token.RBRACK:
			depth--
			if depth == 0 {
				return j+1 < len(toks) && toks[j+1].Kind == token.LPAREN
			}
		case token.SEMICOLON, token.LBRACE:
			return false
		}
	}
	return false
}

func capitalized(s string) bool {
	return s != "" && unicode.IsUpper(rune(s[0]))
}

func allCaps(s string) bool {
	return len(s) > 1 && strings.ToUpper(s) == s && strings.ContainsFunc(s, unicode.IsLetter)
}
