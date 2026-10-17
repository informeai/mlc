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
	"mlc/internal/driver"
	"mlc/internal/lexer"
	"mlc/internal/token"
	"mlc/std"
)

// Hover e "ir para a definição"
//
// O programa é checado como no diagnóstico (driver.Analyze), e o nome sob o
// cursor é ligado à sua declaração pelo resultado do checker (variáveis,
// chamadas, construtores de enum, campos pelo tipo) ou pelas declarações da
// AST (tipos, variantes, nomes declarados). O hover mostra a declaração e os
// comentários // logo acima dela.

// symbol é uma declaração encontrada.
type symbol struct {
	file  string    // arquivo da declaração, como em checker.Source.File
	pos   token.Pos // posição do nome na declaração (zero: começo do arquivo)
	code  string    // assinatura, mostrada como código
	doc   string    // documentação, em texto
	noDef bool      // sem lugar para ir (funções embutidas, tipos básicos)
}

// navigator responde hover e definição para um documento.
type navigator struct {
	prog  *driver.Program
	read  driver.Loader
	text  []byte // texto do documento (Target)
	idx   *index
	texts map[string][]byte
}

func newNavigator(prog *driver.Program, read driver.Loader, text []byte) *navigator {
	return &navigator{prog: prog, read: read, text: text, idx: buildIndex(prog.Target.AST), texts: map[string][]byte{}}
}

// fileText devolve o texto de um arquivo do programa (os da biblioteca padrão
// vêm de dentro do mlc).
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

// ---------- índice das posições da AST do documento ----------

type patternRef struct {
	pat   *ast.Pattern
	match *ast.MatchStmt
	bind  int // índice do nome em Binds; -1 no nome da variante
}

type fieldInitRef struct {
	init *ast.FieldInit
	lit  *ast.CompositeLit
}

type index struct {
	idents     map[token.Pos]*ast.Ident
	sels       map[token.Pos]*ast.SelectorExpr // pela posição do nome depois do '.'
	calls      map[ast.Expr]*ast.CallExpr      // pela expressão chamada
	patterns   map[token.Pos]patternRef
	varDecls   map[token.Pos]*ast.VarDecl
	forVars    map[token.Pos]*ast.ForStmt
	fieldInits map[token.Pos]fieldInitRef
	consts     []*ast.ConstDecl // todas, inclusive as de dentro de blocos, na ordem do arquivo
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
				// f[int](x) e vec.new[int](x): o nome da função fica dentro
				// dos argumentos de tipo
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
							ix.patterns[id.P] = patternRef{p, x, -1} // Vazio => (variante sem dados)
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

// ---------- resolução ----------

// tokenAt devolve o token de texto que contém o deslocamento off (um
// identificador ou uma string), e os tokens anteriores a ele.
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

// resolve encontra a declaração do nome em off, e o token sob o cursor.
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
	// nome qualificado por um módulo: geometria.Ponto em tipos e literais
	if k := len(before); k >= 2 && before[k-1].Kind == token.PERIOD && before[k-2].Kind == token.IDENT {
		if m := n.moduleByName(before[k-2].Lit); m != nil {
			if m.Path == "std/thread" && tok.Lit == "Thread" {
				d := builtinDocs["Thread"]
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
	// constante de bloco: a última com esse nome declarada antes do uso
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

// localAt procura, entre as variáveis do documento, a declarada em p (um
// parâmetro, no lugar onde é declarado).
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
		if n.prog.Info.Prints[call] != nil && n.prog.Info.Prints[call].Mode != checker.PrintFile {
			mod = "std/str"
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
		s.doc = strings.TrimSpace(s.doc + "\n\ninstância: `" + fn.Name + "`")
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
		s.doc = "parâmetro"
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
		// &r.campo como argumento: o tipo de r vem da variável
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
				doc: strings.TrimSpace(docAbove(n.fileText(s.File), fd.P) + "\n\ncampo de `" + s.Name + "`")}
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
				doc: strings.TrimSpace(docAbove(text, vd.P) + "\n\nvariante de `" + en.Name + "`")}
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

// declAt devolve a declaração de topo (ou campo, ou variante) cujo nome está
// em p, no arquivo src.
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
				return &symbol{file: src.File, pos: prm.P, code: prm.Name + " " + typeString(prm.Type), doc: "parâmetro de `" + d.Name + "`"}
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
					doc: strings.TrimSpace(docAbove(text, fd.P) + "\n\ncampo de `" + d.Name + "`")}
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
					doc: strings.TrimSpace(docAbove(text, vd.P) + "\n\nvariante de `" + d.Name + "`")}
			}
			for _, fd := range vd.Fields {
				if fd.P == p {
					return &symbol{file: src.File, pos: fd.P, code: fd.Name + " " + typeString(fd.Type),
						doc: "campo da variante `" + d.Name + "." + vd.Name + "`"}
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
				return &symbol{file: src.File, pos: p, code: signature(text, m.P), doc: strings.TrimSpace(docAbove(text, m.P) + "\n\nmétodo da interface `" + d.Name + "`")}
			}
			for _, prm := range m.Params {
				if prm.P == p {
					return &symbol{file: src.File, pos: p, code: prm.Name + " " + typeString(prm.Type), doc: "parâmetro de `" + d.Name + "." + m.Name + "`"}
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

// typeParam procura o parâmetro de tipo name da declaração genérica que
// contém p (função, struct ou enum do documento).
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
			// fn (v &Vec[T]) push(...): o T vem da struct do receptor
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
			// método do bloco: vê os parâmetros do impl e os próprios
			decls = append(decls, decl{m.P, append(append([]*ast.TypeParam{}, d.TypeParams...), m.TypeParams...), m.Name})
		}
	}
	// a declaração que contém p é a última que começa antes dele
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
				doc: "parâmetro de tipo de `" + best.what + "`"}
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

// typeString escreve um tipo da AST como no código.
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

// declNamed procura a declaração de topo chamada name no arquivo src.
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

// blockSymbol mostra uma struct ou enum inteira (até 20 linhas).
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

// ---------- texto das declarações ----------

// namePos devolve a posição do identificador name a partir de p (a palavra
// fn, struct ou enum da declaração).
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

// signature devolve o cabeçalho de uma função: da linha da declaração até
// antes do '{' do corpo (juntando as linhas de parâmetros quebrados).
func signature(text []byte, p token.Pos) string {
	lines := strings.Split(string(text), "\n")
	var parts []string
	for i := p.Line - 1; i >= 0 && i < len(lines) && i < p.Line+8; i++ {
		l := stripComment(strings.TrimSpace(lines[i]))
		if j := strings.Index(l, "{"); j >= 0 {
			parts = append(parts, strings.TrimSpace(l[:j]))
			break
		}
		parts = append(parts, l)
		if !strings.HasSuffix(l, ",") && !strings.HasSuffix(l, "(") {
			break // extern fn: sem corpo
		}
	}
	return strings.Join(parts, " ")
}

// stripComment tira um comentário // do fim da linha (fora de strings).
func stripComment(l string) string {
	inStr := false
	for i := 0; i+1 < len(l); i++ {
		switch {
		case l[i] == '"' && (i == 0 || l[i-1] != '\\'):
			inStr = !inStr
		case !inStr && l[i] == '/' && l[i+1] == '/':
			return strings.TrimSpace(l[:i])
		}
	}
	return l
}

// docAbove junta os comentários // nas linhas logo acima da linha de p.
func docAbove(text []byte, p token.Pos) string {
	lines := strings.Split(string(text), "\n")
	var doc []string
	for i := p.Line - 2; i >= 0 && i < len(lines); i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "//") {
			break
		}
		doc = append([]string{uncomment(l)}, doc...)
	}
	return formatDoc(doc)
}

// headerDoc é o comentário do começo de um arquivo (a descrição do módulo).
func headerDoc(text []byte) string {
	var doc []string
	for _, l := range strings.Split(string(text), "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "//") {
			break
		}
		doc = append(doc, uncomment(l))
	}
	return formatDoc(doc)
}

func uncomment(l string) string {
	l = strings.TrimPrefix(l, "//")
	return strings.TrimPrefix(l, " ")
}

// formatDoc transforma os comentários em markdown: linhas recuadas (exemplos
// de código) viram um bloco de código.
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

// builtinDocs descreve o que não tem declaração em arquivo.
var builtinDocs = map[string][2]string{
	"alloc":      {"alloc(T) *T · alloc(valor) *T · alloc([]T, n) []T", "Aloca no heap. Devolve um dono, que precisa ser liberado com `free` (o compilador confere)."},
	"free":       {"free(p)", "Libera um ponteiro ou slice dono. Depois disso a variável não pode mais ser usada."},
	"realloc":    {"realloc(s []T, n int) []T", "Muda o tamanho de um slice dono: consome `s` e devolve outro; as posições novas vêm zeradas."},
	"len":        {"len(s) int", "Tamanho de um slice, array ou string (em bytes)."},
	"take":       {"take(&s[i]) ?*T", "Tira o dono da posição `i` de um slice de donos, deixando `nil` no lugar."},
	"replace":    {"replace(&s[i], v) ?*T", "Põe `v` na posição `i` e devolve o dono que estava lá."},
	"drain":      {"for x in drain(s)", "Percorre um slice de donos tirando cada elemento; cada `x` precisa ser liberado ou movido."},
	"print":      {"io.print(a, b, ...)", "Escreve os valores separados por espaço e termina a linha. Cada valor precisa implementar `io.Display` e é escrito pelo seu método `write`, sobre um `io.Writer` com buffer na pilha (sem alocação). O compilador só reescreve a chamada."},
	"eprint":     {"io.eprint(a, b, ...)", "Como `io.print`, mas no erro padrão."},
	"printf":     {"io.printf(\"x = {}\\n\", x)", "Escreve o formato trocando cada `{}` por um valor (que precisa implementar `io.Display`); `{{` e `}}` escrevem chaves. O formato é uma string literal, conferida na compilação: a quantidade de `{}` precisa bater com a de valores. Especificações: `{:x}` `{:X}` `{:b}` `{:o}` `{:#x}` (inteiros), `{:.2}` (reais), `{:8}` `{:<8}` `{:>8}` `{:^8}` `{:*^8}` (largura em caracteres), `{:08}` (zeros), `{:s}` (os bytes de um `[]byte` como texto)."},
	"Thread":     {"thread.Thread[R]", "Handle de uma thread criada por `thread.spawn`, cuja função devolve `R` (`thread.Thread` quando não devolve nada). Só existe atrás de ponteiro e não aceita `free`: precisa ir para `thread.join`."},
	"spawn":      {"thread.spawn(f fn(C) R, ctx C, stack int) *thread.Thread[R]", "Cria uma thread que roda `f(ctx)`. O contexto é **movido** para a thread, e `stack` é o tamanho da pilha em bytes (pelo menos 16 KB, como `thread.STACK_64KB`). O handle devolvido precisa ir para `thread.join` (o compilador confere)."},
	"join":       {"thread.join(t *thread.Thread[R]) R", "Espera a thread terminar e devolve o resultado de `f`, cuja posse volta para quem chamou. Libera o registro da thread."},
	"format":     {"str.format(&dst, \"x = {}\", x) int", "Escreve o formato em `dst`, como `io.printf`, e devolve quantos bytes usou; dá panic se não couber. Não aloca: para saber o tamanho antes, use `str.format_len`."},
	"format_len": {"str.format_len(\"x = {}\", x) int", "Quantos bytes o formato ocupa, sem escrever nada: `n := str.format_len(...)`, `b := alloc([]byte, n)`, `str.format(&b, ...)`."},
	"eprintf":    {"io.eprintf(\"erro: {}\\n\", e)", "Como `io.printf`, mas no erro padrão."},
	"int":        {"int", "Inteiro com sinal de 64 bits. Estouro dá panic."},
	"int8":       {"int8", "Inteiro com sinal de 8 bits (-128 a 127)."},
	"int16":      {"int16", "Inteiro com sinal de 16 bits."},
	"int32":      {"int32", "Inteiro com sinal de 32 bits."},
	"int64":      {"int64", "Inteiro com sinal de 64 bits (o mesmo que `int`)."},
	"uint8":      {"uint8", "Inteiro sem sinal de 8 bits (o mesmo que `byte`)."},
	"uint16":     {"uint16", "Inteiro sem sinal de 16 bits."},
	"uint32":     {"uint32", "Inteiro sem sinal de 32 bits."},
	"uint64":     {"uint64", "Inteiro sem sinal de 64 bits."},
	"byte":       {"byte", "Um byte, de 0 a 255. Literal: `'a'`."},
	"float32":    {"float32", "Real de precisão simples (IEEE 754)."},
	"float64":    {"float64", "Real de precisão dupla (IEEE 754)."},
	"bool":       {"bool", "`true` ou `false`."},
	"string":     {"string", "Texto fixo do programa (literais). Não aloca e não precisa de `free`."},
	"nil":        {"nil", "Ponteiro nulo; só cabe em `?*T`."},
	"auto":       {"impl auto io.Display for T", "Gera o método `write` de uma struct ou enum, no formato `{1 2}` / `Circulo(2)`. Cada campo precisa implementar `io.Display`."},
	"Self":       {"Self", "Numa interface, o tipo que a implementa."},
	"true":       {"true", "Valor verdadeiro de `bool`."},
	"false":      {"false", "Valor falso de `bool`."},
	"any":        {"any", "Restrição de genérico: qualquer tipo que não seja dono."},
	"comparable": {"comparable", "Restrição de genérico: tipos que aceitam `==` e `!=`."},
	"ordered":    {"ordered", "Restrição de genérico: números, que aceitam `<`, `>` e as contas."},
	"number":     {"number", "Restrição de genérico: números (inteiros e reais), com `+ - * /`."},
	"integer":    {"integer", "Restrição de genérico: inteiros, inclusive com `%`."},
	"owned":      {"owned", "Restrição de genérico: aceita os tipos sem dono e os donos simples (`?*X`, `[]X`). Dentro do corpo, um valor `T` é tratado como dono: atribuir move, e todo valor precisa de destino (mover, guardar ou `drop`)."},
	"drop":       {"drop(x)", "Em código genérico, consome `x`: libera se for dono (`free`) e não faz nada se não for (T = int numa instância)."},
}

// ---------- arquivos da biblioteca padrão no disco ----------

// stdPath grava os arquivos da biblioteca padrão numa pasta de cache (uma por
// versão do conteúdo) e devolve o caminho de name ("std/io.mlc"), para o
// editor abrir na definição.
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
