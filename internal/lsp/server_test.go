package lsp

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// client conversa com o servidor por pipes, como um editor.
type client struct {
	t      *testing.T
	conn   *conn
	nextID int
	notes  chan *message // notificações do servidor
	resps  chan *message // respostas
	done   chan int      // código de saída de Run
}

func startServer(t *testing.T) *client {
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	c := &client{t: t, conn: newConn(toClient, fromClient), notes: make(chan *message, 100), resps: make(chan *message, 100), done: make(chan int, 1)}
	go func() {
		c.done <- Run(toServer, fromServer)
		fromServer.Close()
	}()
	go func() {
		for {
			m, err := c.conn.read()
			if err != nil {
				return
			}
			if m.ID != nil {
				c.resps <- m
			} else {
				c.notes <- m
			}
		}
	}()
	return c
}

func (c *client) request(method string, params any) *message {
	c.nextID++
	id := json.RawMessage(strings.TrimSpace(string(mustJSON(c.nextID))))
	if err := c.conn.write(&message{ID: &id, Method: method, Params: mustJSON(params)}); err != nil {
		c.t.Fatal(err)
	}
	select {
	case m := <-c.resps:
		return m
	case <-time.After(10 * time.Second):
		c.t.Fatalf("sem resposta para %s", method)
	}
	return nil
}

func (c *client) notify(method string, params any) {
	if err := c.conn.write(&message{Method: method, Params: mustJSON(params)}); err != nil {
		c.t.Fatal(err)
	}
}

// diagnostics espera a próxima publicação de diagnósticos para uri.
func (c *client) diagnostics(uri string) []diagnostic {
	for {
		select {
		case m := <-c.notes:
			if m.Method != "textDocument/publishDiagnostics" {
				continue
			}
			var p struct {
				URI         string       `json:"uri"`
				Diagnostics []diagnostic `json:"diagnostics"`
			}
			json.Unmarshal(m.Params, &p)
			if p.URI == uri {
				return p.Diagnostics
			}
		case <-time.After(10 * time.Second):
			c.t.Fatalf("sem diagnósticos para %s", uri)
		}
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func resultInto(t *testing.T, m *message, v any) {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("erro do servidor: %s", m.Error.Message)
	}
	b, _ := json.Marshal(m.Result)
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestSession(t *testing.T) {
	dir := t.TempDir()
	c := startServer(t)
	var init struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	resultInto(t, c.request("initialize", map[string]any{"capabilities": map[string]any{}}), &init)
	for _, cap := range []string{"textDocumentSync", "documentFormattingProvider", "semanticTokensProvider"} {
		if init.Capabilities[cap] == nil {
			t.Errorf("falta a capacidade %s", cap)
		}
	}
	c.notify("initialized", map[string]any{})

	// erro de tipo na linha 3 (posição 2 no LSP), que ainda não foi salvo
	uri := "file://" + filepath.Join(dir, "main.mlc")
	bad := "import \"std/io\"\nfn main() {\n    x := \"olá\" + 1\n    io.print(x)\n}\n"
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "mlc", "version": 1, "text": bad}})
	diags := c.diagnostics(uri)
	if len(diags) != 1 || diags[0].Range.Start.Line != 2 {
		t.Fatalf("esperado um erro na linha 3: %+v", diags)
	}
	t.Logf("diagnóstico: %+v", diags[0])

	// corrigido ao digitar: os diagnósticos somem
	good := "import \"std/io\"\nfn main() {\n    x:=1+2\n    io.print(x)\n}\n"
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []any{map[string]any{"text": good}}})
	if diags := c.diagnostics(uri); len(diags) != 0 {
		t.Fatalf("esperado nenhum erro: %+v", diags)
	}

	// vazamento: erro do verificador de posse
	leak := "import \"std/io\"\nfn main() {\n    p := alloc(1)\n    io.print(*p)\n}\n"
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 3}, "contentChanges": []any{map[string]any{"text": leak}}})
	if diags := c.diagnostics(uri); len(diags) == 0 || !strings.Contains(diags[0].Message, "free") {
		t.Fatalf("esperado erro de posse: %+v", diags)
	}
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 4}, "contentChanges": []any{map[string]any{"text": good}}})
	c.diagnostics(uri)

	// formatação
	var edits []textEdit
	resultInto(t, c.request("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": uri}, "options": map[string]any{"tabSize": 4, "insertSpaces": true}}), &edits)
	if len(edits) != 1 || !strings.Contains(edits[0].NewText, "x := 1 + 2") {
		t.Fatalf("formatação inesperada: %+v", edits)
	}

	// cores: "import" é palavra reservada, "std/io" é string
	var toks struct {
		Data []int `json:"data"`
	}
	resultInto(t, c.request("textDocument/semanticTokens/full", map[string]any{"textDocument": map[string]any{"uri": uri}}), &toks)
	if len(toks.Data) < 10 || toks.Data[3] != tKeyword || toks.Data[8] != tString {
		t.Fatalf("semantic tokens inesperados: %v", toks.Data)
	}

	// módulo: o erro aparece no módulo, que é checado pelo main que o importa
	modURI := "file://" + filepath.Join(dir, "geo.mlc")
	main2 := "import \"std/io\"\nimport \"geo\"\nfn main() {\n    io.print(geo.dobro(2))\n}\n"
	os.WriteFile(filepath.Join(dir, "geo.mlc"), []byte("pub fn dobro(x int) int {\n    return x * 2\n}\n"), 0o644)
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 5}, "contentChanges": []any{map[string]any{"text": main2}}})
	c.diagnostics(uri)
	modBad := "pub fn dobro(x int) int {\n    return x * true\n}\n"
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": modURI, "languageId": "mlc", "version": 1, "text": modBad}})
	diags = c.diagnostics(modURI)
	if len(diags) != 1 || diags[0].Range.Start.Line != 1 {
		t.Fatalf("esperado erro na linha 2 do módulo: %+v", diags)
	}

	// método desconhecido devolve erro; shutdown e exit encerram com 0
	if m := c.request("textDocument/references", map[string]any{}); m.Error == nil || m.Error.Code != codeMethodNotFound {
		t.Fatalf("esperado MethodNotFound: %+v", m)
	}
	c.request("shutdown", nil)
	c.notify("exit", nil)
	select {
	case code := <-c.done:
		if code != 0 {
			t.Fatalf("código de saída %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("o servidor não encerrou")
	}
}

func TestPositionsUTF16(t *testing.T) {
	src := []byte("// ação 😀\nx\n")
	l := newLineIndex(src)
	if p := l.position(src, len("// ação 😀")); p.Line != 0 || p.Character != 10 {
		t.Fatalf("posição em UTF-16 errada: %+v", p)
	}
	if p := l.end(src); p.Line != 2 || p.Character != 0 {
		t.Fatalf("fim errado: %+v", p)
	}
}
