// Package doc extracts the documentation of a .mlc module: the // comments right
// above each declaration and the one at the top of the file, as in Go. It's used by
// mlc doc (in the terminal) and by the LSP (in the editor).
package doc

import (
	"strings"

	"mlc/internal/token"
)

// Comment returns the lines of the // comment right above p's line, without
// the slashes. A blank line or a code line ends the comment.
func Comment(text []byte, p token.Pos) []string {
	lines := strings.Split(string(text), "\n")
	var doc []string
	for i := p.Line - 2; i >= 0 && i < len(lines); i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "//") {
			break
		}
		doc = append([]string{uncomment(l)}, doc...)
	}
	return doc
}

// Header returns the comment at the top of the file: the module's description.
func Header(text []byte) []string {
	var doc []string
	for _, l := range strings.Split(string(text), "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "//") {
			break
		}
		doc = append(doc, uncomment(l))
	}
	return doc
}

func uncomment(l string) string {
	l = strings.TrimPrefix(l, "//")
	return strings.TrimPrefix(l, " ")
}

// StripComment removes a // comment from the end of the line (outside strings).
func StripComment(l string) string {
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

// Signature returns a function's header: from the declaration's line until
// before the body's '{' (joining the lines of wrapped parameters).
func Signature(text []byte, p token.Pos) string {
	lines := strings.Split(string(text), "\n")
	var parts []string
	for i := p.Line - 1; i >= 0 && i < len(lines) && i < p.Line+8; i++ {
		l := StripComment(strings.TrimSpace(lines[i]))
		if j := strings.Index(l, "{"); j >= 0 {
			parts = append(parts, strings.TrimSpace(l[:j]))
			break
		}
		parts = append(parts, l)
		if !strings.HasSuffix(l, ",") && !strings.HasSuffix(l, "(") {
			break // extern fn: no body
		}
	}
	return strings.Join(parts, " ")
}

// Block returns the text of a declaration with a body between braces (struct,
// enum, interface), from p's line to the closing brace, as it is in the
// file (with the fields' comments).
func Block(text []byte, p token.Pos) string {
	lines := strings.Split(string(text), "\n")
	var code []string
	depth := 0
	for i := p.Line - 1; i >= 0 && i < len(lines); i++ {
		l := strings.TrimRight(lines[i], " \t")
		code = append(code, l)
		depth += strings.Count(StripComment(l), "{") - strings.Count(StripComment(l), "}")
		if depth <= 0 {
			break
		}
	}
	return strings.Join(code, "\n")
}

// Line returns p's line without surrounding spaces and without a comment.
func Line(text []byte, p token.Pos) string {
	lines := strings.Split(string(text), "\n")
	if p.Line < 1 || p.Line > len(lines) {
		return ""
	}
	return StripComment(strings.TrimSpace(lines[p.Line-1]))
}
