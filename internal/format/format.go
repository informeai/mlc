// Package format implementa o mlc fmt: reescreve um arquivo .mlc na forma
// canônica, com indentação de 4 espaços.
//
// O formatador trabalha sobre os tokens (com os comentários), e não
// reimprimindo a AST: assim os comentários, os parênteses e as quebras de
// linha que quem programa escolheu são preservados. A AST serve só para tirar
// as ambiguidades que os tokens sozinhos não resolvem: se um '-' ou '*' é
// binário (a - b) ou unário (-x, *T), se um '{' abre um literal (Ponto{...})
// ou um bloco, e se um '[' é índice (s[i]) ou começo de tipo ([]int).
//
// Regras:
//   - indentação de 4 espaços por nível de bloco ou de parêntese/colchete
//     aberto no fim da linha; uma linha que continua uma expressão (quebrada
//     depois de um operador) ganha um nível a mais;
//   - um espaço em volta dos operadores binários, de =, := e =>, e depois de
//     vírgulas e palavras-chave; nenhum depois de operador unário;
//   - as quebras de linha do original são mantidas; linhas em branco
//     seguidas viram uma só, e somem logo depois de '{' e antes de '}';
//   - ';' explícito no fim da linha é removido;
//   - comentários no fim de linhas consecutivas ficam alinhados.
package format

import (
	"reflect"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/lexer"
	"mlc/internal/parser"
	"mlc/internal/token"
)

const indentUnit = "    "

// Source devolve src formatado. Um arquivo com erro de sintaxe não é
// formatado: os erros são devolvidos.
func Source(file string, src []byte) ([]byte, []token.Error) {
	f, errs := parser.ParseFile(file, src)
	if len(errs) > 0 {
		return nil, errs
	}
	lx := lexer.NewWithComments(file, src)
	var toks []token.Token
	for {
		t := lx.Next()
		if t.Kind == token.EOF {
			break
		}
		toks = append(toks, t)
	}
	if errs := lx.Errors(); len(errs) > 0 {
		return nil, errs
	}
	p := &printer{src: src, toks: toks, marks: collectMarks(f)}
	p.run()
	return p.output(), nil
}

// marks guarda o que a AST diz sobre posições de tokens.
type marks struct {
	binary    map[token.Pos]bool // operadores binários
	composite map[token.Pos]bool // '{' de literais compostos
	index     map[token.Pos]bool // '[' de índice, sub-slice ou argumentos de tipo
}

// collectMarks percorre a AST (por reflexão, para não depender de cada tipo
// de nó) anotando as posições de que o formatador precisa.
func collectMarks(f *ast.File) marks {
	m := marks{binary: map[token.Pos]bool{}, composite: map[token.Pos]bool{}, index: map[token.Pos]bool{}}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return
			}
			switch n := v.Interface().(type) {
			case *ast.BinaryExpr:
				m.binary[n.OpPos] = true
			case *ast.CompositeLit:
				m.composite[n.Lbrace] = true
			case *ast.ArrayLit:
				m.composite[n.Lbrace] = true
			case *ast.IndexExpr:
				m.index[n.Lbrack] = true
			case *ast.SliceExpr:
				m.index[n.Lbrack] = true
			case *ast.IndexListExpr:
				m.index[n.Lbrack] = true
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
	return m
}

// opener é um '(', '[' ou '{' ainda aberto.
type opener struct {
	kind      token.Kind
	indented  bool // é o último token da linha: o conteúdo vem indentado
	composite bool // '{' de literal composto
	attached  bool // '[' colado no que vem antes: índice ou argumentos de tipo
}

type line struct {
	indent  int
	code    strings.Builder
	comment string
	blank   bool
}

type printer struct {
	src   []byte
	toks  []token.Token
	marks marks

	lines    []*line
	stack    []opener
	prev     *token.Token // último token (não comentário) escrito na linha atual
	prev2    *token.Token // o anterior a prev, na mesma linha
	last     *token.Token // último token (não comentário) escrito
	lastLine int          // linha, no original, do último token ou comentário escrito
	closed   opener       // o par fechado pelo último ']', ')' ou '}'
	attached bool         // o '[' sendo escrito está colado (índice ou tipos)
}

func (p *printer) run() {
	for i := range p.toks {
		t := &p.toks[i]
		switch {
		case t.Kind == token.SEMICOLON && t.Lit == "\n":
			continue // ';' automático: a quebra de linha já está no original
		case t.Kind == token.SEMICOLON:
			if next := p.nextSignificant(i); next == nil || next.Pos.Line > t.Pos.Line || next.Kind == token.RBRACE {
				continue // ';' explícito sem nada depois na linha
			}
		case t.Kind == token.COMMENT:
			p.comment(t)
			continue
		}
		p.token(i, t)
	}
}

// nextSignificant devolve o próximo token depois de i que não é comentário
// nem ';' automático.
func (p *printer) nextSignificant(i int) *token.Token {
	for j := i + 1; j < len(p.toks); j++ {
		t := &p.toks[j]
		if t.Kind == token.COMMENT || (t.Kind == token.SEMICOLON && t.Lit == "\n") {
			continue
		}
		return t
	}
	return nil
}

func (p *printer) cur() *line { return p.lines[len(p.lines)-1] }

// newLine começa uma linha para o token t (que vem numa linha nova do original).
func (p *printer) newLine(t *token.Token, closer bool) {
	if p.lastLine > 0 && t.Pos.Line-p.lastLine >= 2 && !closer && !p.afterOpener() {
		p.lines = append(p.lines, &line{blank: true})
	}
	indent := 0
	for _, o := range p.stack {
		if o.indented {
			indent++
		}
	}
	if closer && len(p.stack) > 0 && p.stack[len(p.stack)-1].indented {
		indent--
	}
	// continuação: a linha anterior parou no meio de uma expressão, ou dentro
	// de um parêntese aberto no meio da linha: f(a,\n b)
	if !closer && p.last != nil {
		top := p.top()
		inOpen := len(p.stack) > 0 && !top.indented && !(top.kind == token.LBRACE && !top.composite)
		if (t.Kind != token.COMMENT && p.continues(p.last)) || inOpen {
			indent++
		}
	}
	p.lines = append(p.lines, &line{indent: indent})
	p.prev, p.prev2 = nil, nil
}

// afterOpener diz se a última linha escrita termina abrindo um bloco ou
// parêntese indentado (então não cabe linha em branco logo depois).
func (p *printer) afterOpener() bool {
	return len(p.stack) > 0 && p.stack[len(p.stack)-1].indented && p.last != nil &&
		(p.last.Kind == token.LBRACE || p.last.Kind == token.LPAREN || p.last.Kind == token.LBRACK)
}

// continues diz se uma quebra de linha depois de t continua a expressão (a
// próxima linha ganha um nível a mais de indentação).
func (p *printer) continues(t *token.Token) bool {
	switch t.Kind {
	case token.LAND, token.LOR, token.ASSIGN, token.DEFINE, token.ARROW, token.PERIOD,
		token.ADD_ASSIGN, token.SUB_ASSIGN, token.MUL_ASSIGN, token.QUO_ASSIGN, token.REM_ASSIGN:
		return true
	}
	return p.marks.binary[t.Pos]
}

func (p *printer) comment(t *token.Token) {
	if len(p.lines) > 0 && t.Pos.Line == p.lastLine && !p.cur().blank && p.cur().code.Len() > 0 {
		p.cur().comment = t.Lit
	} else {
		p.newLine(t, false)
		p.cur().code.WriteString(t.Lit)
		p.cur().comment = "" // a linha é só o comentário
	}
	p.lastLine = t.Pos.Line
}

func (p *printer) token(i int, t *token.Token) {
	closer := t.Kind == token.RPAREN || t.Kind == token.RBRACK || t.Kind == token.RBRACE
	p.attached = t.Kind == token.LBRACK && p.attachedBracket(i, t)
	if len(p.lines) == 0 || t.Pos.Line > p.lastLine {
		p.newLine(t, closer)
	} else if p.prev != nil && p.space(p.prev, t) {
		p.cur().code.WriteByte(' ')
	}
	if closer && len(p.stack) > 0 {
		p.closed = p.stack[len(p.stack)-1]
		p.stack = p.stack[:len(p.stack)-1]
	}
	if t.Kind == token.SEMICOLON {
		p.cur().code.WriteByte(';')
	} else {
		p.cur().code.Write(p.src[t.Off:t.End])
	}
	switch t.Kind {
	case token.LPAREN, token.LBRACK, token.LBRACE:
		o := opener{kind: t.Kind}
		if next := p.nextSignificant(i); next != nil && next.Pos.Line > t.Pos.Line {
			o.indented = true
		}
		o.composite = t.Kind == token.LBRACE && p.marks.composite[t.Pos]
		o.attached = p.attached
		p.stack = append(p.stack, o)
	}
	p.prev2, p.prev, p.last = p.prev, t, t
	p.lastLine = t.Pos.Line
}

// attachedBracket diz se o '[' em i é índice, sub-slice ou lista de tipos,
// colado no nome (s[i], Vec[T], fn f[T any]), e não o começo de um tipo
// ([]int, a [3]byte). Depois de um nome, é começo de tipo quando o ']'
// correspondente é seguido de outro tipo: a [3]int, mas Vec[T]{...},
// f[T any](...) e Result[int].Err.
func (p *printer) attachedBracket(i int, t *token.Token) bool {
	if p.marks.index[t.Pos] || p.prev != nil && p.prev.Kind == token.IMPL {
		return true // s[i], ou impl[T Display]
	}
	if p.prev == nil || p.prev.Kind != token.IDENT {
		return false
	}
	if next := p.nextSignificant(i); next == nil || next.Kind == token.RBRACK {
		return false // []T
	}
	depth := 0
	for j := i; j < len(p.toks); j++ {
		switch p.toks[j].Kind {
		case token.LBRACK:
			depth++
		case token.RBRACK:
			depth--
			if depth == 0 {
				after := p.nextSignificant(j)
				if after == nil || after.Pos.Line > p.toks[j].Pos.Line {
					return true
				}
				switch after.Kind {
				case token.IDENT, token.LBRACK, token.MUL, token.QUESTION, token.AMP:
					return false
				}
				return true
			}
		}
	}
	return true
}

// unary diz se t, um operador, está sendo usado como unário (-x, *p, &x, !b)
// ou faz parte de um tipo (*T, ?*T, &T).
func (p *printer) unary(t *token.Token) bool {
	switch t.Kind {
	case token.NOT, token.QUESTION:
		return true
	case token.SUB, token.MUL, token.AMP, token.XOR: // &x ou a & b, ^x ou a ^ b (+ unário não existe)
		return !p.marks.binary[t.Pos]
	}
	return false
}

func (p *printer) top() opener {
	if len(p.stack) == 0 {
		return opener{}
	}
	return p.stack[len(p.stack)-1]
}

// space diz se vai um espaço entre prev e t, na mesma linha.
func (p *printer) space(prev, t *token.Token) bool {
	switch t.Kind {
	case token.COMMA, token.RPAREN, token.RBRACK, token.PERIOD, token.INC, token.DEC, token.SEMICOLON, token.COLON:
		return false
	}
	switch prev.Kind {
	case token.LPAREN, token.LBRACK, token.PERIOD:
		return false
	}
	if p.unary(prev) {
		return false
	}
	switch {
	case prev.Kind == token.COLON:
		return p.top().kind != token.LBRACK // s[a:b], mas Ponto{x: 1}
	case t.Kind == token.LBRACE:
		return !p.marks.composite[t.Pos]
	case prev.Kind == token.LBRACE:
		return !p.top().composite && t.Kind != token.RBRACE
	case t.Kind == token.RBRACE:
		return !p.top().composite && prev.Kind != token.LBRACE // o '{' ainda está na pilha
	case t.Kind == token.LPAREN && prev.Kind == token.FN:
		// fn (p &Ponto) area(): receptor, separado; fn(int) bool: tipo, colado
		return p.prev2 == nil || p.prev2.Kind == token.PUB || p.prev2.Kind == token.EXTERN
	case t.Kind == token.LPAREN:
		if prev.Kind == token.IDENT {
			return false // chamada ou conversão: f(x), int(x)
		}
		return !(prev.Kind == token.RBRACK && p.closed.attached) // f[int](x)
	case t.Kind == token.LBRACK:
		return prev.Kind != token.RBRACK && !p.attached
	case prev.Kind == token.RBRACK:
		return p.closed.attached // []int e [3]byte, colados; Vec[T] x, separado
	}
	return true
}

func (p *printer) output() []byte {
	// alinha os comentários no fim de linhas consecutivas
	for i := 0; i < len(p.lines); {
		j := i
		width := 0
		for j < len(p.lines) && p.lines[j].comment != "" && p.lines[j].indent == p.lines[i].indent {
			width = max(width, p.lines[j].code.Len())
			j++
		}
		for k := i; k < j; k++ {
			l := p.lines[k]
			l.code.WriteString(strings.Repeat(" ", width-l.code.Len()+1))
			l.code.WriteString(l.comment)
		}
		if j == i {
			j++
		}
		i = j
	}
	var sb strings.Builder
	for i, l := range p.lines {
		if l.blank {
			if i+1 < len(p.lines) {
				sb.WriteByte('\n')
			}
			continue
		}
		sb.WriteString(strings.Repeat(indentUnit, max(l.indent, 0)))
		sb.WriteString(l.code.String())
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}
