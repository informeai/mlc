// Package lexer turns MLC code into tokens, inserting ';' at the end of
// lines with Go's rule.
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

	comments bool // return the comments as COMMENT tokens
}

func New(file string, src []byte) *Lexer {
	return &Lexer{file: file, src: src, line: 1, col: 1, last: token.EOF}
}

// NewWithComments creates a lexer that also returns the comments, as
// COMMENT tokens (used by the formatter). Comments don't change the automatic
// insertion of ';'.
func NewWithComments(file string, src []byte) *Lexer {
	l := New(file, src)
	l.comments = true
	return l
}

func (l *Lexer) Errors() []token.Error { return l.errs }

// Next returns the next token. After the end of the file it always returns EOF.
func (l *Lexer) Next() token.Token {
	tok := l.scan()
	if tok.Kind != token.COMMENT {
		l.last = tok.Kind
	}
	return tok
}

// insertsSemi says whether a line break after k ends the statement.
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

// match consumes the next byte if it's c.
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
		l.errorf(pos, "unexpected character %q", lit)
	}
	return token.Token{Kind: k, Lit: lit, Pos: pos}
}

// wrap reads the % of +%, -% and *% (wrapping arithmetic), with the optional =
// of compound assignment. Returns ILLEGAL if there's no %.
func (l *Lexer) wrap(op, assign token.Kind) token.Kind {
	if !l.match('%') {
		return token.ILLEGAL
	}
	if l.match('=') {
		return assign
	}
	return op
}

// alt is a two-character variant: if the next byte is next, the token is kind.
type alt struct {
	next byte
	kind token.Kind
}

// pick chooses between the simple token def and the two-character variants.
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
	// 0xFF, 0b1010, 0o17: integers in other bases
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
				l.errorf(pos, "malformed number: digits missing after %q", string(l.src[start:l.off]))
			}
			if l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
				l.errorf(l.pos(), "malformed number: %q isn't a valid digit in %q", l.src[l.off], string(l.src[start:l.off]))
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
	// exponent: 1e9, 2.5E-3
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
		l.errorf(l.pos(), "malformed number: letter right after %q", string(l.src[start:l.off]))
		for l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
			l.advance()
		}
	}
	return token.Token{Kind: kind, Lit: string(l.src[start:l.off]), Pos: pos}
}

// digits reads digits (according to ok) and _ separators between them: 1_000_000.
// Returns whether it read any digit.
func (l *Lexer) digits(ok func(byte) bool, start int) bool {
	any, lastUnderscore := false, false
	for l.off < len(l.src) {
		c := l.src[l.off]
		if c == '_' {
			if !any || lastUnderscore {
				l.errorf(l.pos(), "malformed number: _ can only separate digits")
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
		l.errorf(l.pos(), "malformed number: _ can only separate digits")
	}
	return any
}

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// scanString reads a literal between quotes and returns the value already without escapes.
func (l *Lexer) scanString(pos token.Pos) token.Token {
	l.advance() // opening quote
	var sb strings.Builder
	for {
		if l.off >= len(l.src) || l.src[l.off] == '\n' {
			l.errorf(pos, "unterminated string")
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
				l.errorf(escPos, "invalid escape \\%c", e)
			}
			continue
		}
		sb.WriteByte(c)
		l.advance()
	}
	return token.Token{Kind: token.STRING, Lit: sb.String(), Pos: pos}
}

// scanChar reads a byte literal: 'a', '\n', '\”. A literal holds a single byte,
// so letters that take more than one byte in UTF-8 (like 'á') don't fit.
func (l *Lexer) scanChar(pos token.Pos) token.Token {
	l.advance() // opening quote
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
			l.errorf(escPos, "invalid escape \\%c", e)
		}
	}
	if l.off >= len(l.src) || l.src[l.off] != '\'' {
		l.errorf(pos, "unterminated byte literal")
		return token.Token{Kind: token.CHAR, Lit: "\x00", Pos: pos}
	}
	l.advance()
	switch {
	case len(b) == 0:
		l.errorf(pos, "empty byte literal")
		b = []byte{0}
	case len(b) > 1:
		l.errorf(pos, "a byte literal holds a single byte ('%s' takes %d bytes in UTF-8); use a string", b, len(b))
		b = b[:1]
	}
	return token.Token{Kind: token.CHAR, Lit: string(b), Pos: pos}
}
