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
//	impl Display for Point                    // Point's methods satisfy Display
//	impl Display for int { fn (x &int) write(w &Writer) { ... } }
//	impl[T Display] Display for []T { ... }    // generic
//	impl auto Display for Point               // write generated
//	fn show[T Display](x &T) { x.write(...) }
//
// In this version interfaces are static: they only serve as generic
// constraints. Each concrete use becomes an instance, and the call x.write(w)
// inside it is a direct call to the type's method. There's no method
// table, fat pointer or allocation.
//
// The implementation is always explicit: having the methods isn't enough, the impl
// declares the intent and the compiler checks the signatures. Interface
// methods take the receiver borrowed (self &Self).

type Interface struct {
	Name string
	File string
	Pub  bool
	Decl *ast.InterfaceDecl
	mod  *module
}

func (i *Interface) String() string { return i.Name }

// method returns the declaration of the method name, or nil.
func (i *Interface) method(name string) *ast.FuncDecl {
	for _, m := range i.Decl.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// Impl is a declared impl (or a concrete instance of a generic impl).
type Impl struct {
	Iface   *Interface
	Type    Type // the type that implements; with TypeVars in a generic impl
	Methods map[string]*Func
	Decl    *ast.ImplDecl
	mod     *module

	vars      []Type                   // generic impl: the type parameters
	decls     map[string]*ast.FuncDecl // the block's methods, to instantiate
	instances map[string]*Impl         // generic impl: instances per type
	params    map[*TypeVar]*ast.TypeParam
}

// ifaceName is the interface's name as the current module sees it (io.Display).
func (c *checker) ifaceName(i *Interface) string {
	if i.mod == c.mod || i.mod.name == "" {
		return i.Name
	}
	return i.mod.name + "." + i.Name
}

// ---------- collection ----------

func (c *checker) collectInterfaces(f *ast.File) {
	for _, d := range f.Interfaces {
		switch {
		case predeclared[d.Name] || constraints[d.Name]:
			c.errorf(d.P, "the name %q is reserved", d.Name)
			continue
		case c.mod.ifaces[d.Name] != nil || c.mod.structs[d.Name] != nil || c.mod.enums[d.Name] != nil:
			c.errorf(d.P, "%s was already declared in this file", d.Name)
			continue
		}
		seen := map[string]bool{}
		var valid []*ast.FuncDecl
		for _, m := range d.Methods {
			switch {
			case seen[m.Name]:
				c.errorf(m.P, "repeated method %s in the interface %s", m.Name, d.Name)
			case m.Recv != nil || len(m.TypeParams) > 0:
				c.errorf(m.P, "an interface method is written fn name(self &Self, ...), without a receiver or type parameters")
			case m.Result != nil && m.Result.Mod == ast.TypeBorrow:
				c.errorf(m.P, "an interface method can't return a borrow in this version")
			case len(m.Params) == 0 || !isSelfBorrow(m.Params[0].Type):
				c.errorf(m.P, "the first parameter of %s.%s must be self &Self (in this version the receiver is always borrowed)", d.Name, m.Name)
			default:
				valid = append(valid, m)
			}
			seen[m.Name] = true
		}
		// only the well-written methods go in: the rest of the checker counts on
		// self &Self in the first parameter
		decl := *d
		decl.Methods = valid
		c.mod.ifaces[d.Name] = &Interface{Name: d.Name, File: c.mod.file, Pub: d.Pub, Decl: &decl, mod: c.mod}
	}
}

func isSelfBorrow(t *ast.TypeExpr) bool {
	return t != nil && t.Mod == ast.TypeBorrow && t.Elem.Mod == ast.TypeNamed && t.Elem.Name == "Self" && t.Elem.Module == ""
}

// lookupIface resolves the name of an interface (Display or io.Display) in the
// current module.
func (c *checker) lookupIface(name string, pos token.Pos, report bool) *Interface {
	mod, n, qualified := strings.Cut(name, ".")
	if !qualified {
		return c.mod.ifaces[name]
	}
	m := c.mod.uses[mod]
	if m == nil {
		if report {
			c.errorf(pos, "module %s wasn't imported", mod)
		}
		return nil
	}
	i := m.ifaces[n]
	if i != nil && !i.Pub {
		if report {
			c.errorf(pos, "%s isn't public (declare it with pub interface)", name)
		}
		return nil
	}
	return i
}

// splitConstraint splits a combined constraint (owned + hash.Hash) into the
// built-in part (owned, ordered...; "" if none) and the interface's name
// ("" if none). ok is false with two built-ins or two interfaces.
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

// paramIface resolves (once, in the declaration's module) the interface of
// p's constraint, or nil.
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

// newTypeVar creates the TypeVar of a type parameter, resolving the constraint
// (a built-in one, an interface or both: owned + hash.Hash) in the declaration's
// module.
func (c *checker) newTypeVar(name string, p *ast.TypeParam) *TypeVar {
	builtin, iface, ok := splitConstraint(p.Constraint)
	if !ok {
		c.errorf(p.P, "constraint %q: combine at most one built-in constraint and one interface, like owned + hash.Hash", p.Constraint)
		return &TypeVar{Name: name, Constraint: "any"}
	}
	if builtin == "" {
		builtin = "any"
	}
	tv := &TypeVar{Name: name, Constraint: builtin}
	if iface != "" {
		tv.Iface = c.paramIface(p)
		if tv.Iface == nil {
			c.errorf(p.P, "unknown constraint %q (use any, comparable, ordered, number, integer, owned or an interface)", iface)
		}
	}
	return tv
}

// satisfiesParam says whether t satisfies the constraint of the type parameter p.
func (c *checker) satisfiesParam(t Type, p *ast.TypeParam) bool {
	builtin, iface, ok := splitConstraint(p.Constraint)
	if !ok {
		return false
	}
	if builtin != "" && !satisfies(t, builtin) {
		return false
	}
	if isOpaque(t) && builtin != "ref" {
		return false // only exists behind a pointer: the parameter must appear only borrowed
	}
	if iface == "" {
		return true
	}
	i := c.paramIface(p)
	return i != nil && c.implementsAs(t, i, builtin == "owned")
}

// isOpaque says whether t is an opaque C type (extern "C" type).
func isOpaque(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.Extern
}

// implements says whether t implements the interface i.
func (c *checker) implements(t Type, i *Interface) bool { return c.implementsAs(t, i, false) }

// implementsAs is implements; with owned, simple owners also count (the
// owned + Interface constraint).
func (c *checker) implementsAs(t Type, i *Interface, owned bool) bool {
	if tv, ok := t.(*TypeVar); ok {
		return tv.Iface == i
	}
	if (IsOwner(t) && !owned) || isBorrow(t) || isResource(t) || t == Void || t == NilType || t == Invalid || isUntyped(t) {
		return false
	}
	if c.findImpl(i, t) != nil || c.declared[declaredImpl{i, typeKey(t)}] {
		return true
	}
	// [N]T uses the impl of []T, through a view of the array
	a, ok := t.(*Array)
	return ok && c.findImpl(i, c.sliceOf(a.Elem)) != nil
}

// declaredImpl is an announced impl: the interface and the type's key.
type declaredImpl struct {
	iface *Interface
	key   string
}

// declareImpls announces the impls of the file's simple types (non-generic struct or enum,
// of the file itself) before the functions' signatures, which
// can use the type as the argument of a generic with an interface
// constraint (fn f(r &bufio.Reader[MySource])). The methods are checked
// later, in collectImpls, which gives the errors.
func (c *checker) declareImpls(f *ast.File) {
	for _, d := range f.Impls {
		t := d.Type
		if d.Iface.Mod != ast.TypeNamed || len(d.Iface.Args) > 0 || t == nil || t.Mod != ast.TypeNamed || t.Module != "" || len(t.Args) > 0 || len(d.TypeParams) > 0 {
			continue
		}
		name := d.Iface.Name
		if d.Iface.Module != "" {
			name = d.Iface.Module + "." + name
		}
		iface := c.lookupIface(name, d.Iface.P, false)
		if iface == nil {
			continue
		}
		var typ Type
		if s := c.mod.structs[t.Name]; s != nil && s.Params == nil {
			typ = s
		} else if e := c.mod.enums[t.Name]; e != nil && e.Params == nil {
			typ = e
		} else {
			continue
		}
		if c.declared == nil {
			c.declared = map[declaredImpl]bool{}
		}
		c.declared[declaredImpl{iface, typeKey(typ)}] = true
	}
}

// collectImpls registers the file's impls. It comes after the structs'
// methods, which the one-line form uses.
func (c *checker) collectImpls(f *ast.File) {
	for _, d := range f.Impls {
		c.collectImpl(d)
	}
	// impl auto: each field must implement the interface. Checked after
	// all the file's impls, which can come in any order.
	for _, a := range c.autos {
		bad := false
		for _, f := range a.fields {
			if !c.implementsAs(f.t, a.im.Iface, true) { // the generated write reads the field borrowed
				c.errorf(a.im.Decl.P, "impl auto %s for %s: the field %s (%s) doesn't implement %s", c.ifaceName(a.im.Iface), a.im.Type, f.name, f.t, c.ifaceName(a.im.Iface))
				bad = true
			}
		}
		if bad {
			for _, d := range a.im.decls {
				// no cascading errors in the generated code: the body stays empty
				// (or only returns eq's and cmp's zero)
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

// autoImpl keeps an impl auto for checking the fields.
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
		c.errorf(d.Iface.P, "expected the name of an interface after impl")
		return
	}
	name := d.Iface.Name
	if d.Iface.Module != "" {
		name = d.Iface.Module + "." + name
	}
	iface := c.lookupIface(name, d.Iface.P, true)
	if iface == nil {
		c.errorf(d.Iface.P, "%s isn't an interface", name)
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
	// like the type of a &T: the methods take &Self, so structs with owners
	// (which only exist behind a pointer) also implement
	c.withScope(c.mod, binding, func() { t = c.resolveType(d.Type, ctxBorrowElem) })
	if t == Invalid {
		return
	}
	im.Type = t
	// the methods take &Self, so slices and structs with owners can
	// implement; pointers and borrows can't (the method already borrows)
	if isPointerType(t) || isBorrow(t) {
		c.errorf(d.Type.P, "%s can't implement an interface: implement it for the pointed-to type", t)
		return
	}
	// origin rule: the impl lives in the type's file or in the interface's
	if owner := typeModule(t); iface.mod != c.mod && owner != c.mod {
		c.errorf(d.P, "impl %s for %s must be in the file of %s or of %s", c.ifaceName(iface), t, t, c.ifaceName(iface))
		return
	}
	if im.vars == nil {
		if prev := c.findImpl(iface, t); prev != nil {
			c.errorf(d.P, "%s already implements %s (line %d of %s)", t, c.ifaceName(iface), prev.Decl.P.Line, prev.mod.file)
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
			c.errorf(md.P, "the methods of an impl have a receiver: fn (x &%s) %s(...)", d.Type.Name, md.Name)
			continue
		}
		if iface.method(md.Name) == nil {
			c.errorf(md.P, "%s isn't a method of the interface %s", md.Name, c.ifaceName(iface))
			continue
		}
		if im.decls[md.Name] != nil {
			c.errorf(md.P, "repeated method %s in the impl", md.Name)
			continue
		}
		var rt Type
		c.withScope(c.mod, binding, func() { rt = c.resolveType(md.Recv.Type, ctxParam) })
		if want := c.borrowOf(t); rt != want {
			c.errorf(md.Recv.P, "the receiver of %s must be &%s", md.Name, typeString(d.Type))
			continue
		}
		im.decls[md.Name] = md
		if s, ok := t.(*Struct); ok && (s.Methods[md.Name] != nil || s.methodDecls[md.Name] != nil) {
			c.errorf(md.P, "%s already has a method %s; the impl can't declare another", s.Name, md.Name)
		}
	}
	for _, m := range iface.Decl.Methods {
		if im.decls[m.Name] == nil {
			c.errorf(d.P, "%s doesn't implement %s: the method %s is missing", t, c.ifaceName(iface), m.Name)
		}
	}
	if im.vars != nil {
		// abstract check of the body, with the impl's TypeVars
		inst := c.instImplMethods(im, im.vars)
		for _, m := range inst.Methods {
			c.abstracts = append(c.abstracts, abstractCheck{m, binding})
		}
		im.instances[typeKeys(im.vars)] = inst
	} else {
		c.makeImplMethods(im, im, binding)
	}
	c.impls = append(c.impls, im)
	c.info.Impls = append(c.info.Impls, im)
}

func typeKeys(ts []Type) string {
	_, key, _ := typeList(ts)
	return key
}

// autoFields lists the fields (of a struct, or of all the variants of an
// enum) the generated write writes.
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

// typeModule is the module that declares t (struct or enum), or nil for the built-in
// types, slices and arrays.
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

// implFromStruct binds a one-line impl to the struct's already declared
// methods, checking the signatures.
func (c *checker) implFromStruct(im *Impl, t Type) {
	s, ok := t.(*Struct)
	if !ok || s.Params != nil || s.Origin != nil {
		c.errorf(im.Decl.P, "the form impl %s for %s, without a block, only applies to non-generic structs with the methods declared; for other types, write the methods in a block { }", c.ifaceName(im.Iface), t)
		return
	}
	ok = true
	for _, md := range im.Iface.Decl.Methods {
		m := s.Methods[md.Name]
		if m == nil {
			c.errorf(im.Decl.P, "%s doesn't implement %s: the method %s is missing", s.Name, c.ifaceName(im.Iface), c.ifaceSigString(im.Iface, md, s))
			ok = false
			continue
		}
		if m.recv != recvBorrow {
			c.errorf(m.Decl.Recv.P, "%s.%s must take the receiver borrowed (&%s) to satisfy %s", s.Name, md.Name, s.Name, c.ifaceName(im.Iface))
			ok = false
			continue
		}
		if !c.sameSig(im.Iface, md, s, m) {
			c.errorf(im.Decl.P, "%s.%s has the wrong signature for %s: expected %s", s.Name, md.Name, c.ifaceName(im.Iface), c.ifaceSigString(im.Iface, md, s))
			ok = false
			continue
		}
		im.Methods[md.Name] = m
		m.Pub = true // an interface's methods apply where the interface applies
	}
	if ok {
		c.impls = append(c.impls, im)
		c.info.Impls = append(c.info.Impls, im)
	}
}

// ifaceSig resolves the parameters (without self) and the result of the method md of the
// interface, with Self = self.
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

// makeImplMethods creates the functions of the methods of origin's block for the
// instance inst (the impl itself, if it isn't generic), with the types of
// binding. Concrete instances have their body checked and generated.
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
			c.errorf(d.P, "%s has the wrong signature for %s: expected %s", name, c.ifaceName(origin.Iface), c.ifaceSigString(origin.Iface, md, inst.Type))
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

// instImplMethods creates the instance of the generic impl im for the type
// arguments args.
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

// ---------- lookup ----------

// findImpl returns the impl of i for t (instantiating a generic impl, if
// needed), or nil.
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

// matchType checks whether t has the shape of the pattern p (with a generic impl's
// TypeVars), binding each TypeVar to the corresponding type.
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

// implMethod looks for the method name in some impl of t. For a TypeVar,
// returns the method of the constraint's interface (abstract check).
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
			Decl: md, File: tv.Iface.File} // Decl: the signature in the interface (for the editor)
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

// implMethodCall checks x.m(args) when m comes from an impl (or from the constraint of
// a type parameter). ok is false if there's no such method.
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
			m = c.implMethod(c.sliceOf(a.Elem), sel.Sel) // [N]T uses the impl of []T
		}
	}
	if m == nil {
		if tv, ok := base.(*TypeVar); ok {
			if tv.Iface != nil {
				c.errorf(sel.SelPos, "%s has no method %s: the constraint %s doesn't declare it", tv, sel.Sel, c.ifaceName(tv.Iface))
			} else {
				c.errorf(sel.SelPos, "%s has no method %s: the constraint %s has no methods (use an interface)", tv, sel.Sel, tv.Constraint)
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
		// &[N]T calling the method of []T: the view of the borrowed array
		deref := &ast.UnaryExpr{Op: token.MUL, X: sel.X, P: sel.X.Pos()}
		c.info.Types[deref] = t.(*Borrow).Elem
		amp := &ast.UnaryExpr{Op: token.AMP, X: deref, P: sel.X.Pos()}
		c.info.Types[amp] = want
		recv = amp
	case isBorrow(t):
		recv = sel.X // a received borrow, passed along
	case !isPlace(sel.X) && !isPointerType(t):
		c.errorf(sel.X.Pos(), "the method %s borrows the receiver, and a temporary value can't be borrowed: store it in a variable", m.Name)
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

// autoMethods generates, in MLC, the write method of an impl auto io.Display: the
// same format io.print always used ({1 2} for structs, Circle(2)
// for enums). The generated code is checked as if it had been written in the
// impl, and the errors point at the impl's line.
func (c *checker) autoMethods(d *ast.ImplDecl, iface *Interface, t Type) []*ast.FuncDecl {
	isCmp := iface.mod.std && iface.mod.path == "std/cmp" && (iface.Name == "Eq" || iface.Name == "Ord")
	isHash := iface.mod.std && iface.mod.path == "std/hash" && iface.Name == "Hash"
	if !isCmp && !isHash && (iface.Name != "Display" || iface.mod.path != "std/io") {
		c.errorf(d.P, "impl auto only exists for io.Display, cmp.Eq, cmp.Ord and hash.Hash")
		return nil
	}
	if d.Block {
		c.errorf(d.P, "impl auto has no block: the method is generated")
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
			c.errorf(d.P, "impl auto only applies to structs and enums; for %s, write the methods hash and eq in a block", t)
			return nil
		}
		return c.autoCmp(d, false, t, hashSrc) // the eq, together with the hash
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
		c.errorf(d.P, "impl auto only applies to structs and enums; for %s, write the method write in a block", t)
		return nil
	}
	f, errs := parser.ParseFile(c.mod.file, []byte(src.String()))
	if len(errs) > 0 || len(f.Funcs) != 1 {
		c.errorf(d.P, "internal error generating the impl auto: %v", errs)
		return nil
	}
	setPositions(reflect.ValueOf(f.Funcs[0]), d.P)
	return f.Funcs
}

// autoCmp generates the eq (cmp.Eq) or the cmp (cmp.Ord) of an impl auto. In a
// struct, field by field, in declaration order. In an enum, first the
// variant (in declaration order), then its fields.
//
// extra is the code of another method generated along with it (hash.Hash's hash).
func (c *checker) autoCmp(d *ast.ImplDecl, ord bool, t Type, extra string) []*ast.FuncDecl {
	name, result := "eq", "bool"
	if ord {
		name, result = "cmp", "int"
	}
	var src strings.Builder
	src.WriteString(extra)
	fmt.Fprintf(&src, "fn (self &%s) %s(other &%s) %s {\n", typeString(d.Type), name, typeString(d.Type), result)
	// compare writes the comparison of two values (a and b already borrowed or
	// places), with an early return when it decides
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
			// each comparison in a block, for each field's mlc_c
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
					b = bs[k] // the owning field already comes borrowed in the match that only reads
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
			src.WriteString("    return false // different variants\n")
		} else {
			// different variants: the declaration order decides
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
		c.errorf(d.P, "impl auto only applies to structs and enums; for %s, write the method %s in a block", t, name)
		return nil
	}
	src.WriteString("}\n")
	f, errs := parser.ParseFile(c.mod.file, []byte(src.String()))
	if len(errs) > 0 || len(f.Funcs) == 0 {
		c.errorf(d.P, "internal error generating the impl auto: %v\n%s", errs, src.String())
		return nil
	}
	for _, fn := range f.Funcs {
		setPositions(reflect.ValueOf(fn), d.P)
	}
	return f.Funcs
}

// autoHash generates the hash of an impl auto hash.Hash: it combines the hash of each
// field (in a struct) or the variant and its fields (in an enum), like the hash
// of slices, and ends with mix64.
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

// setPositions puts every position of a generated node at p.
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

// autoTypeParams completes an impl auto of a generic type written without the
// arguments (impl auto io.Display for Pair): it becomes impl[A io.Display, B
// io.Display] io.Display for Pair[A, B], since each field must know how to
// write itself.
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

// typeString writes an AST type as in the code.
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
