// Comando mlc: compilador da linguagem MLC (My Lang C).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mlc/internal/driver"
	"mlc/internal/format"
	"mlc/internal/lsp"
	"mlc/internal/token"
)

const usage = `mlc — compilador da linguagem MLC (My Lang C)

Uso:
  mlc build [-o saida] [-emit-c] [-I dir] [-L dir] [-cflags "..."] [-ldflags "..."] arquivo.mlc
                                               compila para um executável
  mlc run [-I dir] [-L dir] [-cflags "..."] [-ldflags "..."] arquivo.mlc [argumentos]
                                               compila e executa
      -I: pasta onde procurar os imports (além da do arquivo, de lib/ e de MLC_PATH)
      -L: pasta onde procurar as bibliotecas de C de link "nome"
      -cflags "...": flags a mais para compilar o C (-O3, -march=native, -fsanitize=address)
      -ldflags "...": flags a mais para ligar (-framework Cocoa, -Wl,-rpath,/opt/lib)
      (também valem as variáveis CC, CFLAGS e LDFLAGS; as opções vêm depois e ganham)
  mlc test [-v] [-run texto] [-bench texto] [-I dir] [-L dir] [-cflags "..."] [-ldflags "..."] [arquivos ou pastas]
                                               roda os testes dos arquivos *_test.mlc
                                               (padrão: a pasta atual e as de dentro)
      -v: mostra cada teste e a saída dele, mesmo quando passa
      -run: só os testes cujo nome contém o texto
      -bench texto: mede também os benchmarks (fn bench_*(b &testing.B)); . para todos
      -benchtime ms: quanto tempo medir cada benchmark (padrão 500)
  mlc fmt [-l] [arquivos ou pastas]            formata no lugar (padrão: a pasta atual)
  mlc fmt -                                    formata a entrada padrão na saída padrão
  mlc lsp                                      servidor de linguagem (LSP) para editores
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		os.Exit(build(os.Args[2:]))
	case "run":
		os.Exit(run(os.Args[2:]))
	case "test":
		os.Exit(testCmd(os.Args[2:]))
	case "fmt":
		os.Exit(fmtCmd(os.Args[2:]))
	case "lsp":
		os.Exit(lsp.Run(os.Stdin, os.Stdout))
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "mlc: comando desconhecido %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func readSource(path string) ([]byte, error) {
	if filepath.Ext(path) != ".mlc" {
		return nil, fmt.Errorf("%s: o arquivo precisa ter a extensão .mlc", path)
	}
	return os.ReadFile(path)
}

// readProgram é readSource para build e run: um _test.mlc é rodado com mlc test.
func readProgram(path string) ([]byte, error) {
	if driver.IsTestFile(path) {
		return nil, fmt.Errorf("%s: arquivo de teste; rode com mlc test %s", path, path)
	}
	return readSource(path)
}

func build(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "caminho do executável (padrão: nome do arquivo sem .mlc)")
	emitC := fs.Bool("emit-c", false, "imprime o C gerado em vez de compilar")
	var libDirs, importDirs dirList
	fs.Var(&libDirs, "L", "pasta onde procurar as bibliotecas de C (pode repetir)")
	fs.Var(&importDirs, "I", "pasta onde procurar os imports (pode repetir)")
	cflags, ldflags := ccFlags(fs)
	fs.Parse(args)
	opts := driver.Options{LibDirs: libDirs, ImportDirs: importDirs, CFlags: driver.SplitFlags(*cflags), LDFlags: driver.SplitFlags(*ldflags)}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := fs.Arg(0)
	src, err := readProgram(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if *emitC {
		csrc, err := driver.CompileOpts(path, src, os.ReadFile, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(csrc)
		return 0
	}

	if *out == "" {
		*out = strings.TrimSuffix(filepath.Base(path), ".mlc")
	}
	if err := driver.BuildOpts(path, src, *out, os.ReadFile, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var libDirs, importDirs dirList
	fs.Var(&libDirs, "L", "pasta onde procurar as bibliotecas de C (pode repetir)")
	fs.Var(&importDirs, "I", "pasta onde procurar os imports (pode repetir)")
	cflags, ldflags := ccFlags(fs)
	fs.Parse(args) // para no primeiro argumento que não é opção: o arquivo
	args = fs.Args()
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := args[0]
	src, err := readProgram(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir, err := os.MkdirTemp("", "mlc-run-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	exe := filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".mlc"))
	opts := driver.Options{LibDirs: libDirs, ImportDirs: importDirs, CFlags: driver.SplitFlags(*cflags), LDFlags: driver.SplitFlags(*ldflags)}
	if err := driver.BuildOpts(path, src, exe, os.ReadFile, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cmd := exec.Command(exe, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// testCmd compila cada arquivo _test.mlc num programa de teste e roda cada
// teste num processo separado. Devolve 1 se algum falhar.
func testCmd(args []string) int {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	verbose := fs.Bool("v", false, "mostra cada teste e a saída dele")
	only := fs.String("run", "", "só os testes cujo nome contém o texto")
	bench := fs.String("bench", "", "roda também os benchmarks cujo nome contém o texto (. para todos)")
	benchTime := fs.Int("benchtime", 500, "quanto tempo (ms) cada benchmark é medido, no mínimo")
	var libDirs, importDirs dirList
	fs.Var(&libDirs, "L", "pasta onde procurar as bibliotecas de C (pode repetir)")
	fs.Var(&importDirs, "I", "pasta onde procurar os imports (pode repetir)")
	cflags, ldflags := ccFlags(fs)
	fs.Parse(args)
	opts := driver.Options{LibDirs: libDirs, ImportDirs: importDirs, CFlags: driver.SplitFlags(*cflags), LDFlags: driver.SplitFlags(*ldflags)}
	bc := benchConfig{filter: *bench, target: time.Duration(*benchTime) * time.Millisecond}
	paths := fs.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d iofs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && driver.IsTestFile(path) {
				files = append(files, path)
			}
			return nil
		})
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "mlc test: nenhum arquivo *_test.mlc encontrado")
		return 1
	}
	dir, err := os.MkdirTemp("", "mlc-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	status := 0
	for n, file := range files {
		if !testFile(file, filepath.Join(dir, fmt.Sprintf("t%d", n)), *only, *verbose, bc, opts) {
			status = 1
		}
	}
	return status
}

// benchConfig diz quais benchmarks rodar (filter vazio: nenhum; ".": todos)
// e por quanto tempo medir cada um.
type benchConfig struct {
	filter string
	target time.Duration
}

// testFile roda os testes de um arquivo (e os benchmarks, com -bench) e diz
// se todos passaram.
func testFile(file, exe, only string, verbose bool, bc benchConfig, opts driver.Options) bool {
	src, err := readSource(file)
	if err == nil {
		var prog driver.TestProgram
		prog, err = driver.BuildTest(file, src, exe, os.ReadFile, opts)
		if err == nil {
			ok := len(prog.Tests) == 0 || runTests(file, exe, prog.Tests, only, verbose)
			if ok && bc.filter != "" {
				ok = runBenches(file, exe, prog.Benches, bc)
			}
			return ok
		}
	}
	fmt.Fprintln(os.Stderr, err)
	fmt.Printf("FALHOU\t%s\t(não compilou)\n", file)
	return false
}

func runTests(file, exe string, names []string, only string, verbose bool) bool {
	start := time.Now()
	ran, failed := 0, 0
	for i, name := range names {
		if only != "" && !strings.Contains(name, only) {
			continue
		}
		ran++
		if verbose {
			fmt.Printf("=== %s\n", name)
		}
		r := driver.RunTest(exe, i, name)
		if r.Status != driver.Passed {
			failed++
		}
		if r.Status == driver.Passed && !verbose {
			continue
		}
		label := "ok"
		if r.Status != driver.Passed {
			label = strings.ToUpper(r.Status.String())
		}
		fmt.Printf("--- %s: %s (%.2fs)\n", label, name, r.Elapsed.Seconds())
		for _, line := range strings.SplitAfter(string(r.Output), "\n") {
			if line != "" {
				fmt.Print("    ", line)
			}
		}
		if len(r.Output) > 0 && r.Output[len(r.Output)-1] != '\n' {
			fmt.Println()
		}
	}
	elapsed := time.Since(start).Seconds()
	switch {
	case failed > 0:
		fmt.Printf("FALHOU\t%s\t%d de %s falharam\t%.2fs\n", file, failed, plural(ran, "teste", "testes"), elapsed)
		return false
	case ran == 0:
		fmt.Printf("ok\t%s\tnenhum teste com %q\n", file, only)
	default:
		fmt.Printf("ok\t%s\t%s\t%.2fs\n", file, plural(ran, "teste", "testes"), elapsed)
	}
	return true
}

// runBenches mede os benchmarks: cada um roda com b.n = 1, 2, ... (num
// processo por rodada, com o tempo medido dentro do programa) até a medida
// passar de bc.target, como o go test -bench.
func runBenches(file, exe string, names []string, bc benchConfig) bool {
	ran := 0
	for i, name := range names {
		if bc.filter != "." && !strings.Contains(name, bc.filter) {
			continue
		}
		ran++
		n := 1
		for {
			r := driver.RunBench(exe, i, n)
			if r.Status != driver.Passed {
				fmt.Printf("--- %s: %s (b.n = %d)\n", strings.ToUpper(r.Status.String()), name, n)
				for _, line := range strings.SplitAfter(string(r.Output), "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Print("    ", line)
					}
				}
				fmt.Printf("FALHOU\t%s\n", file)
				return false
			}
			if time.Duration(r.Ns) >= bc.target || n >= 1_000_000_000 {
				fmt.Printf("%s\t%12d vezes\t%14.1f ns/op\n", name, n, float64(r.Ns)/float64(n))
				break
			}
			next := n * 100
			if r.Ns > 0 {
				// prevê o n que chega ao tempo, com folga, sem saltar mais que 100x
				next = int(float64(n) * float64(bc.target) / float64(r.Ns) * 1.2)
				next = min(max(next, n+1), n*100)
			}
			n = min(next, 1_000_000_000)
		}
	}
	if ran == 0 {
		fmt.Printf("ok\t%s\tnenhum benchmark com %q\n", file, bc.filter)
	}
	return true
}

// fmtCmd formata arquivos .mlc no lugar. Com -l, só lista os que mudariam.
func fmtCmd(args []string) int {
	fs := flag.NewFlagSet("fmt", flag.ExitOnError)
	list := fs.Bool("l", false, "só lista os arquivos cuja formatação é diferente, sem alterá-los")
	fs.Parse(args)
	paths := fs.Args()
	if len(paths) == 1 && paths[0] == "-" {
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		out, errs := format.Source("<stdin>", src)
		if len(errs) > 0 {
			printErrors(errs)
			return 1
		}
		os.Stdout.Write(out)
		return 0
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d iofs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Ext(path) == ".mlc" {
				files = append(files, path)
			}
			return nil
		})
	}
	status := 0
	for _, file := range files {
		src, err := readSource(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			status = 1
			continue
		}
		out, errs := format.Source(file, src)
		if len(errs) > 0 {
			printErrors(errs)
			status = 1
			continue
		}
		if bytes.Equal(src, out) {
			continue
		}
		if *list {
			fmt.Println(file)
			continue
		}
		if err := os.WriteFile(file, out, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			status = 1
		}
	}
	return status
}

func printErrors(errs []token.Error) {
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, e)
	}
}

// ccFlags registra -cflags e -ldflags: flags a mais para o compilador C,
// depois das do mlc e das variáveis CFLAGS e LDFLAGS.
func ccFlags(fs *flag.FlagSet) (cflags, ldflags *string) {
	cflags = fs.String("cflags", "", "flags a mais para compilar o C (separadas por espaço), depois das do mlc e de CFLAGS")
	ldflags = fs.String("ldflags", "", "flags a mais para ligar o programa (separadas por espaço), depois de LDFLAGS")
	return
}

// dirList junta os -L repetidos.
type dirList []string

func (d *dirList) String() string { return strings.Join(*d, ",") }

func (d *dirList) Set(v string) error {
	*d = append(*d, v)
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
