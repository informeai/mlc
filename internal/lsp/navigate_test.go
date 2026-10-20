package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mlc/internal/driver"
	"mlc/internal/lexer"
	"mlc/internal/token"
)

const navMain = `import "std/io"
import "std/vec"
import "geo"

// Forma do desenho
enum Forma {
    Circulo(raio float64)
    Vazio
}

struct Caixa {
    largura int // em cm
}

const LIMITE = 10

// area calcula a área
fn area(f Forma) float64 {
    match f {
        Circulo(r) => return r * r
        Vazio => return 0.0
    }
}

fn (c &Caixa) dobro() int {
    return c.largura * 2
}

fn main() {
    c := Caixa{largura: LIMITE}
    v := vec.new[int](2)
    v.push(c.dobro())
    p := geo.Ponto{x: 1, y: 2}
    io.print(area(Forma.Circulo(1.5)), geo.soma(p), v.len(), len("ab"))
    v.free()
}
`

const navGeo = `// Geometria plana.

pub struct Ponto {
    x int
    y int
}

// soma devolve x + y.
pub fn soma(p Ponto) int {
    return p.x + p.y
}
`

func TestNavigation(t *testing.T) {
	dir := t.TempDir()
	mainPath, geoPath := filepath.Join(dir, "main.mlc"), filepath.Join(dir, "geo.mlc")
	os.WriteFile(mainPath, []byte(navMain), 0o644)
	os.WriteFile(geoPath, []byte(navGeo), 0o644)
	read := driver.Overlay(nil)

	cases := []struct {
		file    string
		at      string // o cursor fica no começo do trecho marcado com « »
		defFile string // "main", "geo", "std/x.mlc" ou "" (sem definição)
		defLine int
		hover   []string
	}{
		{"main", "io.print(«area(Forma", "main", 18, []string{"fn area(f Forma) float64", "area calcula a área"}},
		{"main", "area(Forma.«Circulo(1.5)", "main", 7, []string{"Circulo(raio float64)", "variante de `Forma`"}},
		{"main", "area(«Forma.Circulo", "main", 6, []string{"enum Forma {", "Vazio", "Forma do desenho"}},
		{"main", "return «r * r", "main", 20, []string{"var r float64"}},
		{"main", "        «Circulo(r) =>", "main", 7, []string{"variante de `Forma`"}},
		{"main", "return c.«largura * 2", "main", 12, []string{"largura int", "campo de `Caixa`"}},
		{"main", "Caixa{«largura: LIMITE", "main", 12, []string{"largura int"}},
		{"main", "largura: «LIMITE}", "main", 15, []string{"const LIMITE = 10"}},
		{"main", "v.push(c.«dobro())", "main", 25, []string{"fn (c &Caixa) dobro() int"}},
		{"main", "c := «Caixa{", "main", 11, []string{"struct Caixa {", "largura int // em cm"}},
		{"main", "v := vec.«new[int]", "std/vec.mlc", 0, []string{"pub fn new[T owned](cap int) *Vec[T]", "instância: `vec.new[int]`"}},
		{"main", "v.«push(c", "std/vec.mlc", 0, []string{"push(x T)"}},
		{"main", "p := geo.«Ponto{", "geo", 3, []string{"pub struct Ponto {"}},
		{"main", "geo.«soma(p)", "geo", 9, []string{"pub fn soma(p Ponto) int", "soma devolve x + y."}},
		{"main", "geo.soma(«p)", "main", 33, []string{"var p geo.Ponto"}},
		{"main", "p := «geo.Ponto", "geo", 1, []string{`import "geo"`, "Geometria plana."}},
		{"main", `import «"std/io"`, "std/io.mlc", 1, []string{`import "std/io"`, "Standard input and output"}},
		{"main", "«io.print(area", "std/io.mlc", 1, []string{`import "std/io"`}},
		{"main", "io.«print(area", "std/io.mlc", 1, []string{"io.print(a, b, ...)", "The compiler only rewrites the call"}},
		{"main", "v.len(), «len(\"ab\")", "", 0, []string{"len(s) int"}},
		{"main", "vec.new[«int](2)", "", 0, []string{"64-bit signed integer"}},
		{"main", "fn «area(f Forma)", "main", 18, []string{"fn area(f Forma) float64"}},
		{"main", "fn area(«f Forma)", "main", 18, []string{"f Forma", "parâmetro"}},
		{"geo", "return p.«x + p.y", "geo", 4, []string{"x int", "campo de `geo.Ponto`"}},
		{"geo", "pub fn soma(p «Ponto)", "geo", 3, []string{"pub struct Ponto {"}},
	}
	for _, tc := range cases {
		path, src := mainPath, navMain
		if tc.file == "geo" {
			path, src = geoPath, navGeo
		}
		mark := strings.Index(tc.at, "«")
		needle := strings.Replace(tc.at, "«", "", 1)
		pos := strings.Index(src, needle)
		if pos < 0 {
			t.Fatalf("trecho não encontrado: %q", needle)
		}
		off := pos + mark
		prog := driver.Analyze(path, read)
		if prog == nil {
			t.Fatalf("%s: não analisou", path)
		}
		nav := newNavigator(prog, read, []byte(src))
		sym, _ := nav.resolve(off)
		if sym == nil {
			t.Errorf("%q: nada encontrado", tc.at)
			continue
		}
		hover := sym.code + "\n" + sym.doc
		for _, h := range tc.hover {
			if !strings.Contains(hover, h) {
				t.Errorf("%q: o hover não tem %q:\n%s", tc.at, h, hover)
			}
		}
		switch tc.defFile {
		case "":
			if !sym.noDef {
				t.Errorf("%q: não deveria ter definição (%s:%d)", tc.at, sym.file, sym.pos.Line)
			}
			continue
		case "main":
			tc.defFile = mainPath
		case "geo":
			tc.defFile = geoPath
		}
		// linha 1: o começo do arquivo (posição zero ou a primeira linha)
		if sym.file != tc.defFile || (tc.defLine > 1 && sym.pos.Line != tc.defLine) || (tc.defLine == 1 && sym.pos.Line > 1) {
			t.Errorf("%q: definição em %s:%d, esperado %s:%d", tc.at, sym.file, sym.pos.Line, tc.defFile, tc.defLine)
		}
	}
}

func TestStdPath(t *testing.T) {
	p := stdPath("std/vec.mlc")
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "pub struct Vec") {
		t.Fatalf("std/vec.mlc não foi gravado em %s: %v", p, err)
	}
}

// TestNavigationEverywhere resolve cada identificador dos programas do
// projeto (e de versões com erro de tipo): nada pode dar panic, e quase
// todos os nomes precisam levar a alguma declaração.
func TestNavigationEverywhere(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../../tests/run/*.mlc", "../../tests/run/*/*.mlc", "../../std/*.mlc"} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	total, found := 0, 0
	for _, file := range files {
		abs, _ := filepath.Abs(file)
		src, _ := os.ReadFile(abs)
		for _, text := range []string{string(src), strings.Replace(string(src), "\n}\n", "\n    x := \"a\" + 1\n}\n", 1)} {
			read := driver.Overlay(map[string][]byte{abs: []byte(text)})
			prog := driver.Analyze(abs, read)
			if prog == nil {
				continue
			}
			nav := newNavigator(prog, read, []byte(text))
			lx := lexer.New("", []byte(text))
			for {
				tk := lx.Next()
				if tk.Kind == token.EOF {
					break
				}
				if tk.Kind != token.IDENT || tk.Lit == "_" {
					continue
				}
				total++
				if sym, _ := nav.resolve(tk.Off); sym != nil {
					found++
				} else if testing.Verbose() {
					t.Logf("sem declaração: %s:%d %s", filepath.Base(file), tk.Pos.Line, tk.Lit)
				}
			}
		}
	}
	t.Logf("%d de %d identificadores resolvidos", found, total)
	if total == 0 || found*100/total < 99 {
		t.Fatalf("poucos nomes resolvidos: %d de %d", found, total)
	}
}

const navIface = `import "std/io"

// Medida dá um tamanho
interface Medida {
    fn tamanho(self &Self) int
}

struct Caixa {
    l int
}

fn (c &Caixa) tamanho() int {
    return c.l
}

impl Medida for Caixa

impl auto io.Display for Caixa

fn maior[T Medida](a &T) int {
    return a.tamanho()
}

fn main() {
    c := Caixa{l: 2}
    io.printf("{} {}\n", c, maior(&c))
}
`

func TestNavigationInterfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.mlc")
	os.WriteFile(path, []byte(navIface), 0o644)
	read := driver.Overlay(nil)
	prog := driver.Analyze(path, read)
	if prog == nil {
		t.Fatal("não analisou")
	}
	nav := newNavigator(prog, read, []byte(navIface))
	cases := []struct {
		at, file string
		line     int
		hover    string
	}{
		{"impl «Medida for Caixa", path, 4, "Medida dá um tamanho"},
		{"fn maior[T «Medida]", path, 4, "interface Medida {"},
		{"return a.«tamanho()", path, 5, "fn tamanho(self &Self) int"},
		{"impl auto io.«Display for", "std/io.mlc", 0, "pub interface Display {"},
		{"impl «auto io", "", 0, "impl auto io.Display for T"},
		{"fn tamanho(self &«Self)", "", 0, "the type that implements it"},
		{"io.«printf(", "std/io.mlc", 1, "replacing each `{}` with a value"},
	}
	for _, tc := range cases {
		mark := strings.Index(tc.at, "«")
		needle := strings.Replace(tc.at, "«", "", 1)
		pos := strings.Index(navIface, needle)
		if pos < 0 {
			t.Fatalf("trecho não encontrado: %q", needle)
		}
		sym, _ := nav.resolve(pos + mark)
		if sym == nil {
			t.Errorf("%q: nada encontrado", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: o hover não tem %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
		if tc.file == "" {
			if !sym.noDef {
				t.Errorf("%q: não deveria ter definição", tc.at)
			}
		} else if sym.file != tc.file || (tc.line > 1 && sym.pos.Line != tc.line) {
			t.Errorf("%q: definição em %s:%d, esperado %s:%d", tc.at, sym.file, sym.pos.Line, tc.file, tc.line)
		}
	}
}

// str.format e str.format_len: hover com a documentação e definição no std/str.
func TestNavigationFormat(t *testing.T) {
	src := "import \"std/io\"\nimport \"std/str\"\n\nfn main() {\n    b := alloc([]byte, 8)\n    n := str.format(&b, \"{:>3}\", 7)\n    io.print(n, str.format_len(\"{}\", 1))\n    io.printf(\"{:x}\\n\", 255)\n    free(b)\n}\n"
	path := filepath.Join(t.TempDir(), "main.mlc")
	os.WriteFile(path, []byte(src), 0o644)
	read := driver.Overlay(nil)
	for _, tc := range []struct{ at, file, hover string }{
		{"str.«format(&b", "std/str.mlc", "returns how many bytes it used"},
		{"str.«format_len(", "std/str.mlc", "without writing anything"},
		{"io.«printf(", "std/io.mlc", "`{:x}`"},
	} {
		mark := strings.Index(tc.at, "«")
		off := strings.Index(src, strings.Replace(tc.at, "«", "", 1)) + mark
		prog := driver.Analyze(path, read)
		if prog == nil {
			t.Fatal("não analisou")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nada encontrado", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: o hover não tem %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
		if !strings.HasSuffix(filepath.ToSlash(sym.file), tc.file) {
			t.Errorf("%q: definição em %s, esperado %s", tc.at, sym.file, tc.file)
		}
	}
}

// thread.spawn e thread.join: hover com a documentação e definição no std/thread.
func TestNavigationThreads(t *testing.T) {
	src := "import \"std/thread\"\n\nfn sq(n int) int {\n    return n * n\n}\n\nfn main() {\n    t := thread.spawn(sq, 3, thread.STACK_64KB)\n    r := thread.join(t)\n    var u ?*thread.Thread[int] = nil\n    if r > 0 && u == nil {\n    }\n}\n"
	path := filepath.Join(t.TempDir(), "main.mlc")
	os.WriteFile(path, []byte(src), 0o644)
	read := driver.Overlay(nil)
	for _, tc := range []struct{ at, hover string }{
		{"thread.«spawn(sq", "The context is **moved**"},
		{"thread.«join(t)", "Waits for the thread to finish"},
		{"thread.«STACK_64KB", "STACK_64KB"},
		{"?*thread.«Thread[int]", "Handle of a thread"},
	} {
		mark := strings.Index(tc.at, "«")
		off := strings.Index(src, strings.Replace(tc.at, "«", "", 1)) + mark
		prog := driver.Analyze(path, read)
		if prog == nil {
			t.Fatal("não analisou")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nada encontrado", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: o hover não tem %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
	}
}

// Canais: hover nos métodos das pontas, em chan.new e nos tipos.
func TestNavigationChannels(t *testing.T) {
	src := "import \"std/chan\"\n\nfn main() {\n    rx := chan.new[int](4)\n    tx := rx.sender()\n    tx.send(1)\n    var u ?*chan.Sender[int] = nil\n    match rx.recv() {\n        Got(n) => tx.send(n)\n        Closed => {}\n    }\n    if u == nil {\n    }\n    tx.close()\n    rx.close()\n}\n"
	path := filepath.Join(t.TempDir(), "main.mlc")
	os.WriteFile(path, []byte(src), 0o644)
	read := driver.Overlay(nil)
	for _, tc := range []struct{ at, hover string }{
		{"chan.«new[int]", "Allocates a channel"},
		{"rx.«sender()", "Creates an end that sends"},
		{"tx.«send(1)", "Moves `v` into the channel"},
		{"rx.«recv()", "Got(v)"},
		{"tx.«close()", "Closes the end"},
		{"?*chan.«Sender[int]", "Channel end that sends"},
	} {
		mark := strings.Index(tc.at, "«")
		off := strings.Index(src, strings.Replace(tc.at, "«", "", 1)) + mark
		prog := driver.Analyze(path, read)
		if prog == nil {
			t.Fatal("não analisou")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nada encontrado", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: o hover não tem %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
	}
}

// Mutex: hover em mutex.new, nos métodos (inclusive get, que empresta) e nos tipos.
func TestNavigationMutex(t *testing.T) {
	src := "import \"std/mutex\"\n\nfn main() {\n    m := mutex.new(1)\n    g := m.lock()\n    x := g.get()\n    *x += 1\n    var h ?*mutex.Guard[int] = nil\n    if h == nil {\n    }\n    g.unlock()\n    m.close()\n}\n"
	path := filepath.Join(t.TempDir(), "main.mlc")
	os.WriteFile(path, []byte(src), 0o644)
	read := driver.Overlay(nil)
	for _, tc := range []struct{ at, hover string }{
		{"mutex.«new(1)", "Allocates a mutex"},
		{"m.«lock()", "Waits for the lock"},
		{"g.«get()", "Borrows the value"},
		{"g.«unlock()", "Unlocks the mutex"},
		{"?*mutex.«Guard[int]", "A mutex's lock"},
	} {
		mark := strings.Index(tc.at, "«")
		off := strings.Index(src, strings.Replace(tc.at, "«", "", 1)) + mark
		prog := driver.Analyze(path, read)
		if prog == nil {
			t.Fatal("não analisou")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nada encontrado", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: o hover não tem %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
	}
}

// w.printf: hover com a documentação própria.
func TestNavigationWriterPrintf(t *testing.T) {
	src := "import \"std/io\"\n\nfn main() {\n    w := io.stdout()\n    w.printf(\"{}\\n\", 1)\n    w.flush()\n}\n"
	path := filepath.Join(t.TempDir(), "main.mlc")
	os.WriteFile(path, []byte(src), 0o644)
	read := driver.Overlay(nil)
	prog := driver.Analyze(path, read)
	if prog == nil {
		t.Fatal("não analisou")
	}
	off := strings.Index(src, "printf(")
	sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
	if sym == nil || !strings.Contains(sym.doc, "without creating another and without a `flush`") {
		t.Errorf("hover de w.printf: %+v", sym)
	}
}
