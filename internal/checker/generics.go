package checker

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Genéricos
//
//	fn maior[T ordered](a T, b T) T { ... }
//	struct Par[A any, B any] { a A; b B }
//	fn (v &Vetor[T]) tamanho() int { ... }
//
// A definição é checada uma vez, de forma abstrata: cada parâmetro de tipo é
// um TypeVar, e só vale o que a restrição permite (com T any, a < b é erro na
// própria definição). Cada uso com tipos concretos (maior[int], Par[int,
// string]) cria uma instância: uma cópia da declaração, checada com os tipos
// reais e gerada no C com nome próprio (monomorfização).
//
// Restrições: any e comparable (qualquer tipo sem posse), ordered e number
// (números), integer (inteiros). Nesta versão, argumentos de tipo não podem
// ser donos nem empréstimos.

// TypeVar é um parâmetro de tipo durante a checagem abstrata da definição.
type TypeVar struct {
	Name       string
	Constraint string
	Iface      *Interface // restrição que é uma interface (nil nas embutidas)
}

func (t *TypeVar) String() string { return t.Name }

var constraints = map[string]bool{"any": true, "comparable": true, "ordered": true, "number": true, "integer": true, "owned": true}

// tvHas diz se t é um TypeVar cuja restrição permite a capacidade cap:
// "eq" (==), "order" (<), "arith" (+ - * /) ou "mod" (%).
func tvHas(t Type, cap string) bool {
	tv, ok := t.(*TypeVar)
	if !ok {
		return false
	}
	switch cap {
	case "eq":
		return tv.Constraint != "owned"
	case "order", "arith":
		return tv.Constraint == "ordered" || tv.Constraint == "number" || tv.Constraint == "integer"
	case "mod":
		return tv.Constraint == "integer"
	}
	return false
}

func numLike(t Type) bool   { return isNumeric(t) || tvHas(t, "arith") }
func orderLike(t Type) bool { return isNumeric(t) || tvHas(t, "order") }

// abstract diz se t contém um TypeVar (então é só da checagem abstrata e
// nunca chega ao gerador).
func abstract(t Type) bool {
	switch t := t.(type) {
	case *TypeVar:
		return true
	case *Slice:
		return abstract(t.Elem)
	case *Array:
		return abstract(t.Elem)
	case *Pointer:
		return abstract(t.Elem)
	case *Borrow:
		return abstract(t.Elem)
	case *Struct:
		for _, a := range t.Args {
			if abstract(a) {
				return true
			}
		}
	case *FuncType:
		for _, p := range t.Params {
			if abstract(p) {
				return true
			}
		}
		return abstract(t.Result)
	}
	return false
}

func anyAbstract(ts []Type) bool {
	for _, t := range ts {
		if abstract(t) {
			return true
		}
	}
	return false
}

// satisfies diz se o tipo t atende a restrição c.
func satisfies(t Type, c string) bool {
	if tv, ok := t.(*TypeVar); ok {
		switch c {
		case "owned":
			return true // todo tipo aceito por outra restrição é aceito por owned
		case "any", "comparable":
			return tv.Constraint != "owned"
		case "ordered", "number":
			return tvHas(tv, "order")
		}
		return tv.Constraint == "integer"
	}
	if c == "owned" {
		ok, _ := ownedOK(t)
		return ok
	}
	if IsOwner(t) || isBorrow(t) || isResource(t) || t == Void || t == NilType || t == Invalid || isUntyped(t) {
		return false
	}
	switch c {
	case "ordered", "number":
		return isNumeric(t)
	case "integer":
		return isInteger(t)
	}
	return true
}

// typeName dá um nome de identificador C para t, usado nas instâncias.
func typeName(t Type) string {
	switch t := t.(type) {
	case *Struct:
		return t.CName
	case *Enum:
		return t.CName
	case *Slice:
		return "sl_" + typeName(t.Elem)
	case *Array:
		return fmt.Sprintf("ar%d_%s", t.Len, typeName(t.Elem))
	case *Pointer:
		return "p_" + typeName(t.Elem)
	case *Borrow:
		return "b_" + typeName(t.Elem)
	case *FuncType:
		return fmt.Sprintf("fn%d", t.ID)
	}
	return strings.ReplaceAll(t.String(), " ", "_")
}

// typeKey identifica t no cache de instâncias. TypeVars, structs e enums
// entram pela identidade: dois parâmetros chamados T (o de sort e o de
// reverse) são tipos diferentes, assim como duas structs Ponto de arquivos
// diferentes.
func typeKey(t Type) string {
	switch t := t.(type) {
	case *TypeVar, *Struct, *Enum:
		return fmt.Sprintf("%p", t)
	case *Slice:
		return "[]" + typeKey(t.Elem)
	case *Array:
		return fmt.Sprintf("[%d]%s", t.Len, typeKey(t.Elem))
	case *Pointer:
		return "*" + typeKey(t.Elem)
	case *Borrow:
		return "&" + typeKey(t.Elem)
	case *FuncType:
		var ps []string
		for _, p := range t.Params {
			ps = append(ps, typeKey(p))
		}
		return "fn(" + strings.Join(ps, ",") + ")" + typeKey(t.Result)
	}
	return t.String()
}

func typeList(ts []Type) (display, key, mangled string) {
	var d, k, m []string
	for _, t := range ts {
		d = append(d, t.String())
		k = append(k, typeKey(t))
		m = append(m, typeName(t))
	}
	return strings.Join(d, ", "), strings.Join(k, ","), strings.Join(m, "__")
}

// bind liga os nomes dos parâmetros de tipo aos tipos.
func bind(params []*ast.TypeParam, args []Type) map[string]Type {
	m := map[string]Type{}
	for i, p := range params {
		if i < len(args) {
			m[p.Name] = args[i]
		}
	}
	return m
}

// typeVars cria os TypeVars de uma lista de parâmetros de tipo, checando as
// restrições.
func (c *checker) typeVars(params []*ast.TypeParam) []Type {
	var vs []Type
	seen := map[string]bool{}
	for _, p := range params {
		if seen[p.Name] {
			c.errorf(p.P, "parâmetro de tipo %s repetido", p.Name)
		}
		seen[p.Name] = true
		vs = append(vs, c.newTypeVar(p.Name, p))
	}
	return vs
}

// checkTypeArgs confere quantidade e restrições dos argumentos de tipo.
func (c *checker) checkTypeArgs(what string, params []*ast.TypeParam, args []Type, pos token.Pos) bool {
	if len(args) != len(params) {
		c.errorf(pos, "%s espera %d argumento(s) de tipo, recebeu %d", what, len(params), len(args))
		return false
	}
	ok := true
	for i, p := range params {
		if args[i] == Invalid {
			ok = false
			continue
		}
		if b, _, _ := splitConstraint(p.Constraint); b == "owned" && !abstract(args[i]) && c.instName == "" {
			c.requireFree(args[i], pos, what) // ?*X com X dono: precisa de free
		}
		if !c.satisfiesParam(args[i], p) {
			reason := ""
			builtin, iface, _ := splitConstraint(p.Constraint)
			switch {
			case builtin == "owned" && !satisfies(args[i], "owned"):
				if _, why := ownedOK(args[i]); why != "" {
					reason = " (" + why + ")"
				}
			case builtin != "" && builtin != "owned" && !satisfies(args[i], builtin) && (IsOwner(args[i]) || isBorrow(args[i]) || isResource(args[i])):
				reason = " (é dono: para aceitar donos, a restrição precisa ser owned)"
			case iface != "" && (builtin == "" || satisfies(args[i], builtin)):
				if IsOwner(args[i]) && builtin != "owned" {
					c.errorf(pos, "%s é dono e não atende %s, exigida por %s em %s (para aceitar donos: %s owned + %s)", args[i], p.Constraint, p.Name, what, p.Name, iface)
				} else {
					hint := fmt.Sprintf("declare impl %s for %s", iface, args[i])
					if base := baseOf(args[i]); (iface == "cmp.Eq" || iface == "cmp.Ord" || iface == "io.Display") && (isStructType(base) || isEnumType(base)) {
						hint = fmt.Sprintf("declare impl auto %s for %s, ou escreva o método", iface, base)
					}
					c.errorf(pos, "%s não implementa %s, exigida por %s em %s (%s)", args[i], iface, p.Name, what, hint)
				}
				ok = false
				continue
			}
			c.errorf(pos, "%s não atende %s, exigida por %s em %s%s", args[i], p.Constraint, p.Name, what, reason)
			ok = false
		}
	}
	return ok
}

// ---------- structs genéricas ----------

// instStruct devolve a instância de uma struct genérica com os argumentos dados.
func (c *checker) instStruct(s *Struct, args []Type, pos token.Pos) Type {
	if !c.checkTypeArgs(s.Name, s.Params, args, pos) {
		return Invalid
	}
	display, key, mangled := typeList(args)
	if inst, ok := s.instances[key]; ok {
		return inst
	}
	inst := &Struct{
		Name: s.Name + "[" + display + "]", CName: s.CName + "__" + mangled,
		File: s.File, Pub: s.Pub, Decl: s.Decl, Methods: map[string]*Func{},
		Origin: s, Args: args, mod: s.mod,
	}
	s.instances[key] = inst
	savedMod, savedTP := c.mod, c.typeParams
	c.mod, c.typeParams = s.mod, bind(s.Params, args)
	c.building[inst] = true
	for _, fd := range s.Decl.Fields {
		ft := c.resolveType(fd.Type, ctxField)
		if fs, ok := ft.(*Struct); ok && c.building[fs] {
			c.errorf(fd.P, "struct %s contém a si mesma por valor; use um ponteiro", inst.Name)
			ft = Invalid
		}
		inst.Fields = append(inst.Fields, &Field{Name: fd.Name, Type: ft})
		if IsOwner(ft) || isResource(ft) {
			inst.Resource = true
		}
	}
	delete(c.building, inst)
	c.mod, c.typeParams = savedMod, savedTP
	if !abstract(inst) {
		c.info.Structs = append(c.info.Structs, inst)
	}
	return inst
}

// ---------- funções e métodos genéricos ----------

// instFunc devolve a instância de uma função genérica. Instâncias concretas
// entram na fila para ter o corpo checado (e depois gerado); com argumentos
// abstratos (dentro de outra definição genérica), só a assinatura importa.
func (c *checker) instFunc(g *Func, args []Type, pos token.Pos) *Func {
	if !c.checkTypeArgs(g.Name, g.Decl.TypeParams, args, pos) {
		return nil
	}
	display, key, mangled := typeList(args)
	if inst, ok := g.instances[key]; ok {
		return inst
	}
	decl := g.Decl
	if !anyAbstract(args) {
		decl = ast.Clone(g.Decl)
	}
	inst := &Func{
		Name: g.Name + "[" + display + "]", Result: Void, Decl: decl,
		CName: g.CName + "__" + mangled, File: g.File, Pub: g.Pub, mod: g.mod,
	}
	g.instances[key] = inst
	binding := bind(g.Decl.TypeParams, args)
	c.withScope(g.mod, binding, func() { c.signature(inst, decl, nil) })
	if !anyAbstract(args) {
		c.queue(inst, binding)
	}
	return inst
}

// instMethod devolve o método name da instância de struct s, criando-o a
// partir da declaração genérica.
func (c *checker) instMethod(s *Struct, name string) *Func {
	if m := s.Methods[name]; m != nil {
		return m
	}
	origin := s.Origin
	if origin == nil || origin.methodDecls[name] == nil {
		return nil
	}
	d := origin.methodDecls[name]
	binding := map[string]Type{}
	for i, a := range recvParamNames(d.Recv.Type) {
		if i < len(s.Args) {
			binding[a] = s.Args[i]
		}
	}
	concrete := !abstract(s)
	decl := d
	if concrete {
		decl = ast.Clone(d)
	}
	_, _, mangled := typeList(s.Args)
	m := &Func{
		Name: s.Name + "." + name, Result: Void, Decl: decl,
		CName: origin.CName + "__" + mangled + "__" + name, File: origin.File, Pub: d.Pub, mod: origin.mod,
		Method: true, recv: recvModeOf(d.Recv.Type),
	}
	s.Methods[name] = m
	c.withScope(origin.mod, binding, func() {
		recv := &Local{Name: d.Recv.Name, CName: "l_" + d.Recv.Name, Type: c.recvType(s, m.recv), Pos: d.Recv.P, Param: true}
		c.signature(m, decl, recv)
	})
	if concrete {
		c.queue(m, binding)
	}
	return m
}

func (c *checker) recvType(s *Struct, mode recvMode) Type {
	switch mode {
	case recvBorrow:
		return c.borrowOf(s)
	case recvOwner:
		return c.pointerTo(s, false)
	}
	return s
}

func recvModeOf(t *ast.TypeExpr) recvMode {
	switch t.Mod {
	case ast.TypeBorrow:
		return recvBorrow
	case ast.TypePointer:
		return recvOwner
	}
	return recvValue
}

// withScope executa f no módulo m, com os parâmetros de tipo ligados.
func (c *checker) withScope(m *module, binding map[string]Type, f func()) {
	savedMod, savedTP, savedScope := c.mod, c.typeParams, c.scope
	c.mod, c.typeParams, c.scope = m, binding, nil
	f()
	c.mod, c.typeParams, c.scope = savedMod, savedTP, savedScope
}

type pendingInst struct {
	fn      *Func
	binding map[string]Type
}

func (c *checker) queue(fn *Func, binding map[string]Type) {
	fn.queued = true // o corpo é checado em checkInstances, com binding
	c.info.Funcs = append(c.info.Funcs, fn)
	c.pending = append(c.pending, pendingInst{fn, binding})
}

// checkInstances checa o corpo das instâncias concretas. Checar uma pode criar
// outras, que entram na fila.
func (c *checker) checkInstances() {
	for len(c.pending) > 0 {
		p := c.pending[0]
		c.pending = c.pending[1:]
		saved := c.instName
		c.instName = p.fn.Name
		c.muted = c.probes[p.fn] // os erros de tipo já aparecem na definição
		c.typeParams = p.binding
		c.checkFunc(p.fn)
		c.typeParams = nil
		if c.muted && c.mutedErrs > 0 {
			c.probeBroken[p.fn] = true // não verifica a posse de código mal tipado
		}
		c.instName, c.muted, c.mutedErrs = saved, false, 0
	}
}

// ---------- chamadas ----------

// genericCall checa uma chamada a uma função genérica: f(args), com os tipos
// deduzidos dos argumentos, ou f[T](args), com os tipos explícitos.
func (c *checker) genericCall(e *ast.CallExpr, g *Func, explicit []ast.Expr) Type {
	var args []Type
	if explicit != nil {
		for _, x := range explicit {
			args = append(args, c.exprType(x))
		}
	} else {
		args = c.infer(e, g)
		if args == nil {
			return Invalid // os argumentos já foram avaliados na inferência
		}
	}
	inst := c.instFunc(g, args, e.Lparen)
	if inst == nil {
		c.checkArgs(e)
		return Invalid
	}
	return c.userCall(e, inst)
}

// infer deduz os argumentos de tipo de uma chamada a partir dos argumentos.
func (c *checker) infer(e *ast.CallExpr, g *Func) []Type {
	vars := map[*TypeVar]bool{}
	for _, v := range g.typeVars {
		vars[v.(*TypeVar)] = true
	}
	bound := map[*TypeVar]Type{}
	var untyped []struct {
		p Type
		t Type
	}
	saved := len(c.errs)
	for i, a := range e.Args {
		if i >= len(g.Params) {
			break
		}
		at := c.peekType(a)
		if isUntyped(at) {
			untyped = append(untyped, struct{ p, t Type }{g.Params[i].Type, at})
			continue
		}
		unify(g.Params[i].Type, at, vars, bound)
	}
	c.errs = c.errs[:saved] // os argumentos são checados de novo, com os tipos certos
	for _, u := range untyped {
		if tv, ok := u.p.(*TypeVar); ok && vars[tv] && bound[tv] == nil {
			bound[tv] = DefaultType(u.t)
		}
	}
	var args []Type
	for _, v := range g.typeVars {
		t := bound[v.(*TypeVar)]
		if t == nil {
			c.errorf(e.Lparen, "não foi possível deduzir %s em %s: escreva os tipos, como %s[int](...)", v, g.Name, g.Name)
			return nil
		}
		args = append(args, t)
	}
	return args
}

// peekType calcula o tipo de um argumento antes de saber o tipo do parâmetro.
func (c *checker) peekType(a ast.Expr) Type {
	u, ok := a.(*ast.UnaryExpr)
	if !ok || u.Op != token.AMP {
		return c.value(a)
	}
	if sl, ok := u.X.(*ast.SliceExpr); ok {
		return c.subSlice(sl)
	}
	switch t := c.value(u.X).(type) {
	case *Pointer:
		return c.borrowOf(t.Elem)
	case *Borrow:
		return t
	default:
		return c.borrowOf(t)
	}
}

// unify liga os TypeVars de vars que aparecem em p aos tipos correspondentes de a.
func unify(p, a Type, vars map[*TypeVar]bool, bound map[*TypeVar]Type) {
	switch pt := p.(type) {
	case *TypeVar:
		if vars[pt] && bound[pt] == nil && a != Invalid {
			bound[pt] = a
		}
	case *Slice:
		switch at := a.(type) {
		case *Slice:
			unify(pt.Elem, at.Elem, vars, bound)
		case *Array: // &a, com a [N]T, vai para &[]T
			unify(pt.Elem, at.Elem, vars, bound)
		}
	case *Array:
		if at, ok := a.(*Array); ok {
			unify(pt.Elem, at.Elem, vars, bound)
		}
	case *Borrow:
		switch at := a.(type) {
		case *Borrow:
			unify(pt.Elem, at.Elem, vars, bound)
		case *Array:
			unify(pt.Elem, at, vars, bound)
		}
	case *Pointer:
		if at, ok := a.(*Pointer); ok {
			unify(pt.Elem, at.Elem, vars, bound)
		}
	case *Struct:
		if at, ok := a.(*Struct); ok && pt.Origin != nil && at.Origin == pt.Origin {
			for i := range pt.Args {
				if i < len(at.Args) {
					unify(pt.Args[i], at.Args[i], vars, bound)
				}
			}
		}
	}
}

// exprType lê um tipo escrito como expressão (o argumento de tipo de f[int],
// Par[int, string]{...}).
func (c *checker) exprType(e ast.Expr) Type {
	if u, ok := e.(*ast.UnaryExpr); ok && (u.Op == token.MUL || u.Op == token.AMP) {
		return c.resolveType(typeExprOf(u), ctxVar)
	}
	switch e := e.(type) {
	case *ast.TypeArg:
		return c.resolveType(e.Type, ctxVar)
	case *ast.Ident:
		return c.resolveType(&ast.TypeExpr{Name: e.Name, P: e.P}, ctxVar)
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return c.resolveType(&ast.TypeExpr{Module: id.Name, Name: e.Sel, P: e.SelPos}, ctxVar)
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		base, args := splitIndex(e)
		var targs []*ast.TypeExpr
		for _, a := range args {
			targs = append(targs, typeExprOf(a))
		}
		switch b := base.(type) {
		case *ast.Ident:
			return c.resolveType(&ast.TypeExpr{Name: b.Name, Args: targs, P: b.P}, ctxVar)
		case *ast.SelectorExpr:
			if id, ok := b.X.(*ast.Ident); ok {
				return c.resolveType(&ast.TypeExpr{Module: id.Name, Name: b.Sel, Args: targs, P: b.SelPos}, ctxVar)
			}
		}
	}
	c.errorf(e.Pos(), "esperado um tipo")
	return Invalid
}

// typeExprOf converte uma expressão que escreve um tipo na forma de TypeExpr.
func typeExprOf(e ast.Expr) *ast.TypeExpr {
	switch e := e.(type) {
	case *ast.TypeArg:
		return e.Type
	case *ast.Ident:
		return &ast.TypeExpr{Name: e.Name, P: e.P}
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return &ast.TypeExpr{Module: id.Name, Name: e.Sel, P: e.SelPos}
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		base, args := splitIndex(e)
		t := typeExprOf(base)
		for _, a := range args {
			t.Args = append(t.Args, typeExprOf(a))
		}
		return t
	case *ast.UnaryExpr:
		// *No e &T entre colchetes são lidos como expressões: f[*No](x)
		switch e.Op {
		case token.MUL:
			return &ast.TypeExpr{Mod: ast.TypePointer, Elem: typeExprOf(e.X), P: e.P}
		case token.AMP:
			return &ast.TypeExpr{Mod: ast.TypeBorrow, Elem: typeExprOf(e.X), P: e.P}
		}
	}
	return &ast.TypeExpr{Name: "?", P: e.Pos()}
}

func splitIndex(e ast.Expr) (ast.Expr, []ast.Expr) {
	switch e := e.(type) {
	case *ast.IndexExpr:
		return e.X, []ast.Expr{e.Index}
	case *ast.IndexListExpr:
		return e.X, e.Indices
	}
	return e, nil
}

// genericFuncOf devolve a função genérica nomeada por e (f ou mod.f), se houver.
func (c *checker) genericFuncOf(e ast.Expr) *Func {
	switch e := e.(type) {
	case *ast.Ident:
		if c.scope.lookup(e.Name) == nil {
			return c.mod.generics[e.Name]
		}
	case *ast.SelectorExpr:
		if m := c.userModule(e.X); m != nil {
			if g := m.generics[e.Sel]; g != nil {
				if !g.Pub {
					c.errorf(e.SelPos, "%s.%s não é pública (declare com pub fn)", m.name, e.Sel)
				}
				return g
			}
		}
	}
	return nil
}

// recvParamNames devolve os nomes dos parâmetros de tipo de um receptor como
// &Vetor[T]: ["T"].
func recvParamNames(t *ast.TypeExpr) []string {
	if t.Mod == ast.TypeBorrow || t.Mod == ast.TypePointer {
		t = t.Elem
	}
	var names []string
	for _, a := range t.Args {
		names = append(names, a.Name)
	}
	return names
}

// abstractCheck é uma definição genérica esperando a checagem abstrata do corpo.
type abstractCheck struct {
	fn      *Func
	binding map[string]Type
}

// collectGeneric registra uma função genérica: a assinatura com TypeVars, para
// as chamadas, e o corpo para a checagem abstrata.
func (c *checker) collectGeneric(d *ast.FuncDecl) {
	if d.Extern {
		c.errorf(d.P, "extern fn não pode ser genérica")
	}
	if d.Name == "main" {
		c.errorf(d.P, "main não pode ser genérica")
	}
	g := &Func{
		Name: c.qualify(d.Name), Result: Void, Decl: d,
		CName: "mlc_fn_" + c.mod.prefix + d.Name, File: c.mod.file, Pub: d.Pub, mod: c.mod,
		instances: map[string]*Func{},
	}
	g.typeVars = c.typeVars(d.TypeParams)
	binding := bind(d.TypeParams, g.typeVars)
	c.withScope(c.mod, binding, func() { c.signature(g, d, nil) })
	c.mod.generics[d.Name] = g
	c.abstracts = append(c.abstracts, abstractCheck{g, binding})
}

// collectGenericMethod registra um método de struct genérica. O receptor
// nomeia os parâmetros de tipo da struct: fn (v &Vetor[T]) tamanho() int.
func (c *checker) collectGenericMethod(d *ast.FuncDecl, s *Struct, named *ast.TypeExpr) {
	if len(named.Args) != len(s.Params) {
		c.errorf(named.P, "o receptor precisa nomear os %d parâmetro(s) de tipo de %s, como %s[T]", len(s.Params), s.Name, s.Name)
		return
	}
	seen := map[string]bool{}
	for _, a := range named.Args {
		if a.Mod != ast.TypeNamed || a.Module != "" || len(a.Args) > 0 || seen[a.Name] {
			c.errorf(a.P, "no receptor, os argumentos de tipo precisam ser nomes novos e diferentes, como %s[T]", s.Name)
			return
		}
		seen[a.Name] = true
	}
	for _, f := range s.Decl.Fields {
		if f.Name == d.Name {
			c.errorf(d.P, "%s já tem um campo %s; o método precisa de outro nome", s.Name, d.Name)
			return
		}
	}
	if prev := s.methodDecls[d.Name]; prev != nil {
		c.errorf(d.P, "método %s.%s já declarado na linha %d", s.Name, d.Name, prev.P.Line)
		return
	}
	s.methodDecls[d.Name] = d
	// restrições do receptor: fn (v &Vec[T any]) get() só existe com T any
	var cons []*ast.TypeParam
	for _, a := range named.Args {
		if a.Constraint == "" {
			cons = append(cons, nil)
			continue
		}
		b, iface, ok := splitConstraint(a.Constraint)
		if !ok || (b == "" && c.lookupIface(iface, a.P, false) == nil) {
			c.errorf(a.P, "restrição desconhecida %q", a.Constraint)
		}
		// a restrição do receptor se soma à da struct: em Map[K owned + Hash],
		// fn (m &Map[K any, V]) pede K any + Hash
		sb, siface, _ := splitConstraint(s.Params[len(cons)].Constraint)
		if b == "" {
			b = sb
		}
		if iface == "" {
			iface = siface
		}
		combined := b
		if iface != "" {
			if combined != "" {
				combined += "+"
			}
			combined += iface
		}
		cons = append(cons, &ast.TypeParam{Name: a.Name, Constraint: combined, P: a.P})
	}
	if s.methodConstraints == nil {
		s.methodConstraints = map[string][]*ast.TypeParam{}
	}
	s.methodConstraints[d.Name] = cons
	// checagem abstrata: uma instância de s com TypeVars no lugar dos tipos
	var vars []Type
	for i, a := range named.Args {
		p := s.Params[i]
		if cons[i] != nil {
			p = cons[i] // no corpo, T tem a restrição do receptor
		}
		vars = append(vars, c.newTypeVar(a.Name, p))
	}
	inst, ok := c.instStruct(s, vars, named.P).(*Struct)
	if !ok {
		return
	}
	if m := c.instMethod(inst, d.Name); m != nil {
		binding := map[string]Type{}
		for i, a := range named.Args {
			binding[a.Name] = vars[i]
		}
		c.abstracts = append(c.abstracts, abstractCheck{m, binding})
	}
}

// checkAbstracts checa o corpo das definições genéricas do módulo atual.
func (c *checker) checkAbstracts() {
	for _, a := range c.abstracts {
		if a.fn.mod != c.mod {
			continue
		}
		c.typeParams = a.binding
		c.checkFunc(a.fn)
		c.typeParams = nil
	}
}

// namesType diz se e é o nome de uma struct (local ou mod.Struct), que pode
// receber argumentos de tipo.
func (c *checker) namesType(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return c.scope.lookup(e.Name) == nil && c.mod.structs[e.Name] != nil
	case *ast.SelectorExpr:
		if m := c.userModule(e.X); m != nil {
			return m.structs[e.Sel] != nil
		}
	}
	return false
}

// baseOf tira o ponteiro de ?*T e *T: o tipo que um impl implementa.
func baseOf(t Type) Type {
	if p, ok := t.(*Pointer); ok {
		return p.Elem
	}
	return t
}

func isStructType(t Type) bool {
	_, ok := t.(*Struct)
	return ok
}

func isEnumType(t Type) bool {
	_, ok := t.(*Enum)
	return ok
}
