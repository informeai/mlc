// Package doc extrai a documentação de um módulo .mlc: os comentários // logo
// acima de cada declaração e o do começo do arquivo, como no Go. É usado pelo
// mlc doc (no terminal) e pelo LSP (no editor).
package doc

import (
	"strings"

	"mlc/internal/token"
)

// Comment devolve as linhas do comentário // logo acima da linha de p, sem
// as barras. Uma linha em branco ou de código encerra o comentário.
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

// Header devolve o comentário do começo do arquivo: a descrição do módulo.
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

// StripComment tira um comentário // do fim da linha (fora de strings).
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

// Signature devolve o cabeçalho de uma função: da linha da declaração até
// antes do '{' do corpo (juntando as linhas de parâmetros quebrados).
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
			break // extern fn: sem corpo
		}
	}
	return strings.Join(parts, " ")
}

// Block devolve o texto de uma declaração com corpo entre chaves (struct,
// enum, interface), da linha de p até a chave que fecha, como está no
// arquivo (com os comentários dos campos).
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

// Line devolve a linha de p sem espaços nas pontas e sem comentário.
func Line(text []byte, p token.Pos) string {
	lines := strings.Split(string(text), "\n")
	if p.Line < 1 || p.Line > len(lines) {
		return ""
	}
	return StripComment(strings.TrimSpace(lines[p.Line-1]))
}
