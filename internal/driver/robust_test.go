package driver

import (
	"os"
	"path/filepath"
	"testing"

	"mlc/internal/format"
	"mlc/internal/parser"
)

// Fase 5 (robustez): o compilador nunca dá panic, seja qual for o texto. O
// LSP checa o arquivo a cada tecla, sobre código pela metade, então um
// panic aqui derruba o editor. As sementes são os programas da suíte e a
// biblioteca padrão; go test -fuzz muta os bytes a partir delas
// (make robust).

// seeds acrescenta ao corpus os .mlc da suíte e da std.
func seeds(f *testing.F) {
	var files []string
	for _, pat := range []string{"run/*.mlc", "run/*/*.mlc", "panic/*.mlc", "errors/*.mlc", "testing/*.mlc", "ffi/*.mlc"} {
		m, _ := filepath.Glob(filepath.Join(testsDir, pat))
		files = append(files, m...)
	}
	std, _ := filepath.Glob("../../std/*.mlc")
	for _, path := range append(files, std...) {
		if b, err := os.ReadFile(path); err == nil {
			f.Add(b)
		}
	}
}

// FuzzCheck: parser, checker (tipos e posse) e gerador de C não dão panic.
// Os imports da std resolvem (embutidos); os outros não existem.
func FuzzCheck(f *testing.F) {
	seeds(f)
	read := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	f.Fuzz(func(t *testing.T, src []byte) {
		if _, _, err := Check("fuzz.mlc", src, read, Options{}); err == nil {
			CompileWith("fuzz.mlc", src, read) // aceito: o gerador de C também roda
		}
		Check("fuzz_test.mlc", src, read, Options{}) // como arquivo de teste
	})
}

// FuzzFormat: o formatador não dá panic, e formatar de novo não muda nada
// (o resultado já está na forma canônica) nem cria erro de sintaxe.
func FuzzFormat(f *testing.F) {
	seeds(f)
	f.Fuzz(func(t *testing.T, src []byte) {
		out, errs := format.Source("fuzz.mlc", src)
		if len(errs) > 0 {
			return
		}
		again, errs := format.Source("fuzz.mlc", out)
		if len(errs) > 0 {
			t.Fatalf("o texto formatado não passa no parser: %v\n--- entrada\n%s\n--- formatado\n%s", errs[0], src, out)
		}
		if string(again) != string(out) {
			t.Fatalf("formatar duas vezes muda o texto\n--- uma vez\n%s\n--- duas vezes\n%s", out, again)
		}
		if _, perrs := parser.ParseFile("fuzz.mlc", out); len(perrs) > 0 {
			t.Fatalf("o texto formatado não passa no parser: %v", perrs[0])
		}
	})
}
