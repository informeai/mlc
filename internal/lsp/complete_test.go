package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mlc/internal/driver"
)

const completeSrc = `import "std/io"
import "std/vec"

// a bank account
struct Account {
    owner []byte
    balance int
}

fn (a &Account) deposit(n int) {
    a.balance += n
}

enum Shape {
    Circle(r float64)
    Empty
}

const LIMIT = 10

fn total(items &vec.Vec[int]) int {
    sum := 0
    for i := 0; i < items.len(); i++ {
        sum += items.get(i)
    }
    return sum
}

fn main() {
    acc := alloc(Account{owner: alloc([]byte, 1), balance: 0})
    v := vec.new[int](2)
    s := Shape.Empty
    match s {
        Circle(radius) => io.print(radius)
        Empty => {}
    }
    w := io.stdout()
    w.flush()
    free(acc.owner)
    free(acc)
    v.free()
}
`

// completeAt returns the suggested labels with the cursor right after at
// (the document's text can be changed with edit first).
func completeAt(t *testing.T, src, at string) []string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.mlc")
	os.WriteFile(path, []byte(src), 0o644)
	off := strings.Index(src, at)
	if off < 0 {
		t.Fatalf("piece not found: %q", at)
	}
	off += len(at)
	s := &server{docs: map[string]string{}, lastProg: map[string]*driver.Program{}}
	read := driver.Overlay(map[string][]byte{path: []byte(src)})
	prog := s.analyzeForCompletion("file://"+path, path, []byte(src), off, read)
	var labels []string
	for _, it := range complete(prog, read, []byte(src), off, completionContext([]byte(src), off)) {
		labels = append(labels, it.Label)
	}
	return labels
}

func has(labels []string, want ...string) bool {
	set := map[string]bool{}
	for _, l := range labels {
		set[l] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func TestCompleteModuleMembers(t *testing.T) {
	src := strings.Replace(completeSrc, "    w.flush()\n", "    w.flush()\n    io.pr\n", 1) // incomplete text
	got := completeAt(t, src, "io.pr")
	if !has(got, "print", "printf") || has(got, "eprint", "write") {
		t.Errorf("io.pr: %v", got)
	}
	got = completeAt(t, strings.Replace(completeSrc, "v.free()\n", "v.free()\n    vec.\n", 1), "    vec.")
	if !has(got, "new", "Vec") {
		t.Errorf("vec.: %v", got)
	}
}

func TestCompleteFieldsAndMethods(t *testing.T) {
	src := strings.Replace(completeSrc, "free(acc.owner)", "acc.\n    free(acc.owner)", 1)
	got := completeAt(t, src, "    acc.")
	if !has(got, "owner", "balance", "deposit") {
		t.Errorf("acc.: %v", got)
	}
	got = completeAt(t, strings.Replace(completeSrc, "v.free()\n", "v.free()\n    v.p\n", 1), "    v.p")
	if !has(got, "push", "pop", "put") || has(got, "len") {
		t.Errorf("v.p (generic vec): %v", got)
	}
	got = completeAt(t, strings.Replace(completeSrc, "w.flush()", "w.", 1), "    w.")
	if !has(got, "printf", "flush", "put_int") {
		t.Errorf("w. (io.Writer): %v", got)
	}
	got = completeAt(t, strings.Replace(completeSrc, "sum += items.get(i)", "sum += items.get(i)\n        items.", 1), "        items.")
	if !has(got, "len", "get", "at") {
		t.Errorf("items. (parameter &vec.Vec[int]): %v", got)
	}
}

func TestCompleteScope(t *testing.T) {
	src := strings.Replace(completeSrc, "v.free()\n", "v.free()\n    to\n", 1)
	if got := completeAt(t, src, "    to"); !has(got, "total") {
		t.Errorf("to: %v", got)
	}
	src = strings.Replace(completeSrc, "v.free()\n", "v.free()\n    a\n", 1)
	got := completeAt(t, src, "free()\n    a")
	if !has(got, "acc", "alloc", "Account") {
		t.Errorf("a: %v", got)
	}
	// sum and i belong to total, not to main
	src = strings.Replace(completeSrc, "v.free()\n", "v.free()\n    su\n", 1)
	if got := completeAt(t, src, "free()\n    su"); has(got, "sum") {
		t.Errorf("sum isn't visible in main: %v", got)
	}
	// inside total, sum and the parameter items are
	src = strings.Replace(completeSrc, "    return sum\n", "    s\n    return sum\n", 1)
	if got := completeAt(t, src, "}\n    s"); !has(got, "sum") || has(got, "s") {
		t.Errorf("inside total: %v", got)
	}
	src = strings.Replace(completeSrc, "    return sum\n", "    ite\n    return sum\n", 1)
	if got := completeAt(t, src, "    ite"); !has(got, "items") {
		t.Errorf("parameter: %v", got)
	}
	// the for's i only applies inside the for
	src = strings.Replace(completeSrc, "    return sum\n", "    zz := 1\n    i\n    return sum + zz\n", 1)
	if got := completeAt(t, src, "zz := 1\n    i"); has(got, "i") {
		t.Errorf("i outside the for: %v", got)
	}
}

func TestCompleteKeywordsAndConsts(t *testing.T) {
	src := strings.Replace(completeSrc, "v.free()\n", "v.free()\n    ma\n", 1)
	if got := completeAt(t, src, "free()\n    ma"); !has(got, "match") {
		t.Errorf("ma: %v", got)
	}
	src = strings.Replace(completeSrc, "v.free()\n", "v.free()\n    LI\n", 1)
	if got := completeAt(t, src, "    LI"); !has(got, "LIMIT") {
		t.Errorf("LI: %v", got)
	}
}

func TestCompleteMatchVariants(t *testing.T) {
	src := strings.Replace(completeSrc, "        Empty => {}\n", "        Empty => {}\n        E\n", 1)
	if got := completeAt(t, src, "{}\n        E"); !has(got, "Empty") {
		t.Errorf("variants in the match: %v", got)
	}
}

func TestCompleteImports(t *testing.T) {
	src := strings.Replace(completeSrc, `import "std/vec"`, `import "std/ve`, 1)
	got := completeAt(t, src, `import "std/ve`)
	if !has(got, "vec") || has(got, "std/vec") {
		t.Errorf("import \"std/ve: %v", got)
	}
	src = strings.Replace(completeSrc, `import "std/vec"`, `import "s`, 1)
	if got := completeAt(t, src, `import "s`); !has(got, "std/vec", "std/io") {
		t.Errorf("import \"s: %v", got)
	}
}

func TestCompleteLogger(t *testing.T) {
	src := `import "std/log"

fn main() {
    lg := log.new(log.STDERR, log.INFO)
    lg.close()
}
`
	got := completeAt(t, strings.Replace(src, "    lg.close()", "    lg.\n    lg.close()", 1), "    lg.")
	if !has(got, "info", "warn", "error", "debug", "close", "set_level") {
		t.Errorf("lg.: %v", got)
	}
}
