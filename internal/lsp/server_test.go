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

// client talks to the server through pipes, like an editor.
type client struct {
	t      *testing.T
	conn   *conn
	nextID int
	notes  chan *message // the server's notifications
	resps  chan *message // responses
	done   chan int      // Run's exit code
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
		c.t.Fatalf("no response for %s", method)
	}
	return nil
}

func (c *client) notify(method string, params any) {
	if err := c.conn.write(&message{Method: method, Params: mustJSON(params)}); err != nil {
		c.t.Fatal(err)
	}
}

// diagnostics waits for the next publication of diagnostics for uri.
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
			c.t.Fatalf("no diagnostics for %s", uri)
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
		t.Fatalf("server error: %s", m.Error.Message)
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
			t.Errorf("missing capability %s", cap)
		}
	}
	c.notify("initialized", map[string]any{})

	// type error on line 3 (position 2 in the LSP), not saved yet
	uri := "file://" + filepath.Join(dir, "main.mlc")
	bad := "import \"std/io\"\nfn main() {\n    x := \"née\" + 1\n    io.print(x)\n}\n"
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "mlc", "version": 1, "text": bad}})
	diags := c.diagnostics(uri)
	if len(diags) != 1 || diags[0].Range.Start.Line != 2 {
		t.Fatalf("expected an error on line 3: %+v", diags)
	}
	t.Logf("diagnostic: %+v", diags[0])

	// fixed while typing: the diagnostics go away
	good := "import \"std/io\"\nfn main() {\n    x:=1+2\n    io.print(x)\n}\n"
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []any{map[string]any{"text": good}}})
	if diags := c.diagnostics(uri); len(diags) != 0 {
		t.Fatalf("expected no error: %+v", diags)
	}

	// leak: an ownership checker error
	leak := "import \"std/io\"\nfn main() {\n    p := alloc(1)\n    io.print(*p)\n}\n"
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 3}, "contentChanges": []any{map[string]any{"text": leak}}})
	if diags := c.diagnostics(uri); len(diags) == 0 || !strings.Contains(diags[0].Message, "free") {
		t.Fatalf("expected an ownership error: %+v", diags)
	}
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 4}, "contentChanges": []any{map[string]any{"text": good}}})
	c.diagnostics(uri)

	// formatting
	var edits []textEdit
	resultInto(t, c.request("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": uri}, "options": map[string]any{"tabSize": 4, "insertSpaces": true}}), &edits)
	if len(edits) != 1 || !strings.Contains(edits[0].NewText, "x := 1 + 2") {
		t.Fatalf("unexpected formatting: %+v", edits)
	}

	// colors: "import" is a keyword, "std/io" is a string
	var toks struct {
		Data []int `json:"data"`
	}
	resultInto(t, c.request("textDocument/semanticTokens/full", map[string]any{"textDocument": map[string]any{"uri": uri}}), &toks)
	if len(toks.Data) < 10 || toks.Data[3] != tKeyword || toks.Data[8] != tString {
		t.Fatalf("unexpected semantic tokens: %v", toks.Data)
	}

	// module: the error shows up in the module, which is checked through the main that imports it
	modURI := "file://" + filepath.Join(dir, "geo.mlc")
	main2 := "import \"std/io\"\nimport \"geo\"\nfn main() {\n    io.print(geo.double(2))\n}\n"
	os.WriteFile(filepath.Join(dir, "geo.mlc"), []byte("pub fn double(x int) int {\n    return x * 2\n}\n"), 0o644)
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 5}, "contentChanges": []any{map[string]any{"text": main2}}})
	c.diagnostics(uri)
	modBad := "pub fn double(x int) int {\n    return x * true\n}\n"
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": modURI, "languageId": "mlc", "version": 1, "text": modBad}})
	diags = c.diagnostics(modURI)
	if len(diags) != 1 || diags[0].Range.Start.Line != 1 {
		t.Fatalf("expected an error on line 2 of the module: %+v", diags)
	}

	// an unknown method returns an error; shutdown and exit end with 0
	if m := c.request("textDocument/references", map[string]any{}); m.Error == nil || m.Error.Code != codeMethodNotFound {
		t.Fatalf("expected MethodNotFound: %+v", m)
	}
	c.request("shutdown", nil)
	c.notify("exit", nil)
	select {
	case code := <-c.done:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server didn't stop")
	}
}

func TestPositionsUTF16(t *testing.T) {
	src := []byte("// naïf 😀\nx\n")
	l := newLineIndex(src)
	if p := l.position(src, len("// naïf 😀")); p.Line != 0 || p.Character != 10 {
		t.Fatalf("wrong UTF-16 position: %+v", p)
	}
	if p := l.end(src); p.Line != 2 || p.Character != 0 {
		t.Fatalf("wrong end: %+v", p)
	}
}
