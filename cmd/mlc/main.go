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

	"mlc/internal/driver"
	"mlc/internal/format"
	"mlc/internal/lsp"
	"mlc/internal/token"
)

const usage = `mlc — compilador da linguagem MLC (My Lang C)

Uso:
  mlc build [-o saida] [-emit-c] [-L dir] arquivo.mlc   compila para um executável
  mlc run [-L dir] arquivo.mlc [argumentos]             compila e executa
                                       (-L: pasta das bibliotecas de C de link "nome")
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

func build(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "caminho do executável (padrão: nome do arquivo sem .mlc)")
	emitC := fs.Bool("emit-c", false, "imprime o C gerado em vez de compilar")
	var libDirs dirList
	fs.Var(&libDirs, "L", "pasta onde procurar as bibliotecas de C (pode repetir)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := fs.Arg(0)
	src, err := readSource(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if *emitC {
		csrc, err := driver.Compile(path, src)
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
	if err := driver.BuildOpts(path, src, *out, os.ReadFile, driver.Options{LibDirs: libDirs}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var libDirs dirList
	fs.Var(&libDirs, "L", "pasta onde procurar as bibliotecas de C (pode repetir)")
	fs.Parse(args) // para no primeiro argumento que não é opção: o arquivo
	args = fs.Args()
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := args[0]
	src, err := readSource(path)
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
	if err := driver.BuildOpts(path, src, exe, os.ReadFile, driver.Options{LibDirs: libDirs}); err != nil {
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

// dirList junta os -L repetidos.
type dirList []string

func (d *dirList) String() string { return strings.Join(*d, ",") }

func (d *dirList) Set(v string) error {
	*d = append(*d, v)
	return nil
}
