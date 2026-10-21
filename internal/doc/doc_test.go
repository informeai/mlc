package doc

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mlc/std"
)

func render(t *testing.T, dirs []string, all bool, args ...string) string {
	t.Helper()
	q, err := Lookup(args, dirs, all)
	if err != nil {
		t.Fatalf("mlc doc %s: %v", strings.Join(args, " "), err)
	}
	var b strings.Builder
	q.Write(&b)
	return b.String()
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestStdModule(t *testing.T) {
	out := render(t, nil, false, "http")
	contains(t, out,
		`import "std/http"`,
		"    HTTP/1.1 over any Stream",
		"\nConstants\n", "const TOO_BIG = 1000",
		"\nTypes\n", "  struct Message\n", "      fn (m &Message) read_body[S ref + Stream](c &S, dst &[]byte) result.Result[int, int]",
		"\nFunctions\n", "  fn respond_str[S ref + Stream](c &S, status int, content_type string, body string) result.Result[int, int]",
		"impl Stream for net.Conn")
	if strings.Contains(out, "pub ") || strings.Contains(out, "fn end_head") {
		t.Errorf("showed pub or a method that isn't pub:\n%s", out)
	}
	if render(t, nil, false, "std/http") != out {
		t.Error("std/http and http should give the same result")
	}
}

func TestSymbols(t *testing.T) {
	contains(t, render(t, nil, false, "http.Message"),
		"struct Message {\n    buf [8192]byte", "    Message is a received request", "\nMethods\n",
		"  fn (m &Message) keep_alive() bool\n      keep_alive")
	contains(t, render(t, nil, false, "http.Message.read_body"),
		"fn (m &Message) read_body", "    read_body reads up to len(dst) bytes")
	contains(t, render(t, nil, false, "http", "message.READ_BODY"), "fn (m &Message) read_body")
	contains(t, render(t, nil, false, "http.respond"), "fn respond[S ref + Stream]", "    respond sends")
	contains(t, render(t, nil, false, "http.Stream"), "interface Stream {", "fn write(self &Self")
	contains(t, render(t, nil, false, "result.Result"), "enum Result[T owned, E owned] {", "Ok(value T)")
	contains(t, render(t, nil, false, "io.write"), "fn write(s &[]byte)") // extern fn: without the extern
}

func TestBuiltins(t *testing.T) {
	contains(t, render(t, nil, false, "alloc"), "alloc(T) *T", "    Allocates on the heap.")
	contains(t, render(t, nil, false, "io.printf"), `io.printf("x = {}\n", x)`)
	out := render(t, nil, false, "thread.spawn")
	contains(t, out, "thread.spawn(f fn(C) R", "is moved to the thread")
	if strings.Contains(out, "**") {
		t.Errorf("the markdown bold should go away:\n%s", out)
	}
	contains(t, render(t, nil, false, "log.Logger.info"), `lg.info("...", a, ...)`)
}

func TestUserModule(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "util"), 0o755)
	os.WriteFile(filepath.Join(dir, "util", "text.mlc"), []byte(`// The project's text.
import "std/str"

// Name keeps a name.
pub struct Name {
    buf [64]byte
    n int
}

// set copies s.
pub fn (x &Name) set(s string) {
    x.n = str.copy(&x.buf, s)
}

// shout writes s into dst.
pub fn shout(dst &[]byte, s string) int {
    return internal_(dst, s)
}

// internal_ isn't pub.
fn internal_(dst &[]byte, s string) int {
    return str.copy(dst, s)
}
`), 0o644)
	dirs := []string{dir}
	out := render(t, dirs, false, "util/text")
	contains(t, out, `import "util/text"`, "    The project's text.", "  struct Name\n      fn (x &Name) set(s string)", "  fn shout(")
	if strings.Contains(out, "internal_") {
		t.Errorf("showed a function that isn't pub:\n%s", out)
	}
	contains(t, render(t, dirs, false, "util/text.shout"), "    shout writes s into dst.")
	if _, err := Lookup([]string{"util/text.internal_"}, dirs, false); err == nil {
		t.Error("internal_ isn't pub and should give an error")
	}
	contains(t, render(t, dirs, true, "util/text.internal_"), "    internal_ isn't pub.")
	// a file with nothing pub (a program) shows everything
	main := filepath.Join(dir, "app.mlc")
	os.WriteFile(main, []byte("// sum adds two numbers.\nfn sum(a int, b int) int {\n    return a + b\n}\n\nfn main() {\n}\n"), 0o644)
	contains(t, render(t, nil, false, main), "fn sum(a int, b int) int", "fn main()")
	contains(t, render(t, nil, false, main, "sum"), "    sum adds two numbers.")
}

func TestErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"nothing"}, `module "nothing" not found`},
		{[]string{"std/nothing"}, `the standard library has no module "std/nothing"`},
		{[]string{"http.nothing"}, `std/http has no "nothing"`},
		{[]string{"http.Message", "x"}, "usage: mlc doc"},
	} {
		_, err := Lookup(c.args, nil, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("mlc doc %v: error %v, wanted %q", c.args, err, c.want)
		}
	}
}

// Every standard library module has a description, and every pub function, type and
// method has a comment: it's what mlc doc shows.
func TestStdDocumented(t *testing.T) {
	entries, _ := fs.ReadDir(std.FS, ".")
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".mlc") || strings.HasSuffix(name, "_test.mlc") {
			continue
		}
		text, _ := std.FS.ReadFile(name)
		m, err := Parse(name, text)
		if err != nil {
			t.Fatal(err)
		}
		if len(Header(text)) == 0 {
			t.Errorf("%s: no description at the top of the file", name)
		}
		for _, d := range m.File.Funcs {
			if d.Pub && len(commentAt(text, d.P.Line)) == 0 {
				t.Errorf("%s:%d: fn %s without a comment", name, d.P.Line, d.Name)
			}
		}
		for _, d := range m.File.Structs {
			if d.Pub && len(commentAt(text, d.P.Line)) == 0 {
				t.Errorf("%s:%d: struct %s without a comment", name, d.P.Line, d.Name)
			}
		}
	}
}

func TestIndex(t *testing.T) {
	var b strings.Builder
	WriteIndex(&b)
	contains(t, b.String(), "  http       HTTP/1.1", "  vec        Growing vector.", "mlc doc builtin")
	for _, l := range strings.Split(b.String(), "\n") {
		if n := len([]rune(l)); n > 80 {
			t.Errorf("line with %d columns: %s", n, l)
		}
	}
}
