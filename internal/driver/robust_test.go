package driver

import (
	"os"
	"path/filepath"
	"testing"

	"mlc/internal/format"
	"mlc/internal/parser"
)

// Phase 5 (robustness): the compiler never panics, whatever the text. The
// LSP checks the file on every keystroke, over half-written code, so a
// panic here brings the editor down. The seeds are the suite's programs and the
// standard library; go test -fuzz mutates the bytes starting from them
// (make robust).

// seeds adds the .mlc files of the suite and of the std to the corpus.
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

// FuzzCheck: the parser, the checker (types and ownership) and the C generator don't panic.
// The std's imports resolve (built in); the others don't exist.
func FuzzCheck(f *testing.F) {
	seeds(f)
	read := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	f.Fuzz(func(t *testing.T, src []byte) {
		if _, _, err := Check("fuzz.mlc", src, read, Options{}); err == nil {
			CompileWith("fuzz.mlc", src, read) // accepted: the C generator runs too
		}
		Check("fuzz_test.mlc", src, read, Options{}) // as a test file
	})
}

// FuzzFormat: the formatter doesn't panic, and formatting again changes nothing
// (the result is already in canonical form) nor creates a syntax error.
func FuzzFormat(f *testing.F) {
	seeds(f)
	f.Fuzz(func(t *testing.T, src []byte) {
		out, errs := format.Source("fuzz.mlc", src)
		if len(errs) > 0 {
			return
		}
		again, errs := format.Source("fuzz.mlc", out)
		if len(errs) > 0 {
			t.Fatalf("the formatted text doesn't pass the parser: %v\n--- input\n%s\n--- formatted\n%s", errs[0], src, out)
		}
		if string(again) != string(out) {
			t.Fatalf("formatting twice changes the text\n--- once\n%s\n--- twice\n%s", out, again)
		}
		if _, perrs := parser.ParseFile("fuzz.mlc", out); len(perrs) > 0 {
			t.Fatalf("the formatted text doesn't pass the parser: %v", perrs[0])
		}
	})
}
