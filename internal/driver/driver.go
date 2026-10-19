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
	csrc, info, err := compileInfo(file, src, read, opts)
	if err != nil {
		return "", nil, err
	}
	return csrc, info.Links, nil
}

// compileInfo é compile devolvendo o resultado da checagem. Um arquivo
// _test.mlc vira um programa de teste (veja BuildTest).
func compileInfo(file string, src []byte, read Loader, opts Options) (string, *checker.Info, error) {
	srcs, errs := load(file, src, read, opts.ImportDirs...)
	if len(errs) > 0 {
		return "", nil, Errors(errs)
	}
	srcs[len(srcs)-1].Test = IsTestFile(file)
	info, errs := checker.CheckProgram(srcs)
	if len(errs) > 0 {
		return "", nil, Errors(errs)
	}
	return codegen.Generate(file, srcs[len(srcs)-1].AST, info), info, nil
}

// IsTestFile diz se file é um arquivo de teste (nome_test.mlc): em vez de
// main, ele declara funções fn test_*(), que o mlc test roda.
func IsTestFile(file string) bool {
	return strings.HasSuffix(filepath.Base(file), "_test.mlc")
}

// extraCFlags é acrescentado à linha do compilador C. Os testes usam
// -Werror: aviso no C gerado é bug do mlc.
var extraCFlags []string

// Options ajusta a construção do executável.
type Options struct {
	LibDirs    []string // -L: pastas onde procurar as bibliotecas de C de link "nome"
	ImportDirs []string // -I: pastas onde procurar os imports (além de lib/ e MLC_PATH)
	CFlags     []string // -cflags: flags a mais para a compilação do C (depois das do ambiente, CFLAGS)
	LDFlags    []string // -ldflags: flags a mais para a ligação (depois das do ambiente, LDFLAGS)
}

// SplitFlags separa uma lista de flags escrita como texto ("-O3
// -march=native") pelos espaços. Não há aspas: uma flag com espaço dentro
// não cabe aqui.
func SplitFlags(s string) []string {
	return strings.Fields(s)
}

// Build compila src até um executável em out. O compilador C é o da
// variável CC, ou cc. As flags do C são as do mlc, depois as das variáveis
// CFLAGS e LDFLAGS, depois as de Options (que, por virem por último, ganham
// numa contradição, como -O3 depois do -O2 do mlc).
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
type LinkError struct {
	Output string
	Flags  string // as flags de ligação de quem compila (LDFLAGS e -ldflags), se houver
}

func (e *LinkError) Error() string {
	msg := "não foi possível ligar o programa: uma biblioteca de C (link \"...\") ou uma função de C (extern \"C\" fn) não foi encontrada"
	if e.Flags != "" {
		msg += ", ou uma das flags de ligação (" + e.Flags + ") não vale nesse compilador"
	}
	return msg + "\n" + e.Output
}

// BuildOpts é BuildWith com opções. O C é compilado em duas etapas: a
// compilação (uma falha aí é bug do mlc, porque o checker aceitou o
// programa) e a ligação com as bibliotecas (uma falha aí é do programa).
func BuildOpts(file string, src []byte, out string, read Loader, opts Options) error {
	csrc, info, err := compileInfo(file, src, read, opts)
	if err != nil {
		return err
	}
	return buildC(csrc, info.Links, out, opts)
}

// TestProgram são os nomes dos testes e dos benchmarks de um programa de
// teste, na ordem do código.
type TestProgram struct {
	Tests   []string
	Benches []string
}

// BuildTest compila o arquivo de teste file até um executável em out. O
// executável roda o teste de índice i com o argumento i (veja RunTest) e o
// benchmark i com "bench i n" (veja RunBench).
func BuildTest(file string, src []byte, out string, read Loader, opts Options) (TestProgram, error) {
	var p TestProgram
	if !IsTestFile(file) {
		return p, fmt.Errorf("%s: um arquivo de teste termina em _test.mlc", file)
	}
	csrc, info, err := compileInfo(file, src, read, opts)
	if err != nil {
		return p, err
	}
	if err := buildC(csrc, info.Links, out, opts); err != nil {
		return p, err
	}
	for _, fn := range info.Tests {
		p.Tests = append(p.Tests, fn.Name)
	}
	for _, fn := range info.Benches {
		p.Benches = append(p.Benches, fn.Name)
	}
	return p, nil
}

// buildC compila e liga o C gerado com o runtime.
func buildC(csrc string, links []string, out string, opts Options) error {

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
	// as flags de quem compila: do ambiente, depois da linha de comando
	userC := append(SplitFlags(os.Getenv("CFLAGS")), opts.CFlags...)
	userLD := append(SplitFlags(os.Getenv("LDFLAGS")), opts.LDFlags...)
	var objs []string
	for _, name := range []string{"program", "mlc_runtime"} {
		obj := filepath.Join(dir, name+".o")
		args := append([]string{"-std=c11", "-O2", "-Wall", "-Wno-unused-function", "-pthread"}, extraCFlags...)
		args = append(args, userC...)
		args = append(args, "-I", dir, "-c", "-o", obj, filepath.Join(dir, name+".c"))
		output, err := exec.Command(cc, args...).CombinedOutput()
		if err != nil {
			if len(userC) > 0 {
				// com flags de quem compila, a falha pode ser delas
				return fmt.Errorf("o compilador C (%s) recusou a compilação com as flags %q (de CFLAGS e -cflags): confira se elas existem nesse compilador; sem elas, seria um erro interno do mlc\n%s", cc, strings.Join(userC, " "), output)
			}
			// Se o checker aceitou o programa, o C gerado deveria compilar:
			// uma falha aqui é bug do mlc, não do programa do usuário.
			return fmt.Errorf("erro interno: %s rejeitou o código gerado: %v\n%s", cc, err, output)
		}
		if len(output) > 0 {
			if len(userC) > 0 {
				fmt.Fprintf(os.Stderr, "mlc: aviso do compilador C (com as flags %q):\n%s", strings.Join(userC, " "), output)
			} else {
				fmt.Fprintf(os.Stderr, "mlc: aviso do compilador C (provável bug do mlc):\n%s", output)
			}
		}
		objs = append(objs, obj)
	}
	args := append([]string{"-pthread", "-o", out}, objs...)
	for _, d := range opts.LibDirs {
		args = append(args, "-L", d)
	}
	args = append(args, userLD...)
	for _, l := range links {
		args = append(args, "-l"+l)
	}
	args = append(args, "-lm") // libm, para std/math
	if output, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
		return &LinkError{Output: string(output), Flags: strings.Join(userLD, " ")}
	}
	return nil
}
