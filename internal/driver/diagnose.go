package driver

import (
	"os"
	"path/filepath"

	"mlc/internal/checker"
	"mlc/internal/parser"
	"mlc/internal/token"
)

// Diagnose checks the file target (syntax, types and ownership) without generating C, and
// returns the errors that belong to it. It's what the LSP uses on each edit; read
// can return the not-yet-saved text of the files open in the editor.
//
// A file with fn main is checked as a program. A module (without main) is
// checked through the nearest main.mlc, going up the folders, when that program
// imports it: that way its imports resolve as in a real build. Without
// that main.mlc, the module is checked alone, without requiring main. A
// _test.mlc file is checked as a test program.
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
		// loose module: checked as if it were the main file, without requiring main
		if !loose || e.Msg != "function main not declared" {
			out = append(out, e)
		}
	}
	return out
}

// entryFor picks the main file of the program through which target is
// checked: target itself if it has fn main, otherwise the nearest main.mlc
// that imports it. loose says target is a loose module, checked alone.
func entryFor(target string, src []byte, read Loader) (file string, fileSrc []byte, loose bool) {
	if IsTestFile(target) {
		return target, src, false // test file: it's the main file of the test program
	}
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

// Program is a checked program, for navigation in the editor (hover, go to
// definition): the files with the ASTs and the checker's result, which comes
// filled as far as checking managed to go, even with errors.
type Program struct {
	Srcs   []checker.Source
	Info   *checker.Info
	Target *checker.Source // the requested file, inside Srcs
}

// Analyze checks target's program like Diagnose, but returns the ASTs and the
// checker's result. With a syntax or import error, returns nil.
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
	srcs[len(srcs)-1].Test = IsTestFile(file)
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

// programErrors checks file's program and returns target's errors. An
// error in an imported file shows up at the start of target, with the file's
// name, when target is the main file.
func programErrors(file string, src []byte, read Loader, target string) []token.Error {
	srcs, errs := load(file, src, read)
	if len(errs) == 0 {
		srcs[len(srcs)-1].Test = IsTestFile(file)
		_, errs = checker.CheckProgram(srcs)
	}
	var out []token.Error
	for _, e := range errs {
		switch {
		case filepath.Clean(e.File) == target:
			out = append(out, e)
		case file == target:
			out = append(out, token.Error{File: target, Pos: token.Pos{Line: 1, Col: 1},
				Msg: "error in an imported file: " + e.Error()})
		}
	}
	return out
}

// findMain looks for main.mlc in dir and in the folders above (up to 8 levels).
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

// Overlay returns a Loader that reads first from files (clean path → text)
// and, if it doesn't find it, from the disk.
func Overlay(files map[string][]byte) Loader {
	return func(name string) ([]byte, error) {
		if src, ok := files[filepath.Clean(name)]; ok {
			return src, nil
		}
		return os.ReadFile(name)
	}
}
