// Package lsp implements mlc lsp: a Language Server Protocol server,
// over standard input and output, for editors (VS Code, Neovim...).
//
// Features:
//   - diagnostics as you type: on each change, the open files are
//     checked again (syntax, types and ownership) without generating C, and the errors
//     show up on the line;
//   - semantic tokens: colors for keywords, types, functions,
//     literals and comments, even in editors without their own grammar;
//   - document formatting through mlc fmt;
//   - hover, go to definition and completions (complete.go).
package lsp

import (
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"mlc/internal/driver"
	"mlc/internal/format"
	"mlc/internal/token"
)

type server struct {
	conn     *conn
	docs     map[string]string          // URI → current text
	lastProg map[string]*driver.Program // URI → the last analysis that succeeded (for completions)
	shutdown bool
}

// Run serves an LSP client on r/w until the exit message. Returns the process's
// exit code: 0 if the client asked for shutdown first, 1 otherwise.
func Run(r io.Reader, w io.Writer) int {
	s := &server{conn: newConn(r, w), docs: map[string]string{}, lastProg: map[string]*driver.Program{}}
	for {
		m, err := s.conn.read()
		if err != nil {
			return 1 // the client closed the connection (or sent garbage) without exit
		}
		if m.Method == "exit" {
			if s.shutdown {
				return 0
			}
			return 1
		}
		s.handle(m)
	}
}

func (s *server) handle(m *message) {
	isRequest := m.ID != nil
	switch m.Method {
	case "initialize":
		s.conn.reply(m.ID, map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync": map[string]any{
					"openClose": true,
					"change":    1, // the whole text on each change
					"save":      false,
				},
				"documentFormattingProvider": true,
				"completionProvider":         map[string]any{"triggerCharacters": []string{".", "\""}},
				"hoverProvider":              true,
				"definitionProvider":         true,
				"semanticTokensProvider": map[string]any{
					"legend": map[string]any{
						"tokenTypes":     tokenTypes,
						"tokenModifiers": tokenModifiers,
					},
					"full": true,
				},
			},
			"serverInfo": map[string]any{"name": "mlc"},
		})
	case "initialized":
	case "shutdown":
		s.shutdown = true
		s.conn.reply(m.ID, nil)
	case "textDocument/didOpen":
		var p didOpenParams
		if json.Unmarshal(m.Params, &p) == nil {
			s.docs[p.TextDocument.URI] = p.TextDocument.Text
			s.diagnoseAll()
		}
	case "textDocument/didChange":
		var p didChangeParams
		if json.Unmarshal(m.Params, &p) == nil && len(p.ContentChanges) > 0 {
			s.docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text
			s.diagnoseAll()
		}
	case "textDocument/didClose":
		var p docParams
		if json.Unmarshal(m.Params, &p) == nil {
			delete(s.docs, p.TextDocument.URI)
			delete(s.lastProg, p.TextDocument.URI)
			s.publish(p.TextDocument.URI, nil)
			s.diagnoseAll()
		}
	case "textDocument/formatting":
		var p docParams
		if json.Unmarshal(m.Params, &p) != nil {
			s.conn.replyError(m.ID, codeInvalidParams, "invalid parameters")
			return
		}
		s.formatting(m, p.TextDocument.URI)
	case "textDocument/hover":
		s.hover(m)
	case "textDocument/completion":
		s.completion(m)
	case "textDocument/definition":
		s.definition(m)
	case "textDocument/semanticTokens/full":
		var p docParams
		if json.Unmarshal(m.Params, &p) != nil {
			s.conn.replyError(m.ID, codeInvalidParams, "invalid parameters")
			return
		}
		text, ok := s.docs[p.TextDocument.URI]
		if !ok {
			s.conn.reply(m.ID, nil)
			return
		}
		s.conn.reply(m.ID, map[string]any{"data": semanticTokens([]byte(text))})
	default:
		if isRequest {
			s.conn.replyError(m.ID, codeMethodNotFound, "method not supported: "+m.Method)
		}
	}
}

// uriPath converts file:///a/b.mlc into /a/b.mlc.
func uriPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return filepath.Clean(filepath.FromSlash(u.Path))
}

// loader reads the program's files, with the not-yet-saved text of the
// open documents instead of what's on the disk.
func (s *server) loader() driver.Loader {
	overlay := map[string][]byte{}
	for uri, text := range s.docs {
		if p := uriPath(uri); p != "" {
			overlay[p] = []byte(text)
		}
	}
	return driver.Overlay(overlay)
}

// navigate prepares navigation in the document and converts the cursor position.
func (s *server) navigate(params json.RawMessage) (*navigator, int, bool) {
	var p struct {
		TextDocument docID    `json:"textDocument"`
		Position     position `json:"position"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil, 0, false
	}
	text, ok := s.docs[p.TextDocument.URI]
	path := uriPath(p.TextDocument.URI)
	if !ok || path == "" {
		return nil, 0, false
	}
	read := s.loader()
	prog := driver.Analyze(path, read)
	if prog == nil {
		return nil, 0, false
	}
	src := []byte(text)
	return newNavigator(prog, read, src), newLineIndex(src).offsetOf(src, p.Position), true
}

func (s *server) hover(m *message) {
	nav, off, ok := s.navigate(m.Params)
	if !ok {
		s.conn.reply(m.ID, nil)
		return
	}
	sym, tok := nav.resolve(off)
	if sym == nil || sym.code == "" {
		s.conn.reply(m.ID, nil)
		return
	}
	value := "```mlc\n" + sym.code + "\n```"
	if sym.doc != "" {
		value += "\n\n" + sym.doc
	}
	lines := newLineIndex(nav.text)
	s.conn.reply(m.ID, map[string]any{
		"contents": map[string]any{"kind": "markdown", "value": value},
		"range":    lspRange{Start: lines.position(nav.text, tok.Off), End: lines.position(nav.text, tok.End)},
	})
}

func (s *server) definition(m *message) {
	nav, off, ok := s.navigate(m.Params)
	if !ok {
		s.conn.reply(m.ID, nil)
		return
	}
	sym, _ := nav.resolve(off)
	if sym == nil || sym.noDef || sym.file == "" {
		s.conn.reply(m.ID, nil)
		return
	}
	path := sym.file
	if nav.isStd(sym.file) {
		path = stdPath(sym.file)
	}
	text := nav.fileText(sym.file)
	var start position
	if sym.pos.Line > 0 {
		start = newLineIndex(text).position(text, newLineIndex(text).offset(text, sym.pos))
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	s.conn.reply(m.ID, map[string]any{"uri": uri, "range": lspRange{Start: start, End: start}})
}

// diagnoseAll checks every open document again (a change in a
// module can create or fix errors in whoever imports it) and publishes the errors.
func (s *server) diagnoseAll() {
	read := s.loader()
	uris := make([]string, 0, len(s.docs))
	for uri := range s.docs {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		path := uriPath(uri)
		if path == "" || filepath.Ext(path) != ".mlc" {
			continue
		}
		text := []byte(s.docs[uri])
		lines := newLineIndex(text)
		var diags []diagnostic
		for _, e := range driver.Diagnose(path, read) {
			diags = append(diags, diagnostic{
				Range:    lines.errorRange(text, e.Pos),
				Severity: 1,
				Source:   "mlc",
				Message:  e.Msg,
			})
		}
		s.publish(uri, diags)
	}
}

func (s *server) publish(uri string, diags []diagnostic) {
	if diags == nil {
		diags = []diagnostic{} // an empty list clears the old errors in the editor
	}
	s.conn.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": diags})
}

func (s *server) formatting(m *message, uri string) {
	text, ok := s.docs[uri]
	if !ok {
		s.conn.reply(m.ID, nil)
		return
	}
	out, errs := format.Source(uriPath(uri), []byte(text))
	if len(errs) > 0 {
		// with a syntax error there's no formatting; the error is already in the diagnostics
		s.conn.reply(m.ID, []textEdit{})
		return
	}
	if string(out) == text {
		s.conn.reply(m.ID, []textEdit{})
		return
	}
	lines := newLineIndex([]byte(text))
	s.conn.reply(m.ID, []textEdit{{Range: lspRange{End: lines.end([]byte(text))}, NewText: string(out)}})
}

// ---------- positions ----------
//
// The compiler counts columns in bytes, starting at 1; the LSP counts in
// UTF-16 units, starting at 0.

type lineIndex []int // offset of the start of each line

func newLineIndex(src []byte) lineIndex {
	idx := lineIndex{0}
	for i, c := range src {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

// offset returns the byte offset of the compiler position p.
func (l lineIndex) offset(src []byte, p token.Pos) int {
	if p.Line < 1 {
		return 0
	}
	if p.Line > len(l) {
		return len(src)
	}
	return min(l[p.Line-1]+max(p.Col-1, 0), len(src))
}

// position converts a byte offset into an LSP position.
func (l lineIndex) position(src []byte, off int) position {
	line := sort.Search(len(l), func(i int) bool { return l[i] > off }) - 1
	return position{Line: line, Character: utf16Len(src[l[line]:off])}
}

func (l lineIndex) end(src []byte) position { return l.position(src, len(src)) }

// offsetOf converts an LSP position (column in UTF-16) into a byte
// offset.
func (l lineIndex) offsetOf(src []byte, p position) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(l) {
		return len(src)
	}
	off, units := l[p.Line], 0
	for off < len(src) && src[off] != '\n' && units < p.Character {
		r, size := utf8.DecodeRune(src[off:])
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
		off += size
	}
	return off
}

// errorRange marks, starting at the error's position, the word or symbol that
// is there (at least one character, without going past the end of the line).
func (l lineIndex) errorRange(src []byte, p token.Pos) lspRange {
	start := l.offset(src, p)
	end := start
	for end < len(src) && isWordByte(src[end]) {
		end++
	}
	if end == start && end < len(src) && src[end] != '\n' {
		_, size := utf8.DecodeRune(src[end:])
		end += size
	}
	return lspRange{Start: l.position(src, start), End: l.position(src, end)}
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func utf16Len(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
