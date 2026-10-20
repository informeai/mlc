package doc

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/parser"
	"mlc/internal/token"
	"mlc/std"
)

// Module is a parsed .mlc file (without type checking), with the text the
// comments come from.
type Module struct {
	Path string // as in the import ("std/http", "util/text") or the file's path
	Name string // the qualifier used in the code: http, text
	Text []byte
	File *ast.File
	// All: also shows what isn't pub. It holds on its own for a file without
	// any pub declaration (a program, not a library).
	All bool
}

// Parse parses a module.
func Parse(path string, text []byte) (*Module, error) {
	f, errs := parser.ParseFile(path, text)
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s:%d:%d: %s", path, errs[0].Pos.Line, errs[0].Pos.Col, errs[0].Msg)
	}
	name := strings.TrimSuffix(filepath.Base(path), ".mlc")
	m := &Module{Path: path, Name: name, Text: text, File: f}
	m.All = !m.hasPub()
	return m, nil
}

func (m *Module) hasPub() bool {
	f := m.File
	for _, d := range f.Funcs {
		if d.Pub {
			return true
		}
	}
	for _, d := range f.Structs {
		if d.Pub {
			return true
		}
	}
	for _, d := range f.Enums {
		if d.Pub {
			return true
		}
	}
	for _, d := range f.Consts {
		if d.Pub {
			return true
		}
	}
	for _, d := range f.Interfaces {
		if d.Pub {
			return true
		}
	}
	return false
}

// ---------- where to find ----------

// Query is what mlc doc received: a module, one of its symbols, or a built-in.
type Query struct {
	Module  *Module
	Symbol  string // "", "Message", "respond", "Message.read_body"
	Builtin string // key of Builtins, when the request is a built-in
}

// Lookup resolves mlc doc's arguments: [module[.symbol]] [symbol].
// The module is from the standard library (http or std/http), a .mlc file, or
// an import looked up in dirs (the import search folders). A name that
// isn't a module can be a built-in (alloc, io.printf, thread.spawn). With
// all, it also shows what isn't pub.
func Lookup(args []string, dirs []string, all bool) (*Query, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, errors.New("usage: mlc doc [module[.symbol]] [symbol]")
	}
	arg := args[0]
	mod, sym := arg, ""
	if !strings.HasSuffix(arg, ".mlc") {
		slash := strings.LastIndex(arg, "/")
		if i := strings.Index(arg[slash+1:], "."); i >= 0 {
			mod, sym = arg[:slash+1+i], arg[slash+2+i:]
		}
	}
	if len(args) == 2 {
		if sym != "" {
			return nil, errors.New("usage: mlc doc [module[.symbol]] [symbol]")
		}
		sym = args[1]
	}
	m, err := findModule(mod, dirs)
	if err != nil {
		if key := builtinKey(arg); key != "" && len(args) == 1 {
			return &Query{Builtin: key}, nil
		}
		return nil, err
	}
	m.All = m.All || all
	q := &Query{Module: m, Symbol: sym}
	if sym != "" && !m.has(sym) {
		// io.printf, thread.spawn, log.Logger.info: rewritten by the compiler
		for _, k := range []string{m.Name + "." + sym, sym} {
			if key := builtinKey(k); key != "" {
				return &Query{Builtin: key}, nil
			}
		}
		return nil, fmt.Errorf("%s has no %q (see mlc doc %s)", m.Path, sym, m.Path)
	}
	return q, nil
}

func builtinKey(s string) string {
	if _, ok := Builtins[s]; ok {
		return s
	}
	for k := range Builtins {
		if strings.EqualFold(k, s) {
			return k
		}
	}
	return ""
}

// findModule finds the module as the import would.
func findModule(mod string, dirs []string) (*Module, error) {
	if strings.HasSuffix(mod, ".mlc") {
		text, err := os.ReadFile(mod)
		if err != nil {
			return nil, err
		}
		return Parse(mod, text)
	}
	name := strings.TrimPrefix(mod, "std/")
	if strings.HasPrefix(mod, "std/") || !strings.Contains(mod, "/") {
		if text, err := std.FS.ReadFile(name + ".mlc"); err == nil && !strings.HasSuffix(name, "_test") {
			m, err := Parse("std/"+name+".mlc", text)
			if m != nil {
				m.Path = "std/" + name
			}
			return m, err
		}
		if strings.HasPrefix(mod, "std/") {
			return nil, fmt.Errorf("the standard library has no module %q (see mlc doc)", mod)
		}
	}
	for _, d := range dirs {
		file := filepath.Join(d, filepath.FromSlash(mod)+".mlc")
		text, err := os.ReadFile(file)
		if err == nil {
			m, err := Parse(file, text)
			if m != nil {
				m.Path = mod
			}
			return m, err
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	var where []string
	for _, d := range dirs {
		where = append(where, strconv.Quote(d))
	}
	return nil, fmt.Errorf("module %q not found: it isn't in the standard library nor in %s (other folders: -I or MLC_PATH)", mod, strings.Join(where, ", "))
}

// ---------- what to write ----------

// Write writes the requested documentation.
func (q *Query) Write(w io.Writer) {
	switch {
	case q.Builtin != "":
		writeBuiltin(w, q.Builtin)
	case q.Symbol == "":
		q.Module.writeOverview(w)
	default:
		q.Module.writeSymbol(w, q.Symbol)
	}
}

// WriteIndex lists the standard library's modules with the first sentence of
// each one's description.
func WriteIndex(w io.Writer) {
	entries, _ := fs.ReadDir(std.FS, ".")
	fmt.Fprintln(w, "Standard library (import \"std/<name>\"):")
	fmt.Fprintln(w)
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".mlc") && !strings.HasSuffix(n, "_test.mlc") {
			names = append(names, strings.TrimSuffix(n, ".mlc"))
		}
	}
	sort.Strings(names)
	for _, n := range names {
		text, _ := std.FS.ReadFile(n + ".mlc")
		fmt.Fprintf(w, "  %-10s %s\n", n, short(summary(Header(text)), 66))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Embutidos: alloc, free, realloc, len, take, replace, drain, drop, io.print,")
	fmt.Fprintln(w, "io.printf, thread.spawn, the types (int, byte, string...) and the generic")
	fmt.Fprintln(w, "constraints (any, owned, ref...): mlc doc builtin lists them all.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "mlc doc http              the module: description, constants, types, functions")
	fmt.Fprintln(w, "mlc doc http.Message      a type, with its fields and methods")
	fmt.Fprintln(w, "mlc doc http.respond      a function (or http.Message.read_body, a method)")
	fmt.Fprintln(w, "mlc doc util/text         a library of yours (looked up like the import)")
	fmt.Fprintln(w, "mlc doc alloc             a built-in")
}

// WriteBuiltins lists the built-ins.
func WriteBuiltins(w io.Writer) {
	var keys []string
	for k := range Builtins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "Built-ins (mlc doc <name> for the description):")
	fmt.Fprintln(w)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-18s %s\n", k, Builtins[k][0])
	}
}

func writeBuiltin(w io.Writer, key string) {
	d := Builtins[key]
	fmt.Fprintln(w, d[0])
	fmt.Fprintln(w)
	writeText(w, []string{strings.ReplaceAll(d[1], "**", "")}) // markdown bold
	fmt.Fprintln(w)
}

func (m *Module) writeOverview(w io.Writer) {
	f := m.File
	if strings.HasSuffix(m.Path, ".mlc") {
		fmt.Fprintf(w, "// %s\n", m.Path)
	} else {
		fmt.Fprintf(w, "import %q\n", m.Path)
	}
	if h := Header(m.Text); len(h) > 0 {
		fmt.Fprintln(w)
		writeText(w, h)
	}
	var consts []string
	for _, d := range f.Consts {
		if m.shown(d.Pub) {
			consts = append(consts, m.rawLine(d.P.Line))
		}
	}
	section(w, "Constants", consts)

	var types []string
	for _, d := range f.Interfaces {
		if m.shown(d.Pub) {
			types = appendType(types, m.typeEntry(d.P.Line, d.Name))
		}
	}
	for _, d := range f.Structs {
		if m.shown(d.Pub) {
			types = appendType(types, m.typeEntry(d.P.Line, d.Name))
		}
	}
	for _, d := range f.Enums {
		if m.shown(d.Pub) {
			types = appendType(types, m.typeEntry(d.P.Line, d.Name))
		}
	}
	section(w, "Types", types)

	var funcs []string
	for _, d := range f.Funcs {
		if d.Recv == nil && m.shown(d.Pub) {
			funcs = append(funcs, display(Signature(m.Text, d.P)))
		}
	}
	section(w, "Functions", funcs)

	var impls []string
	for _, d := range f.Impls {
		impls = append(impls, Line(m.Text, d.P))
	}
	section(w, "Implementations", impls)
}

// appendType adds a type's entry to the list, with a blank line
// before it when the previous type or this one has methods (to separate the groups).
func appendType(types, entry []string) []string {
	if len(types) > 0 && (len(entry) > 1 || strings.HasPrefix(types[len(types)-1], "    ")) {
		types = append(types, "")
	}
	return append(types, entry...)
}

// typeEntry is the type's line followed by its methods, indented.
func (m *Module) typeEntry(line int, name string) []string {
	head := display(strings.TrimSuffix(strings.TrimSpace(StripComment(m.rawLine(line))), "{"))
	out := []string{strings.TrimSpace(head)}
	for _, d := range m.methods(name) {
		out = append(out, "    "+display(Signature(m.Text, d.P)))
	}
	return out
}

func section(w io.Writer, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n\n", title)
	for _, l := range lines {
		if l == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintln(w, "  "+l)
	}
}

// writeSymbol writes a type (declaration, description, implementations and
// methods), a function, a method or a constant.
func (m *Module) writeSymbol(w io.Writer, sym string) {
	f := m.File
	typ, member, _ := strings.Cut(sym, ".")
	if member != "" {
		for _, d := range m.methods(typ) {
			if strings.EqualFold(d.Name, member) {
				m.writeDecl(w, display(Signature(m.Text, d.P)), d.P.Line)
				return
			}
		}
		for _, d := range f.Interfaces {
			if strings.EqualFold(d.Name, typ) {
				for _, md := range d.Methods {
					if strings.EqualFold(md.Name, member) {
						m.writeDecl(w, Line(m.Text, md.P), md.P.Line)
						return
					}
				}
			}
		}
		for _, d := range f.Enums {
			if strings.EqualFold(d.Name, typ) {
				for _, v := range d.Variants {
					if strings.EqualFold(v.Name, member) {
						m.writeDecl(w, Line(m.Text, v.P), v.P.Line)
						return
					}
				}
			}
		}
		return
	}
	for _, d := range f.Funcs {
		if d.Recv == nil && strings.EqualFold(d.Name, sym) && m.shown(d.Pub) {
			m.writeDecl(w, display(Signature(m.Text, d.P)), d.P.Line)
			return
		}
	}
	for _, d := range f.Consts {
		if strings.EqualFold(d.Name, sym) && m.shown(d.Pub) {
			m.writeDecl(w, m.rawLine(d.P.Line), d.P.Line)
			return
		}
	}
	for _, d := range f.Interfaces {
		if strings.EqualFold(d.Name, sym) && m.shown(d.Pub) {
			m.writeType(w, d.Name, d.P.Line)
			return
		}
	}
	for _, d := range f.Structs {
		if strings.EqualFold(d.Name, sym) && m.shown(d.Pub) {
			m.writeType(w, d.Name, d.P.Line)
			return
		}
	}
	for _, d := range f.Enums {
		if strings.EqualFold(d.Name, sym) && m.shown(d.Pub) {
			m.writeType(w, d.Name, d.P.Line)
			return
		}
	}
}

// has says whether the module declares sym (Type, function, constant or Type.member).
func (m *Module) has(sym string) bool {
	var b strings.Builder
	m.writeSymbol(&b, sym)
	return b.Len() > 0
}

func (m *Module) writeDecl(w io.Writer, code string, line int) {
	fmt.Fprintln(w, code)
	if c := commentAt(m.Text, line); len(c) > 0 {
		fmt.Fprintln(w)
		writeText(w, c)
	}
}

func (m *Module) writeType(w io.Writer, name string, line int) {
	fmt.Fprintln(w, display(blockAt(m.Text, line)))
	if c := commentAt(m.Text, line); len(c) > 0 {
		fmt.Fprintln(w)
		writeText(w, c)
	}
	var impls []string
	for _, d := range m.File.Impls {
		if d.Type != nil && strings.EqualFold(d.Type.Name, name) {
			impls = append(impls, Line(m.Text, d.P))
		}
	}
	section(w, "Implementations", impls)
	ms := m.methods(name)
	if len(ms) == 0 {
		return
	}
	fmt.Fprintf(w, "\nMethods\n")
	for _, d := range ms {
		fmt.Fprintf(w, "\n  %s\n", display(Signature(m.Text, d.P)))
		if s := summary(commentAt(m.Text, d.P.Line)); s != "" {
			fmt.Fprintf(w, "      %s\n", s)
		}
	}
}

// methods are the visible methods of the type name, in file order.
func (m *Module) methods(name string) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, d := range m.File.Funcs {
		if d.Recv != nil && strings.EqualFold(recvName(d.Recv.Type), name) && m.shown(d.Pub) {
			out = append(out, d)
		}
	}
	return out
}

func recvName(t *ast.TypeExpr) string {
	for t != nil && t.Mod != ast.TypeNamed {
		t = t.Elem
	}
	if t == nil {
		return ""
	}
	return t.Name
}

func (m *Module) shown(pub bool) bool { return pub || m.All }

func (m *Module) rawLine(line int) string {
	lines := strings.Split(string(m.Text), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return display(strings.TrimSpace(lines[line-1]))
}

func posAt(line int) token.Pos { return token.Pos{Line: line, Col: 1} }

func commentAt(text []byte, line int) []string {
	return Comment(text, posAt(line))
}

func blockAt(text []byte, line int) string {
	return Block(text, posAt(line))
}

// display removes the pub (everything mlc doc shows is visible to whoever
// imports) and the extern of an extern fn (implemented in the runtime: for the
// caller, it's a function like the others).
func display(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		rest := strings.TrimPrefix(l[len(indent):], "pub ")
		if strings.HasPrefix(rest, "extern fn ") {
			rest = strings.TrimPrefix(rest, "extern ")
		}
		lines[i] = indent + rest
	}
	return strings.Join(lines, "\n")
}

// summary is the first sentence of a comment.
func summary(lines []string) string {
	var parts []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "    ") {
			break
		}
		parts = append(parts, strings.TrimSpace(l))
	}
	s := strings.Join(parts, " ")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if i := strings.Index(s, ": "); i >= 0 && i > 60 {
		s = s[:i] + "."
	}
	return s
}

// short cuts a long sentence at the first ":" or "(" before n characters,
// or at the last word that fits.
func short(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	for _, sep := range []string{": ", " (", ", "} {
		if i := strings.Index(s, sep); i > 10 && len([]rune(s[:i])) <= n {
			return s[:i] + "."
		}
	}
	r := []rune(s)[:n]
	if i := strings.LastIndex(string(r), " "); i > 0 {
		return string(r)[:i] + "…"
	}
	return string(r) + "…"
}

// writeText writes a comment indented by 4 spaces (the code examples,
// already indented in the comment, end up with 8). Long one-line
// paragraphs (the built-ins' texts) are wrapped at 76 columns.
func writeText(w io.Writer, lines []string) {
	for _, l := range lines {
		if l == "" {
			fmt.Fprintln(w)
			continue
		}
		if strings.HasPrefix(l, "    ") || len(l) <= 76 {
			fmt.Fprintln(w, "    "+l)
			continue
		}
		line := ""
		for _, word := range strings.Fields(l) {
			if line != "" && len(line)+1+len(word) > 76 {
				fmt.Fprintln(w, "    "+line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		if line != "" {
			fmt.Fprintln(w, "    "+line)
		}
	}
}
