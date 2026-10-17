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
	csrc, _, err := compile(file, src, read, Options{})
	return csrc, err
}

// CompileOpts é CompileWith com opções (as pastas de -I).
func CompileOpts(file string, src []byte, read Loader, opts Options) (string, error) {
	csrc, _, err := compile(file, src, read, opts)
	return csrc, err
}

// compile traduz o programa para C e devolve também as bibliotecas de C
// pedidas com link "nome".
func compile(file string, src []byte, read Loader, opts Options) (string, []string, error) {
	srcs, errs := load(file, src, read, opts.ImportDirs...)
	if len(errs) > 0 {
		return "", nil, Errors(errs)
	}
	info, errs := checker.CheckProgram(srcs)
	if len(errs) > 0 {
		return "", nil, Errors(errs)
	}
	return codegen.Generate(file, srcs[len(srcs)-1].AST, info), info.Links, nil
}

// extraCFlags é acrescentado à linha do compilador C. Os testes usam
// -Werror: aviso no C gerado é bug do mlc.
var extraCFlags []string

// Options ajusta a construção do executável.
type Options struct {
	LibDirs    []string // -L: pastas onde procurar as bibliotecas de C de link "nome"
	ImportDirs []string // -I: pastas onde procurar os imports (além de lib/ e MLC_PATH)
}

// Build compila src até um executável em out. O compilador C é o da
// variável CC, ou cc.
func Build(file string, src []byte, out string) error {
	return BuildWith(file, src, out, os.ReadFile)
}

// BuildWith é Build com outra forma de ler os arquivos importados.
func BuildWith(file string, src []byte, out string, read Loader) error {
	return BuildOpts(file, src, out, read, Options{})
}

// LinkError é uma falha ao ligar o programa: uma biblioteca de C (link
// "nome") ou um símbolo (extern "C" fn) que não foi encontrado. É erro do
// programa, não do mlc.
type LinkError struct{ Output string }

func (e *LinkError) Error() string {
	return "não foi possível ligar o programa: uma biblioteca de C (link \"...\") ou uma função de C (extern \"C\" fn) não foi encontrada\n" + e.Output
}

// BuildOpts é BuildWith com opções. O C é compilado em duas etapas: a
// compilação (uma falha aí é bug do mlc, porque o checker aceitou o
// programa) e a ligação com as bibliotecas (uma falha aí é do programa).
func BuildOpts(file string, src []byte, out string, read Loader, opts Options) error {
	csrc, links, err := compile(file, src, read, opts)
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
	var objs []string
	for _, name := range []string{"program", "mlc_runtime"} {
		obj := filepath.Join(dir, name+".o")
		args := append([]string{"-std=c11", "-O2", "-Wall", "-Wno-unused-function"}, extraCFlags...)
		args = append(args, "-I", dir, "-c", "-o", obj, filepath.Join(dir, name+".c"))
		output, err := exec.Command(cc, args...).CombinedOutput()
		if err != nil {
			// Se o checker aceitou o programa, o C gerado deveria compilar:
			// uma falha aqui é bug do mlc, não do programa do usuário.
			return fmt.Errorf("erro interno: %s rejeitou o código gerado: %v\n%s", cc, err, output)
		}
		if len(output) > 0 {
			fmt.Fprintf(os.Stderr, "mlc: aviso do compilador C (provável bug do mlc):\n%s", output)
		}
		objs = append(objs, obj)
	}
	args := append([]string{"-o", out}, objs...)
	for _, d := range opts.LibDirs {
		args = append(args, "-L", d)
	}
	for _, l := range links {
		args = append(args, "-l"+l)
	}
	args = append(args, "-lm") // libm, para std/math
	if output, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
		return &LinkError{Output: string(output)}
	}
	return nil
}
