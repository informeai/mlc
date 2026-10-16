package driver

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnoseLooseModule(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "util.mlc")
	read := Overlay(map[string][]byte{file: []byte("pub fn dobro(x int) int {\n    return x * 2\n}\n")})
	if errs := Diagnose(file, read); len(errs) != 0 {
		t.Fatalf("módulo sem erros não deveria ter diagnósticos: %v", errs)
	}
	read = Overlay(map[string][]byte{file: []byte("pub fn dobro(x int) int {\n    return y\n}\n")})
	errs := Diagnose(file, read)
	if len(errs) != 1 || errs[0].Pos.Line != 2 || !strings.Contains(errs[0].Msg, "y") {
		t.Fatalf("esperado erro na linha 2: %v", errs)
	}
}
