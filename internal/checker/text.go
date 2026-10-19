package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
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
//
// str.format(&dst, "...", x) e str.format_len("...", x) são a mesma
// reescrita sobre um Writer que escreve em dst (por um destino registrado no
// runtime) ou que só conta os bytes. Nos formatos, {:x}, {:.2}, {:>8} e
// afins viram campos do Writer durante o write do valor (FormatSpec).

// PrintPlan é a reescrita de uma chamada de impressão.
type PrintPlan struct {
	Parts    []PrintPart
	Args     []ast.Expr // os valores, na ordem (avaliados antes de escrever)
	ArgTypes []Type
	Writer   *Struct
	PutStr   *Func
	Flush    *Func
	Fd       int      // 1: saída padrão; 2: erro padrão
	Mode     int      // PrintFile, PrintSink (str.format) ou PrintCount (str.format_len)
	Dst      ast.Expr // str.format: o destino (&[]byte)
	Recv     ast.Expr // w.printf: o Writer (variável, &io.Writer ou *io.Writer); lg.info: o Logger
	Level    int      // lg.debug, info, warn, error: o nível (log.DEBUG...)

	// métodos do Writer usados com especificação ({:...})
	SpecBegin, SpecEnd, Measure, PadBefore, PadAfter, PutBytes *Func
}

const (
	PrintFile   = iota // io.print, io.printf: num arquivo
	PrintSink          // str.format: no destino
	PrintCount         // str.format_len: só conta
	PrintWriter        // w.printf: num Writer que já existe, sem flush no fim
	PrintLog           // lg.info: numa linha do logger, se o nível estiver ativo
)

// FormatSpec é a especificação de um {} do formato: {:[[fill]align][#][0][width][.prec][x|X|b|o|s]}.
type FormatSpec struct {
	Fill  byte // ' ' se não for dado
	Align byte // '<', '>' ou '^'; 0: o padrão (números à direita, o resto à esquerda)
	Width int
	Zero  bool // {:08}: zeros depois do sinal (só números)
	Prec  int  // {:.2}: casas decimais; -1 se não for dado (só reais)
	Base  int  // 16, 2, 8; 0: decimal (só inteiros)
	Upper bool // {:X}
	Alt   bool // {:#x}: prefixo 0x, 0b, 0o
	Text  bool // {:s}: os bytes de um []byte como texto
}

// PrintPart é um texto fixo (Arg < 0) ou um valor escrito pelo seu write.
type PrintPart struct {
	Lit   string
	Arg   int
	Write *Func
	View  bool // [N]T escrito pelo impl de []T
	Ptr   bool // o valor é um empréstimo (&T): escreve o apontado, sem copiar
	Spec  *FormatSpec
}

// ioBuiltin diz se m.sel é uma das escritas formatadas embutidas no
// compilador: print, eprint, printf ou eprintf do std/io, ou format e
// format_len do std/str.
func ioBuiltin(m *module, sel string) string {
	switch {
	case !m.std:
	case m.path == "std/io":
		switch sel {
		case "print", "eprint", "printf", "eprintf":
			return sel
		}
	case m.path == "std/str":
		switch sel {
		case "format", "format_len":
			return sel
		}
	}
	return ""
}

// writerPrintf checa w.printf("...", a, ...): o formato do io.printf sobre
// um Writer que já existe, sem flush no fim (quem chama faz w.flush()).
func (c *checker) writerPrintf(e *ast.CallExpr, sel *ast.SelectorExpr, t Type) Type {
	if !isBorrow(t) && !isPointerType(t) && !isPlace(sel.X) {
		c.errorf(sel.X.Pos(), "w.printf escreve num Writer guardado numa variável ou campo (ou num &io.Writer): um Writer temporário perderia o que foi escrito")
		c.checkArgs(e)
		return Void
	}
	c.printRecv = sel.X
	defer func() { c.printRecv = nil }()
	return c.ioPrint(e, "wprintf")
}

// logLevels são os métodos de log.Logger com formato, e o nível de cada um.
var logLevels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// isLogger diz se s é o log.Logger da biblioteca padrão.
func isLogger(s *Struct) bool {
	return s.mod != nil && s.mod.std && s.mod.path == "std/log" && s.Decl != nil && s.Decl.Name == "Logger"
}

// loggerPrintf checa lg.info("...", a, ...) e os outros níveis: o formato
// do io.printf, escrito numa linha do logger.
func (c *checker) loggerPrintf(e *ast.CallExpr, sel *ast.SelectorExpr) Type {
	c.printRecv, c.printLevel = sel.X, logLevels[sel.Sel]
	defer func() { c.printRecv = nil }()
	return c.ioPrint(e, "log."+sel.Sel)
}

// ioPrint checa io.print(a, b, ...), io.printf("...", a, b, ...),
// str.format(&dst, "...", a, ...) e str.format_len("...", a, ...).
func (c *checker) ioPrint(e *ast.CallExpr, kind string) Type {
	io := c.modules["std/io"]
	disp := io.ifaces["Display"]
	writer := io.structs["Writer"]
	plan := &PrintPlan{Writer: writer, PutStr: writer.Methods["put_str"], Flush: writer.Methods["flush"], Fd: 1,
		SpecBegin: writer.Methods["spec_begin"], SpecEnd: writer.Methods["spec_end"], Measure: writer.Methods["measure"],
		PadBefore: writer.Methods["pad_before"], PadAfter: writer.Methods["pad_after"], PutBytes: writer.Methods["put_bytes"]}
	name, result := "io."+kind, Type(Void)
	switch kind {
	case "eprint", "eprintf":
		plan.Fd = 2
	case "format":
		plan.Mode, name, result = PrintSink, "str.format", Int
	case "format_len":
		plan.Mode, name, result = PrintCount, "str.format_len", Int
	case "wprintf":
		plan.Mode, plan.Recv, name = PrintWriter, c.printRecv, "Writer.printf"
	case "log.debug", "log.info", "log.warn", "log.error":
		plan.Mode, plan.Recv, plan.Level, name = PrintLog, c.printRecv, c.printLevel, "Logger."+kind[len("log."):]
	}
	args := e.Args
	if kind == "format" {
		if len(args) == 0 {
			c.errorf(e.Lparen, "str.format precisa do destino e do formato: str.format(&buf, \"x = {}\", x)")
			return result
		}
		c.borrowArg(args[0], c.borrowOf(c.sliceOf(Byte)), "o destino de str.format")
		plan.Dst, args = args[0], args[1:]
	}
	var format []string     // pedaços de texto entre os {}
	var specs []*FormatSpec // a especificação de cada {} (nil: sem)
	if kind != "print" && kind != "eprint" {
		if len(args) == 0 {
			c.errorf(e.Lparen, "%s precisa de um formato: %s(\"x = {}\\n\", x)", name, name)
			return result
		}
		lit, ok := args[0].(*ast.StringLit)
		if !ok {
			c.errorf(args[0].Pos(), "o formato de %s precisa ser uma string literal", name)
			for _, a := range args {
				c.peekType(a)
			}
			return result
		}
		c.value(lit)
		var ok2 bool
		format, specs, ok2 = c.parseFormat(lit, name)
		args = args[1:]
		if ok2 && len(format)-1 != len(args) {
			c.errorf(lit.P, "o formato tem %d {} e a chamada passa %d valor(es)", len(format)-1, len(args))
		}
	}
	for i, a := range args {
		var t Type
		if u, ok := a.(*ast.UnaryExpr); ok && u.Op == token.AMP {
			t = c.peekType(a) // &x e &s[a:b]: escritos sem cópia
		} else {
			t = c.valueDefault(a)
		}
		var spec *FormatSpec
		if i < len(specs) {
			spec = specs[i]
		}
		plan.Args = append(plan.Args, a)
		plan.ArgTypes = append(plan.ArgTypes, t)
		elem, isSl := sliceElem(t)
		switch {
		case t == Invalid:
			plan.Parts = append(plan.Parts, PrintPart{Arg: i})
			continue
		case (isPointerType(t) && !isBorrow(t)) || t == NilType:
			c.errorf(a.Pos(), "%s não escreve ponteiros (%s); escreva o conteúdo: *p ou p.campo", name, t)
			continue
		case isSl && (isPointerType(elem) || isSlicePtrElem(elem)):
			c.errorf(a.Pos(), "%s não escreve %s: os elementos são ponteiros", name, t)
			continue
		}
		part := PrintPart{Arg: i, Spec: spec}
		if b, ok := t.(*Borrow); ok {
			t, part.Ptr = b.Elem, true // escreve o apontado, sem copiar
		} else {
			c.noCopy(t, a.Pos())
		}
		if spec != nil {
			c.checkSpec(spec, t, a)
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
	return result
}

// checkSpec confere que a especificação serve para o tipo do valor: base só
// em inteiros, casas decimais só em reais, zeros só em números. Completa o
// alinhamento padrão: números à direita, o resto à esquerda.
func (c *checker) checkSpec(spec *FormatSpec, t Type, a ast.Expr) {
	isInt, isReal := isInteger(t), t == Float64 || t == Float32
	if spec.Text {
		elem, isSl := sliceElem(t)
		if arr, ok := t.(*Array); ok {
			elem, isSl = arr.Elem, true
		}
		if !isSl || elem != Byte {
			c.errorf(a.Pos(), "{:s} escreve os bytes de um []byte como texto, e o valor é %s", t)
		}
		if spec.Align == 0 {
			spec.Align = '<'
		}
		return
	}
	switch {
	case spec.Base != 0 && !isInt:
		c.errorf(a.Pos(), "{:x}, {:b} e {:o} só valem para inteiros, e o valor é %s", t)
	case spec.Alt && spec.Base == 0:
		c.errorf(a.Pos(), "# só vale com x, X, b ou o: {:#x}")
	case spec.Prec >= 0 && !isReal:
		c.errorf(a.Pos(), "{:.N} (casas decimais) só vale para float64 e float32, e o valor é %s", t)
	case spec.Zero && !isInt && !isReal:
		c.errorf(a.Pos(), "{:0N} (zeros à esquerda) só vale para números, e o valor é %s", t)
	case spec.Zero && spec.Align != 0:
		c.errorf(a.Pos(), "{:0N} não se combina com alinhamento: use {:0N} ou {:>N}")
	}
	if spec.Align == 0 {
		spec.Align = '<'
		if isInt || isReal {
			spec.Align = '>'
		}
	}
}

// parseFormat divide o formato nos textos entre os {}: "a {} b {:x}" dá
// ["a ", " b ", ""], com a especificação de cada {} (nil quando não tem).
// {{ e }} escrevem chaves.
func (c *checker) parseFormat(lit *ast.StringLit, name string) ([]string, []*FormatSpec, bool) {
	var out []string
	var specs []*FormatSpec
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
		case s[i] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				c.errorf(lit.P, "'{' sozinho no formato de %s: use {} para um valor ou {{ para escrever {", name)
				return nil, nil, false
			}
			inner := s[i+1 : i+end]
			var spec *FormatSpec
			if inner != "" {
				body, ok := strings.CutPrefix(inner, ":")
				if !ok {
					c.errorf(lit.P, "{%s} no formato de %s: use {} para um valor, {:...} para um valor com especificação ({:x}, {:.2}, {:>8}) ou {{ para escrever {", inner, name)
					return nil, nil, false
				}
				var msg string
				if spec, msg = parseSpec(body); msg != "" {
					c.errorf(lit.P, "{%s} no formato de %s: %s", inner, name, msg)
					return nil, nil, false
				}
			}
			out = append(out, cur.String())
			specs = append(specs, spec)
			cur.Reset()
			i += end
		case s[i] == '}':
			c.errorf(lit.P, "'}' sozinho no formato de %s: use }} para escrever }", name)
			return nil, nil, false
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(out, cur.String()), specs, true
}

// parseSpec lê o que vem depois de {: — [[fill]align][#][0][width][.prec][x|X|b|o|s] —
// ou explica o que está errado.
func parseSpec(s string) (*FormatSpec, string) {
	spec := &FormatSpec{Fill: ' ', Prec: -1}
	isAlign := func(b byte) bool { return b == '<' || b == '>' || b == '^' }
	switch {
	case len(s) >= 2 && isAlign(s[1]):
		if s[0] >= 0x80 {
			return nil, "o caractere de preenchimento precisa ser ASCII"
		}
		spec.Fill, spec.Align, s = s[0], s[1], s[2:]
	case len(s) >= 1 && isAlign(s[0]):
		spec.Align, s = s[0], s[1:]
	}
	if strings.HasPrefix(s, "#") {
		spec.Alt, s = true, s[1:]
	}
	if strings.HasPrefix(s, "0") {
		spec.Zero, s = true, s[1:]
	}
	digits := func() (int, bool) {
		n, k := 0, 0
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			n = n*10 + int(s[k]-'0')
			k++
			if n > 1000 {
				return 0, false
			}
		}
		s = s[k:]
		return n, k > 0
	}
	w, hasWidth := digits()
	if w > 255 || (!hasWidth && len(s) > 0 && s[0] >= '0' && s[0] <= '9') {
		return nil, "a largura vai até 255"
	}
	spec.Width = w
	if spec.Zero && !hasWidth {
		return nil, "depois do 0 vem a largura: {:08}"
	}
	if strings.HasPrefix(s, ".") {
		s = s[1:]
		p, ok := digits()
		if !ok || p > 30 {
			return nil, "depois do . vem o número de casas decimais, até 30: {:.2}"
		}
		spec.Prec = p
	}
	switch s {
	case "":
	case "x":
		spec.Base = 16
	case "X":
		spec.Base, spec.Upper = 16, true
	case "b":
		spec.Base = 2
	case "o":
		spec.Base = 8
	case "s":
		spec.Text = true
	default:
		return nil, "especificação desconhecida; use [[preenchimento]alinhamento][#][0][largura][.casas][x|X|b|o|s], como {:>8}, {:08.2}, {:#x} ou {:s}"
	}
	if spec.Text && (spec.Alt || spec.Zero || spec.Prec >= 0) {
		return nil, "{:s} só aceita largura e alinhamento: {:s}, {:<10s}"
	}
	return spec, ""
}
