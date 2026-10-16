package driver

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/parser"
	"mlc/internal/token"
	"mlc/std"
)

// Loader lê um arquivo do programa pelo nome (o caminho do arquivo principal
// ou o de um import, relativo à pasta do principal).
type Loader func(name string) ([]byte, error)

// loader carrega o arquivo principal e, recursivamente, os arquivos que ele
// importa. Os caminhos de import são relativos à pasta do arquivo principal:
// import "util/texto" carrega util/texto.mlc. Cada arquivo é carregado uma
// vez, e a lista final tem cada um depois dos que ele importa.
type loader struct {
	root  string
	read  Loader
	srcs  []checker.Source
	state map[string]int // 1: carregando; 2: pronto
	stack []string       // imports sendo carregados, para descrever ciclos
	errs  []token.Error
}

func load(file string, src []byte, read Loader) ([]checker.Source, []token.Error) {
	f, errs := parser.ParseFile(file, src)
	if len(errs) > 0 {
		return nil, errs
	}
	l := &loader{root: filepath.Dir(file), read: read, state: map[string]int{}}
	l.imports(file, f)
	if len(l.errs) > 0 {
		return nil, l.errs
	}
	return append(l.srcs, checker.Source{File: file, AST: f}), nil
}

func (l *loader) errorf(file string, pos token.Pos, format string, args ...any) {
	l.errs = append(l.errs, token.Error{File: file, Pos: pos, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) imports(file string, f *ast.File) {
	for _, imp := range f.Imports {
		if msg := badPath(imp.Path); msg != "" {
			l.errorf(file, imp.P, "import %q: %s", imp.Path, msg)
			continue
		}
		l.visit(file, imp)
	}
}

func (l *loader) visit(from string, imp *ast.Import) {
	switch l.state[imp.Path] {
	case 2:
		return
	case 1:
		cycle := imp.Path
		for i := len(l.stack) - 1; i >= 0; i-- {
			cycle = l.stack[i] + " → " + cycle
			if l.stack[i] == imp.Path {
				break
			}
		}
		l.errorf(from, imp.P, "importação circular: %s", cycle)
		return
	}
	isStd := strings.HasPrefix(imp.Path, "std/")
	name := filepath.Join(l.root, filepath.FromSlash(imp.Path)+".mlc")
	read := l.read
	if isStd {
		// biblioteca padrão: embutida no compilador
		name = imp.Path + ".mlc"
		read = func(string) ([]byte, error) { return std.FS.ReadFile(strings.TrimPrefix(name, "std/")) }
	}
	src, err := read(name)
	if err != nil {
		if isStd {
			l.errorf(from, imp.P, "a biblioteca padrão não tem o módulo %q", imp.Path)
		} else if errors.Is(err, fs.ErrNotExist) {
			hint := ""
			if _, err := std.FS.ReadFile(imp.Path + ".mlc"); err == nil {
				hint = fmt.Sprintf(" (para o da biblioteca padrão: import \"std/%s\")", imp.Path)
			}
			l.errorf(from, imp.P, "módulo desconhecido %q: arquivo %s não encontrado%s", imp.Path, name, hint)
		} else {
			l.errorf(from, imp.P, "não foi possível ler %s: %v", name, err)
		}
		return
	}
	f, errs := parser.ParseFile(name, src)
	l.state[imp.Path] = 1
	if len(errs) > 0 {
		l.errs = append(l.errs, errs...)
		l.state[imp.Path] = 2
		return
	}
	l.stack = append(l.stack, imp.Path)
	l.imports(name, f)
	l.stack = l.stack[:len(l.stack)-1]
	l.state[imp.Path] = 2
	l.srcs = append(l.srcs, checker.Source{Path: imp.Path, File: name, AST: f, Std: isStd})
}

// badPath explica o que há de errado num caminho de import, ou devolve "".
func badPath(p string) string {
	switch {
	case p == "":
		return "caminho vazio"
	case strings.HasSuffix(p, ".mlc"):
		return fmt.Sprintf("escreva sem a extensão: import %q", strings.TrimSuffix(p, ".mlc"))
	case strings.HasPrefix(p, "/"):
		return "o caminho é relativo à pasta do arquivo principal, sem / no começo"
	case strings.Contains(p, "\\"):
		return "use / para separar pastas"
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return "o caminho não pode ter . nem .. (é relativo à pasta do arquivo principal)"
		}
		if !isIdent(part) {
			return fmt.Sprintf("%q não é um nome válido (letras, dígitos e _, começando por letra)", part)
		}
	}
	return ""
}

func isIdent(s string) bool {
	for i, c := range s {
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return s != ""
}
