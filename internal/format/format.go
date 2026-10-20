// Package format implements mlc fmt: it rewrites a .mlc file in the
// canonical form, with 4-space indentation.
//
// The formatter works over the tokens (with the comments), and not by
// reprinting the AST: that way the comments, the parentheses and the line
// breaks the programmer chose are preserved. The AST only serves to remove
// the ambiguities the tokens alone don't resolve: whether a '-' or '*' is
// binary (a - b) or unary (-x, *T), whether a '{' opens a literal (Point{...})
// or a block, and whether a '[' is an index (s[i]) or the start of a type ([]int).
//
// Rules:
//   - 4-space indentation per level of block or of parenthesis/bracket
//     open at the end of the line; a line that continues an expression (broken
//     after an operator) gets one more level;
//   - one space around binary operators, around =, := and =>, and after
//     commas and keywords; none after a unary operator;
//   - the original's line breaks are kept; consecutive blank lines
//     become one, and vanish right after '{' and before '}';
//   - an explicit ';' at the end of the line is removed;
//   - comments at the end of consecutive lines are aligned.
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

// Source returns src formatted. A file with a syntax error isn't
// formatted: the errors are returned.
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

// marks keeps what the AST says about token positions.
type marks struct {
	binary    map[token.Pos]bool // binary operators
	composite map[token.Pos]bool // '{' of composite literals
	index     map[token.Pos]bool // '[' of an index, sub-slice or type arguments
}

// collectMarks walks the AST (by reflection, so as not to depend on each node
// type) noting the positions the formatter needs.
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

// opener is a '(', '[' or '{' still open.
type opener struct {
	kind      token.Kind
	indented  bool // it's the line's last token: the content comes indented
	composite bool // '{' of a composite literal
	attached  bool // '[' attached to what comes before: an index or type arguments
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
	prev     *token.Token // last token (not a comment) written on the current line
	prev2    *token.Token // the one before prev, on the same line
	last     *token.Token // last token (not a comment) written
	lastLine int          // line, in the original, of the last token or comment written
	closed   opener       // the pair closed by the last ']', ')' or '}'
	attached bool         // the '[' being written is attached (an index or types)
}

func (p *printer) run() {
	for i := range p.toks {
		t := &p.toks[i]
		switch {
		case t.Kind == token.SEMICOLON && t.Lit == "\n":
			continue // automatic ';': the line break is already in the original
		case t.Kind == token.SEMICOLON:
			if next := p.afterSemicolons(i); next == nil || next.Pos.Line > t.Pos.Line || next.Kind == token.RBRACE {
				continue // explicit ';' with nothing after it on the line (also ;; before })
			}
		case t.Kind == token.COMMENT:
			p.comment(t)
			continue
		}
		p.token(i, t)
	}
}

// nextSignificant returns the next token after i that isn't a comment
// nor an automatic ';'.
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

// afterSemicolons returns the next token after i that isn't a comment
// nor a ';' (automatic or explicit): what comes after a run of ;;.
func (p *printer) afterSemicolons(i int) *token.Token {
	for j := i + 1; j < len(p.toks); j++ {
		t := &p.toks[j]
		if t.Kind == token.COMMENT || t.Kind == token.SEMICOLON {
			continue
		}
		return t
	}
	return nil
}

func (p *printer) cur() *line { return p.lines[len(p.lines)-1] }

// newLine starts a line for the token t (which comes on a new line in the original).
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
	// continuation: the previous line stopped in the middle of an expression, or inside
	// a parenthesis opened in the middle of the line: f(a,\n b)
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

// afterOpener says whether the last line written ends opening an indented block or
// parenthesis (so a blank line doesn't fit right after).
func (p *printer) afterOpener() bool {
	return len(p.stack) > 0 && p.stack[len(p.stack)-1].indented && p.last != nil &&
		(p.last.Kind == token.LBRACE || p.last.Kind == token.LPAREN || p.last.Kind == token.LBRACK)
}

// continues says whether a line break after t continues the expression (the
// next line gets one more level of indentation).
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
		p.cur().comment = "" // the line is only the comment
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
		if next := p.afterSemicolons(i); next != nil && next.Pos.Line > t.Pos.Line {
			o.indented = true
		}
		o.composite = t.Kind == token.LBRACE && p.marks.composite[t.Pos]
		o.attached = p.attached
		p.stack = append(p.stack, o)
	}
	p.prev2, p.prev, p.last = p.prev, t, t
	p.lastLine = t.Pos.Line
}

// attachedBracket says whether the '[' at i is an index, sub-slice or type list,
// attached to the name (s[i], Vec[T], fn f[T any]), and not the start of a type
// ([]int, a [3]byte). After a name, it's the start of a type when the matching ']'
// is followed by another type: a [3]int, but Vec[T]{...},
// f[T any](...) and Result[int].Err.
func (p *printer) attachedBracket(i int, t *token.Token) bool {
	if p.marks.index[t.Pos] || p.prev != nil && p.prev.Kind == token.IMPL {
		return true // s[i], or impl[T Display]
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

// unary says whether t, an operator, is used as unary (-x, *p, &x, !b)
// or is part of a type (*T, ?*T, &T).
func (p *printer) unary(t *token.Token) bool {
	switch t.Kind {
	case token.NOT, token.QUESTION:
		return true
	case token.SUB, token.MUL, token.AMP, token.XOR: // &x or a & b, ^x or a ^ b (unary + doesn't exist)
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

// space says whether a space goes between prev and t, on the same line.
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
		// & &x and - -x need the space: attached they become && and --
		return prev.Kind == t.Kind && (t.Kind == token.AMP || t.Kind == token.SUB)
	}
	switch {
	case prev.Kind == token.COLON:
		return p.top().kind != token.LBRACK // s[a:b], but Point{x: 1}
	case t.Kind == token.LBRACE:
		return !p.marks.composite[t.Pos]
	case prev.Kind == token.LBRACE:
		return !p.top().composite && t.Kind != token.RBRACE
	case t.Kind == token.RBRACE:
		return !p.top().composite && prev.Kind != token.LBRACE // the '{' is still on the stack
	case t.Kind == token.LPAREN && prev.Kind == token.FN:
		// fn (p &Point) area(): a receiver, separate; fn(int) bool: a type, attached
		return p.prev2 == nil || p.prev2.Kind == token.PUB || p.prev2.Kind == token.EXTERN
	case t.Kind == token.LPAREN:
		if prev.Kind == token.IDENT {
			return false // a call or conversion: f(x), int(x)
		}
		return !(prev.Kind == token.RBRACK && p.closed.attached) // f[int](x)
	case t.Kind == token.LBRACK:
		return prev.Kind != token.RBRACK && !p.attached
	case prev.Kind == token.RBRACK:
		return p.closed.attached // []int and [3]byte, attached; Vec[T] x, separate
	}
	return true
}

func (p *printer) output() []byte {
	// aligns the comments at the end of consecutive lines
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
