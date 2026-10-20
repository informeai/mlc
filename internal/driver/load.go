package driver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/parser"
	"mlc/internal/token"
	"mlc/std"
)

// Loader reads a program file by name (the main file's path
// or an import's, relative to the main file's folder).
type Loader func(name string) ([]byte, error)

// loader loads the main file and, recursively, the files it
// imports. Import paths are relative to the main file's folder:
// import "util/text" loads util/text.mlc. Each file is loaded once,
// and the final list has each one after the ones it imports.
type loader struct {
	root  string
	read  Loader
	srcs  []checker.Source
	state map[string]int // 1: loading; 2: done
	stack []string       // imports being loaded, to describe cycles
	errs  []token.Error
	dirs  []string // where to look for imports, in order (SearchDirs)
}

// SearchDirs returns the folders where an import "x/y" is looked up, in order:
// the main file's, the lib/ folder next to it (libraries copied
// into the project), the -I folders and those of MLC_PATH (separated by : ).
// The first one that has the file wins.
func SearchDirs(root string, importDirs []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	add(root)
	add(filepath.Join(root, "lib"))
	for _, d := range importDirs {
		add(d)
	}
	for _, d := range filepath.SplitList(os.Getenv("MLC_PATH")) {
		add(d)
	}
	return out
}

func load(file string, src []byte, read Loader, importDirs ...string) ([]checker.Source, []token.Error) {
	f, errs := parser.ParseFile(file, src)
	if len(errs) > 0 {
		return nil, errs
	}
	root := filepath.Dir(file)
	l := &loader{root: root, read: read, state: map[string]int{}, dirs: SearchDirs(root, importDirs)}
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
		l.errorf(from, imp.P, "circular import: %s", cycle)
		return
	}
	isStd := strings.HasPrefix(imp.Path, "std/")
	var name string
	var src []byte
	var err error
	if isStd {
		// standard library: built into the compiler
		name = imp.Path + ".mlc"
		src, err = std.FS.ReadFile(strings.TrimPrefix(name, "std/"))
	} else {
		// the first search folder that has the file
		err = fs.ErrNotExist
		for _, d := range l.dirs {
			name = filepath.Join(d, filepath.FromSlash(imp.Path)+".mlc")
			if src, err = l.read(name); err == nil || !errors.Is(err, fs.ErrNotExist) {
				break
			}
		}
	}
	if err != nil {
		if isStd {
			l.errorf(from, imp.P, "the standard library has no module %q", imp.Path)
		} else if errors.Is(err, fs.ErrNotExist) {
			hint := ""
			if _, err := std.FS.ReadFile(imp.Path + ".mlc"); err == nil {
				hint = fmt.Sprintf(" (for the standard library's: import \"std/%s\")", imp.Path)
			}
			var where []string
			for _, d := range l.dirs {
				where = append(where, strconv.Quote(d))
			}
			l.errorf(from, imp.P, "unknown module %q: file %s.mlc not found (searched in %s; other folders: -I or MLC_PATH)%s",
				imp.Path, imp.Path, strings.Join(where, ", "), hint)
		} else {
			l.errorf(from, imp.P, "couldn't read %s: %v", name, err)
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

// badPath explains what's wrong with an import path, or returns "".
func badPath(p string) string {
	switch {
	case p == "":
		return "empty path"
	case strings.HasSuffix(p, ".mlc"):
		return fmt.Sprintf("write it without the extension: import %q", strings.TrimSuffix(p, ".mlc"))
	case strings.HasPrefix(p, "/"):
		return "the path is relative to the main file's folder, without / at the start"
	case strings.Contains(p, "\\"):
		return "use / to separate folders"
	case strings.HasSuffix(p, "_test"):
		return "a _test.mlc file is a test program (mlc test) and can't be imported"
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return "the path can't have . or .. (it's relative to the main file's folder)"
		}
		if !isIdent(part) {
			return fmt.Sprintf("%q isn't a valid name (letters, digits and _, starting with a letter)", part)
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
