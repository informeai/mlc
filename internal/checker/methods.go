package checker

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Métodos
//
// "fn (p &Ponto) area() int" declara o método area de Ponto. O tipo do
// receptor diz o que a chamada p.area() faz com p:
//
//	fn (p Ponto)   recebe uma cópia
//	fn (p &Ponto)  empresta (lê e altera, não libera)
//	fn (p *Ponto)  recebe a posse (por exemplo, para liberar)
//
// Na chamada, o empréstimo ou o move acontece sem & explícito: a declaração
// do método já diz qual é. Métodos só existem em structs e só podem ser
// declarados no arquivo da struct.

type recvMode int

const (
	recvValue  recvMode = iota // fn (p Ponto)
	recvBorrow                 // fn (p &Ponto)
	recvOwner                  // fn (p *Ponto)
)

// signature resolve os parâmetros e o retorno de uma função ou método. recv,
// se não for nil, vira o primeiro parâmetro.
func (c *checker) signature(fn *Func, d *ast.FuncDecl, recv *Local) {
	seen := map[string]bool{}
	if recv != nil {
		fn.Params = append(fn.Params, recv)
		seen[recv.Name] = true
	}
	for _, p := range d.Params {
		switch {
		case predeclared[p.Name]:
			c.errorf(p.P, "o nome %q é reservado", p.Name)
		case seen[p.Name]:
			c.errorf(p.P, "parâmetro %s repetido", p.Name)
		}
		seen[p.Name] = true
		fn.Params = append(fn.Params, &Local{
			Name: p.Name, CName: "l_" + p.Name, Type: c.resolveType(p.Type, ctxParam), Pos: p.P, Param: true,
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
		c.errorf(rt.P, "o receptor de um método precisa ser S, &S ou *S, com S uma struct")
		return
	}
	if named.Mod != ast.TypeNamed || named.Module != "" {
		c.errorf(rt.P, "métodos só podem ser declarados no arquivo da struct, com receptor S, &S ou *S")
		return
	}
	s := c.mod.structs[named.Name]
	if s == nil {
		c.errorf(named.P, "métodos só existem em structs: %s não é uma struct deste arquivo", named.Name)
		return
	}
	if s.Params != nil {
		c.collectGenericMethod(d, s, named)
		return
	}
	if len(named.Args) > 0 {
		c.errorf(named.P, "%s não é genérica", s.Name)
		return
	}
	switch {
	case s.Methods[d.Name] != nil:
		c.errorf(d.P, "método %s.%s já declarado na linha %d", s.Name, d.Name, s.Methods[d.Name].Decl.P.Line)
		return
	case s.Field(d.Name) != nil:
		c.errorf(d.P, "%s já tem um campo %s; o método precisa de outro nome", s.Name, d.Name)
		return
	case mode == recvValue && s.Resource:
		c.errorf(rt.P, "%s contém ponteiros donos e não pode ser copiada: use o receptor &%s ou *%s", s.Name, named.Name, named.Name)
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
		// método com parâmetros de tipo próprios: um modelo, instanciado na chamada
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

// methodCall checa x.m(args). ok é false se x não tem método m (e então a
// chamada é checada como antes).
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
	if s == nil {
		// int, []T, parâmetros de tipo...: só métodos de impl
		return c.implMethodCall(e, sel, t)
	}
	m := s.Methods[sel.Sel]
	if m == nil && s.Origin != nil {
		if msg := c.methodConstraintError(s, sel.Sel); msg != "" {
			c.errorf(sel.SelPos, "%s", msg) // fn (v &Vec[T any]) get(), chamado com T dono
			c.checkArgs(e)
			return Invalid, true
		}
		m = c.instMethod(s, sel.Sel) // método de struct genérica
	}
	if m == nil {
		if f := s.Field(sel.Sel); f != nil {
			if ft, ok := f.Type.(*FuncType); ok {
				c.expr(sel) // s.campo(x), com o campo um valor fn
				return c.indirectCall(e, ft), true
			}
		}
		if rt, ok := c.implMethodCall(e, sel, t); ok {
			return rt, true // método de um impl com bloco (ou impl auto)
		}
		if s.Field(sel.Sel) != nil {
			c.errorf(sel.SelPos, "%s.%s é um campo, não um método", s.Name, sel.Sel)
		} else {
			c.errorf(sel.SelPos, "%s não tem método %s", s.Name, sel.Sel)
		}
		c.checkArgs(e)
		return Invalid, true
	}
	if !m.Pub && m.mod != c.mod {
		c.errorf(sel.SelPos, "o método %s não é público (declare com pub fn)", m.Name)
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

// structBehind devolve a struct de um valor S, *S, ?*S ou &S.
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

// receiverArg monta o primeiro argumento da chamada a partir de x, conforme
// o receptor do método: x (cópia ou posse), *x (cópia através de ponteiro) ou
// &x (empréstimo). O nó criado recebe o tipo e segue para a checagem de posse
// e para o gerador como um argumento comum.
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
			return x // empréstimo recebido, repassado
		}
		if _, isStruct := t.(*Struct); isStruct && !isPlace(x) {
			c.errorf(pos, "o método %s empresta o receptor, e um valor temporário não pode ser emprestado: guarde em uma variável", m.Name)
			return nil
		}
		amp := &ast.UnaryExpr{Op: token.AMP, X: x, P: pos}
		c.info.Types[amp] = m.Params[0].Type
		return amp
	}
	if _, isPtr := t.(*Pointer); !isPtr {
		c.errorf(pos, "o método %s recebe a posse do receptor (*%s): chame num ponteiro dono", m.Name, s.Name)
		return nil
	}
	return x
}

// callArgs checa os argumentos de uma chamada contra params.
func (c *checker) callArgs(e *ast.CallExpr, fn *Func, params []*Local) {
	if len(e.Args) != len(params) {
		c.errorf(e.Lparen, "%s espera %d argumento(s), recebeu %d", fn.Name, len(params), len(e.Args))
	}
	for i, a := range e.Args {
		context := fmt.Sprintf("argumento %d de %s", i+1, fn.Name)
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
		if opt, ok := params[i].Type.(*OptArg); ok {
			// ?&T: nil (NULL no C) ou o que &T aceitaria
			if isNil(a) {
				c.value(a)
				continue
			}
			context += " (" + opt.String() + ": nil ou um empréstimo)"
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

// CallArgs devolve os argumentos de uma chamada como a função os recebe: num
// método, o receptor vem primeiro.
func (info *Info) CallArgs(e *ast.CallExpr) []ast.Expr {
	if r, ok := info.Recv[e]; ok {
		return append([]ast.Expr{r}, e.Args...)
	}
	return e.Args
}
