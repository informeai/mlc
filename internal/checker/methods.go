package checker

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Methods
//
// "fn (p &Point) area() int" declares the method area of Point. The
// receiver's type says what the call p.area() does with p:
//
//	fn (p Point)   receives a copy
//	fn (p &Point)  borrows (reads and changes, doesn't free)
//	fn (p *Point)  receives ownership (for example, to free)
//
// At the call, the borrow or the move happens without an explicit &: the method's
// declaration already says which one. Methods only exist on structs and can only be
// declared in the struct's file.

type recvMode int

const (
	recvValue  recvMode = iota // fn (p Point)
	recvBorrow                 // fn (p &Point)
	recvOwner                  // fn (p *Point)
)

// signature resolves the parameters and the result of a function or method. recv,
// if not nil, becomes the first parameter.
func (c *checker) signature(fn *Func, d *ast.FuncDecl, recv *Local) {
	seen := map[string]bool{}
	if recv != nil {
		fn.Params = append(fn.Params, recv)
		seen[recv.Name] = true
	}
	for _, p := range d.Params {
		switch {
		case predeclared[p.Name]:
			c.errorf(p.P, "the name %q is reserved", p.Name)
		case seen[p.Name]:
			c.errorf(p.P, "repeated parameter %s", p.Name)
		}
		seen[p.Name] = true
		if p.Kept && !d.ExternC {
			c.errorf(p.P, "kept only exists in the parameters of an extern \"C\" fn: it says C keeps the callback or its context after the call")
		}
		fn.Params = append(fn.Params, &Local{
			Name: p.Name, CName: "l_" + p.Name, Type: c.resolveType(p.Type, ctxParam), Pos: p.P, Param: true, Kept: p.Kept,
		})
	}
	if d.Result != nil {
		fn.Result = c.resolveType(d.Result, ctxResult)
	}
	c.setBorrowFrom(fn, d)
}

func (c *checker) collectMethod(d *ast.FuncDecl) {
	rt := d.Recv.Type
	mode := recvValue
	named := rt
	switch rt.Mod {
	case ast.TypeBorrow:
		mode, named = recvBorrow, rt.Elem
	case ast.TypePointer:
		mode, named = recvOwner, rt.Elem
	case ast.TypeNamed:
	default:
		c.errorf(rt.P, "a method's receiver must be S, &S or *S, with S a struct")
		return
	}
	if named.Mod != ast.TypeNamed || named.Module != "" {
		c.errorf(rt.P, "methods can only be declared in the struct's file, with receiver S, &S or *S")
		return
	}
	s := c.mod.structs[named.Name]
	if s == nil {
		c.errorf(named.P, "methods only exist on structs: %s isn't a struct of this file", named.Name)
		return
	}
	if s.Params != nil {
		c.collectGenericMethod(d, s, named)
		return
	}
	if len(named.Args) > 0 {
		c.errorf(named.P, "%s isn't generic", s.Name)
		return
	}
	switch {
	case s.Methods[d.Name] != nil:
		c.errorf(d.P, "method %s.%s already declared on line %d", s.Name, d.Name, s.Methods[d.Name].Decl.P.Line)
		return
	case s.Field(d.Name) != nil:
		c.errorf(d.P, "%s already has a field %s; the method needs another name", s.Name, d.Name)
		return
	case mode == recvValue && s.Resource:
		c.errorf(rt.P, "%s contains owning pointers and can't be copied: use the receiver &%s or *%s", s.Name, named.Name, named.Name)
		return
	}
	var t Type = s
	switch mode {
	case recvBorrow:
		t = c.borrowOf(s)
	case recvOwner:
		t = c.pointerTo(s, false)
	}
	fn := &Func{
		Name: s.Name + "." + d.Name, Result: Void, Decl: d,
		CName: "mlc_fn_" + c.mod.prefix + named.Name + "__" + d.Name, File: c.mod.file, Pub: d.Pub, mod: c.mod,
		Method: true, recv: mode,
	}
	recv := &Local{Name: d.Recv.Name, CName: "l_" + d.Recv.Name, Type: t, Pos: d.Recv.P, Param: true}
	s.Methods[d.Name] = fn
	if len(d.TypeParams) > 0 {
		// a method with its own type parameters: a template, instantiated at the call
		c.genericMethod(fn, d, c.mod, map[string]Type{}, recv)
		full := map[string]Type{}
		for k, v := range fn.methodBinding {
			full[k] = v
		}
		c.abstracts = append(c.abstracts, abstractCheck{fn, full})
		return
	}
	c.signature(fn, d, recv)
	c.info.Funcs = append(c.info.Funcs, fn)
}

// methodCall checks x.m(args). ok is false if x has no method m (and then the
// call is checked as before).
func (c *checker) methodCall(e *ast.CallExpr, sel *ast.SelectorExpr) (Type, bool) {
	t := c.value(sel.X)
	s := structBehind(t)
	if s != nil && s.ChanEnd != 0 {
		return c.chanMethod(e, sel, t, s), true
	}
	if s != nil && s.MutexKind != 0 {
		return c.mutexMethod(e, sel, t, s), true
	}
	if s != nil && sel.Sel == "printf" && s.mod != nil && s.mod.std && s.mod.path == "std/io" && s.Decl != nil && s.Decl.Name == "Writer" {
		return c.writerPrintf(e, sel, t), true
	}
	if _, ok := logLevels[sel.Sel]; ok && s != nil && isLogger(s) {
		return c.loggerPrintf(e, sel), true
	}
	if s == nil {
		// int, []T, type parameters...: only impl methods
		return c.implMethodCall(e, sel, t)
	}
	m := s.Methods[sel.Sel]
	if m == nil && s.Origin != nil {
		if msg := c.methodConstraintError(s, sel.Sel); msg != "" {
			c.errorf(sel.SelPos, "%s", msg) // fn (v &Vec[T any]) get(), called with an owning T
			c.checkArgs(e)
			return Invalid, true
		}
		m = c.instMethod(s, sel.Sel) // a generic struct's method
	}
	if m == nil {
		if f := s.Field(sel.Sel); f != nil {
			if ft, ok := f.Type.(*FuncType); ok {
				c.expr(sel) // s.field(x), with the field an fn value
				return c.indirectCall(e, ft), true
			}
		}
		if rt, ok := c.implMethodCall(e, sel, t); ok {
			return rt, true // a method of an impl with a block (or impl auto)
		}
		if s.Field(sel.Sel) != nil {
			c.errorf(sel.SelPos, "%s.%s is a field, not a method", s.Name, sel.Sel)
		} else {
			c.errorf(sel.SelPos, "%s has no method %s", s.Name, sel.Sel)
		}
		c.checkArgs(e)
		return Invalid, true
	}
	if !m.Pub && m.mod != c.mod {
		c.errorf(sel.SelPos, "the method %s isn't public (declare it with pub fn)", m.Name)
	}
	if m.typeVars != nil {
		return c.genericMethodCall(e, sel, t, s, m, nil), true
	}
	recv := c.receiverArg(sel.X, t, s, m)
	if recv == nil {
		c.checkArgs(e)
		return Invalid, true
	}
	c.info.Recv[e] = recv
	c.callArgs(e, m, m.Params[1:])
	c.info.Calls[e] = Callee{Func: m}
	return m.Result, true
}

// structBehind returns the struct of a value S, *S, ?*S or &S.
func structBehind(t Type) *Struct {
	switch u := t.(type) {
	case *Pointer:
		t = u.Elem
	case *Borrow:
		t = u.Elem
	}
	s, _ := t.(*Struct)
	return s
}

// receiverArg builds the call's first argument from x, according to
// the method's receiver: x (copy or ownership), *x (copy through a pointer) or
// &x (borrow). The created node gets the type and goes on to the ownership check
// and the generator as an ordinary argument.
func (c *checker) receiverArg(x ast.Expr, t Type, s *Struct, m *Func) ast.Expr {
	pos := x.Pos()
	switch m.recv {
	case recvValue:
		if _, isStruct := t.(*Struct); isStruct {
			return x
		}
		deref := &ast.UnaryExpr{Op: token.MUL, X: x, P: pos}
		c.info.Types[deref] = s
		return deref
	case recvBorrow:
		if isBorrow(t) {
			return x // a received borrow, passed along
		}
		if _, isStruct := t.(*Struct); isStruct && !isPlace(x) {
			c.errorf(pos, "the method %s borrows the receiver, and a temporary value can't be borrowed: store it in a variable", m.Name)
			return nil
		}
		amp := &ast.UnaryExpr{Op: token.AMP, X: x, P: pos}
		c.info.Types[amp] = m.Params[0].Type
		return amp
	}
	if _, isPtr := t.(*Pointer); !isPtr {
		c.errorf(pos, "the method %s takes ownership of the receiver (*%s): call it on an owning pointer", m.Name, s.Name)
		return nil
	}
	return x
}

// callArgs checks a call's arguments against params.
func (c *checker) callArgs(e *ast.CallExpr, fn *Func, params []*Local) {
	if len(e.Args) != len(params) {
		c.errorf(e.Lparen, "%s expects %d argument(s), got %d", fn.Name, len(params), len(e.Args))
	}
	for i, a := range e.Args {
		context := fmt.Sprintf("argument %d of %s", i+1, fn.Name)
		if i >= len(params) {
			c.value(a)
			continue
		}
		if b, ok := params[i].Type.(*Borrow); ok {
			c.borrowArg(a, b, context)
			continue
		}
		if oh, ok := params[i].Type.(*OutHandle); ok {
			c.outArg(a, oh, context)
			continue
		}
		if ft, ok := params[i].Type.(*FuncType); ok && fn.CSym != "" {
			c.callbackArg(a, ft, context)
			continue
		}
		if opt, ok := params[i].Type.(*OptArg); ok {
			// ?&T: nil (NULL in C) or what &T would accept
			if isNil(a) {
				c.value(a)
				continue
			}
			context += " (" + opt.String() + ": nil or a borrow)"
			switch in := opt.Inner.(type) {
			case *Borrow:
				c.borrowArg(a, in, context)
			case *OutHandle:
				c.outArg(a, in, context)
			}
			continue
		}
		c.valueAs(a, params[i].Type, context)
	}
}

// CallArgs returns a call's arguments as the function receives them: in a
// method, the receiver comes first.
func (info *Info) CallArgs(e *ast.CallExpr) []ast.Expr {
	if r, ok := info.Recv[e]; ok {
		return append([]ast.Expr{r}, e.Args...)
	}
	return e.Args
}
