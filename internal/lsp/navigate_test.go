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

// Shape of the drawing
enum Shape {
    Circle(radius float64)
    Empty
}

struct Crate {
    width int // in cm
}

const LIMIT = 10

// area computes the area
fn area(f Shape) float64 {
    match f {
        Circle(r) => return r * r
        Empty => return 0.0
    }
}

fn (c &Crate) double() int {
    return c.width * 2
}

fn main() {
    c := Crate{width: LIMIT}
    v := vec.new[int](2)
    v.push(c.double())
    p := geo.Point{x: 1, y: 2}
    io.print(area(Shape.Circle(1.5)), geo.sum(p), v.len(), len("ab"))
    v.free()
}
`

const navGeo = `// Plane geometry.

pub struct Point {
    x int
    y int
}

// sum returns x + y.
pub fn sum(p Point) int {
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
		at      string // the cursor goes at the start of the piece marked with « »
		defFile string // "main", "geo", "std/x.mlc" or "" (no definition)
		defLine int
		hover   []string
	}{
		{"main", "io.print(«area(Shape", "main", 18, []string{"fn area(f Shape) float64", "area computes the area"}},
		{"main", "area(Shape.«Circle(1.5)", "main", 7, []string{"Circle(radius float64)", "variant of `Shape`"}},
		{"main", "area(«Shape.Circle", "main", 6, []string{"enum Shape {", "Empty", "Shape of the drawing"}},
		{"main", "return «r * r", "main", 20, []string{"var r float64"}},
		{"main", "        «Circle(r) =>", "main", 7, []string{"variant of `Shape`"}},
		{"main", "return c.«width * 2", "main", 12, []string{"width int", "field of `Crate`"}},
		{"main", "Crate{«width: LIMIT", "main", 12, []string{"width int"}},
		{"main", "width: «LIMIT}", "main", 15, []string{"const LIMIT = 10"}},
		{"main", "v.push(c.«double())", "main", 25, []string{"fn (c &Crate) double() int"}},
		{"main", "c := «Crate{", "main", 11, []string{"struct Crate {", "width int // in cm"}},
		{"main", "v := vec.«new[int]", "std/vec.mlc", 0, []string{"pub fn new[T owned](cap int) *Vec[T]", "instance: `vec.new[int]`"}},
		{"main", "v.«push(c", "std/vec.mlc", 0, []string{"push(x T)"}},
		{"main", "p := geo.«Point{", "geo", 3, []string{"pub struct Point {"}},
		{"main", "geo.«sum(p)", "geo", 9, []string{"pub fn sum(p Point) int", "sum returns x + y."}},
		{"main", "geo.sum(«p)", "main", 33, []string{"var p geo.Point"}},
		{"main", "p := «geo.Point", "geo", 1, []string{`import "geo"`, "Plane geometry."}},
		{"main", `import «"std/io"`, "std/io.mlc", 1, []string{`import "std/io"`, "Standard input and output"}},
		{"main", "«io.print(area", "std/io.mlc", 1, []string{`import "std/io"`}},
		{"main", "io.«print(area", "std/io.mlc", 1, []string{"io.print(a, b, ...)", "The compiler only rewrites the call"}},
		{"main", "v.len(), «len(\"ab\")", "", 0, []string{"len(s) int"}},
		{"main", "vec.new[«int](2)", "", 0, []string{"64-bit signed integer"}},
		{"main", "fn «area(f Shape)", "main", 18, []string{"fn area(f Shape) float64"}},
		{"main", "fn area(«f Shape)", "main", 18, []string{"f Shape", "parameter"}},
		{"geo", "return p.«x + p.y", "geo", 4, []string{"x int", "field of `geo.Point`"}},
		{"geo", "pub fn sum(p «Point)", "geo", 3, []string{"pub struct Point {"}},
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
			t.Fatalf("piece not found: %q", needle)
		}
		off := pos + mark
		prog := driver.Analyze(path, read)
		if prog == nil {
			t.Fatalf("%s: didn't analyze", path)
		}
		nav := newNavigator(prog, read, []byte(src))
		sym, _ := nav.resolve(off)
		if sym == nil {
			t.Errorf("%q: nothing found", tc.at)
			continue
		}
		hover := sym.code + "\n" + sym.doc
		for _, h := range tc.hover {
			if !strings.Contains(hover, h) {
				t.Errorf("%q: the hover doesn't have %q:\n%s", tc.at, h, hover)
			}
		}
		switch tc.defFile {
		case "":
			if !sym.noDef {
				t.Errorf("%q: shouldn't have a definition (%s:%d)", tc.at, sym.file, sym.pos.Line)
			}
			continue
		case "main":
			tc.defFile = mainPath
		case "geo":
			tc.defFile = geoPath
		}
		// line 1: the start of the file (position zero or the first line)
		if sym.file != tc.defFile || (tc.defLine > 1 && sym.pos.Line != tc.defLine) || (tc.defLine == 1 && sym.pos.Line > 1) {
			t.Errorf("%q: definition at %s:%d, expected %s:%d", tc.at, sym.file, sym.pos.Line, tc.defFile, tc.defLine)
		}
	}
}

func TestStdPath(t *testing.T) {
	p := stdPath("std/vec.mlc")
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "pub struct Vec") {
		t.Fatalf("std/vec.mlc wasn't written in %s: %v", p, err)
	}
}

// TestNavigationEverywhere resolves each identifier of the project's
// programs (and of versions with a type error): nothing may panic, and almost
// every name must lead to some declaration.
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
					t.Logf("no declaration: %s:%d %s", filepath.Base(file), tk.Pos.Line, tk.Lit)
				}
			}
		}
	}
	t.Logf("%d of %d identifiers resolved", found, total)
	if total == 0 || found*100/total < 99 {
		t.Fatalf("too few names resolved: %d of %d", found, total)
	}
}

const navIface = `import "std/io"

// Measure gives a size
interface Measure {
    fn size(self &Self) int
}

struct Crate {
    l int
}

fn (c &Crate) size() int {
    return c.l
}

impl Measure for Crate

impl auto io.Display for Crate

fn larger[T Measure](a &T) int {
    return a.size()
}

fn main() {
    c := Crate{l: 2}
    io.printf("{} {}\n", c, larger(&c))
}
`

func TestNavigationInterfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.mlc")
	os.WriteFile(path, []byte(navIface), 0o644)
	read := driver.Overlay(nil)
	prog := driver.Analyze(path, read)
	if prog == nil {
		t.Fatal("didn't analyze")
	}
	nav := newNavigator(prog, read, []byte(navIface))
	cases := []struct {
		at, file string
		line     int
		hover    string
	}{
		{"impl «Measure for Crate", path, 4, "Measure gives a size"},
		{"fn larger[T «Measure]", path, 4, "interface Measure {"},
		{"return a.«size()", path, 5, "fn size(self &Self) int"},
		{"impl auto io.«Display for", "std/io.mlc", 0, "pub interface Display {"},
		{"impl «auto io", "", 0, "impl auto io.Display for T"},
		{"fn size(self &«Self)", "", 0, "the type that implements it"},
		{"io.«printf(", "std/io.mlc", 1, "replacing each `{}` with a value"},
	}
	for _, tc := range cases {
		mark := strings.Index(tc.at, "«")
		needle := strings.Replace(tc.at, "«", "", 1)
		pos := strings.Index(navIface, needle)
		if pos < 0 {
			t.Fatalf("piece not found: %q", needle)
		}
		sym, _ := nav.resolve(pos + mark)
		if sym == nil {
			t.Errorf("%q: nothing found", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: the hover doesn't have %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
		if tc.file == "" {
			if !sym.noDef {
				t.Errorf("%q: shouldn't have a definition", tc.at)
			}
		} else if sym.file != tc.file || (tc.line > 1 && sym.pos.Line != tc.line) {
			t.Errorf("%q: definition at %s:%d, expected %s:%d", tc.at, sym.file, sym.pos.Line, tc.file, tc.line)
		}
	}
}

// str.format and str.format_len: hover with the documentation and the definition in std/str.
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
			t.Fatal("didn't analyze")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nothing found", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: the hover doesn't have %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
		if !strings.HasSuffix(filepath.ToSlash(sym.file), tc.file) {
			t.Errorf("%q: definition at %s, expected %s", tc.at, sym.file, tc.file)
		}
	}
}

// thread.spawn and thread.join: hover with the documentation and the definition in std/thread.
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
			t.Fatal("didn't analyze")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nothing found", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: the hover doesn't have %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
	}
}

// Channels: hover on the ends' methods, on chan.new and on the types.
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
			t.Fatal("didn't analyze")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nothing found", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: the hover doesn't have %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
	}
}

// Mutex: hover on mutex.new, on the methods (including get, which borrows) and on the types.
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
			t.Fatal("didn't analyze")
		}
		sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
		if sym == nil {
			t.Errorf("%q: nothing found", tc.at)
			continue
		}
		if !strings.Contains(sym.code+"\n"+sym.doc, tc.hover) {
			t.Errorf("%q: the hover doesn't have %q:\n%s\n%s", tc.at, tc.hover, sym.code, sym.doc)
		}
	}
}

// w.printf: hover with its own documentation.
func TestNavigationWriterPrintf(t *testing.T) {
	src := "import \"std/io\"\n\nfn main() {\n    w := io.stdout()\n    w.printf(\"{}\\n\", 1)\n    w.flush()\n}\n"
	path := filepath.Join(t.TempDir(), "main.mlc")
	os.WriteFile(path, []byte(src), 0o644)
	read := driver.Overlay(nil)
	prog := driver.Analyze(path, read)
	if prog == nil {
		t.Fatal("didn't analyze")
	}
	off := strings.Index(src, "printf(")
	sym, _ := newNavigator(prog, read, []byte(src)).resolve(off)
	if sym == nil || !strings.Contains(sym.doc, "without creating another and without a `flush`") {
		t.Errorf("w.printf's hover: %+v", sym)
	}
}
