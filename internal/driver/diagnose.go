package driver

import (
	"os"
	"path/filepath"

	"mlc/internal/checker"
	"mlc/internal/parser"
	"mlc/internal/token"
)

// Diagnose checa o arquivo target (sintaxe, tipos e posse) sem gerar C, e
// devolve os erros que pertencem a ele. É o que o LSP usa a cada edição; read
// pode devolver o texto ainda não salvo dos arquivos abertos no editor.
//
// Um arquivo com fn main é checado como programa. Um módulo (sem main) é
// checado pelo main.mlc mais próximo, subindo as pastas, quando esse programa
// o importa: assim os imports dele resolvem como num build de verdade. Sem
// esse main.mlc, o módulo é checado sozinho, sem exigir main.
func Diagnose(target string, read Loader) []token.Error {
	target = filepath.Clean(target)
	src, err := read(target)
	if err != nil {
		return []token.Error{{File: target, Pos: token.Pos{Line: 1, Col: 1}, Msg: err.Error()}}
	}
	if _, errs := parser.ParseFile(target, src); len(errs) > 0 {
		return errs
	}
	file, fileSrc, loose := entryFor(target, src, read)
	var out []token.Error
	for _, e := range programErrors(file, fileSrc, read, target) {
		// módulo solto: checado como se fosse o principal, sem exigir main
		if !loose || e.Msg != "função main não declarada" {
			out = append(out, e)
		}
	}
	return out
}

// entryFor escolhe o arquivo principal do programa pelo qual target é
// checado: o próprio target se tiver fn main, senão o main.mlc mais próximo
// que o importa. loose diz que target é um módulo solto, checado sozinho.
func entryFor(target string, src []byte, read Loader) (file string, fileSrc []byte, loose bool) {
	if f, errs := parser.ParseFile(target, src); len(errs) == 0 {
		for _, fn := range f.Funcs {
			if fn.Name == "main" && fn.Recv == nil {
				return target, src, false
			}
		}
	}
	if main := findMain(filepath.Dir(target), read); main != "" && main != target {
		if mainSrc, err := read(main); err == nil {
			if srcs, errs := load(main, mainSrc, read); loads(srcs, target) || loadErrorsIn(errs, target) {
				return main, mainSrc, false
			}
		}
	}
	return target, src, true
}

// Program é um programa checado, para navegação no editor (hover, ir para a
// definição): os arquivos com as ASTs e o resultado do checker, que vem
// preenchido até onde a checagem conseguiu ir, mesmo com erros.
type Program struct {
	Srcs   []checker.Source
	Info   *checker.Info
	Target *checker.Source // o arquivo pedido, dentro de Srcs
}

// Analyze checa o programa de target como Diagnose, mas devolve as ASTs e o
// resultado do checker. Com erro de sintaxe ou de import, devolve nil.
func Analyze(target string, read Loader) *Program {
	target = filepath.Clean(target)
	src, err := read(target)
	if err != nil {
		return nil
	}
	file, fileSrc, _ := entryFor(target, src, read)
	srcs, errs := load(file, fileSrc, read)
	if len(errs) > 0 {
		return nil
	}
	info, _ := checker.CheckProgram(srcs)
	p := &Program{Srcs: srcs, Info: info}
	for i := range srcs {
		if filepath.Clean(srcs[i].File) == target {
			p.Target = &p.Srcs[i]
		}
	}
	if p.Target == nil {
		return nil
	}
	return p
}

// programErrors checa o programa de file e devolve os erros de target. Um
// erro num arquivo importado aparece no começo de target, com o nome do
// arquivo, quando target é o principal.
func programErrors(file string, src []byte, read Loader, target string) []token.Error {
	srcs, errs := load(file, src, read)
	if len(errs) == 0 {
		_, errs = checker.CheckProgram(srcs)
	}
	var out []token.Error
	for _, e := range errs {
		switch {
		case filepath.Clean(e.File) == target:
			out = append(out, e)
		case file == target:
			out = append(out, token.Error{File: target, Pos: token.Pos{Line: 1, Col: 1},
				Msg: "erro num arquivo importado: " + e.Error()})
		}
	}
	return out
}

// findMain procura main.mlc em dir e nas pastas acima (até 8 níveis).
func findMain(dir string, read Loader) string {
	for range 8 {
		name := filepath.Join(dir, "main.mlc")
		if _, err := read(name); err == nil {
			return name
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func loads(srcs []checker.Source, target string) bool {
	for _, s := range srcs {
		if filepath.Clean(s.File) == target {
			return true
		}
	}
	return false
}

func loadErrorsIn(errs []token.Error, target string) bool {
	for _, e := range errs {
		if filepath.Clean(e.File) == target {
			return true
		}
	}
	return false
}

// Overlay devolve um Loader que lê primeiro de files (caminho limpo → texto)
// e, se não achar, do disco.
func Overlay(files map[string][]byte) Loader {
	return func(name string) ([]byte, error) {
		if src, ok := files[filepath.Clean(name)]; ok {
			return src, nil
		}
		return os.ReadFile(name)
	}
}
