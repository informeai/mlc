package driver

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnoseLooseModule(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "util.mlc")
	read := Overlay(map[string][]byte{file: []byte("pub fn double(x int) int {\n    return x * 2\n}\n")})
	if errs := Diagnose(file, read); len(errs) != 0 {
		t.Fatalf("a module without errors shouldn't have diagnostics: %v", errs)
	}
	read = Overlay(map[string][]byte{file: []byte("pub fn double(x int) int {\n    return y\n}\n")})
	errs := Diagnose(file, read)
	if len(errs) != 1 || errs[0].Pos.Line != 2 || !strings.Contains(errs[0].Msg, "y") {
		t.Fatalf("expected an error on line 2: %v", errs)
	}
}

// A _test.mlc is checked as a test program: without requiring main, and with
// a test's own errors.
func TestDiagnoseTestFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "count_test.mlc")
	ok := "import \"std/testing\"\n\nfn test_soma() {\n    testing.eq_int(1 + 1, 2)\n}\n"
	if errs := Diagnose(file, Overlay(map[string][]byte{file: []byte(ok)})); len(errs) != 0 {
		t.Fatalf("a test file without errors shouldn't have diagnostics: %v", errs)
	}
	bad := "fn test_soma(x int) {\n}\n"
	errs := Diagnose(file, Overlay(map[string][]byte{file: []byte(bad)}))
	if len(errs) != 1 || !strings.Contains(errs[0].Msg, "takes no parameters") {
		t.Fatalf("expected the error of the test with a parameter: %v", errs)
	}
	if p := Analyze(file, Overlay(map[string][]byte{file: []byte(ok)})); p == nil || len(p.Info.Tests) != 1 {
		t.Fatalf("Analyze should find the test")
	}
}
