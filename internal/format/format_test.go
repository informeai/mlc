package format

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mlc/internal/lexer"
	"mlc/internal/parser"
	"mlc/internal/token"
)

// significant devolve os tokens de src, com comentários e sem os ';'
// (que mudam com as quebras de linha), para comparar dois textos.
func significant(src []byte) []string {
	lx := lexer.NewWithComments("x.mlc", src)
	var out []string
	for {
		t := lx.Next()
		if t.Kind == token.EOF {
			return out
		}
		if t.Kind == token.SEMICOLON {
			continue
		}
		out = append(out, t.Kind.String()+" "+string(src[t.Off:t.End]))
	}
}

// TestRepository formata todos os .mlc do projeto: o resultado tem os
// mesmos tokens, ainda compila sintaticamente e formatar de novo não muda
// nada.
func TestRepository(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../../std/*.mlc", "../../tests/*/*.mlc", "../../tests/*/*/*.mlc"} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	if len(files) < 100 {
		t.Fatalf("poucos arquivos encontrados: %d", len(files))
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, errs := parser.ParseFile(file, src); len(errs) > 0 {
			continue // erro de sintaxe proposital (tests/errors)
		}
		out, errs := Source(file, src)
		if len(errs) > 0 {
			t.Errorf("%s: %v", file, errs)
			continue
		}
		if a, b := strings.Join(significant(src), "\n"), strings.Join(significant(out), "\n"); a != b {
			t.Errorf("%s: a formatação mudou os tokens:\n%s", file, out)
			continue
		}
		if _, errs := parser.ParseFile(file, out); len(errs) > 0 {
			t.Errorf("%s: o resultado não compila: %v\n%s", file, errs, out)
			continue
		}
		again, _ := Source(file, out)
		if string(again) != string(out) {
			t.Errorf("%s: formatar duas vezes muda o resultado:\n%s\n---\n%s", file, out, again)
		}
	}
}

func TestCanonical(t *testing.T) {
	src := `import "std/io"
import    "std/str"


// Ponto no plano
struct Ponto {
  x int  // abscissa
  y int // ordenada
}

pub fn (p &Ponto) soma()int{
	return p.x+p.y
}
fn grande[T ordered](a T,b T) T { if a>b { return a }; return b }

fn main(){
  var a [3]int=[3]int{1,2,3}
  s:=alloc([]int,4)
  p:=Ponto{x:1,y:-2}
  var q ?*Ponto=nil


  io.print(a[0] * -a[1], len(s[1:3]), p.soma(), grande[int](1,2), q==nil)
  if (p==Ponto{x: 1, y: -2})&&
  !false {
    io.print("igual");
  }
  match a[0] {
    1=>io.print("um") ; 2 => {}
    _ => {
      x := 1+
        2
      io.print(x)
    }
  }
  free(s)
}
`
	want := `import "std/io"
import "std/str"

// Ponto no plano
struct Ponto {
    x int // abscissa
    y int // ordenada
}

pub fn (p &Ponto) soma() int {
    return p.x + p.y
}
fn grande[T ordered](a T, b T) T { if a > b { return a }; return b }

fn main() {
    var a [3]int = [3]int{1, 2, 3}
    s := alloc([]int, 4)
    p := Ponto{x: 1, y: -2}
    var q ?*Ponto = nil

    io.print(a[0] * -a[1], len(s[1:3]), p.soma(), grande[int](1, 2), q == nil)
    if (p == Ponto{x: 1, y: -2}) &&
        !false {
        io.print("igual")
    }
    match a[0] {
        1 => io.print("um"); 2 => {}
        _ => {
            x := 1 +
                2
            io.print(x)
        }
    }
    free(s)
}
`
	got, errs := Source("x.mlc", []byte(src))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if string(got) != want {
		t.Errorf("resultado:\n%s\nesperado:\n%s", got, want)
	}
}

func TestSyntaxError(t *testing.T) {
	if _, errs := Source("x.mlc", []byte("fn main( {\n}\n")); len(errs) == 0 {
		t.Fatal("um arquivo com erro de sintaxe não deveria ser formatado")
	}
}

// TestMessy desarruma cada arquivo do projeto (tira a indentação e põe de 1
// a 3 espaços entre todos os tokens da mesma linha) e confere que formatar a
// versão bagunçada dá o mesmo que formatar a original.
func TestMessy(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../../std/*.mlc", "../../tests/*/*.mlc", "../../tests/*/*/*.mlc"} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, file := range files {
		src, _ := os.ReadFile(file)
		if _, errs := parser.ParseFile(file, src); len(errs) > 0 {
			continue
		}
		want, _ := Source(file, src)
		var sb strings.Builder
		lx := lexer.NewWithComments(file, src)
		line := 1
		for {
			tk := lx.Next()
			if tk.Kind == token.EOF {
				break
			}
			if tk.Kind == token.SEMICOLON && tk.Lit == "\n" {
				continue
			}
			if tk.Pos.Line > line {
				sb.WriteString(strings.Repeat("\n", tk.Pos.Line-line))
				line = tk.Pos.Line
			} else if sb.Len() > 0 {
				sb.WriteString(strings.Repeat(" ", 1+rng.IntN(3)))
			}
			sb.Write(src[tk.Off:tk.End])
		}
		sb.WriteByte('\n')
		got, errs := Source(file, []byte(sb.String()))
		if len(errs) > 0 {
			t.Errorf("%s: %v\n%s", file, errs, sb.String())
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s: a versão bagunçada formata diferente:\n%s", file, diffLines(string(want), string(got)))
		}
	}
}

// diffLines mostra as primeiras linhas diferentes.
func diffLines(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for i := 0; i < max(len(la), len(lb)) && len(out) < 12; i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			out = append(out, "- "+x, "+ "+y)
		}
	}
	return strings.Join(out, "\n")
}
