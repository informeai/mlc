// Comando mlc: compilador da linguagem MLC (My Lang C).
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mlc/internal/driver"
)

const usage = `mlc — compilador da linguagem MLC (My Lang C)

Uso:
  mlc build [-o saida] [-emit-c] arquivo.mlc   compila para um executável
  mlc run arquivo.mlc [argumentos]             compila e executa
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
	if err := driver.Build(path, src, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(args []string) int {
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
	if err := driver.Build(path, src, exe); err != nil {
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
