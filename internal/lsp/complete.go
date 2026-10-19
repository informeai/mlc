package lsp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/driver"
	"mlc/internal/token"
	"mlc/std"
)

// Sugestões ao digitar (textDocument/completion)
//
// O contexto vem do texto antes do cursor:
//
//	io.pr|        os membros públicos do módulo io
//	conta.sa|     os campos e métodos do tipo de conta
//	import "std/v|  os módulos
//	tot|          o que é visível ali: variáveis, funções, tipos, módulos,
//	              funções embutidas e palavras reservadas
//
// Os tipos vêm da análise do programa. Enquanto se digita, o texto quase
// sempre está incompleto (io. sem nada depois); então a análise é tentada
// com o texto atual, depois com um nome provisório no lugar do cursor,
// depois com a linha do cursor em branco e, por fim, a última que deu certo
// para o documento é usada.

type completionItem struct {
	Label         string  `json:"label"`
	Kind          int     `json:"kind"`
	Detail        string  `json:"detail,omitempty"`
	Documentation *markup `json:"documentation,omitempty"`
	SortText      string  `json:"sortText,omitempty"`
}

type markup struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// CompletionItemKind do LSP.
const (
	kindMethod     = 2
	kindFunction   = 3
	kindField      = 5
	kindVariable   = 6
	kindInterface  = 8
	kindModule     = 9
	kindEnum       = 13
	kindKeyword    = 14
	kindEnumMember = 20
	kindConstant   = 21
	kindStruct     = 22
)

// placeholder é o nome posto no lugar do cursor quando o texto atual não
// passa pelo parser (io. sem nada depois).
const placeholder = "mlc_completion"

// cctx é o contexto do cursor, lido do texto.
type cctx struct {
	prefix     string   // o pedaço de nome já digitado
	chain      []string // antes do '.': conta.dono. dá [conta dono]; nil sem '.'
	member     bool     // depois de um '.'
	inImport   bool     // dentro do caminho de um import
	importPath string   // o caminho digitado até o cursor (std/v)
}

func completionContext(src []byte, off int) cctx {
	start := off
	for start > 0 && isWordByte(src[start-1]) {
		start--
	}
	ctx := cctx{prefix: string(src[start:off])}
	lineStart := start
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}
	line := string(src[lineStart:off])
	if q := strings.Index(line, "\""); q >= 0 && strings.HasPrefix(strings.TrimSpace(line), "import") && strings.Count(line, "\"") == 1 {
		ctx.inImport, ctx.importPath = true, line[q+1:]
		return ctx
	}
	if start == 0 || src[start-1] != '.' {
		return ctx
	}
	ctx.member = true
	// a cadeia de nomes antes do '.': a.b.c.
	end := start - 1
	for {
		w := end
		for w > 0 && isWordByte(src[w-1]) {
			w--
		}
		if w == end {
			ctx.chain = nil // )., ]. ou nada: o tipo não é lido pelo texto
			return ctx
		}
		ctx.chain = append([]string{string(src[w:end])}, ctx.chain...)
		if w > 0 && src[w-1] == '.' {
			end = w - 1
			continue
		}
		return ctx
	}
}

func (s *server) completion(m *message) {
	var p struct {
		TextDocument docID    `json:"textDocument"`
		Position     position `json:"position"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		s.conn.replyError(m.ID, codeInvalidParams, "parâmetros inválidos")
		return
	}
	text, ok := s.docs[p.TextDocument.URI]
	path := uriPath(p.TextDocument.URI)
	if !ok || path == "" {
		s.conn.reply(m.ID, nil)
		return
	}
	src := []byte(text)
	off := newLineIndex(src).offsetOf(src, p.Position)
	read := s.loader()
	prog := s.analyzeForCompletion(p.TextDocument.URI, path, src, off, read)
	items := complete(prog, read, src, off, completionContext(src, off))
	if items == nil {
		items = []completionItem{}
	}
	s.conn.reply(m.ID, map[string]any{"isIncomplete": false, "items": items})
}

// analyzeForCompletion analisa o programa do documento, tolerando o texto
// incompleto de quem está digitando.
func (s *server) analyzeForCompletion(uri, path string, src []byte, off int, read driver.Loader) *driver.Program {
	if prog := driver.Analyze(path, read); prog != nil {
		s.lastProg[uri] = prog
		return prog
	}
	with := func(text []byte) driver.Loader {
		return func(name string) ([]byte, error) {
			if filepath.Clean(name) == filepath.Clean(path) {
				return text, nil
			}
			return read(name)
		}
	}
	// um nome provisório no cursor: io. vira io.mlc_completion
	patched := append(append(append([]byte{}, src[:off]...), placeholder...), src[off:]...)
	if prog := driver.Analyze(path, with(patched)); prog != nil {
		s.lastProg[uri] = prog
		return prog
	}
	// a linha do cursor em branco (do mesmo tamanho, para as posições não mudarem)
	blank := append([]byte{}, src...)
	for i := off - 1; i >= 0 && blank[i] != '\n'; i-- {
		blank[i] = ' '
	}
	for i := off; i < len(blank) && blank[i] != '\n'; i++ {
		blank[i] = ' '
	}
	if prog := driver.Analyze(path, with(blank)); prog != nil {
		s.lastProg[uri] = prog
		return prog
	}
	return s.lastProg[uri]
}

// complete monta as sugestões. prog pode ser nil (nada analisado ainda).
func complete(prog *driver.Program, read driver.Loader, src []byte, off int, ctx cctx) []completionItem {
	if ctx.inImport {
		return importItems(prog, ctx.importPath)
	}
	var items []completionItem
	if prog == nil {
		if !ctx.member {
			items = append(items, keywordItems()...)
		}
		return filterItems(items, ctx.prefix)
	}
	n := newNavigator(prog, read, src)
	sc := newScope(n, src, off)
	if ctx.member {
		if ctx.chain == nil {
			return nil
		}
		if len(ctx.chain) == 1 && sc.local(ctx.chain[0]) == nil {
			if mod := n.moduleByName(ctx.chain[0]); mod != nil {
				return filterItems(moduleItems(n, mod), ctx.prefix)
			}
		}
		if t := sc.chainType(ctx.chain); t != nil {
			return filterItems(memberItems(n, t), ctx.prefix)
		}
		return nil
	}
	items = append(items, sc.items()...)
	items = append(items, topLevelItems(n)...)
	items = append(items, sc.variantItems()...)
	for _, imp := range prog.Target.AST.Imports {
		name := imp.Path[strings.LastIndex(imp.Path, "/")+1:]
		items = append(items, completionItem{Label: name, Kind: kindModule, Detail: "import \"" + imp.Path + "\"", SortText: "2"})
	}
	items = append(items, keywordItems()...)
	return filterItems(dedupe(items), ctx.prefix)
}

// filterItems deixa os itens que começam com o que já foi digitado, sem
// diferenciar maiúsculas (como os editores filtram).
func filterItems(items []completionItem, prefix string) []completionItem {
	if prefix == "" {
		return items
	}
	low := strings.ToLower(prefix)
	var out []completionItem
	for _, it := range items {
		if strings.HasPrefix(strings.ToLower(it.Label), low) && it.Label != prefix {
			out = append(out, it)
		}
	}
	return out
}

func dedupe(items []completionItem) []completionItem {
	seen := map[string]bool{}
	var out []completionItem
	for _, it := range items {
		if !seen[it.Label] {
			seen[it.Label] = true
			out = append(out, it)
		}
	}
	return out
}

func doc(text string) *markup {
	if text == "" {
		return nil
	}
	return &markup{Kind: "markdown", Value: text}
}

// ---------- módulos ----------

// importItems sugere os módulos da biblioteca padrão e os .mlc da pasta do
// programa. O editor troca só o pedaço depois da última '/'.
func importItems(prog *driver.Program, typed string) []completionItem {
	var paths []string
	entries, _ := fs.ReadDir(std.FS, ".")
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".mlc") && !strings.HasSuffix(name, "_test.mlc") {
			paths = append(paths, "std/"+strings.TrimSuffix(name, ".mlc"))
		}
	}
	if prog != nil {
		dir := filepath.Dir(prog.Target.File)
		local, _ := os.ReadDir(dir)
		for _, e := range local {
			name := e.Name()
			if strings.HasSuffix(name, ".mlc") && name != "main.mlc" && !strings.HasSuffix(name, "_test.mlc") && filepath.Join(dir, name) != prog.Target.File {
				paths = append(paths, strings.TrimSuffix(name, ".mlc"))
			}
		}
	}
	sort.Strings(paths)
	cut := strings.LastIndex(typed, "/") + 1
	var items []completionItem
	for _, p := range paths {
		if strings.HasPrefix(p, typed) {
			items = append(items, completionItem{Label: p[cut:], Kind: kindModule, Detail: p})
		}
	}
	return items
}

// moduleBuiltins são as funções e tipos embutidos no compilador, que não
// estão declarados no arquivo do módulo.
var moduleBuiltins = map[string][]struct {
	name, doc string
	kind      int
}{
	"std/io":     {{"print", "print", kindFunction}, {"eprint", "eprint", kindFunction}, {"printf", "printf", kindFunction}, {"eprintf", "eprintf", kindFunction}},
	"std/str":    {{"format", "format", kindFunction}, {"format_len", "format_len", kindFunction}},
	"std/thread": {{"spawn", "spawn", kindFunction}, {"join", "join", kindFunction}, {"Thread", "Thread", kindStruct}},
	"std/chan":   {{"new", "chan.new", kindFunction}, {"Sender", "Sender", kindStruct}, {"Receiver", "Receiver", kindStruct}},
	"std/mutex":  {{"new", "mutex.new", kindFunction}, {"Mutex", "Mutex", kindStruct}, {"Guard", "Guard", kindStruct}},
}

// moduleItems sugere o que o módulo exporta.
func moduleItems(n *navigator, mod *checker.Source) []completionItem {
	text := n.fileText(mod.File)
	var items []completionItem
	add := func(name string, kind int, p token.Pos) {
		items = append(items, completionItem{Label: name, Kind: kind, Detail: strings.TrimSpace(lineAt(text, p)), Documentation: doc(docAbove(text, p)), SortText: "1"})
	}
	f := mod.AST
	for _, d := range f.Funcs {
		if d.Pub && d.Recv == nil {
			add(d.Name, kindFunction, d.P)
		}
	}
	for _, d := range f.Structs {
		if d.Pub {
			add(d.Name, kindStruct, d.P)
		}
	}
	for _, d := range f.Enums {
		if d.Pub {
			add(d.Name, kindEnum, d.P)
		}
	}
	for _, d := range f.Consts {
		if d.Pub {
			add(d.Name, kindConstant, d.P)
		}
	}
	for _, d := range f.Interfaces {
		if d.Pub {
			add(d.Name, kindInterface, d.P)
		}
	}
	for _, b := range moduleBuiltins[mod.Path] {
		d := builtinDocs[b.doc]
		items = append(items, completionItem{Label: b.name, Kind: b.kind, Detail: d[0], Documentation: doc(d[1]), SortText: "1"})
	}
	return items
}

// topLevelItems sugere as declarações do próprio arquivo.
func topLevelItems(n *navigator) []completionItem {
	text := n.text
	var items []completionItem
	add := func(name string, kind int, p token.Pos) {
		items = append(items, completionItem{Label: name, Kind: kind, Detail: strings.TrimSpace(lineAt(text, p)), Documentation: doc(docAbove(text, p)), SortText: "1"})
	}
	f := n.prog.Target.AST
	for _, d := range f.Funcs {
		if d.Recv == nil {
			add(d.Name, kindFunction, d.P)
		}
	}
	for _, d := range f.Structs {
		add(d.Name, kindStruct, d.P)
	}
	for _, d := range f.Enums {
		add(d.Name, kindEnum, d.P)
	}
	for _, d := range f.Consts {
		add(d.Name, kindConstant, d.P)
	}
	for _, d := range f.Interfaces {
		add(d.Name, kindInterface, d.P)
	}
	return items
}

var keywords = []string{"fn", "struct", "enum", "interface", "impl", "import", "pub", "const", "var", "if", "else", "for", "in",
	"match", "return", "break", "continue", "extern", "link", "nil", "true", "false"}

var builtinFuncs = []string{"alloc", "free", "realloc", "len", "take", "replace", "drop", "drain"}

var basicTypeNames = []string{"int", "int8", "int16", "int32", "int64", "byte", "uint16", "uint32", "uint64", "float32", "float64", "bool", "string"}

func keywordItems() []completionItem {
	var items []completionItem
	for _, k := range keywords {
		items = append(items, completionItem{Label: k, Kind: kindKeyword, SortText: "9"})
	}
	for _, b := range builtinFuncs {
		d := builtinDocs[b]
		items = append(items, completionItem{Label: b, Kind: kindFunction, Detail: d[0], Documentation: doc(d[1]), SortText: "3"})
	}
	for _, t := range basicTypeNames {
		d := builtinDocs[t]
		items = append(items, completionItem{Label: t, Kind: kindStruct, Detail: d[0], Documentation: doc(d[1]), SortText: "4"})
	}
	return items
}

// ---------- membros de um tipo ----------

// memberItems sugere os campos e métodos de um valor do tipo t.
func memberItems(n *navigator, t checker.Type) []completionItem {
	switch u := t.(type) {
	case *checker.Borrow:
		t = u.Elem
	case *checker.Pointer:
		t = u.Elem
	}
	var items []completionItem
	if s, ok := t.(*checker.Struct); ok {
		items = append(items, specialMembers(s)...)
		if !s.Extern {
			for _, f := range s.Fields {
				items = append(items, completionItem{Label: f.Name, Kind: kindField, Detail: f.Name + " " + f.Type.String(), SortText: "0"})
			}
		}
		items = append(items, declaredMethods(n, s)...)
	}
	items = append(items, implMethods(n, t)...)
	return dedupe(items)
}

// specialMembers são os métodos embutidos de threads, canais, mutex e do
// io.Writer (printf).
func specialMembers(s *checker.Struct) []completionItem {
	var names []string
	prefix := ""
	switch {
	case s.ChanEnd == checker.ChanSender:
		names, prefix = []string{"send", "clone", "close"}, "chan."
	case s.ChanEnd == checker.ChanReceiver:
		names, prefix = []string{"recv", "sender", "clone", "close"}, "chan."
	case s.MutexKind == checker.MutexHandle:
		names, prefix = []string{"lock", "clone", "close", "notify", "notify_all"}, "mutex."
	case s.MutexKind == checker.MutexGuard:
		names, prefix = []string{"get", "unlock", "wait", "notify", "notify_all"}, "mutex."
	case s.Decl != nil && s.Decl.Name == "Logger" && strings.HasSuffix(s.File, "log.mlc"):
		names, prefix = []string{"debug", "info", "warn", "error"}, "Logger."
	case s.Decl != nil && s.Decl.Name == "Writer" && strings.HasSuffix(s.File, "io.mlc"):
		d := builtinDocs["Writer.printf"]
		return []completionItem{{Label: "printf", Kind: kindMethod, Detail: d[0], Documentation: doc(d[1]), SortText: "1"}}
	}
	var items []completionItem
	for _, name := range names {
		d := builtinDocs[prefix+name]
		items = append(items, completionItem{Label: name, Kind: kindMethod, Detail: d[0], Documentation: doc(d[1]), SortText: "1"})
	}
	return items
}

// declaredMethods lê os métodos da struct no arquivo dela (os de structs
// genéricas também, pela declaração).
func declaredMethods(n *navigator, s *checker.Struct) []completionItem {
	base := s
	if s.Origin != nil {
		base = s.Origin
	}
	if base.Decl == nil {
		return nil
	}
	var src *checker.Source
	for i := range n.prog.Srcs {
		if n.prog.Srcs[i].File == base.File {
			src = &n.prog.Srcs[i]
		}
	}
	if src == nil {
		return nil
	}
	sameFile := filepath.Clean(src.File) == filepath.Clean(n.prog.Target.File)
	text := n.fileText(src.File)
	var items []completionItem
	for _, d := range src.AST.Funcs {
		if d.Recv == nil || (!d.Pub && !sameFile) {
			continue
		}
		rt := d.Recv.Type
		if rt.Mod == ast.TypeBorrow || rt.Mod == ast.TypePointer {
			rt = rt.Elem
		}
		if rt.Name != base.Decl.Name || rt.Module != "" {
			continue
		}
		items = append(items, completionItem{Label: d.Name, Kind: kindMethod, Detail: strings.TrimSpace(lineAt(text, d.P)), Documentation: doc(docAbove(text, d.P)), SortText: "1"})
	}
	return items
}

// implMethods sugere os métodos que vêm de um impl (cmp, eq, hash, write).
func implMethods(n *navigator, t checker.Type) []completionItem {
	var items []completionItem
	for _, im := range n.prog.Info.Impls {
		if !implFor(im.Type, t) || im.Iface == nil || im.Iface.Decl == nil {
			continue
		}
		text := n.fileText(im.Iface.File)
		for _, md := range im.Iface.Decl.Methods {
			items = append(items, completionItem{Label: md.Name, Kind: kindMethod, Detail: strings.TrimSpace(lineAt(text, md.P)), Documentation: doc(docAbove(text, md.P)), SortText: "1"})
		}
	}
	return items
}

// implFor diz se um impl do tipo it vale para valores do tipo t (o mesmo
// tipo, a mesma struct genérica, ou um impl genérico de []T para um slice).
func implFor(it, t checker.Type) bool {
	if it == t {
		return true
	}
	switch a := it.(type) {
	case *checker.Struct:
		b, ok := t.(*checker.Struct)
		return ok && a.Origin != nil && (b.Origin == a.Origin || b == a.Origin)
	case *checker.Slice:
		switch t.(type) {
		case *checker.Slice, *checker.Array:
			return true
		}
	}
	return false
}

// ---------- o que é visível no cursor ----------

// scope encontra as variáveis visíveis no cursor, na função que o contém.
type scope struct {
	n      *navigator
	lines  lineIndex
	off    int
	fn     *ast.FuncDecl
	blocks []*ast.Block
	locals []*checker.Local // as visíveis, a última declaração de cada nome por último
}

func newScope(n *navigator, src []byte, off int) *scope {
	sc := &scope{n: n, lines: newLineIndex(src), off: off}
	for _, d := range n.prog.Target.AST.Funcs {
		if d.Body != nil && sc.at(d.P) <= off && off <= sc.at(d.Body.End) {
			sc.fn = d
		}
	}
	if sc.fn == nil {
		return sc
	}
	walkNodes(sc.fn.Body, func(x any) {
		if b, ok := x.(*ast.Block); ok {
			sc.blocks = append(sc.blocks, b)
		}
	})
	info := n.prog.Info
	var found []*checker.Local
	// os parâmetros (e o receptor): pela função checada, ou pelos usos
	for _, f := range info.Funcs {
		if f.Decl == sc.fn {
			found = append(found, f.Params...)
		}
	}
	if len(found) == 0 {
		for _, l := range info.Locals {
			if l.Param && sc.at(sc.fn.P) <= sc.at(l.Pos) && sc.at(l.Pos) <= sc.at(sc.fn.Body.P) {
				found = append(found, l)
			}
		}
	}
	for vd, l := range info.Decls {
		if p := sc.at(vd.P); p < off && sc.inside(p) && sc.visibleFrom(p) {
			found = append(found, l)
		}
	}
	for fs, l := range info.ForVars {
		if fs.Body != nil && sc.at(fs.Body.P) <= off && off <= sc.at(fs.Body.End) {
			found = append(found, l)
		}
	}
	walkNodes(sc.fn.Body, func(x any) {
		m, ok := x.(*ast.MatchStmt)
		if !ok {
			return
		}
		for i, arm := range m.Arms {
			start := sc.at(arm.P)
			end := sc.at(maxPosOf(arm))
			if i+1 < len(m.Arms) {
				end = min(end, sc.at(m.Arms[i+1].P))
			}
			if start <= off && off <= end+1 {
				for _, p := range arm.Patterns {
					for _, l := range info.PatternBinds[p] {
						if l != nil {
							found = append(found, l)
						}
					}
				}
			}
		}
	})
	sort.SliceStable(found, func(i, j int) bool { return sc.at(found[i].Pos) < sc.at(found[j].Pos) })
	sc.locals = found
	return sc
}

func (sc *scope) at(p token.Pos) int { return sc.lines.offset(sc.n.text, p) }

func (sc *scope) inside(p int) bool {
	return sc.at(sc.fn.Body.P) <= p && p <= sc.at(sc.fn.Body.End)
}

// visibleFrom diz se uma declaração em p ainda vale no cursor: o bloco mais
// interno que a contém também contém o cursor.
func (sc *scope) visibleFrom(p int) bool {
	var inner *ast.Block
	for _, b := range sc.blocks {
		if sc.at(b.P) <= p && p <= sc.at(b.End) {
			if inner == nil || sc.at(b.P) >= sc.at(inner.P) {
				inner = b
			}
		}
	}
	return inner == nil || (sc.at(inner.P) <= sc.off && sc.off <= sc.at(inner.End))
}

// local devolve a variável visível com esse nome (a declarada por último).
func (sc *scope) local(name string) *checker.Local {
	for i := len(sc.locals) - 1; i >= 0; i-- {
		if sc.locals[i].Name == name {
			return sc.locals[i]
		}
	}
	return nil
}

// chainType devolve o tipo de a.b.c, com a uma variável visível.
func (sc *scope) chainType(chain []string) checker.Type {
	l := sc.local(chain[0])
	if l == nil {
		return nil
	}
	t := l.Type
	for _, name := range chain[1:] {
		switch u := t.(type) {
		case *checker.Borrow:
			t = u.Elem
		case *checker.Pointer:
			t = u.Elem
		}
		s, ok := t.(*checker.Struct)
		if !ok {
			return nil
		}
		f := s.Field(name)
		if f == nil {
			return nil
		}
		t = f.Type
	}
	return t
}

func (sc *scope) items() []completionItem {
	var items []completionItem
	seen := map[string]bool{}
	for i := len(sc.locals) - 1; i >= 0; i-- {
		l := sc.locals[i]
		if seen[l.Name] || l.Name == placeholder || l.Name == "_" {
			continue
		}
		seen[l.Name] = true
		items = append(items, completionItem{Label: l.Name, Kind: kindVariable, Detail: l.Name + " " + l.Type.String(), SortText: "0"})
	}
	return items
}

// variantItems sugere as variantes do enum de um match que contém o cursor.
func (sc *scope) variantItems() []completionItem {
	if sc.fn == nil {
		return nil
	}
	var items []completionItem
	// o match não guarda onde o } dele fica: ele vai até o próximo comando do
	// bloco (ou até o fim do bloco)
	walkNodes(sc.fn.Body, func(x any) {
		b, ok := x.(*ast.Block)
		if !ok {
			return
		}
		for i, st := range b.Stmts {
			m, ok := st.(*ast.MatchStmt)
			if !ok {
				continue
			}
			end := sc.at(b.End)
			if i+1 < len(b.Stmts) {
				end = sc.at(b.Stmts[i+1].Pos())
			}
			if sc.at(m.P) <= sc.off && sc.off <= end {
				items = append(items, sc.enumVariants(m)...)
			}
		}
	})
	return dedupe(items)
}

// enumVariants sugere as variantes do enum sobre o qual o match é feito.
func (sc *scope) enumVariants(m *ast.MatchStmt) []completionItem {
	t := sc.n.prog.Info.Types[m.Subject]
	if b, ok := t.(*checker.Borrow); ok {
		t = b.Elem
	}
	en, ok := t.(*checker.Enum)
	if !ok {
		return nil
	}
	var items []completionItem
	for _, v := range en.Variants {
		detail := v.Name
		if len(v.Fields) > 0 {
			var fs []string
			for _, f := range v.Fields {
				fs = append(fs, f.Name+" "+f.Type.String())
			}
			detail += "(" + strings.Join(fs, ", ") + ")"
		}
		items = append(items, completionItem{Label: v.Name, Kind: kindEnumMember, Detail: detail, SortText: "0"})
	}
	return items
}

// walkNodes visita os nós da AST a partir de root.
func walkNodes(root any, f func(any)) {
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return
			}
			f(v.Interface())
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
	walk(reflect.ValueOf(root))
}

// maxPosOf devolve a maior posição dentro de um nó (onde ele termina, mais
// ou menos).
func maxPosOf(root any) token.Pos {
	var best token.Pos
	walkNodes(root, func(x any) {
		if b, ok := x.(*ast.Block); ok && best.Before(b.End) {
			best = b.End
		}
	})
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Struct:
			if p, ok := v.Interface().(token.Pos); ok {
				if best.Before(p) {
					best = p
				}
				return
			}
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(root))
	return best
}
