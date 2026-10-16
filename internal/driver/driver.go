// Package driver encadeia as fases do compilador e chama o compilador C.
package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mlc/internal/checker"
	"mlc/internal/codegen"
	"mlc/internal/token"
	"mlc/rt"
)

// Errors é a lista de erros de compilação de um arquivo.
type Errors []token.Error

func (e Errors) Error() string {
	lines := make([]string, len(e))
	for i, err := range e {
		lines[i] = err.Error()
	}
	return strings.Join(lines, "\n")
}

// Compile traduz o programa cujo arquivo principal é file para C. Os imports
// são lidos do disco, relativos à pasta de file.
func Compile(file string, src []byte) (string, error) {
	return CompileWith(file, src, os.ReadFile)
}

// CompileWith é Compile com outra forma de ler os arquivos importados.
func CompileWith(file string, src []byte, read Loader) (string, error) {
	srcs, errs := load(file, src, read)
	if len(errs) > 0 {
		return "", Errors(errs)
	}
	info, errs := checker.CheckProgram(srcs)
	if len(errs) > 0 {
		return "", Errors(errs)
	}
	return codegen.Generate(file, srcs[len(srcs)-1].AST, info), nil
}

// extraCFlags é acrescentado à linha do compilador C. Os testes usam
// -Werror: aviso no C gerado é bug do mlc.
var extraCFlags []string

// Build compila src até um executável em out. O compilador C é o da
// variável CC, ou cc.
func Build(file string, src []byte, out string) error {
	return BuildWith(file, src, out, os.ReadFile)
}

// BuildWith é Build com outra forma de ler os arquivos importados.
func BuildWith(file string, src []byte, out string, read Loader) error {
	csrc, err := CompileWith(file, src, read)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "mlc-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	files := map[string]string{
		"program.c":     csrc,
		"mlc_runtime.h": rt.Header,
		"mlc_runtime.c": rt.Source,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}

	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	args := append([]string{"-std=c11", "-O2", "-Wall", "-Wno-unused-function"}, extraCFlags...)
	args = append(args, "-I", dir, "-o", out,
		filepath.Join(dir, "program.c"),
		filepath.Join(dir, "mlc_runtime.c"),
	)
	cmd := exec.Command(cc, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Se o checker aceitou o programa, o C gerado deveria compilar:
		// uma falha aqui é bug do mlc, não do programa do usuário.
		return fmt.Errorf("erro interno: %s rejeitou o código gerado: %v\n%s", cc, err, output)
	}
	if len(output) > 0 {
		fmt.Fprintf(os.Stderr, "mlc: aviso do compilador C (provável bug do mlc):\n%s", output)
	}
	return nil
}
