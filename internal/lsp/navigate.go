package lsp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	mlcdoc "mlc/internal/doc"
	"mlc/internal/driver"
	"mlc/internal/lexer"
	"mlc/internal/token"
	"mlc/std"
)

// Hover and "go to definition"
//
// The program is checked as in the diagnostics (driver.Analyze), and the name under the
// cursor is bound to its declaration by the checker's result (variables,
// calls, enum constructors, fields by type) or by the AST's declarations
// (types, variants, declared names). The hover shows the declaration and the
// // comments right above it.

// symbol is a declaration that was found.
type symbol struct {
	file  string    // the declaration's file, as in checker.Source.File
	pos   token.Pos // the name's position in the declaration (zero: start of the file)
	code  string    // the signature, shown as code
	doc   string    // the documentation, as text
	noDef bool      // nowhere to go (built-in functions, basic types)
}

// navigator answers hover and definition for a document.
type navigator struct {
	prog  *driver.Program
	read  driver.Loader
	text  []byte // the document's text (Target)
	idx   *index
	texts map[string][]byte
}

func newNavigator(prog *driver.Program, read driver.Loader, text []byte) *navigator {
	return &navigator{prog: prog, read: read, text: text, idx: buildIndex(prog.Target.AST), texts: map[string][]byte{}}
}

// fileText returns the text of a program file (the standard library's
// come from inside mlc).
func (n *navigator) fileText(name string) []byte {
	if t, ok := n.texts[name]; ok {
		return t
	}
	var t []byte
	if filepath.Clean(name) == filepath.Clean(n.prog.Target.File) {
		t = n.text
	} else if rest, ok := strings.CutPrefix(name, "std/"); ok && n.isStd(name) {
		t, _ = std.FS.ReadFile(rest)
	} else {
		t, _ = n.read(name)
	}
	n.texts[name] = t
	return t
}

func (n *navigator) isStd(file string) bool {
	for _, s := range n.prog.Srcs {
		if s.File == file {
			return s.Std
		}
	}
	return false
}

// ---------- index of the positions of the document's AST ----------

type patternRef struct {
	pat   *ast.Pattern
	match *ast.MatchStmt
	bind  int // index of the name in Binds; -1 on the variant's name
}

type fieldInitRef struct {
	init *ast.FieldInit
	lit  *ast.CompositeLit
}

type index struct {
	idents     map[token.Pos]*ast.Ident
	sels       map[token.Pos]*ast.SelectorExpr // by the position of the name after the '.'
	calls      map[ast.Expr]*ast.CallExpr      // by the called expression
	patterns   map[token.Pos]patternRef
	varDecls   map[token.Pos]*ast.VarDecl
	forVars    map[token.Pos]*ast.ForStmt
	fieldInits map[token.Pos]fieldInitRef
	consts     []*ast.ConstDecl // all of them, including the ones inside blocks, in file order
}

func buildIndex(f *ast.File) *index {
	ix := &index{
		idents: map[token.Pos]*ast.Ident{}, sels: map[token.Pos]*ast.SelectorExpr{},
		calls: map[ast.Expr]*ast.CallExpr{}, patterns: map[token.Pos]patternRef{},
		varDecls: map[token.Pos]*ast.VarDecl{}, forVars: map[token.Pos]*ast.ForStmt{},
		fieldInits: map[token.Pos]fieldInitRef{},
	}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return
			}
			switch x := v.Interface().(type) {
			case *ast.Ident:
				if _, seen := ix.idents[x.P]; !seen {
					ix.idents[x.P] = x
				}
			case *ast.SelectorExpr:
				ix.sels[x.SelPos] = x
			case *ast.CallExpr:
				ix.calls[x.Fun] = x
				// f[int](x) and vec.new[int](x): the function's name is inside
				// the type arguments
				switch fun := x.Fun.(type) {
				case *ast.IndexExpr:
					ix.calls[fun.X] = x
				case *ast.IndexListExpr:
					ix.calls[fun.X] = x
				}
			case *ast.VarDecl:
				ix.varDecls[x.P] = x
			case *ast.ConstDecl:
				ix.consts = append(ix.consts, x)
			case *ast.ForStmt:
				if x.Var != nil {
					ix.forVars[x.Var.P] = x
				}
			case *ast.CompositeLit:
				for _, fi := range x.Fields {
					ix.fieldInits[fi.P] = fieldInitRef{fi, x}
				}
			case *ast.MatchStmt:
				for _, arm := range x.Arms {
					for _, p := range arm.Patterns {
						if p.Variant != nil {
							ix.patterns[p.Variant.P] = patternRef{p, x, -1}
						} else if id, ok := p.Lit.(*ast.Ident); ok {
							ix.patterns[id.P] = patternRef{p, x, -1} // Empty => (a variant without data)
						}
						for i, b := range p.Binds {
							ix.patterns[b.P] = patternRef{p, x, i}
						}
					}
				}
			}
			walk(v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(f))
	return ix
}

// ---------- resolution ----------

// tokenAt returns the text token that contains the offset off (an
// identifier or a string), and the tokens before it.
func tokenAt(text []byte, off int) (tok token.Token, before []token.Token, ok bool) {
	lx := lexer.New("", text)
	var toks []token.Token
	for {
		t := lx.Next()
		if t.Kind == token.EOF {
			break
		}
		if t.Kind == token.SEMICOLON && t.Lit == "\n" {
			continue
		}
		if (t.Kind == token.IDENT || t.Kind == token.STRING) && t.Off <= off && off <= t.End {
			return t, toks, true
		}
		toks = append(toks, t)
		if t.Off > off {
			break
		}
	}
	return token.Token{}, nil, false
}

// resolve finds the declaration of the name at off, and the token under the cursor.
func (n *navigator) resolve(off int) (*symbol, token.Token) {
	tok, before, ok := tokenAt(n.text, off)
	if !ok {
		return nil, tok
	}
	if tok.Kind == token.STRING {
		for _, imp := range n.prog.Target.AST.Imports {
			if imp.Path == tok.Lit && imp.P.Line == tok.Pos.Line {
				return n.moduleSymbol(imp.Path), tok
			}
		}
		return nil, tok
	}
	return n.resolveIdent(tok, before), tok
}

func (n *navigator) resolveIdent(tok token.Token, before []token.Token) *symbol {
	info, ix, p := n.prog.Info, n.idx, tok.Pos
	if id := ix.idents[p]; id != nil {
		if call := ix.calls[id]; call != nil {
			if s := n.calleeSymbol(call); s != nil {
				return s
			}
		}
		if l := info.Locals[id]; l != nil {
			return n.localSymbol(l)
		}
	}
	if sel := ix.sels[p]; sel != nil {
		if s := n.selectorSymbol(sel); s != nil {
			return s
		}
	}
	if ref, ok := ix.patterns[p]; ok {
		if s := n.patternSymbol(ref); s != nil {
			return s
		}
	}
	if vd := ix.varDecls[p]; vd != nil && info.Decls[vd] != nil {
		return n.localSymbol(info.Decls[vd])
	}
	if fs := ix.forVars[p]; fs != nil && info.ForVars[fs] != nil {
		return n.localSymbol(info.ForVars[fs])
	}
	if ref, ok := ix.fieldInits[p]; ok {
		if s, ok := info.Types[ref.lit].(*checker.Struct); ok {
			return n.fieldSymbol(s, ref.init.Name)
		}
	}
	if s := n.declAt(n.prog.Target, p); s != nil {
		return s
	}
	if s := n.typeParam(tok.Lit, p); s != nil {
		return s
	}
	if l := n.localAt(p); l != nil {
		return n.localSymbol(l)
	}
	// a name qualified by a module: geometry.Point in types and literals
	if k := len(before); k >= 2 && before[k-1].Kind == token.PERIOD && before[k-2].Kind == token.IDENT {
		if m := n.moduleByName(before[k-2].Lit); m != nil {
			if (m.Path == "std/thread" && tok.Lit == "Thread") || (m.Path == "std/chan" && (tok.Lit == "Sender" || tok.Lit == "Receiver")) ||
				(m.Path == "std/mutex" && (tok.Lit == "Mutex" || tok.Lit == "Guard")) {
				d := builtinDocs[tok.Lit]
				s := n.moduleSymbol(m.Path)
				if s == nil {
					return &symbol{code: d[0], doc: d[1], noDef: true}
				}
				s.code, s.doc = d[0], d[1]
				return s
			}
			return n.declNamed(m, tok.Lit)
		}
	}
	if m := n.moduleByName(tok.Lit); m != nil {
		return n.moduleSymbol(m.Path)
	}
	if s := n.declNamed(n.prog.Target, tok.Lit); s != nil {
		return s
	}
	// a block constant: the last one with that name declared before the use
	for i := len(ix.consts) - 1; i >= 0; i-- {
		if d := ix.consts[i]; d.Name == tok.Lit && !p.Before(d.P) {
			return &symbol{file: n.prog.Target.File, pos: d.P, code: lineAt(n.text, d.P), doc: docAbove(n.text, d.P)}
		}
	}
	if d, ok := builtinDocs[tok.Lit]; ok {
		return &symbol{code: d[0], doc: d[1], noDef: true}
	}
	return nil
}

// localAt looks, among the document's variables, for the one declared at p (a
// parameter, at the place where it's declared).
func (n *navigator) localAt(p token.Pos) *checker.Local {
	for id, l := range n.prog.Info.Locals {
		if l.Pos == p && n.idx.idents[id.P] == id {
			return l
		}
	}
	return nil
}

func (n *navigator) moduleByName(name string) *checker.Source {
	for _, imp := range n.prog.Target.AST.Imports {
		if path.Base(imp.Path) == name {
			for i := range n.prog.Srcs {
				if n.prog.Srcs[i].Path == imp.Path {
					return &n.prog.Srcs[i]
				}
			}
		}
	}
	return nil
}

func (n *navigator) moduleSymbol(p string) *symbol {
	for _, s := range n.prog.Srcs {
		if s.Path == p {
			return &symbol{file: s.File, code: fmt.Sprintf("import %q", p), doc: headerDoc(n.fileText(s.File))}
		}
	}
	return nil
}

func (n *navigator) calleeSymbol(call *ast.CallExpr) *symbol {
	info := n.prog.Info
	if v := info.EnumCtors[call]; v != nil {
		return n.variantSymbol(v)
	}
	c, ok := info.Calls[call]
	if !ok {
		return nil
	}
	if c.Builtin == checker.MutexOp {
		d := builtinDocs["mutex."+info.Mutexes[call].Op]
		s := n.moduleSymbol("std/mutex")
		if s == nil {
			return &symbol{code: d[0], doc: d[1], noDef: true}
		}
		s.code, s.doc = d[0], d[1]
		return s
	}
	if c.Builtin == checker.ChanOp {
		d := builtinDocs["chan."+info.Chans[call].Op]
		s := n.moduleSymbol("std/chan")
		if s == nil {
			return &symbol{code: d[0], doc: d[1], noDef: true}
		}
		s.code, s.doc = d[0], d[1]
		return s
	}
	if c.Builtin == checker.ThreadSpawn || c.Builtin == checker.ThreadJoin {
		d := builtinDocs["spawn"]
		if c.Builtin == checker.ThreadJoin {
			d = builtinDocs["join"]
		}
		s := n.moduleSymbol("std/thread")
		if s == nil {
			return &symbol{code: d[0], doc: d[1], noDef: true}
		}
		s.code, s.doc = d[0], d[1]
		return s
	}
	if c.Builtin == checker.IoPrint {
		d := builtinDocs["print"]
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if dd, ok := builtinDocs[sel.Sel]; ok {
				d = dd
			}
		}
		mod := "std/io"
		if plan := n.prog.Info.Prints[call]; plan != nil {
			switch plan.Mode {
			case checker.PrintSink, checker.PrintCount:
				mod = "std/str"
			case checker.PrintWriter:
				d = builtinDocs["Writer.printf"]
			case checker.PrintLog:
				mod = "std/log"
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					d = builtinDocs["Logger."+sel.Sel]
				}
			}
		}
		s := n.moduleSymbol(mod)
		if s == nil {
			return &symbol{code: d[0], doc: d[1], noDef: true}
		}
		s.code, s.doc = d[0], d[1]
		return s
	}
	if c.Func == nil {
		if id, ok := call.Fun.(*ast.Ident); ok {
			if d, ok := builtinDocs[id.Name]; ok {
				return &symbol{code: d[0], doc: d[1], noDef: true}
			}
		}
		return nil
	}
	return n.funcSymbol(c.Func)
}

func (n *navigator) funcSymbol(fn *checker.Func) *symbol {
	if fn.Decl == nil {
		return nil
	}
	text := n.fileText(fn.File)
	s := &symbol{file: fn.File, pos: namePos(text, fn.Decl.P, fn.Decl.Name), code: signature(text, fn.Decl.P), doc: docAbove(text, fn.Decl.P)}
	if strings.Contains(fn.Name, "[") {
		s.doc = strings.TrimSpace(s.doc + "\n\ninstance: `" + fn.Name + "`")
	}
	return s
}

func (n *navigator) localSymbol(l *checker.Local) *symbol {
	s := &symbol{file: n.prog.Target.File, pos: l.Pos}
	switch {
	case l.IsConst:
		s.code = fmt.Sprintf("const %s %s = %v", l.Name, l.Type, l.Const)
	case l.Param:
		s.code = fmt.Sprintf("%s %s", l.Name, l.Type)
		s.doc = "parameter"
	default:
		s.code = fmt.Sprintf("var %s %s", l.Name, l.Type)
	}
	if l.IsConst {
		s.doc = docAbove(n.text, l.Pos)
	}
	return s
}

func (n *navigator) selectorSymbol(sel *ast.SelectorExpr) *symbol {
	info := n.prog.Info
	if call := n.idx.calls[sel]; call != nil {
		if s := n.calleeSymbol(call); s != nil {
			return s
		}
	}
	if v := info.EnumCtors[sel]; v != nil {
		return n.variantSymbol(v)
	}
	if id, ok := sel.X.(*ast.Ident); ok && info.Locals[id] == nil {
		if m := n.moduleByName(id.Name); m != nil {
			return n.declNamed(m, sel.Sel)
		}
	}
	t := info.Types[sel.X]
	if t == nil {
		// &r.field as an argument: r's type comes from the variable
		if id, ok := sel.X.(*ast.Ident); ok && info.Locals[id] != nil {
			t = info.Locals[id].Type
		}
	}
	for {
		switch tt := t.(type) {
		case *checker.Pointer:
			t = tt.Elem
			continue
		case *checker.Borrow:
			t = tt.Elem
			continue
		case *checker.Struct:
			return n.fieldSymbol(tt, sel.Sel)
		}
		return nil
	}
}

func (n *navigator) fieldSymbol(s *checker.Struct, name string) *symbol {
	f := s.Field(name)
	if f == nil || s.Decl == nil {
		return nil
	}
	for _, fd := range s.Decl.Fields {
		if fd.Name == name {
			return &symbol{file: s.File, pos: fd.P, code: fmt.Sprintf("%s %s", f.Name, f.Type),
				doc: strings.TrimSpace(docAbove(n.fileText(s.File), fd.P) + "\n\nfield of `" + s.Name + "`")}
		}
	}
	return nil
}

func (n *navigator) variantSymbol(v *checker.Variant) *symbol {
	en := v.Enum
	if en.Decl == nil {
		return nil
	}
	text := n.fileText(en.File)
	for _, vd := range en.Decl.Variants {
		if vd.Name == v.Name {
			return &symbol{file: en.File, pos: vd.P, code: lineAt(text, vd.P),
				doc: strings.TrimSpace(docAbove(text, vd.P) + "\n\nvariant of `" + en.Name + "`")}
		}
	}
	return nil
}

func (n *navigator) patternSymbol(ref patternRef) *symbol {
	info := n.prog.Info
	if ref.bind >= 0 {
		if binds := info.PatternBinds[ref.pat]; ref.bind < len(binds) && binds[ref.bind] != nil {
			return n.localSymbol(binds[ref.bind])
		}
		return nil
	}
	name := ""
	if ref.pat.Variant != nil {
		name = ref.pat.Variant.Name
	} else if id, ok := ref.pat.Lit.(*ast.Ident); ok {
		name = id.Name
	}
	if en, ok := info.Types[ref.match.Subject].(*checker.Enum); ok {
		if v := en.Variant(name); v != nil {
			return n.variantSymbol(v)
		}
	}
	return nil
}

// declAt returns the top-level declaration (or field, or variant) whose name is
// at p, in the file src.
func (n *navigator) declAt(src *checker.Source, p token.Pos) *symbol {
	text := n.fileText(src.File)
	f := src.AST
	for _, d := range f.Funcs {
		if namePos(text, d.P, d.Name) == p {
			return n.funcDeclSymbol(src, d)
		}
		params := d.Params
		if d.Recv != nil {
			params = append([]*ast.Param{d.Recv}, params...)
		}
		for _, prm := range params {
			if prm.P == p {
				return &symbol{file: src.File, pos: prm.P, code: prm.Name + " " + typeString(prm.Type), doc: "parameter of `" + d.Name + "`"}
			}
		}
	}
	for _, d := range f.Structs {
		if namePos(text, d.P, d.Name) == p {
			return n.blockSymbol(src.File, d.P, d.Name)
		}
		for _, fd := range d.Fields {
			if fd.P == p {
				return &symbol{file: src.File, pos: fd.P, code: lineAt(text, fd.P),
					doc: strings.TrimSpace(docAbove(text, fd.P) + "\n\nfield of `" + d.Name + "`")}
			}
		}
	}
	for _, d := range f.Enums {
		if namePos(text, d.P, d.Name) == p {
			return n.blockSymbol(src.File, d.P, d.Name)
		}
		for _, vd := range d.Variants {
			if vd.P == p {
				return &symbol{file: src.File, pos: vd.P, code: lineAt(text, vd.P),
					doc: strings.TrimSpace(docAbove(text, vd.P) + "\n\nvariant of `" + d.Name + "`")}
			}
			for _, fd := range vd.Fields {
				if fd.P == p {
					return &symbol{file: src.File, pos: fd.P, code: fd.Name + " " + typeString(fd.Type),
						doc: "field of the variant `" + d.Name + "." + vd.Name + "`"}
				}
			}
		}
	}
	for _, d := range f.Consts {
		if d.P == p {
			return &symbol{file: src.File, pos: d.P, code: lineAt(text, d.P), doc: docAbove(text, d.P)}
		}
	}
	for _, d := range f.Interfaces {
		if namePos(text, d.P, d.Name) == p {
			return n.blockSymbol(src.File, d.P, d.Name)
		}
		for _, m := range d.Methods {
			if namePos(text, m.P, m.Name) == p {
				return &symbol{file: src.File, pos: p, code: signature(text, m.P), doc: strings.TrimSpace(docAbove(text, m.P) + "\n\nmethod of the interface `" + d.Name + "`")}
			}
			for _, prm := range m.Params {
				if prm.P == p {
					return &symbol{file: src.File, pos: p, code: prm.Name + " " + typeString(prm.Type), doc: "parameter of `" + d.Name + "." + m.Name + "`"}
				}
			}
		}
	}
	for _, d := range f.Impls {
		for _, m := range d.Methods {
			if namePos(text, m.P, m.Name) == p {
				return n.funcDeclSymbol(src, m)
			}
		}
	}
	return nil
}

// typeParam looks for the type parameter name of the generic declaration that
// contains p (a function, struct or enum of the document).
func (n *navigator) typeParam(name string, p token.Pos) *symbol {
	f := n.prog.Target.AST
	type decl struct {
		start  token.Pos
		params []*ast.TypeParam
		what   string
	}
	var decls []decl
	for _, d := range f.Funcs {
		params := d.TypeParams
		if d.Recv != nil && d.Recv.Type != nil {
			// fn (v &Vec[T]) push(...): the T comes from the receiver's struct
			for _, s := range f.Structs {
				if s.Name == baseTypeName(d.Recv.Type) {
					params = append(params, s.TypeParams...)
				}
			}
		}
		decls = append(decls, decl{d.P, params, d.Name})
	}
	for _, d := range f.Structs {
		decls = append(decls, decl{d.P, d.TypeParams, d.Name})
	}
	for _, d := range f.Enums {
		decls = append(decls, decl{d.P, d.TypeParams, d.Name})
	}
	for _, d := range f.Impls {
		decls = append(decls, decl{d.P, d.TypeParams, "impl " + typeString(d.Iface) + " for " + typeString(d.Type)})
		for _, m := range d.Methods {
			// a block method: sees the impl's parameters and its own
			decls = append(decls, decl{m.P, append(append([]*ast.TypeParam{}, d.TypeParams...), m.TypeParams...), m.Name})
		}
	}
	// the declaration that contains p is the last one that starts before it
	var best *decl
	for i := range decls {
		if !p.Before(decls[i].start) && (best == nil || best.start.Before(decls[i].start)) {
			best = &decls[i]
		}
	}
	if best == nil {
		return nil
	}
	for _, tp := range best.params {
		if tp.Name == name {
			return &symbol{file: n.prog.Target.File, pos: tp.P, code: tp.Name + " " + tp.Constraint,
				doc: "type parameter of `" + best.what + "`"}
		}
	}
	return nil
}

func baseTypeName(t *ast.TypeExpr) string {
	for t != nil && t.Elem != nil {
		t = t.Elem
	}
	if t == nil {
		return ""
	}
	return t.Name
}

// typeString writes an AST type as in the code.
func typeString(t *ast.TypeExpr) string {
	if t == nil {
		return ""
	}
	switch t.Mod {
	case ast.TypeSlice:
		return "[]" + typeString(t.Elem)
	case ast.TypeArray:
		if lit, ok := t.Len.(*ast.IntLit); ok {
			return "[" + lit.Value + "]" + typeString(t.Elem)
		}
		return "[N]" + typeString(t.Elem)
	case ast.TypePointer:
		return "*" + typeString(t.Elem)
	case ast.TypeNullPtr:
		return "?*" + typeString(t.Elem)
	case ast.TypeOptBorrow:
		return "?&" + typeString(t.Elem)
	case ast.TypeBorrow:
		return "&" + typeString(t.Elem)
	}
	s := t.Name
	if t.Module != "" {
		s = t.Module + "." + s
	}
	if len(t.Args) > 0 {
		var args []string
		for _, a := range t.Args {
			args = append(args, typeString(a))
		}
		s += "[" + strings.Join(args, ", ") + "]"
	}
	return s
}

// declNamed looks for the top-level declaration called name in the file src.
func (n *navigator) declNamed(src *checker.Source, name string) *symbol {
	text := n.fileText(src.File)
	f := src.AST
	for _, d := range f.Funcs {
		if d.Name == name && d.Recv == nil {
			return n.funcDeclSymbol(src, d)
		}
	}
	for _, d := range f.Structs {
		if d.Name == name {
			return n.blockSymbol(src.File, d.P, d.Name)
		}
	}
	for _, d := range f.Enums {
		if d.Name == name {
			return n.blockSymbol(src.File, d.P, d.Name)
		}
	}
	for _, d := range f.Consts {
		if d.Name == name {
			return &symbol{file: src.File, pos: d.P, code: lineAt(text, d.P), doc: docAbove(text, d.P)}
		}
	}
	for _, d := range f.Interfaces {
		if d.Name == name {
			return n.blockSymbol(src.File, d.P, d.Name)
		}
	}
	return nil
}

func (n *navigator) funcDeclSymbol(src *checker.Source, d *ast.FuncDecl) *symbol {
	text := n.fileText(src.File)
	return &symbol{file: src.File, pos: namePos(text, d.P, d.Name), code: signature(text, d.P), doc: docAbove(text, d.P)}
}

// blockSymbol shows a whole struct or enum (up to 20 lines).
func (n *navigator) blockSymbol(file string, p token.Pos, name string) *symbol {
	text := n.fileText(file)
	lines := strings.Split(string(text), "\n")
	start := p.Line - 1
	var code []string
	depth := 0
	for i := start; i < len(lines) && i < start+20; i++ {
		l := strings.TrimRight(lines[i], " \t")
		code = append(code, l)
		depth += strings.Count(l, "{") - strings.Count(l, "}")
		if depth <= 0 && strings.Contains(l, "}") {
			break
		}
	}
	return &symbol{file: file, pos: namePos(text, p, name), code: strings.Join(code, "\n"), doc: docAbove(text, p)}
}

// ---------- text of the declarations ----------

// namePos returns the position of the identifier name starting at p (the word
// fn, struct or enum of the declaration).
func namePos(text []byte, p token.Pos, name string) token.Pos {
	lx := lexer.New("", text)
	for {
		t := lx.Next()
		if t.Kind == token.EOF {
			return p
		}
		if t.Kind == token.IDENT && t.Lit == name && !t.Pos.Before(p) {
			return t.Pos
		}
	}
}

func lineAt(text []byte, p token.Pos) string {
	lines := strings.Split(string(text), "\n")
	if p.Line < 1 || p.Line > len(lines) {
		return ""
	}
	return stripComment(strings.TrimSpace(lines[p.Line-1]))
}

// signature returns a function's header (see doc.Signature).
func signature(text []byte, p token.Pos) string { return mlcdoc.Signature(text, p) }

// stripComment removes a // comment from the end of the line (outside strings).
func stripComment(l string) string { return mlcdoc.StripComment(l) }

// docAbove joins the // comments on the lines right above p's line.
func docAbove(text []byte, p token.Pos) string { return formatDoc(mlcdoc.Comment(text, p)) }

// headerDoc is the comment at the top of a file (the module's description).
func headerDoc(text []byte) string { return formatDoc(mlcdoc.Header(text)) }

// formatDoc turns the comments into markdown: indented lines (code
// examples) become a code block.
func formatDoc(lines []string) string {
	var out []string
	inCode := false
	for _, l := range lines {
		code := strings.HasPrefix(l, "    ")
		switch {
		case code && !inCode:
			out = append(out, "```mlc")
			inCode = true
		case !code && inCode && l != "":
			out = append(out, "```")
			inCode = false
		}
		if code {
			l = l[4:]
		}
		out = append(out, l)
	}
	if inCode {
		out = append(out, "```")
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// builtinDocs describes what has no declaration in a file (the same as mlc doc).
var builtinDocs = mlcdoc.Builtins

// ---------- standard library files on disk ----------

// stdPath writes the standard library's files into a cache folder (one per
// version of the content) and returns the path of name ("std/io.mlc"), for the
// editor to open on definition.
func stdPath(name string) string {
	rest, ok := strings.CutPrefix(name, "std/")
	if !ok {
		return ""
	}
	entries, err := std.FS.ReadDir(".")
	if err != nil {
		return ""
	}
	h := sha256.New()
	for _, e := range entries {
		b, _ := std.FS.ReadFile(e.Name())
		h.Write([]byte(e.Name()))
		h.Write(b)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "mlc", "std-"+hex.EncodeToString(h.Sum(nil))[:12])
	target := filepath.Join(dir, rest)
	if _, err := os.Stat(target); err != nil {
		os.MkdirAll(dir, 0o755)
		for _, e := range entries {
			if b, err := std.FS.ReadFile(e.Name()); err == nil && strings.HasSuffix(e.Name(), ".mlc") {
				os.WriteFile(filepath.Join(dir, e.Name()), b, 0o444)
			}
		}
	}
	return target
}
