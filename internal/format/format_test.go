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

// significant returns the tokens of src, with comments and without the ';'
// (which change with line breaks), to compare two texts.
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

// TestRepository formats every .mlc of the project: the result has the
// same tokens, still parses and formatting again changes
// nothing.
func TestRepository(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../../std/*.mlc", "../../tests/*/*.mlc", "../../tests/*/*/*.mlc"} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	if len(files) < 100 {
		t.Fatalf("too few files found: %d", len(files))
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, errs := parser.ParseFile(file, src); len(errs) > 0 {
			continue // intentional syntax error (tests/errors)
		}
		out, errs := Source(file, src)
		if len(errs) > 0 {
			t.Errorf("%s: %v", file, errs)
			continue
		}
		if a, b := strings.Join(significant(src), "\n"), strings.Join(significant(out), "\n"); a != b {
			t.Errorf("%s: the formatting changed the tokens:\n%s", file, out)
			continue
		}
		if _, errs := parser.ParseFile(file, out); len(errs) > 0 {
			t.Errorf("%s: the result doesn't compile: %v\n%s", file, errs, out)
			continue
		}
		again, _ := Source(file, out)
		if string(again) != string(out) {
			t.Errorf("%s: formatting twice changes the result:\n%s\n---\n%s", file, out, again)
		}
	}
}

func TestCanonical(t *testing.T) {
	src := `import "std/io"
import    "std/str"


// Point on the plane
struct Point {
  x int  // abscissa
  y int // ordinate
}

pub fn (p &Point) sum()int{
	return p.x+p.y
}
fn larger[T ordered](a T,b T) T { if a>b { return a }; return b }

fn main(){
  var a [3]int=[3]int{1,2,3}
  s:=alloc([]int,4)
  p:=Point{x:1,y:-2}
  var q ?*Point=nil


  io.print(a[0] * -a[1], len(s[1:3]), p.sum(), larger[int](1,2), q==nil)
  if (p==Point{x: 1, y: -2})&&
  !false {
    io.print("equal");
  }
  match a[0] {
    1=>io.print("one") ; 2 => {}
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

// Point on the plane
struct Point {
    x int // abscissa
    y int // ordinate
}

pub fn (p &Point) sum() int {
    return p.x + p.y
}
fn larger[T ordered](a T, b T) T { if a > b { return a }; return b }

fn main() {
    var a [3]int = [3]int{1, 2, 3}
    s := alloc([]int, 4)
    p := Point{x: 1, y: -2}
    var q ?*Point = nil

    io.print(a[0] * -a[1], len(s[1:3]), p.sum(), larger[int](1, 2), q == nil)
    if (p == Point{x: 1, y: -2}) &&
        !false {
        io.print("equal")
    }
    match a[0] {
        1 => io.print("one"); 2 => {}
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
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSyntaxError(t *testing.T) {
	if _, errs := Source("x.mlc", []byte("fn main( {\n}\n")); len(errs) == 0 {
		t.Fatal("a file with a syntax error shouldn't be formatted")
	}
}

// TestMessy scrambles each file of the project (removes the indentation and puts 1
// to 3 spaces between all the tokens of the same line) and checks that formatting the
// scrambled version gives the same as formatting the original.
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
			t.Errorf("%s: the scrambled version formats differently:\n%s", file, diffLines(string(want), string(got)))
		}
	}
}

// diffLines shows the first different lines.
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
