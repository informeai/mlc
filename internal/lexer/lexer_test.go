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
	got := kinds("+ ++ += - -- -= * *= / /= % %= = == => ! != < <= > >= := : && || ( ) [ ] { } , ; . & ?")
	want := "+ ++ += - -- -= * *= / /= % %= = == => ! != < <= > >= := : && || ( ) [ ] { } , ; . & ?"
	if got != want {
		t.Errorf("\ngot  %s\nwant %s", got, want)
	}
}

func TestSemicolonInsertion(t *testing.T) {
	tests := []struct{ src, want string }{
		{"x\n", "IDENT ;"},
		{"x", "IDENT ;"}, // fim do arquivo também termina o comando
		{"f(1)\n", "IDENT ( INT ) ;"},
		{"a +\nb\n", "IDENT + IDENT ;"}, // operador no fim da linha continua a expressão
		{"fn main() {\n}\n", "fn IDENT ( ) { } ;"},
		{"return\n", "return ;"},
		{"i++\n", "IDENT ++ ;"},
		{"x // comentário\ny\n", "IDENT ; IDENT ;"},
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
		t.Errorf("erros inesperados: %v", l.Errors())
	}
}

func TestErrors(t *testing.T) {
	tests := []struct{ src, want string }{
		{`"abc`, "string não terminada"},
		{`"\q"`, `escape inválido \q`},
		{"@", "caractere inesperado"},
		{"|x", "caractere inesperado"},
		{"#", "caractere inesperado"},
		{"12ab", "número malformado"},
	}
	for _, tt := range tests {
		l := New("t.mlc", []byte(tt.src))
		for l.Next().Kind != token.EOF {
		}
		if len(l.Errors()) == 0 || !strings.Contains(l.Errors()[0].Msg, tt.want) {
			t.Errorf("%q: erros %v, esperado %q", tt.src, l.Errors(), tt.want)
		}
	}
}

func TestPositions(t *testing.T) {
	l := New("t.mlc", []byte("fn\n  main"))
	l.Next() // fn
	tok := l.Next()
	if tok.Kind != token.IDENT || tok.Pos != (token.Pos{Line: 2, Col: 3}) {
		t.Errorf("got %v em %v", tok.Kind, tok.Pos)
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
			t.Errorf("%s: got %v %q, erros %v", tt.src, tok.Kind, tok.Lit, l.Errors())
		}
	}
	for _, src := range []string{`''`, `'ab'`, `'á'`, `'a`} {
		l := New("t.mlc", []byte(src))
		for l.Next().Kind != token.EOF {
		}
		if len(l.Errors()) == 0 {
			t.Errorf("%s: esperado erro", src)
		}
	}
}

func TestFloatLiterals(t *testing.T) {
	for _, src := range []string{"1.5", "1e9", "2.5E-3", "7e+2", "0.0"} {
		l := New("t.mlc", []byte(src))
		tok := l.Next()
		if tok.Kind != token.FLOAT || tok.Lit != src || len(l.Errors()) > 0 {
			t.Errorf("%s: got %v %q, erros %v", src, tok.Kind, tok.Lit, l.Errors())
		}
	}
	// "1e" sem dígitos não é expoente: é o número 1 seguido de letra
	l := New("t.mlc", []byte("1e"))
	for l.Next().Kind != token.EOF {
	}
	if len(l.Errors()) == 0 {
		t.Error("1e: esperado erro de número malformado")
	}
}
