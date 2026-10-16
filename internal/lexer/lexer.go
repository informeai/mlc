// Package lexer transforma código MLC em tokens, inserindo ';' no fim das
// linhas com a mesma regra do Go.
package lexer

import (
	"fmt"
	"strings"

	"mlc/internal/token"
)

type Lexer struct {
	file string
	src  []byte
	off  int
	line int
	col  int
	last token.Kind
	errs []token.Error

	comments bool // devolver os comentários como tokens COMMENT
}

func New(file string, src []byte) *Lexer {
	return &Lexer{file: file, src: src, line: 1, col: 1, last: token.EOF}
}

// NewWithComments cria um lexer que também devolve os comentários, como
// tokens COMMENT (usado pelo formatador). Comentários não mudam a inserção
// automática de ';'.
func NewWithComments(file string, src []byte) *Lexer {
	l := New(file, src)
	l.comments = true
	return l
}

func (l *Lexer) Errors() []token.Error { return l.errs }

// Next devolve o próximo token. Depois do fim do arquivo devolve sempre EOF.
func (l *Lexer) Next() token.Token {
	tok := l.scan()
	if tok.Kind != token.COMMENT {
		l.last = tok.Kind
	}
	return tok
}

// insertsSemi diz se uma quebra de linha depois de k termina o comando.
func insertsSemi(k token.Kind) bool {
	switch k {
	case token.IDENT, token.INT, token.FLOAT, token.STRING, token.CHAR,
		token.RPAREN, token.RBRACK, token.RBRACE,
		token.INC, token.DEC,
		token.BREAK, token.CONTINUE, token.RETURN:
		return true
	}
	return false
}

func (l *Lexer) pos() token.Pos { return token.Pos{Line: l.line, Col: l.col} }

func (l *Lexer) peek(n int) byte {
	if l.off+n < len(l.src) {
		return l.src[l.off+n]
	}
	return 0
}

func (l *Lexer) advance() {
	if l.src[l.off] == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	l.off++
}

// match consome o próximo byte se ele for c.
func (l *Lexer) match(c byte) bool {
	if l.off < len(l.src) && l.src[l.off] == c {
		l.advance()
		return true
	}
	return false
}

func (l *Lexer) errorf(pos token.Pos, format string, args ...any) {
	l.errs = append(l.errs, token.Error{File: l.file, Pos: pos, Msg: fmt.Sprintf(format, args...)})
}

func (l *Lexer) scan() token.Token {
	for l.off < len(l.src) {
		c := l.src[l.off]
		switch {
		case c == '\n':
			if insertsSemi(l.last) {
				pos, off := l.pos(), l.off
				l.advance()
				return token.Token{Kind: token.SEMICOLON, Lit: "\n", Pos: pos, Off: off, End: off}
			}
			l.advance()
		case c == ' ' || c == '\t' || c == '\r':
			l.advance()
		case c == '/' && l.peek(1) == '/':
			pos, start := l.pos(), l.off
			for l.off < len(l.src) && l.src[l.off] != '\n' {
				l.advance()
			}
			if l.comments {
				text := strings.TrimRight(string(l.src[start:l.off]), " \t\r")
				return token.Token{Kind: token.COMMENT, Lit: text, Pos: pos, Off: start, End: l.off}
			}
		default:
			start := l.off
			tok := l.scanToken()
			tok.Off, tok.End = start, l.off
			return tok
		}
	}
	pos := l.pos()
	if insertsSemi(l.last) {
		return token.Token{Kind: token.SEMICOLON, Lit: "\n", Pos: pos, Off: l.off, End: l.off}
	}
	return token.Token{Kind: token.EOF, Pos: pos, Off: l.off, End: l.off}
}

func isLetter(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

func (l *Lexer) scanToken() token.Token {
	pos := l.pos()
	start := l.off
	c := l.src[l.off]
	switch {
	case isLetter(c):
		return l.scanIdent(pos)
	case isDigit(c):
		return l.scanNumber(pos)
	case c == '"':
		return l.scanString(pos)
	case c == '\'':
		return l.scanChar(pos)
	}

	l.advance()
	k := token.ILLEGAL
	switch c {
	case '+':
		if k = l.wrap(token.ADD_WRAP, token.ADD_WRAP_ASSIGN); k == token.ILLEGAL {
			k = l.pick(token.ADD, alt{'+', token.INC}, alt{'=', token.ADD_ASSIGN})
		}
	case '-':
		if k = l.wrap(token.SUB_WRAP, token.SUB_WRAP_ASSIGN); k == token.ILLEGAL {
			k = l.pick(token.SUB, alt{'-', token.DEC}, alt{'=', token.SUB_ASSIGN})
		}
	case '*':
		if k = l.wrap(token.MUL_WRAP, token.MUL_WRAP_ASSIGN); k == token.ILLEGAL {
			k = l.pick(token.MUL, alt{'=', token.MUL_ASSIGN})
		}
	case '/':
		k = l.pick(token.QUO, alt{'=', token.QUO_ASSIGN})
	case '%':
		k = l.pick(token.REM, alt{'=', token.REM_ASSIGN})
	case '=':
		k = l.pick(token.ASSIGN, alt{'=', token.EQL}, alt{'>', token.ARROW})
	case '!':
		k = l.pick(token.NOT, alt{'=', token.NEQ})
	case '<':
		if l.match('<') {
			k = l.pick(token.SHL, alt{'=', token.SHL_ASSIGN})
		} else {
			k = l.pick(token.LSS, alt{'=', token.LEQ})
		}
	case '>':
		if l.match('>') {
			k = l.pick(token.SHR, alt{'=', token.SHR_ASSIGN})
		} else {
			k = l.pick(token.GTR, alt{'=', token.GEQ})
		}
	case ':':
		k = l.pick(token.COLON, alt{'=', token.DEFINE})
	case '&':
		k = l.pick(token.AMP, alt{'&', token.LAND}, alt{'=', token.AND_ASSIGN})
	case '|':
		k = l.pick(token.OR, alt{'|', token.LOR}, alt{'=', token.OR_ASSIGN})
	case '^':
		k = l.pick(token.XOR, alt{'=', token.XOR_ASSIGN})
	case '(':
		k = token.LPAREN
	case ')':
		k = token.RPAREN
	case '[':
		k = token.LBRACK
	case ']':
		k = token.RBRACK
	case '{':
		k = token.LBRACE
	case '}':
		k = token.RBRACE
	case ',':
		k = token.COMMA
	case ';':
		k = token.SEMICOLON
	case '.':
		k = token.PERIOD
	case '?':
		k = token.QUESTION
	}
	lit := string(l.src[start:l.off])
	if k == token.ILLEGAL {
		l.errorf(pos, "caractere inesperado %q", lit)
	}
	return token.Token{Kind: k, Lit: lit, Pos: pos}
}

// wrap lê o % de +%, -% e *% (aritmética que dá a volta), com o = opcional
// da atribuição composta. Devolve ILLEGAL se não houver %.
func (l *Lexer) wrap(op, assign token.Kind) token.Kind {
	if !l.match('%') {
		return token.ILLEGAL
	}
	if l.match('=') {
		return assign
	}
	return op
}

// alt é uma variante de dois caracteres: se o próximo byte for next, o token é kind.
type alt struct {
	next byte
	kind token.Kind
}

// pick escolhe entre o token simples def e as variantes de dois caracteres.
func (l *Lexer) pick(def token.Kind, alts ...alt) token.Kind {
	for _, a := range alts {
		if l.match(a.next) {
			return a.kind
		}
	}
	return def
}

func (l *Lexer) scanIdent(pos token.Pos) token.Token {
	start := l.off
	for l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
		l.advance()
	}
	lit := string(l.src[start:l.off])
	return token.Token{Kind: token.Lookup(lit), Lit: lit, Pos: pos}
}

func (l *Lexer) scanNumber(pos token.Pos) token.Token {
	start := l.off
	kind := token.INT
	// 0xFF, 0b1010, 0o17: inteiros em outras bases
	if l.peek(0) == '0' {
		var base func(byte) bool
		switch l.peek(1) {
		case 'x', 'X':
			base = isHex
		case 'b', 'B':
			base = func(c byte) bool { return c == '0' || c == '1' }
		case 'o', 'O':
			base = func(c byte) bool { return c >= '0' && c <= '7' }
		}
		if base != nil {
			l.advance()
			l.advance()
			if !l.digits(base, start) {
				l.errorf(pos, "número malformado: faltam dígitos depois de %q", string(l.src[start:l.off]))
			}
			if l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
				l.errorf(l.pos(), "número malformado: %q não é um dígito válido em %q", l.src[l.off], string(l.src[start:l.off]))
				for l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
					l.advance()
				}
			}
			return token.Token{Kind: token.INT, Lit: string(l.src[start:l.off]), Pos: pos}
		}
	}
	l.digits(isDigit, start)
	if l.peek(0) == '.' && isDigit(l.peek(1)) {
		kind = token.FLOAT
		l.advance()
		l.digits(isDigit, start)
	}
	// expoente: 1e9, 2.5E-3
	if c := l.peek(0); c == 'e' || c == 'E' {
		sign := l.peek(1) == '+' || l.peek(1) == '-'
		if isDigit(l.peek(1)) || (sign && isDigit(l.peek(2))) {
			kind = token.FLOAT
			l.advance()
			if sign {
				l.advance()
			}
			l.digits(isDigit, start)
		}
	}
	if l.off < len(l.src) && isLetter(l.src[l.off]) {
		l.errorf(l.pos(), "número malformado: letra logo após %q", string(l.src[start:l.off]))
		for l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
			l.advance()
		}
	}
	return token.Token{Kind: kind, Lit: string(l.src[start:l.off]), Pos: pos}
}

// digits lê dígitos (conforme ok) e separadores _ entre eles: 1_000_000.
// Devolve se leu algum dígito.
func (l *Lexer) digits(ok func(byte) bool, start int) bool {
	any, lastUnderscore := false, false
	for l.off < len(l.src) {
		c := l.src[l.off]
		if c == '_' {
			if !any || lastUnderscore {
				l.errorf(l.pos(), "número malformado: _ só pode separar dígitos")
			}
			lastUnderscore = true
			l.advance()
			continue
		}
		if !ok(c) {
			break
		}
		any, lastUnderscore = true, false
		l.advance()
	}
	if lastUnderscore {
		l.errorf(l.pos(), "número malformado: _ só pode separar dígitos")
	}
	return any
}

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// scanString lê um literal entre aspas e devolve o valor já sem escapes.
func (l *Lexer) scanString(pos token.Pos) token.Token {
	l.advance() // aspas de abertura
	var sb strings.Builder
	for {
		if l.off >= len(l.src) || l.src[l.off] == '\n' {
			l.errorf(pos, "string não terminada")
			break
		}
		c := l.src[l.off]
		if c == '"' {
			l.advance()
			break
		}
		if c == '\\' {
			l.advance()
			if l.off >= len(l.src) {
				continue
			}
			escPos := l.pos()
			e := l.src[l.off]
			l.advance()
			switch e {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '0':
				sb.WriteByte(0)
			case '\\', '"':
				sb.WriteByte(e)
			default:
				l.errorf(escPos, "escape inválido \\%c", e)
			}
			continue
		}
		sb.WriteByte(c)
		l.advance()
	}
	return token.Token{Kind: token.STRING, Lit: sb.String(), Pos: pos}
}

// scanChar lê um literal de byte: 'a', '\n', '\”. Um literal vale um byte só,
// então letras que ocupam mais de um byte em UTF-8 (como 'á') não cabem.
func (l *Lexer) scanChar(pos token.Pos) token.Token {
	l.advance() // aspas de abertura
	var b []byte
	for l.off < len(l.src) && l.src[l.off] != '\'' && l.src[l.off] != '\n' {
		c := l.src[l.off]
		l.advance()
		if c != '\\' {
			b = append(b, c)
			continue
		}
		if l.off >= len(l.src) {
			break
		}
		escPos := l.pos()
		e := l.src[l.off]
		l.advance()
		switch e {
		case 'n':
			b = append(b, '\n')
		case 't':
			b = append(b, '\t')
		case 'r':
			b = append(b, '\r')
		case '0':
			b = append(b, 0)
		case '\\', '\'', '"':
			b = append(b, e)
		default:
			l.errorf(escPos, "escape inválido \\%c", e)
		}
	}
	if l.off >= len(l.src) || l.src[l.off] != '\'' {
		l.errorf(pos, "literal de byte não terminado")
		return token.Token{Kind: token.CHAR, Lit: "\x00", Pos: pos}
	}
	l.advance()
	switch {
	case len(b) == 0:
		l.errorf(pos, "literal de byte vazio")
		b = []byte{0}
	case len(b) > 1:
		l.errorf(pos, "um literal de byte vale um byte só ('%s' ocupa %d bytes em UTF-8); use uma string", b, len(b))
		b = b[:1]
	}
	return token.Token{Kind: token.CHAR, Lit: string(b), Pos: pos}
}
