package lexer

import (
	"strings"
	"testing"

	"mlc/internal/token"
)

func kinds(src string) string {
	l := New("t.mlc", []byte(src))
	var parts []string
	for {
		tok := l.Next()
		if tok.Kind == token.EOF {
			break
		}
		parts = append(parts, tok.Kind.String())
	}
	return strings.Join(parts, " ")
}

func TestOperators(t *testing.T) {
	got := kinds("+ ++ += - -- -= * *= / /= % %= = == => ! != < <= > >= := : && || ( ) [ ] { } , ; . & ? | ^ << >> &= |= ^= <<= >>= +% -% *% +%= -%= *%=")
	want := "+ ++ += - -- -= * *= / /= % %= = == => ! != < <= > >= := : && || ( ) [ ] { } , ; . & ? | ^ << >> &= |= ^= <<= >>= +% -% *% +%= -%= *%="
	if got != want {
		t.Errorf("\ngot  %s\nwant %s", got, want)
	}
}

func TestSemicolonInsertion(t *testing.T) {
	tests := []struct{ src, want string }{
		{"x\n", "IDENT ;"},
		{"x", "IDENT ;"}, // end of file also ends the statement
		{"f(1)\n", "IDENT ( INT ) ;"},
		{"a +\nb\n", "IDENT + IDENT ;"}, // an operator at the end of the line continues the expression
		{"fn main() {\n}\n", "fn IDENT ( ) { } ;"},
		{"return\n", "return ;"},
		{"i++\n", "IDENT ++ ;"},
		{"x // comment\ny\n", "IDENT ; IDENT ;"},
		{"\n\n\nx\n\n\n", "IDENT ;"},
	}
	for _, tt := range tests {
		if got := kinds(tt.src); got != tt.want {
			t.Errorf("%q:\ngot  %s\nwant %s", tt.src, got, tt.want)
		}
	}
}

func TestStringEscapes(t *testing.T) {
	l := New("t.mlc", []byte(`"a\n\t\"\\b"`))
	tok := l.Next()
	if tok.Kind != token.STRING || tok.Lit != "a\n\t\"\\b" {
		t.Errorf("got %v %q", tok.Kind, tok.Lit)
	}
	if len(l.Errors()) != 0 {
		t.Errorf("unexpected errors: %v", l.Errors())
	}
}

func TestErrors(t *testing.T) {
	tests := []struct{ src, want string }{
		{`"abc`, "unterminated string"},
		{`"\q"`, `invalid escape \q`},
		{"@", "unexpected character"},
		{"$x", "unexpected character"},
		{"0x", "digits missing"},
		{"1__0", "_ can only separate digits"},
		{"0b12", "isn't a valid digit"},
		{"#", "unexpected character"},
		{"12ab", "malformed number"},
	}
	for _, tt := range tests {
		l := New("t.mlc", []byte(tt.src))
		for l.Next().Kind != token.EOF {
		}
		if len(l.Errors()) == 0 || !strings.Contains(l.Errors()[0].Msg, tt.want) {
			t.Errorf("%q: errors %v, expected %q", tt.src, l.Errors(), tt.want)
		}
	}
}

func TestPositions(t *testing.T) {
	l := New("t.mlc", []byte("fn\n  main"))
	l.Next() // fn
	tok := l.Next()
	if tok.Kind != token.IDENT || tok.Pos != (token.Pos{Line: 2, Col: 3}) {
		t.Errorf("got %v at %v", tok.Kind, tok.Pos)
	}
}

func TestCharLiterals(t *testing.T) {
	tests := []struct {
		src  string
		want byte
	}{
		{`'a'`, 'a'}, {`'\n'`, '\n'}, {`'\''`, '\''}, {`'\\'`, '\\'}, {`'\0'`, 0},
	}
	for _, tt := range tests {
		l := New("t.mlc", []byte(tt.src))
		tok := l.Next()
		if tok.Kind != token.CHAR || tok.Lit[0] != tt.want || len(l.Errors()) > 0 {
			t.Errorf("%s: got %v %q, errs %v", tt.src, tok.Kind, tok.Lit, l.Errors())
		}
	}
	for _, src := range []string{`''`, `'ab'`, `'é'`, `'a`} {
		l := New("t.mlc", []byte(src))
		for l.Next().Kind != token.EOF {
		}
		if len(l.Errors()) == 0 {
			t.Errorf("%s: expected an error", src)
		}
	}
}

func TestFloatLiterals(t *testing.T) {
	for _, src := range []string{"1.5", "1e9", "2.5E-3", "7e+2", "0.0"} {
		l := New("t.mlc", []byte(src))
		tok := l.Next()
		if tok.Kind != token.FLOAT || tok.Lit != src || len(l.Errors()) > 0 {
			t.Errorf("%s: got %v %q, errs %v", src, tok.Kind, tok.Lit, l.Errors())
		}
	}
	// "1e" without digits isn't an exponent: it's the number 1 followed by a letter
	l := New("t.mlc", []byte("1e"))
	for l.Next().Kind != token.EOF {
	}
	if len(l.Errors()) == 0 {
		t.Error("1e: expected a malformed number error")
	}
}

func TestIntPrefixes(t *testing.T) {
	for _, src := range []string{"0xFF", "0Xff", "0b1010", "0o17", "1_000_000", "0x_FF"} {
		l := New("t.mlc", []byte(src))
		tok := l.Next()
		if tok.Kind != token.INT || tok.Lit != src || (len(l.Errors()) > 0 && src != "0x_FF") {
			t.Errorf("%q: %v %q %v", src, tok.Kind, tok.Lit, l.Errors())
		}
	}
}
