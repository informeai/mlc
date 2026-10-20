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
			t.Errorf("faltou %q em:\n%s", w, out)
		}
	}
}

func TestStdModule(t *testing.T) {
	out := render(t, nil, false, "http")
	contains(t, out,
		`import "std/http"`,
		"    HTTP/1.1 over any Stream",
		"\nConstantes\n", "const TOO_BIG = 1000",
		"\nTipos\n", "  struct Message\n", "      fn (m &Message) read_body[S ref + Stream](c &S, dst &[]byte) result.Result[int, int]",
		"\nFunções\n", "  fn respond_str[S ref + Stream](c &S, status int, content_type string, body string) result.Result[int, int]",
		"impl Stream for net.Conn")
	if strings.Contains(out, "pub ") || strings.Contains(out, "fn end_head") {
		t.Errorf("mostrou pub ou um método que não é pub:\n%s", out)
	}
	if render(t, nil, false, "std/http") != out {
		t.Error("std/http e http deviam dar o mesmo")
	}
}

func TestSymbols(t *testing.T) {
	contains(t, render(t, nil, false, "http.Message"),
		"struct Message {\n    buf [8192]byte", "    Message is a received request", "\nMétodos\n",
		"  fn (m &Message) keep_alive() bool\n      keep_alive")
	contains(t, render(t, nil, false, "http.Message.read_body"),
		"fn (m &Message) read_body", "    read_body reads up to len(dst) bytes")
	contains(t, render(t, nil, false, "http", "message.READ_BODY"), "fn (m &Message) read_body")
	contains(t, render(t, nil, false, "http.respond"), "fn respond[S ref + Stream]", "    respond sends")
	contains(t, render(t, nil, false, "http.Stream"), "interface Stream {", "fn write(self &Self")
	contains(t, render(t, nil, false, "result.Result"), "enum Result[T owned, E owned] {", "Ok(value T)")
	contains(t, render(t, nil, false, "io.write"), "fn write(s &[]byte)") // extern fn: sem o extern
}

func TestBuiltins(t *testing.T) {
	contains(t, render(t, nil, false, "alloc"), "alloc(T) *T", "    Allocates on the heap.")
	contains(t, render(t, nil, false, "io.printf"), `io.printf("x = {}\n", x)`)
	out := render(t, nil, false, "thread.spawn")
	contains(t, out, "thread.spawn(f fn(C) R", "is moved to the thread")
	if strings.Contains(out, "**") {
		t.Errorf("o negrito do markdown devia sair:\n%s", out)
	}
	contains(t, render(t, nil, false, "log.Logger.info"), `lg.info("...", a, ...)`)
}

func TestUserModule(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "util"), 0o755)
	os.WriteFile(filepath.Join(dir, "util", "texto.mlc"), []byte(`// Texto do projeto.
import "std/str"

// Nome guarda um nome.
pub struct Nome {
    buf [64]byte
    n int
}

// set copia s.
pub fn (x &Nome) set(s string) {
    x.n = str.copy(&x.buf, s)
}

// grita escreve s em dst.
pub fn grita(dst &[]byte, s string) int {
    return interna(dst, s)
}

// interna não é pub.
fn interna(dst &[]byte, s string) int {
    return str.copy(dst, s)
}
`), 0o644)
	dirs := []string{dir}
	out := render(t, dirs, false, "util/texto")
	contains(t, out, `import "util/texto"`, "    Texto do projeto.", "  struct Nome\n      fn (x &Nome) set(s string)", "  fn grita(")
	if strings.Contains(out, "interna") {
		t.Errorf("mostrou uma função que não é pub:\n%s", out)
	}
	contains(t, render(t, dirs, false, "util/texto.grita"), "    grita escreve s em dst.")
	if _, err := Lookup([]string{"util/texto.interna"}, dirs, false); err == nil {
		t.Error("interna não é pub e devia dar erro")
	}
	contains(t, render(t, dirs, true, "util/texto.interna"), "    interna não é pub.")
	// um arquivo sem nada pub (um programa) mostra tudo
	main := filepath.Join(dir, "app.mlc")
	os.WriteFile(main, []byte("// soma dois números.\nfn soma(a int, b int) int {\n    return a + b\n}\n\nfn main() {\n}\n"), 0o644)
	contains(t, render(t, nil, false, main), "fn soma(a int, b int) int", "fn main()")
	contains(t, render(t, nil, false, main, "soma"), "    soma dois números.")
}

func TestErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"nada"}, `módulo "nada" não encontrado`},
		{[]string{"std/nada"}, `a biblioteca padrão não tem o módulo "std/nada"`},
		{[]string{"http.nada"}, `std/http não tem "nada"`},
		{[]string{"http.Message", "x"}, "uso: mlc doc"},
	} {
		_, err := Lookup(c.args, nil, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("mlc doc %v: erro %v, queria %q", c.args, err, c.want)
		}
	}
}

// Todo módulo da biblioteca padrão tem descrição, e toda função, tipo e
// método pub tem comentário: é o que o mlc doc mostra.
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
			t.Errorf("%s: sem descrição no começo do arquivo", name)
		}
		for _, d := range m.File.Funcs {
			if d.Pub && len(commentAt(text, d.P.Line)) == 0 {
				t.Errorf("%s:%d: fn %s sem comentário", name, d.P.Line, d.Name)
			}
		}
		for _, d := range m.File.Structs {
			if d.Pub && len(commentAt(text, d.P.Line)) == 0 {
				t.Errorf("%s:%d: struct %s sem comentário", name, d.P.Line, d.Name)
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
			t.Errorf("linha com %d colunas: %s", n, l)
		}
	}
}
