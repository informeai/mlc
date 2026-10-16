package checker

import (
	"strings"

	"mlc/internal/ast"
)

// Texto e impressão
//
// Texto é montado num buffer []byte que quem programa aloca com alloc e
// cresce com realloc. As funções de texto (std/str) e de entrada e saída
// (std/io) nunca alocam: escrevem no buffer emprestado, devolvem quantos bytes
// usaram e dão panic se não couber.
//
// io.print, io.eprint, io.printf e io.eprintf aceitam qualquer quantidade de
// argumentos, de qualquer tipo que implemente io.Display. O compilador só os
// reescreve: cada valor vira uma chamada direta ao seu método write, sobre
// um io.Writer com buffer fixo na pilha. A formatação em si é MLC (std/io).
//
//	io.printf("x = {}\n", x)  →  w := io.stdout(); w.put_str("x = ");
//	                              x.write(&w); w.put_str("\n"); w.flush()

// PrintPlan é a reescrita de uma chamada de impressão.
type PrintPlan struct {
	Parts    []PrintPart
	Args     []ast.Expr // os valores, na ordem (avaliados antes de escrever)
	ArgTypes []Type
	Writer   *Struct
	PutStr   *Func
	Flush    *Func
	Fd       int // 1: saída padrão; 2: erro padrão
}

// PrintPart é um texto fixo (Arg < 0) ou um valor escrito pelo seu write.
type PrintPart struct {
	Lit   string
	Arg   int
	Write *Func
	View  bool // [N]T escrito pelo impl de []T
	Ptr   bool // o valor é um empréstimo (&T): escreve o apontado, sem copiar
}

// ioBuiltin diz se m.sel é uma das impressões do std/io, embutidas no
// compilador: print, eprint, printf ou eprintf.
func ioBuiltin(m *module, sel string) string {
	if !m.std || m.path != "std/io" {
		return ""
	}
	switch sel {
	case "print", "eprint", "printf", "eprintf":
		return sel
	}
	return ""
}

// ioPrint checa io.print(a, b, ...) e io.printf("...", a, b, ...).
func (c *checker) ioPrint(e *ast.CallExpr, kind string) Type {
	io := c.modules["std/io"]
	disp := io.ifaces["Display"]
	writer := io.structs["Writer"]
	plan := &PrintPlan{Writer: writer, PutStr: writer.Methods["put_str"], Flush: writer.Methods["flush"], Fd: 1}
	if strings.HasPrefix(kind, "e") {
		plan.Fd = 2
	}
	args := e.Args
	var format []string // pedaços de texto entre os {}
	if strings.HasSuffix(kind, "f") {
		if len(args) == 0 {
			c.errorf(e.Lparen, "io.%s precisa de um formato: io.%s(\"x = {}\\n\", x)", kind, kind)
			return Void
		}
		lit, ok := args[0].(*ast.StringLit)
		if !ok {
			c.errorf(args[0].Pos(), "o formato de io.%s precisa ser uma string literal", kind)
			c.checkArgs(e)
			return Void
		}
		c.value(lit)
		var ok2 bool
		format, ok2 = c.parseFormat(lit, kind)
		args = args[1:]
		if ok2 && len(format)-1 != len(args) {
			c.errorf(lit.P, "o formato tem %d {} e a chamada passa %d valor(es)", len(format)-1, len(args))
		}
	}
	for i, a := range args {
		t := c.valueDefault(a)
		plan.Args = append(plan.Args, a)
		plan.ArgTypes = append(plan.ArgTypes, t)
		elem, isSl := sliceElem(t)
		switch {
		case t == Invalid:
			plan.Parts = append(plan.Parts, PrintPart{Arg: i})
			continue
		case (isPointerType(t) && !isBorrow(t)) || t == NilType:
			c.errorf(a.Pos(), "io.%s não imprime ponteiros (%s); imprima o conteúdo: *p ou p.campo", kind, t)
			continue
		case isSl && (isPointerType(elem) || isSlicePtrElem(elem)):
			c.errorf(a.Pos(), "io.%s não imprime %s: os elementos são ponteiros", kind, t)
			continue
		}
		part := PrintPart{Arg: i}
		if b, ok := t.(*Borrow); ok {
			t, part.Ptr = b.Elem, true // escreve o apontado, sem copiar
		} else {
			c.noCopy(t, a.Pos())
		}
		var write *Func
		if tv, ok := t.(*TypeVar); ok {
			write = c.implMethod(tv, "write") // checagem abstrata: T io.Display
		} else if im := c.findImpl(disp, t); im != nil {
			write = im.Methods["write"]
		} else if arr, ok := t.(*Array); ok {
			if im := c.findImpl(disp, c.sliceOf(arr.Elem)); im != nil {
				write = im.Methods["write"]
				part.View = true
			}
		}
		if write == nil {
			// num slice ou array, o que falta é o Display do elemento
			inner := t
			for {
				if s, ok := inner.(*Slice); ok {
					inner = s.Elem
				} else if arr, ok := inner.(*Array); ok {
					inner = arr.Elem
				} else {
					break
				}
			}
			switch inner.(type) {
			case *Struct, *Enum:
				if inner != t {
					c.errorf(a.Pos(), "%s não implementa io.Display, porque %s não implementa: declare impl auto io.Display for %s (ou escreva o método write)", t, inner, inner)
				} else {
					c.errorf(a.Pos(), "%s não implementa io.Display: declare impl auto io.Display for %s (ou escreva o método write)", t, t)
				}
			default:
				c.errorf(a.Pos(), "%s não implementa io.Display", t)
			}
			continue
		}
		part.Write = write
		plan.Parts = append(plan.Parts, part)
	}
	// junta os valores com o texto: o formato, ou espaços e \n no print
	var parts []PrintPart
	lit := func(s string) {
		if s == "" {
			return
		}
		if n := len(parts); n > 0 && parts[n-1].Arg < 0 {
			parts[n-1].Lit += s
			return
		}
		parts = append(parts, PrintPart{Lit: s, Arg: -1})
	}
	for i, p := range plan.Parts {
		switch {
		case format != nil && i < len(format):
			lit(format[i])
		case format == nil && i > 0:
			lit(" ")
		}
		if p.Write != nil {
			parts = append(parts, p)
		}
	}
	if format != nil {
		if n := len(format); n > 0 && len(plan.Parts) < n {
			for _, s := range format[len(plan.Parts):] {
				lit(s)
			}
		}
	} else {
		lit("\n")
	}
	plan.Parts = parts
	c.info.Prints[e] = plan
	c.info.Calls[e] = Callee{Builtin: IoPrint}
	return Void
}

// parseFormat divide o formato nos textos entre os {}: "a {} b {}" dá
// ["a ", " b ", ""]. {{ e }} escrevem chaves.
func (c *checker) parseFormat(lit *ast.StringLit, kind string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	s := lit.Value
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '{' && i+1 < len(s) && s[i+1] == '{':
			cur.WriteByte('{')
			i++
		case s[i] == '}' && i+1 < len(s) && s[i+1] == '}':
			cur.WriteByte('}')
			i++
		case s[i] == '{' && i+1 < len(s) && s[i+1] == '}':
			out = append(out, cur.String())
			cur.Reset()
			i++
		case s[i] == '{':
			c.errorf(lit.P, "'{' sozinho no formato de io.%s: use {} para um valor ou {{ para escrever {", kind)
			return nil, false
		case s[i] == '}':
			c.errorf(lit.P, "'}' sozinho no formato de io.%s: use }} para escrever }", kind)
			return nil, false
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(out, cur.String()), true
}
