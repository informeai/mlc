package checker

import (
	"fmt"
	"reflect"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/parser"
	"mlc/internal/token"
)

// Interfaces
//
//	interface Display { fn write(self &Self, w &Writer) }
//	impl Display for Ponto                    // os métodos de Ponto cumprem Display
//	impl Display for int { fn (x &int) write(w &Writer) { ... } }
//	impl[T Display] Display for []T { ... }    // genérico
//	impl auto Display for Ponto               // write gerado (só io.Display)
//	fn show[T Display](x &T) { x.write(...) }
//
// Nesta versão as interfaces são estáticas: só servem de restrição de
// genérico. Cada uso concreto vira uma instância, e a chamada x.write(w)
// dentro dela é uma chamada direta ao método do tipo. Não há tabela de
// métodos, ponteiro gordo nem alocação.
//
// A implementação é sempre explícita: ter os métodos não basta, o impl
// declara a intenção e o compilador confere as assinaturas. Os métodos de
// interface recebem o receptor emprestado (self &Self).

type Interface struct {
	Name string
	File string
	Pub  bool
	Decl *ast.InterfaceDecl
	mod  *module
}

func (i *Interface) String() string { return i.Name }

// method devolve a declaração do método name, ou nil.
func (i *Interface) method(name string) *ast.FuncDecl {
	for _, m := range i.Decl.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// Impl é um impl declarado (ou uma instância concreta de um impl genérico).
type Impl struct {
	Iface   *Interface
	Type    Type // o tipo que implementa; com TypeVars num impl genérico
	Methods map[string]*Func
	Decl    *ast.ImplDecl
	mod     *module

	vars      []Type                   // impl genérico: os parâmetros de tipo
	decls     map[string]*ast.FuncDecl // métodos do bloco, para instanciar
	instances map[string]*Impl         // impl genérico: instâncias por tipo
	params    map[*TypeVar]*ast.TypeParam
}

// ifaceName é o nome da interface como o módulo atual a vê (io.Display).
func (c *checker) ifaceName(i *Interface) string {
	if i.mod == c.mod || i.mod.name == "" {
		return i.Name
	}
	return i.mod.name + "." + i.Name
}

// ---------- coleta ----------

func (c *checker) collectInterfaces(f *ast.File) {
	for _, d := range f.Interfaces {
		switch {
		case predeclared[d.Name] || constraints[d.Name]:
			c.errorf(d.P, "o nome %q é reservado", d.Name)
			continue
		case c.mod.ifaces[d.Name] != nil || c.mod.structs[d.Name] != nil || c.mod.enums[d.Name] != nil:
			c.errorf(d.P, "%s já foi declarado neste arquivo", d.Name)
			continue
		}
		seen := map[string]bool{}
		for _, m := range d.Methods {
			switch {
			case seen[m.Name]:
				c.errorf(m.P, "método %s repetido na interface %s", m.Name, d.Name)
			case m.Recv != nil || len(m.TypeParams) > 0:
				c.errorf(m.P, "um método de interface é escrito fn nome(self &Self, ...), sem receptor nem parâmetros de tipo")
			case m.Result != nil && m.Result.Mod == ast.TypeBorrow:
				c.errorf(m.P, "um método de interface não pode devolver empréstimo nesta versão")
			case len(m.Params) == 0 || !isSelfBorrow(m.Params[0].Type):
				c.errorf(m.P, "o primeiro parâmetro de %s.%s precisa ser self &Self (nesta versão o receptor é sempre emprestado)", d.Name, m.Name)
			}
			seen[m.Name] = true
		}
		c.mod.ifaces[d.Name] = &Interface{Name: d.Name, File: c.mod.file, Pub: d.Pub, Decl: d, mod: c.mod}
	}
}

func isSelfBorrow(t *ast.TypeExpr) bool {
	return t != nil && t.Mod == ast.TypeBorrow && t.Elem.Mod == ast.TypeNamed && t.Elem.Name == "Self" && t.Elem.Module == ""
}

// lookupIface resolve o nome de uma interface (Display ou io.Display) no
// módulo atual.
func (c *checker) lookupIface(name string, pos token.Pos, report bool) *Interface {
	mod, n, qualified := strings.Cut(name, ".")
	if !qualified {
		return c.mod.ifaces[name]
	}
	m := c.mod.uses[mod]
	if m == nil {
		if report {
			c.errorf(pos, "módulo %s não foi importado", mod)
		}
		return nil
	}
	i := m.ifaces[n]
	if i != nil && !i.Pub {
		if report {
			c.errorf(pos, "%s não é pública (declare com pub interface)", name)
		}
		return nil
	}
	return i
}

// splitConstraint separa uma restrição combinada (owned + hash.Hash) na
// parte embutida (owned, ordered...; "" se não houver) e no nome da interface
// ("" se não houver). ok é false com duas embutidas ou duas interfaces.
func splitConstraint(c string) (builtin, iface string, ok bool) {
	for _, part := range strings.Split(c, "+") {
		if constraints[part] {
			if builtin != "" {
				return "", "", false
			}
			builtin = part
			continue
		}
		if iface != "" {
			return "", "", false
		}
		iface = part
	}
	return builtin, iface, true
}

// paramIface resolve (uma vez, no módulo da declaração) a interface da
// restrição de p, ou nil.
func (c *checker) paramIface(p *ast.TypeParam) *Interface {
	if i, ok := c.paramIfaces[p]; ok {
		return i
	}
	var i *Interface
	if _, name, ok := splitConstraint(p.Constraint); ok && name != "" {
		i = c.lookupIface(name, p.P, false)
	}
	c.paramIfaces[p] = i
	return i
}

// newTypeVar cria o TypeVar de um parâmetro de tipo, resolvendo a restrição
// (uma embutida, uma interface ou as duas: owned + hash.Hash) no módulo da
// declaração.
func (c *checker) newTypeVar(name string, p *ast.TypeParam) *TypeVar {
	builtin, iface, ok := splitConstraint(p.Constraint)
	if !ok {
		c.errorf(p.P, "restrição %q: combine no máximo uma restrição embutida e uma interface, como owned + hash.Hash", p.Constraint)
		return &TypeVar{Name: name, Constraint: "any"}
	}
	if builtin == "" {
		builtin = "any"
	}
	tv := &TypeVar{Name: name, Constraint: builtin}
	if iface != "" {
		tv.Iface = c.paramIface(p)
		if tv.Iface == nil {
			c.errorf(p.P, "restrição desconhecida %q (use any, comparable, ordered, number, integer, owned ou uma interface)", iface)
		}
	}
	return tv
}

// satisfiesParam diz se t atende a restrição do parâmetro de tipo p.
func (c *checker) satisfiesParam(t Type, p *ast.TypeParam) bool {
	builtin, iface, ok := splitConstraint(p.Constraint)
	if !ok {
		return false
	}
	if builtin != "" && !satisfies(t, builtin) {
		return false
	}
	if iface == "" {
		return true
	}
	i := c.paramIface(p)
	return i != nil && c.implementsAs(t, i, builtin == "owned")
}

// implements diz se t implementa a interface i.
func (c *checker) implements(t Type, i *Interface) bool { return c.implementsAs(t, i, false) }

// implementsAs é implements; com owned, donos simples também valem (a
// restrição owned + Interface).
func (c *checker) implementsAs(t Type, i *Interface, owned bool) bool {
	if tv, ok := t.(*TypeVar); ok {
		return tv.Iface == i
	}
	if (IsOwner(t) && !owned) || isBorrow(t) || isResource(t) || t == Void || t == NilType || t == Invalid || isUntyped(t) {
		return false
	}
	if c.findImpl(i, t) != nil {
		return true
	}
	// [N]T usa o impl de []T, através de uma visão do array
	a, ok := t.(*Array)
	return ok && c.findImpl(i, c.sliceOf(a.Elem)) != nil
}

// collectImpls registra os impl do arquivo. Vem depois dos métodos das
// structs, que a forma de uma linha usa.
func (c *checker) collectImpls(f *ast.File) {
	for _, d := range f.Impls {
		c.collectImpl(d)
	}
	// impl auto: cada campo precisa implementar a interface. Conferido depois
	// de todos os impl do arquivo, que podem vir em qualquer ordem.
	for _, a := range c.autos {
		bad := false
		for _, f := range a.fields {
			if !c.implementsAs(f.t, a.im.Iface, true) { // o write gerado lê o campo emprestado
				c.errorf(a.im.Decl.P, "impl auto %s for %s: o campo %s (%s) não implementa %s", c.ifaceName(a.im.Iface), a.im.Type, f.name, f.t, c.ifaceName(a.im.Iface))
				bad = true
			}
		}
		if bad {
			for _, d := range a.im.decls {
				// sem erros em cascata no código gerado: o corpo fica vazio
				// (ou só devolve o zero do eq e do cmp)
				d.Body.Stmts = nil
				switch d.Name {
				case "cmp":
					d.Body.Stmts = []ast.Stmt{&ast.ReturnStmt{Value: &ast.IntLit{Value: "0", P: d.P}, P: d.P}}
				case "eq":
					d.Body.Stmts = []ast.Stmt{&ast.ReturnStmt{Value: &ast.Ident{Name: "false", P: d.P}, P: d.P}}
				}
			}
		}
	}
	c.autos = nil
}

// autoImpl guarda um impl auto para a conferência dos campos.
type autoImpl struct {
	im     *Impl
	fields []autoField
}

type autoField struct {
	name string
	t    Type
}

func (c *checker) collectImpl(d *ast.ImplDecl) {
	if d.Iface.Mod != ast.TypeNamed || len(d.Iface.Args) > 0 {
		c.errorf(d.Iface.P, "esperado o nome de uma interface depois de impl")
		return
	}
	name := d.Iface.Name
	if d.Iface.Module != "" {
		name = d.Iface.Module + "." + name
	}
	iface := c.lookupIface(name, d.Iface.P, true)
	if iface == nil {
		c.errorf(d.Iface.P, "%s não é uma interface", name)
		return
	}
	im := &Impl{Iface: iface, Decl: d, mod: c.mod, Methods: map[string]*Func{}}
	binding := map[string]Type{}
	if d.Auto {
		c.autoTypeParams(d, iface)
	}
	if len(d.TypeParams) > 0 {
		im.params = map[*TypeVar]*ast.TypeParam{}
		for _, p := range d.TypeParams {
			tv := c.newTypeVar(p.Name, p)
			im.vars = append(im.vars, tv)
			im.params[tv] = p
			binding[p.Name] = tv
		}
		im.instances = map[string]*Impl{}
	}
	var t Type
	// como o tipo de um &T: os métodos recebem &Self, então structs com donos
	// (que só existem atrás de ponteiro) também implementam
	c.withScope(c.mod, binding, func() { t = c.resolveType(d.Type, ctxBorrowElem) })
	if t == Invalid {
		return
	}
	im.Type = t
	// os métodos recebem &Self, então slices e structs com donos podem
	// implementar; ponteiros e empréstimos, não (o método já empresta)
	if isPointerType(t) || isBorrow(t) {
		c.errorf(d.Type.P, "%s não pode implementar uma interface: implemente para o tipo apontado", t)
		return
	}
	// regra de origem: o impl fica no arquivo do tipo ou no da interface
	if owner := typeModule(t); iface.mod != c.mod && owner != c.mod {
		c.errorf(d.P, "impl %s for %s precisa ficar no arquivo de %s ou no de %s", c.ifaceName(iface), t, t, c.ifaceName(iface))
		return
	}
	if im.vars == nil {
		if prev := c.findImpl(iface, t); prev != nil {
			c.errorf(d.P, "%s já implementa %s (linha %d de %s)", t, c.ifaceName(iface), prev.Decl.P.Line, prev.mod.file)
			return
		}
	}
	methods := d.Methods
	switch {
	case d.Auto:
		methods = c.autoMethods(d, iface, t)
		if methods == nil {
			return
		}
		c.autos = append(c.autos, autoImpl{im: im, fields: autoFields(t)})
	case !d.Block:
		c.implFromStruct(im, t)
		return
	}
	im.decls = map[string]*ast.FuncDecl{}
	for _, md := range methods {
		if md.Recv == nil {
			c.errorf(md.P, "os métodos de um impl têm receptor: fn (x &%s) %s(...)", d.Type.Name, md.Name)
			continue
		}
		if iface.method(md.Name) == nil {
			c.errorf(md.P, "%s não é método da interface %s", md.Name, c.ifaceName(iface))
			continue
		}
		if im.decls[md.Name] != nil {
			c.errorf(md.P, "método %s repetido no impl", md.Name)
			continue
		}
		var rt Type
		c.withScope(c.mod, binding, func() { rt = c.resolveType(md.Recv.Type, ctxParam) })
		if want := c.borrowOf(t); rt != want {
			c.errorf(md.Recv.P, "o receptor de %s precisa ser &%s", md.Name, typeString(d.Type))
			continue
		}
		im.decls[md.Name] = md
		if s, ok := t.(*Struct); ok && (s.Methods[md.Name] != nil || s.methodDecls[md.Name] != nil) {
			c.errorf(md.P, "%s já tem um método %s; o impl não pode declarar outro", s.Name, md.Name)
		}
	}
	for _, m := range iface.Decl.Methods {
		if im.decls[m.Name] == nil {
			c.errorf(d.P, "%s não implementa %s: falta o método %s", t, c.ifaceName(iface), m.Name)
		}
	}
	if im.vars != nil {
		// checagem abstrata do corpo, com os TypeVars do impl
		inst := c.instImplMethods(im, im.vars)
		for _, m := range inst.Methods {
			c.abstracts = append(c.abstracts, abstractCheck{m, binding})
		}
		im.instances[typeKeys(im.vars)] = inst
	} else {
		c.makeImplMethods(im, im, binding)
	}
	c.impls = append(c.impls, im)
}

func typeKeys(ts []Type) string {
	_, key, _ := typeList(ts)
	return key
}

// autoFields lista os campos (de uma struct, ou de todas as variantes de um
// enum) que o write gerado escreve.
func autoFields(t Type) []autoField {
	var out []autoField
	switch t := t.(type) {
	case *Struct:
		for _, f := range t.Fields {
			out = append(out, autoField{f.Name, f.Type})
		}
	case *Enum:
		for _, v := range t.Variants {
			for _, f := range v.Fields {
				out = append(out, autoField{v.Name + "." + f.Name, f.Type})
			}
		}
	}
	return out
}

// typeModule é o módulo que declara t (struct ou enum), ou nil para os tipos
// embutidos, slices e arrays.
func typeModule(t Type) *module {
	switch t := t.(type) {
	case *Struct:
		if t.Origin != nil {
			return t.Origin.mod
		}
		return t.mod
	case *Enum:
		if t.Origin != nil {
			return t.Origin.mod
		}
		return t.mod
	}
	return nil
}

// implFromStruct liga um impl de uma linha aos métodos já declarados da
// struct, conferindo as assinaturas.
func (c *checker) implFromStruct(im *Impl, t Type) {
	s, ok := t.(*Struct)
	if !ok || s.Params != nil || s.Origin != nil {
		c.errorf(im.Decl.P, "a forma impl %s for %s, sem bloco, só vale para structs não genéricas com os métodos declarados; para outros tipos, escreva os métodos num bloco { }", c.ifaceName(im.Iface), t)
		return
	}
	ok = true
	for _, md := range im.Iface.Decl.Methods {
		m := s.Methods[md.Name]
		if m == nil {
			c.errorf(im.Decl.P, "%s não implementa %s: falta o método %s", s.Name, c.ifaceName(im.Iface), c.ifaceSigString(im.Iface, md, s))
			ok = false
			continue
		}
		if m.recv != recvBorrow {
			c.errorf(m.Decl.Recv.P, "%s.%s precisa receber o receptor emprestado (&%s) para cumprir %s", s.Name, md.Name, s.Name, c.ifaceName(im.Iface))
			ok = false
			continue
		}
		if !c.sameSig(im.Iface, md, s, m) {
			c.errorf(im.Decl.P, "%s.%s tem a assinatura errada para %s: esperado %s", s.Name, md.Name, c.ifaceName(im.Iface), c.ifaceSigString(im.Iface, md, s))
			ok = false
			continue
		}
		im.Methods[md.Name] = m
		m.Pub = true // os métodos de uma interface valem onde a interface vale
	}
	if ok {
		c.impls = append(c.impls, im)
	}
}

// ifaceSig resolve os parâmetros (sem o self) e o retorno do método md da
// interface, com Self = self.
func (c *checker) ifaceSig(i *Interface, md *ast.FuncDecl, self Type) (params []Type, result Type) {
	result = Void
	c.withScope(i.mod, map[string]Type{"Self": self}, func() {
		for _, p := range md.Params[1:] {
			params = append(params, c.resolveType(p.Type, ctxParam))
		}
		if md.Result != nil {
			result = c.resolveType(md.Result, ctxResult)
		}
	})
	return params, result
}

func (c *checker) sameSig(i *Interface, md *ast.FuncDecl, self Type, m *Func) bool {
	params, result := c.ifaceSig(i, md, self)
	if len(m.Params) != len(params)+1 || m.Result != result {
		return false
	}
	for k, p := range params {
		if m.Params[k+1].Type != p {
			return false
		}
	}
	return true
}

func (c *checker) ifaceSigString(i *Interface, md *ast.FuncDecl, self Type) string {
	params, result := c.ifaceSig(i, md, self)
	var ps []string
	for k, p := range params {
		ps = append(ps, md.Params[k+1].Name+" "+p.String())
	}
	s := fmt.Sprintf("fn (self &%s) %s(%s)", self, md.Name, strings.Join(ps, ", "))
	if result != Void {
		s += " " + result.String()
	}
	return s
}

// makeImplMethods cria as funções dos métodos do bloco de origin para a
// instância inst (o próprio impl, se não for genérico), com os tipos de
// binding. Instâncias concretas têm o corpo checado e gerado.
func (c *checker) makeImplMethods(origin, inst *Impl, binding map[string]Type) {
	concrete := !abstract(inst.Type)
	_, _, mangled := typeList([]Type{inst.Type})
	for name, d := range origin.decls {
		decl := d
		if concrete && origin.vars != nil {
			decl = ast.Clone(d)
		}
		m := &Func{
			Name: inst.Type.String() + "." + name, Result: Void, Decl: decl,
			CName: "mlc_fn_" + origin.mod.prefix + "impl_" + origin.Iface.Name + "__" + mangled + "__" + name,
			File:  origin.mod.file, Pub: true, mod: origin.mod, Method: true, recv: recvBorrow,
		}
		c.withScope(origin.mod, binding, func() {
			recv := &Local{Name: d.Recv.Name, CName: "l_" + d.Recv.Name, Type: c.borrowOf(inst.Type), Pos: d.Recv.P, Param: true}
			c.signature(m, decl, recv)
		})
		if md := origin.Iface.method(name); md != nil && !c.sameSig(origin.Iface, md, inst.Type, m) {
			c.errorf(d.P, "%s tem a assinatura errada para %s: esperado %s", name, c.ifaceName(origin.Iface), c.ifaceSigString(origin.Iface, md, inst.Type))
		}
		inst.Methods[name] = m
		switch {
		case !concrete:
		case origin.vars != nil:
			c.queue(m, binding)
		default:
			c.info.Funcs = append(c.info.Funcs, m)
		}
	}
}

// instImplMethods cria a instância do impl genérico im para os argumentos de
// tipo args.
func (c *checker) instImplMethods(im *Impl, args []Type) *Impl {
	binding := map[string]Type{}
	for k, p := range im.Decl.TypeParams {
		binding[p.Name] = args[k]
	}
	inst := &Impl{Iface: im.Iface, Decl: im.Decl, mod: im.mod, Methods: map[string]*Func{}}
	c.withScope(im.mod, binding, func() { inst.Type = c.resolveType(im.Decl.Type, ctxVar) })
	c.makeImplMethods(im, inst, binding)
	return inst
}

// ---------- busca ----------

// findImpl devolve o impl de i para t (instanciando um impl genérico, se
// preciso), ou nil.
func (c *checker) findImpl(i *Interface, t Type) *Impl {
	for _, im := range c.impls {
		if im.Iface != i {
			continue
		}
		if im.vars == nil {
			if typeKey(im.Type) == typeKey(t) {
				return im
			}
			continue
		}
		bound := map[*TypeVar]Type{}
		if !matchType(im.Type, t, bound) {
			continue
		}
		args := make([]Type, len(im.vars))
		ok := true
		for k, v := range im.vars {
			args[k] = bound[v.(*TypeVar)]
			if args[k] == nil || !c.satisfiesParam(args[k], im.params[v.(*TypeVar)]) {
				ok = false
			}
		}
		if !ok {
			continue
		}
		key := typeKeys(args)
		if inst, ok := im.instances[key]; ok {
			return inst
		}
		inst := c.instImplMethods(im, args)
		im.instances[key] = inst
		return inst
	}
	return nil
}

// matchType confere se t tem a forma do padrão p (com os TypeVars de um impl
// genérico), ligando cada TypeVar ao tipo correspondente.
func matchType(p, t Type, bound map[*TypeVar]Type) bool {
	switch pt := p.(type) {
	case *TypeVar:
		if prev, ok := bound[pt]; ok {
			return typeKey(prev) == typeKey(t)
		}
		bound[pt] = t
		return true
	case *Slice:
		tt, ok := t.(*Slice)
		return ok && matchType(pt.Elem, tt.Elem, bound)
	case *Array:
		tt, ok := t.(*Array)
		return ok && tt.Len == pt.Len && matchType(pt.Elem, tt.Elem, bound)
	case *Struct:
		tt, ok := t.(*Struct)
		if !ok || pt.Origin == nil {
			return ok && tt == pt
		}
		if tt.Origin != pt.Origin {
			return false
		}
		for k := range pt.Args {
			if !matchType(pt.Args[k], tt.Args[k], bound) {
				return false
			}
		}
		return true
	}
	return typeKey(p) == typeKey(t)
}

// implMethod procura o método name de algum impl de t. Para um TypeVar,
// devolve o método da interface da restrição (checagem abstrata).
func (c *checker) implMethod(t Type, name string) *Func {
	if tv, ok := t.(*TypeVar); ok {
		if tv.Iface == nil || tv.Iface.method(name) == nil {
			return nil
		}
		key := fmt.Sprintf("%p.%s", tv, name)
		if m, ok := c.tvMethods[key]; ok {
			return m
		}
		md := tv.Iface.method(name)
		params, result := c.ifaceSig(tv.Iface, md, tv)
		m := &Func{Name: tv.Name + "." + name, Result: result, Method: true, recv: recvBorrow, mod: c.mod, Pub: true,
			Decl: md, File: tv.Iface.File} // Decl: a assinatura na interface (para o editor)
		m.Params = append(m.Params, &Local{Name: md.Params[0].Name, Type: c.borrowOf(tv), Param: true})
		for k, p := range params {
			m.Params = append(m.Params, &Local{Name: md.Params[k+1].Name, Type: p, Param: true})
		}
		c.tvMethods[key] = m
		return m
	}
	seen := map[*Interface]bool{}
	for _, im := range c.impls {
		if seen[im.Iface] || im.Iface.method(name) == nil {
			continue
		}
		seen[im.Iface] = true
		if found := c.findImpl(im.Iface, t); found != nil {
			return found.Methods[name]
		}
	}
	return nil
}

// implMethodCall checa x.m(args) quando m vem de um impl (ou da restrição de
// um parâmetro de tipo). ok é false se não houver tal método.
func (c *checker) implMethodCall(e *ast.CallExpr, sel *ast.SelectorExpr, t Type) (Type, bool) {
	base := t
	switch u := t.(type) {
	case *Pointer:
		base = u.Elem
	case *Borrow:
		base = u.Elem
	}
	m := c.implMethod(base, sel.Sel)
	if m == nil {
		if a, ok := base.(*Array); ok {
			m = c.implMethod(c.sliceOf(a.Elem), sel.Sel) // [N]T usa o impl de []T
		}
	}
	if m == nil {
		if tv, ok := base.(*TypeVar); ok {
			if tv.Iface != nil {
				c.errorf(sel.SelPos, "%s não tem método %s: a restrição %s não o declara", tv, sel.Sel, c.ifaceName(tv.Iface))
			} else {
				c.errorf(sel.SelPos, "%s não tem método %s: a restrição %s não tem métodos (use uma interface)", tv, sel.Sel, tv.Constraint)
			}
			c.checkArgs(e)
			return Invalid, true
		}
		return nil, false
	}
	want := m.Params[0].Type
	var recv ast.Expr
	switch {
	case isBorrow(t) && isArray(t.(*Borrow).Elem) && t != want:
		// &[N]T chamando o método de []T: a visão do array emprestado
		deref := &ast.UnaryExpr{Op: token.MUL, X: sel.X, P: sel.X.Pos()}
		c.info.Types[deref] = t.(*Borrow).Elem
		amp := &ast.UnaryExpr{Op: token.AMP, X: deref, P: sel.X.Pos()}
		c.info.Types[amp] = want
		recv = amp
	case isBorrow(t):
		recv = sel.X // empréstimo recebido, repassado
	case !isPlace(sel.X) && !isPointerType(t):
		c.errorf(sel.X.Pos(), "o método %s empresta o receptor, e um valor temporário não pode ser emprestado: guarde em uma variável", m.Name)
		c.checkArgs(e)
		return Invalid, true
	default:
		amp := &ast.UnaryExpr{Op: token.AMP, X: sel.X, P: sel.X.Pos()}
		c.info.Types[amp] = want
		recv = amp
	}
	c.info.Recv[e] = recv
	c.callArgs(e, m, m.Params[1:])
	c.info.Calls[e] = Callee{Func: m}
	return m.Result, true
}

// ---------- impl auto ----------

// autoMethods gera, em MLC, o método write de um impl auto io.Display: o
// mesmo formato que o io.print sempre usou ({1 2} para structs, Circulo(2)
// para enums). O código gerado é checado como se tivesse sido escrito no
// impl, e os erros apontam para a linha do impl.
func (c *checker) autoMethods(d *ast.ImplDecl, iface *Interface, t Type) []*ast.FuncDecl {
	isCmp := iface.mod.std && iface.mod.path == "std/cmp" && (iface.Name == "Eq" || iface.Name == "Ord")
	isHash := iface.mod.std && iface.mod.path == "std/hash" && iface.Name == "Hash"
	if !isCmp && !isHash && (iface.Name != "Display" || iface.mod.path != "std/io") {
		c.errorf(d.P, "impl auto só existe para io.Display, cmp.Eq, cmp.Ord e hash.Hash")
		return nil
	}
	if d.Block {
		c.errorf(d.P, "impl auto não tem bloco: o método é gerado")
		return nil
	}
	if isCmp {
		return c.autoCmp(d, iface.Name == "Ord", t, "")
	}
	if isHash {
		mix := "mix64"
		if c.mod != iface.mod {
			mix = iface.mod.name + ".mix64"
		}
		hashSrc, ok := autoHash(d, t, mix)
		if !ok {
			c.errorf(d.P, "impl auto só vale para structs e enums; para %s, escreva os métodos hash e eq num bloco", t)
			return nil
		}
		return c.autoCmp(d, false, t, hashSrc) // o eq, junto com o hash
	}
	writer := "Writer"
	if c.mod != iface.mod {
		writer = iface.mod.name + ".Writer"
	}
	var src strings.Builder
	switch t := t.(type) {
	case *Struct:
		fmt.Fprintf(&src, "fn (self &%s) write(w &%s) {\n    w.put_str(\"{\")\n", typeString(d.Type), writer)
		for k, f := range t.Fields {
			if k > 0 {
				src.WriteString("    w.put_str(\" \")\n")
			}
			fmt.Fprintf(&src, "    self.%s.write(w)\n", f.Name)
		}
		src.WriteString("    w.put_str(\"}\")\n}\n")
	case *Enum:
		fmt.Fprintf(&src, "fn (self &%s) write(w &%s) {\n    match *self {\n", typeString(d.Type), writer)
		for _, v := range t.Variants {
			if len(v.Fields) == 0 {
				fmt.Fprintf(&src, "        %s => w.put_str(%q)\n", v.Name, v.Name)
				continue
			}
			var binds []string
			for k := range v.Fields {
				binds = append(binds, fmt.Sprintf("mlc_f%d", k))
			}
			fmt.Fprintf(&src, "        %s(%s) => {\n            w.put_str(%q)\n", v.Name, strings.Join(binds, ", "), v.Name+"(")
			for k, b := range binds {
				if k > 0 {
					src.WriteString("            w.put_str(\", \")\n")
				}
				fmt.Fprintf(&src, "            %s.write(w)\n", b)
			}
			src.WriteString("            w.put_str(\")\")\n        }\n")
		}
		src.WriteString("    }\n}\n")
	default:
		c.errorf(d.P, "impl auto só vale para structs e enums; para %s, escreva o método write num bloco", t)
		return nil
	}
	f, errs := parser.ParseFile(c.mod.file, []byte(src.String()))
	if len(errs) > 0 || len(f.Funcs) != 1 {
		c.errorf(d.P, "erro interno ao gerar o impl auto: %v", errs)
		return nil
	}
	setPositions(reflect.ValueOf(f.Funcs[0]), d.P)
	return f.Funcs
}

// autoCmp gera o eq (cmp.Eq) ou o cmp (cmp.Ord) de um impl auto. Numa
// struct, campo a campo, na ordem da declaração. Num enum, primeiro a
// variante (na ordem da declaração), depois os campos dela.
//
// extra é o código de outro método gerado junto (o hash de hash.Hash).
func (c *checker) autoCmp(d *ast.ImplDecl, ord bool, t Type, extra string) []*ast.FuncDecl {
	name, result := "eq", "bool"
	if ord {
		name, result = "cmp", "int"
	}
	var src strings.Builder
	src.WriteString(extra)
	fmt.Fprintf(&src, "fn (self &%s) %s(other &%s) %s {\n", typeString(d.Type), name, typeString(d.Type), result)
	// compare escreve a comparação de dois valores (a e b já emprestados ou
	// lugares), com retorno antecipado quando decide
	compare := func(indent, a, b string) {
		if ord {
			fmt.Fprintf(&src, "%smlc_c := %s.cmp(%s)\n%sif mlc_c != 0 {\n%s    return mlc_c\n%s}\n", indent, a, b, indent, indent, indent)
		} else {
			fmt.Fprintf(&src, "%sif !%s.eq(%s) {\n%s    return false\n%s}\n", indent, a, b, indent, indent)
		}
	}
	same := "true"
	if ord {
		same = "0"
	}
	switch t := t.(type) {
	case *Struct:
		for _, f := range t.Fields {
			// cada comparação num bloco, para o mlc_c de cada campo
			src.WriteString("    {\n")
			compare("        ", "self."+f.Name, "&other."+f.Name)
			src.WriteString("    }\n")
		}
		fmt.Fprintf(&src, "    return %s\n", same)
	case *Enum:
		src.WriteString("    match *self {\n")
		for _, v := range t.Variants {
			var as, bs []string
			for k := range v.Fields {
				as = append(as, fmt.Sprintf("mlc_a%d", k))
				bs = append(bs, fmt.Sprintf("mlc_b%d", k))
			}
			pat := func(names []string) string {
				if len(names) == 0 {
					return v.Name
				}
				return v.Name + "(" + strings.Join(names, ", ") + ")"
			}
			fmt.Fprintf(&src, "        %s => {\n            match *other {\n                %s => {\n", pat(as), pat(bs))
			for k, f := range v.Fields {
				b := "&" + bs[k]
				if IsOwner(f.Type) {
					b = bs[k] // o campo dono já vem emprestado no match que só lê
				}
				src.WriteString("                    {\n")
				compare("                        ", as[k], b)
				src.WriteString("                    }\n")
			}
			fmt.Fprintf(&src, "                    return %s\n                }\n", same)
			if len(t.Variants) > 1 {
				src.WriteString("                _ => {}\n")
			}
			src.WriteString("            }\n        }\n")
		}
		src.WriteString("    }\n")
		if !ord {
			src.WriteString("    return false // variantes diferentes\n")
		} else {
			// variantes diferentes: a ordem da declaração decide
			rank := func(v string) {
				fmt.Fprintf(&src, "    match *%s {\n", v)
				for k, vr := range t.Variants {
					p := vr.Name
					if len(vr.Fields) > 0 {
						p += "(" + strings.TrimSuffix(strings.Repeat("_, ", len(vr.Fields)), ", ") + ")"
					}
					fmt.Fprintf(&src, "        %s => mlc_%s = %d\n", p, v, k)
				}
				src.WriteString("    }\n")
			}
			src.WriteString("    mlc_self := 0\n")
			rank("self")
			src.WriteString("    mlc_other := 0\n")
			rank("other")
			src.WriteString("    return mlc_self - mlc_other\n")
		}
	default:
		c.errorf(d.P, "impl auto só vale para structs e enums; para %s, escreva o método %s num bloco", t, name)
		return nil
	}
	src.WriteString("}\n")
	f, errs := parser.ParseFile(c.mod.file, []byte(src.String()))
	if len(errs) > 0 || len(f.Funcs) == 0 {
		c.errorf(d.P, "erro interno ao gerar o impl auto: %v\n%s", errs, src.String())
		return nil
	}
	for _, fn := range f.Funcs {
		setPositions(reflect.ValueOf(fn), d.P)
	}
	return f.Funcs
}

// autoHash gera o hash de um impl auto hash.Hash: combina o hash de cada
// campo (numa struct) ou a variante e os campos dela (num enum), como o hash
// dos slices, e termina com mix64.
func autoHash(d *ast.ImplDecl, t Type, mix string) (string, bool) {
	var src strings.Builder
	fmt.Fprintf(&src, "fn (self &%s) hash() uint64 {\n    var mlc_h uint64 = 0xCBF2_9CE4_8422_2325\n", typeString(d.Type))
	combine := func(indent, v string) {
		fmt.Fprintf(&src, "%smlc_h = (mlc_h ^ %s.hash()) *%% 0x0000_0100_0000_01B3\n", indent, v)
	}
	switch t := t.(type) {
	case *Struct:
		for _, f := range t.Fields {
			combine("    ", "self."+f.Name)
		}
	case *Enum:
		src.WriteString("    match *self {\n")
		for k, v := range t.Variants {
			var binds []string
			for i := range v.Fields {
				binds = append(binds, fmt.Sprintf("mlc_a%d", i))
			}
			p := v.Name
			if len(binds) > 0 {
				p += "(" + strings.Join(binds, ", ") + ")"
			}
			fmt.Fprintf(&src, "        %s => {\n            mlc_h = (mlc_h ^ %d) *%% 0x0000_0100_0000_01B3\n", p, k)
			for _, b := range binds {
				combine("            ", b)
			}
			src.WriteString("        }\n")
		}
		src.WriteString("    }\n")
	default:
		return "", false
	}
	fmt.Fprintf(&src, "    return %s(mlc_h)\n}\n\n", mix)
	return src.String(), true
}

// setPositions põe todas as posições de um nó gerado em p.
func setPositions(v reflect.Value, p token.Pos) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			setPositions(v.Elem(), p)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(p) {
			v.Set(reflect.ValueOf(p))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			setPositions(v.Field(i), p)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			setPositions(v.Index(i), p)
		}
	}
}

// autoTypeParams completa um impl auto de um tipo genérico escrito sem os
// argumentos (impl auto io.Display for Par): vira impl[A io.Display, B
// io.Display] io.Display for Par[A, B], já que cada campo precisa saber se
// escrever.
func (c *checker) autoTypeParams(d *ast.ImplDecl, iface *Interface) {
	t := d.Type
	if t.Mod != ast.TypeNamed || len(t.Args) > 0 || len(d.TypeParams) > 0 || t.Module != "" {
		return
	}
	var params []*ast.TypeParam
	if s := c.mod.structs[t.Name]; s != nil {
		params = s.Params
	} else if en := c.mod.enums[t.Name]; en != nil {
		params = en.Params
	}
	for _, p := range params {
		d.TypeParams = append(d.TypeParams, &ast.TypeParam{Name: p.Name, Constraint: c.ifaceName(iface), P: d.P})
		t.Args = append(t.Args, &ast.TypeExpr{Name: p.Name, P: d.P})
	}
}

// typeString escreve um tipo da AST como no código.
func typeString(t *ast.TypeExpr) string {
	if t == nil {
		return ""
	}
	switch t.Mod {
	case ast.TypeSlice:
		return "[]" + typeString(t.Elem)
	case ast.TypePointer:
		return "*" + typeString(t.Elem)
	case ast.TypeNullPtr:
		return "?*" + typeString(t.Elem)
	case ast.TypeOptBorrow:
		return "?&" + typeString(t.Elem)
	case ast.TypeBorrow:
		return "&" + typeString(t.Elem)
	case ast.TypeArray:
		if lit, ok := t.Len.(*ast.IntLit); ok {
			return "[" + lit.Value + "]" + typeString(t.Elem)
		}
		return "[N]" + typeString(t.Elem)
	}
	s := t.Name
	if t.Module != "" {
		s = t.Module + "." + s
	}
	if len(t.Args) > 0 {
		var args []string
		for _, a := range t.Args {
			args = append(args, typeString(a))
		}
		s += "[" + strings.Join(args, ", ") + "]"
	}
	return s
}
