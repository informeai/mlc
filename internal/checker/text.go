package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Text and printing
//
// Text is assembled in a []byte buffer the programmer allocates with alloc and
// grows with realloc. The text functions (std/str) and the input and output ones
// (std/io) never allocate: they write into the borrowed buffer, return how many bytes
// they used and panic if it doesn't fit.
//
// io.print, io.eprint, io.printf and io.eprintf accept any number of
// arguments, of any type that implements io.Display. The compiler only
// rewrites them: each value becomes a direct call to its write method, over
// an io.Writer with a fixed buffer on the stack. The formatting itself is MLC (std/io).
//
//	io.printf("x = {}\n", x)  →  w := io.stdout(); w.put_str("x = ");
//	                              x.write(&w); w.put_str("\n"); w.flush()
//
// str.format(&dst, "...", x) and str.format_len("...", x) are the same
// rewrite over a Writer that writes into dst (through a destination registered in the
// runtime) or that only counts the bytes. In the formats, {:x}, {:.2}, {:>8} and
// the like become Writer fields during the value's write (FormatSpec).

// PrintPlan is the rewrite of a printing call.
type PrintPlan struct {
	Parts    []PrintPart
	Args     []ast.Expr // the values, in order (evaluated before writing)
	ArgTypes []Type
	Writer   *Struct
	PutStr   *Func
	Flush    *Func
	Fd       int      // 1: standard output; 2: standard error
	Mode     int      // PrintFile, PrintSink (str.format) or PrintCount (str.format_len)
	Dst      ast.Expr // str.format: the destination (&[]byte)
	Recv     ast.Expr // w.printf: the Writer (a variable, &io.Writer or *io.Writer); lg.info: the Logger
	Level    int      // lg.debug, info, warn, error: the level (log.DEBUG...)

	// Writer methods used with a spec ({:...})
	SpecBegin, SpecEnd, Measure, PadBefore, PadAfter, PutBytes *Func
}

const (
	PrintFile   = iota // io.print, io.printf: into a file
	PrintSink          // str.format: into the destination
	PrintCount         // str.format_len: only counts
	PrintWriter        // w.printf: into a Writer that already exists, without a flush at the end
	PrintLog           // lg.info: into a logger line, if the level is enabled
)

// FormatSpec is the spec of a {} of the format: {:[[fill]align][#][0][width][.prec][x|X|b|o|s]}.
type FormatSpec struct {
	Fill  byte // ' ' if not given
	Align byte // '<', '>' or '^'; 0: the default (numbers on the right, the rest on the left)
	Width int
	Zero  bool // {:08}: zeros after the sign (numbers only)
	Prec  int  // {:.2}: decimal places; -1 if not given (reals only)
	Base  int  // 16, 2, 8; 0: decimal (integers only)
	Upper bool // {:X}
	Alt   bool // {:#x}: prefix 0x, 0b, 0o
	Text  bool // {:s}: the bytes of a []byte as text
}

// PrintPart is a fixed text (Arg < 0) or a value written by its write.
type PrintPart struct {
	Lit   string
	Arg   int
	Write *Func
	View  bool // [N]T written by the impl of []T
	Ptr   bool // the value is a borrow (&T): writes the pointee, without copying
	Spec  *FormatSpec
}

// ioBuiltin says whether m.sel is one of the formatted writes built into the
// compiler: print, eprint, printf or eprintf from std/io, or format and
// format_len from std/str.
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

// writerPrintf checks w.printf("...", a, ...): io.printf's format over
// a Writer that already exists, without a flush at the end (the caller does w.flush()).
func (c *checker) writerPrintf(e *ast.CallExpr, sel *ast.SelectorExpr, t Type) Type {
	if !isBorrow(t) && !isPointerType(t) && !isPlace(sel.X) {
		c.errorf(sel.X.Pos(), "w.printf writes into a Writer stored in a variable or field (or into a &io.Writer): a temporary Writer would lose what was written")
		c.checkArgs(e)
		return Void
	}
	c.printRecv = sel.X
	defer func() { c.printRecv = nil }()
	return c.ioPrint(e, "wprintf")
}

// logLevels are the log.Logger methods with a format, and each one's level.
var logLevels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// isLogger says whether s is the standard library's log.Logger.
func isLogger(s *Struct) bool {
	return s.mod != nil && s.mod.std && s.mod.path == "std/log" && s.Decl != nil && s.Decl.Name == "Logger"
}

// loggerPrintf checks lg.info("...", a, ...) and the other levels: io.printf's
// format, written into a logger line.
func (c *checker) loggerPrintf(e *ast.CallExpr, sel *ast.SelectorExpr) Type {
	c.printRecv, c.printLevel = sel.X, logLevels[sel.Sel]
	defer func() { c.printRecv = nil }()
	return c.ioPrint(e, "log."+sel.Sel)
}

// ioPrint checks io.print(a, b, ...), io.printf("...", a, b, ...),
// str.format(&dst, "...", a, ...) and str.format_len("...", a, ...).
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
			c.errorf(e.Lparen, "str.format needs the destination and the format: str.format(&buf, \"x = {}\", x)")
			return result
		}
		c.borrowArg(args[0], c.borrowOf(c.sliceOf(Byte)), "the destination of str.format")
		plan.Dst, args = args[0], args[1:]
	}
	var format []string     // pieces of text between the {}
	var specs []*FormatSpec // the spec of each {} (nil: none)
	if kind != "print" && kind != "eprint" {
		if len(args) == 0 {
			c.errorf(e.Lparen, "%s needs a format: %s(\"x = {}\\n\", x)", name, name)
			return result
		}
		lit, ok := args[0].(*ast.StringLit)
		if !ok {
			c.errorf(args[0].Pos(), "the format of %s must be a string literal", name)
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
			c.errorf(lit.P, "the format has %d {} and the call passes %d value(s)", len(format)-1, len(args))
		}
	}
	for i, a := range args {
		var t Type
		if u, ok := a.(*ast.UnaryExpr); ok && u.Op == token.AMP {
			t = c.peekType(a) // &x and &s[a:b]: written without copying
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
			c.errorf(a.Pos(), "%s doesn't write pointers (%s); write the pointee: *p or p.field", name, t)
			continue
		case isSl && (isPointerType(elem) || isSlicePtrElem(elem)):
			c.errorf(a.Pos(), "%s doesn't write %s: the elements are pointers", name, t)
			continue
		}
		part := PrintPart{Arg: i, Spec: spec}
		if b, ok := t.(*Borrow); ok {
			t, part.Ptr = b.Elem, true // writes the pointee, without copying
		} else {
			c.noCopy(t, a.Pos())
		}
		if spec != nil {
			c.checkSpec(spec, t, a)
		}
		var write *Func
		if tv, ok := t.(*TypeVar); ok {
			write = c.implMethod(tv, "write") // abstract check: T io.Display
		} else if im := c.findImpl(disp, t); im != nil {
			write = im.Methods["write"]
		} else if arr, ok := t.(*Array); ok {
			if im := c.findImpl(disp, c.sliceOf(arr.Elem)); im != nil {
				write = im.Methods["write"]
				part.View = true
			}
		}
		if write == nil {
			// in a slice or array, what's missing is the element's Display
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
					c.errorf(a.Pos(), "%s doesn't implement io.Display, because %s doesn't: declare impl auto io.Display for %s (or write the write method)", t, inner, inner)
				} else {
					c.errorf(a.Pos(), "%s doesn't implement io.Display: declare impl auto io.Display for %s (or write the write method)", t, t)
				}
			default:
				c.errorf(a.Pos(), "%s doesn't implement io.Display", t)
			}
			continue
		}
		part.Write = write
		plan.Parts = append(plan.Parts, part)
	}
	// joins the values with the text: the format, or spaces and \n in print
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

// checkSpec checks that the spec fits the value's type: base only
// on integers, decimal places only on reals, zeros only on numbers. It completes the
// default alignment: numbers on the right, the rest on the left.
func (c *checker) checkSpec(spec *FormatSpec, t Type, a ast.Expr) {
	isInt, isReal := isInteger(t), t == Float64 || t == Float32
	if spec.Text {
		elem, isSl := sliceElem(t)
		if arr, ok := t.(*Array); ok {
			elem, isSl = arr.Elem, true
		}
		if !isSl || elem != Byte {
			c.errorf(a.Pos(), "{:s} writes the bytes of a []byte as text, and the value is %s", t)
		}
		if spec.Align == 0 {
			spec.Align = '<'
		}
		return
	}
	switch {
	case spec.Base != 0 && !isInt:
		c.errorf(a.Pos(), "{:x}, {:b} and {:o} only apply to integers, and the value is %s", t)
	case spec.Alt && spec.Base == 0:
		c.errorf(a.Pos(), "# only applies with x, X, b or o: {:#x}")
	case spec.Prec >= 0 && !isReal:
		c.errorf(a.Pos(), "{:.N} (decimal places) only applies to float64 and float32, and the value is %s", t)
	case spec.Zero && !isInt && !isReal:
		c.errorf(a.Pos(), "{:0N} (leading zeros) only applies to numbers, and the value is %s", t)
	case spec.Zero && spec.Align != 0:
		c.errorf(a.Pos(), "{:0N} doesn't combine with alignment: use {:0N} or {:>N}")
	}
	if spec.Align == 0 {
		spec.Align = '<'
		if isInt || isReal {
			spec.Align = '>'
		}
	}
}

// parseFormat splits the format into the texts between the {}: "a {} b {:x}" gives
// ["a ", " b ", ""], with the spec of each {} (nil when it has none).
// {{ and }} write braces.
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
				c.errorf(lit.P, "lone '{' in the format of %s: use {} for a value or {{ to write {", name)
				return nil, nil, false
			}
			inner := s[i+1 : i+end]
			var spec *FormatSpec
			if inner != "" {
				body, ok := strings.CutPrefix(inner, ":")
				if !ok {
					c.errorf(lit.P, "{%s} in the format of %s: use {} for a value, {:...} for a value with a spec ({:x}, {:.2}, {:>8}) or {{ to write {", inner, name)
					return nil, nil, false
				}
				var msg string
				if spec, msg = parseSpec(body); msg != "" {
					c.errorf(lit.P, "{%s} in the format of %s: %s", inner, name, msg)
					return nil, nil, false
				}
			}
			out = append(out, cur.String())
			specs = append(specs, spec)
			cur.Reset()
			i += end
		case s[i] == '}':
			c.errorf(lit.P, "lone '}' in the format of %s: use }} to write }", name)
			return nil, nil, false
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(out, cur.String()), specs, true
}

// parseSpec reads what comes after {: — [[fill]align][#][0][width][.prec][x|X|b|o|s] —
// or explains what's wrong.
func parseSpec(s string) (*FormatSpec, string) {
	spec := &FormatSpec{Fill: ' ', Prec: -1}
	isAlign := func(b byte) bool { return b == '<' || b == '>' || b == '^' }
	switch {
	case len(s) >= 2 && isAlign(s[1]):
		if s[0] >= 0x80 {
			return nil, "the fill character must be ASCII"
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
		return nil, "the width goes up to 255"
	}
	spec.Width = w
	if spec.Zero && !hasWidth {
		return nil, "after the 0 comes the width: {:08}"
	}
	if strings.HasPrefix(s, ".") {
		s = s[1:]
		p, ok := digits()
		if !ok || p > 30 {
			return nil, "after the . comes the number of decimal places, up to 30: {:.2}"
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
		return nil, "unknown spec; use [[fill]align][#][0][width][.places][x|X|b|o|s], like {:>8}, {:08.2}, {:#x} or {:s}"
	}
	if spec.Text && (spec.Alt || spec.Zero || spec.Prec >= 0) {
		return nil, "{:s} only accepts width and alignment: {:s}, {:<10s}"
	}
	return spec, ""
}
