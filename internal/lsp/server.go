// Package lsp implementa o mlc lsp: um servidor Language Server Protocol,
// via entrada e saída padrão, para editores (VS Code, Neovim...).
//
// Recursos:
//   - diagnósticos ao digitar: a cada mudança, os arquivos abertos são
//     checados de novo (sintaxe, tipos e posse) sem gerar C, e os erros
//     aparecem na linha;
//   - semantic tokens: cores para palavras reservadas, tipos, funções,
//     literais e comentários, mesmo em editores sem gramática própria;
//   - formatação do documento pelo mlc fmt.
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
	docs     map[string]string // URI → texto atual
	shutdown bool
}

// Run atende um cliente LSP em r/w até a mensagem exit. Devolve o código de
// saída do processo: 0 se o cliente pediu shutdown antes, 1 se não.
func Run(r io.Reader, w io.Writer) int {
	s := &server{conn: newConn(r, w), docs: map[string]string{}}
	for {
		m, err := s.conn.read()
		if err != nil {
			return 1 // o cliente fechou a conexão (ou mandou lixo) sem exit
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
					"change":    1, // o texto inteiro a cada mudança
					"save":      false,
				},
				"documentFormattingProvider": true,
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
			s.publish(p.TextDocument.URI, nil)
			s.diagnoseAll()
		}
	case "textDocument/formatting":
		var p docParams
		if json.Unmarshal(m.Params, &p) != nil {
			s.conn.replyError(m.ID, codeInvalidParams, "parâmetros inválidos")
			return
		}
		s.formatting(m, p.TextDocument.URI)
	case "textDocument/semanticTokens/full":
		var p docParams
		if json.Unmarshal(m.Params, &p) != nil {
			s.conn.replyError(m.ID, codeInvalidParams, "parâmetros inválidos")
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
			s.conn.replyError(m.ID, codeMethodNotFound, "método não suportado: "+m.Method)
		}
	}
}

// uriPath converte file:///a/b.mlc em /a/b.mlc.
func uriPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return filepath.Clean(filepath.FromSlash(u.Path))
}

// diagnoseAll checa de novo todos os documentos abertos (uma mudança num
// módulo pode criar ou resolver erros em quem o importa) e publica os erros.
func (s *server) diagnoseAll() {
	overlay := map[string][]byte{}
	for uri, text := range s.docs {
		if p := uriPath(uri); p != "" {
			overlay[p] = []byte(text)
		}
	}
	read := driver.Overlay(overlay)
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
		diags = []diagnostic{} // lista vazia limpa os erros antigos no editor
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
		// com erro de sintaxe não há formatação; o erro já está nos diagnósticos
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

// ---------- posições ----------
//
// O compilador conta colunas em bytes, a partir de 1; o LSP conta em
// unidades UTF-16, a partir de 0.

type lineIndex []int // deslocamento do começo de cada linha

func newLineIndex(src []byte) lineIndex {
	idx := lineIndex{0}
	for i, c := range src {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

// offset devolve o deslocamento em bytes da posição p do compilador.
func (l lineIndex) offset(src []byte, p token.Pos) int {
	if p.Line < 1 {
		return 0
	}
	if p.Line > len(l) {
		return len(src)
	}
	return min(l[p.Line-1]+max(p.Col-1, 0), len(src))
}

// position converte um deslocamento em bytes numa posição do LSP.
func (l lineIndex) position(src []byte, off int) position {
	line := sort.Search(len(l), func(i int) bool { return l[i] > off }) - 1
	return position{Line: line, Character: utf16Len(src[l[line]:off])}
}

func (l lineIndex) end(src []byte) position { return l.position(src, len(src)) }

// errorRange marca, a partir da posição do erro, a palavra ou o símbolo que
// está ali (pelo menos um caractere, sem passar do fim da linha).
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
