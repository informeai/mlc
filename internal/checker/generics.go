package checker

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Generics
//
//	fn larger[T ordered](a T, b T) T { ... }
//	struct Pair[A any, B any] { a A; b B }
//	fn (v &Vector[T]) size() int { ... }
//
// The definition is checked once, abstractly: each type parameter is
// a TypeVar, and only what the constraint allows is valid (with T any, a < b is an error in
// the definition itself). Each use with concrete types (larger[int], Pair[int,
// string]) creates an instance: a copy of the declaration, checked with the real
// types and generated in C with its own name (monomorphization).
//
// Constraints: any and comparable (any type without ownership), ordered and number
// (numbers), integer (integers), owned (also simple owners) and ref (any type,
// only borrowed); an interface, alone or combined with +.

// TypeVar is a type parameter during the abstract check of the definition.
type TypeVar struct {
	Name       string
	Constraint string
	Iface      *Interface // a constraint that's an interface (nil for the built-in ones)
}

func (t *TypeVar) String() string { return t.Name }

var constraints = map[string]bool{"any": true, "comparable": true, "ordered": true, "number": true, "integer": true, "owned": true, "ref": true}

// The ref constraint accepts any type (including structs with owners), because
// the parameter only appears borrowed: &C. It's the one for a callback's context
// (fn (m &Map[K, V]) each_with[C ref](ctx &C, f fn(&C, &K, &V))).

// tvHas says whether t is a TypeVar whose constraint allows the capability cap:
// "eq" (==), "order" (<), "arith" (+ - * /) or "mod" (%).
func tvHas(t Type, cap string) bool {
	tv, ok := t.(*TypeVar)
	if !ok {
		return false
	}
	switch cap {
	case "eq":
		return tv.Constraint != "owned" && tv.Constraint != "ref"
	case "order", "arith":
		return tv.Constraint == "ordered" || tv.Constraint == "number" || tv.Constraint == "integer"
	case "mod":
		return tv.Constraint == "integer"
	}
	return false
}

func numLike(t Type) bool   { return isNumeric(t) || tvHas(t, "arith") }
func orderLike(t Type) bool { return isNumeric(t) || tvHas(t, "order") }

// abstract says whether t contains a TypeVar (so it belongs only to the abstract check and
// never gets to the generator).
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

// satisfies says whether the type t satisfies the constraint c.
func satisfies(t Type, c string) bool {
	if tv, ok := t.(*TypeVar); ok {
		switch c {
		case "ref":
			return true // ref accepts everything
		case "owned":
			return tv.Constraint != "ref" // every type accepted by another constraint (except ref) is accepted by owned
		case "any", "comparable":
			return tv.Constraint != "owned" && tv.Constraint != "ref"
		case "ordered", "number":
			return tvHas(tv, "order")
		}
		return tv.Constraint == "integer"
	}
	if c == "owned" {
		ok, _ := ownedOK(t)
		return ok
	}
	if c == "ref" {
		return !isBorrow(t) && t != Void && t != NilType && t != Invalid && !isUntyped(t)
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

// typeName gives a C identifier name for t, used in the instances.
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

// typeKey identifies t in the instance cache. TypeVars, structs and enums
// go in by identity: two parameters called T (sort's and
// reverse's) are different types, just like two Point structs from different
// files.
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

// bind binds the names of the type parameters to the types.
func bind(params []*ast.TypeParam, args []Type) map[string]Type {
	m := map[string]Type{}
	for i, p := range params {
		if i < len(args) {
			m[p.Name] = args[i]
		}
	}
	return m
}

// typeVars creates the TypeVars of a list of type parameters, checking the
// constraints.
func (c *checker) typeVars(params []*ast.TypeParam) []Type {
	var vs []Type
	seen := map[string]bool{}
	for _, p := range params {
		if seen[p.Name] {
			c.errorf(p.P, "repeated type parameter %s", p.Name)
		}
		seen[p.Name] = true
		vs = append(vs, c.newTypeVar(p.Name, p))
	}
	return vs
}

// checkTypeArgs checks the number and the constraints of the type arguments.
func (c *checker) checkTypeArgs(what string, params []*ast.TypeParam, args []Type, pos token.Pos) bool {
	if len(args) != len(params) {
		c.errorf(pos, "%s expects %d type argument(s), got %d", what, len(params), len(args))
		return false
	}
	ok := true
	for i, p := range params {
		if args[i] == Invalid {
			ok = false
			continue
		}
		if b, _, _ := splitConstraint(p.Constraint); b == "owned" && !abstract(args[i]) && c.instName == "" {
			c.requireFree(args[i], pos, what) // ?*X with X an owner: needs free
		}
		if !c.satisfiesParam(args[i], p) {
			reason := ""
			builtin, iface, _ := splitConstraint(p.Constraint)
			switch {
			case builtin == "owned" && !satisfies(args[i], "owned"):
				if _, why := ownedOK(args[i]); why != "" {
					reason = " (" + why + ")"
				}
			case isOpaque(args[i]) && builtin != "ref":
				reason = fmt.Sprintf(" (%s is an opaque C type and only exists behind a pointer: the generic needs the ref constraint, which uses %s only borrowed, like [%s %s])", args[i], p.Name, p.Name, refConstraint(iface))
			case builtin == "ref" && isBorrow(args[i]):
				reason = " (a borrow can't be a type argument: the parameter is already used as &" + p.Name + ")"
			case builtin != "" && builtin != "owned" && !satisfies(args[i], builtin) && (IsOwner(args[i]) || isBorrow(args[i]) || isResource(args[i])):
				reason = " (it's an owner: to accept owners, the constraint must be owned)"
			case iface != "" && (builtin == "" || satisfies(args[i], builtin)):
				if IsOwner(args[i]) && builtin != "owned" {
					c.errorf(pos, "%s is an owner and doesn't satisfy %s, required by %s in %s (to accept owners: %s owned + %s)", args[i], p.Constraint, p.Name, what, p.Name, iface)
				} else {
					hint := fmt.Sprintf("declare impl %s for %s", iface, args[i])
					if base := baseOf(args[i]); (iface == "cmp.Eq" || iface == "cmp.Ord" || iface == "io.Display") && (isStructType(base) || isEnumType(base)) {
						hint = fmt.Sprintf("declare impl auto %s for %s, or write the method", iface, base)
					}
					c.errorf(pos, "%s doesn't implement %s, required by %s in %s (%s)", args[i], iface, p.Name, what, hint)
				}
				ok = false
				continue
			}
			c.errorf(pos, "%s doesn't satisfy %s, required by %s in %s%s", args[i], p.Constraint, p.Name, what, reason)
			ok = false
		}
	}
	return ok
}

// refConstraint writes the ref constraint, with iface's interface if any
// (for the error message's example).
func refConstraint(iface string) string {
	if iface == "" {
		return "ref"
	}
	return "ref + " + iface
}

// ---------- generic structs ----------

// instStruct returns the instance of a generic struct with the given arguments.
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
			c.errorf(fd.P, "struct %s contains itself by value; use a pointer", inst.Name)
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

// ---------- generic functions and methods ----------

// instFunc returns the instance of a generic function. Concrete instances
// go into the queue to have their body checked (and then generated); with abstract
// arguments (inside another generic definition), only the signature matters.
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

// instMethod returns the method name of the struct instance s, creating it
// from the generic declaration.
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
	if len(d.TypeParams) > 0 {
		// a method with its own type parameters: a template, instantiated at the call
		c.withScope(origin.mod, binding, func() {
			recv := &Local{Name: d.Recv.Name, CName: "l_" + d.Recv.Name, Type: c.recvType(s, m.recv), Pos: d.Recv.P, Param: true}
			c.genericMethod(m, d, origin.mod, binding, recv)
		})
		return m
	}
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

// withScope runs f in the module m, with the type parameters bound.
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
	fn.queued = true // the body is checked in checkInstances, with binding
	c.info.Funcs = append(c.info.Funcs, fn)
	c.pending = append(c.pending, pendingInst{fn, binding})
}

// checkInstances checks the body of the concrete instances. Checking one can create
// others, which go into the queue.
func (c *checker) checkInstances() {
	for len(c.pending) > 0 {
		p := c.pending[0]
		c.pending = c.pending[1:]
		saved := c.instName
		c.instName = p.fn.Name
		c.muted = c.probes[p.fn] // type errors already show up in the definition
		c.typeParams = p.binding
		c.checkFunc(p.fn)
		c.typeParams = nil
		if c.muted && c.mutedErrs > 0 {
			c.probeBroken[p.fn] = true // doesn't check the ownership of badly typed code
		}
		c.instName, c.muted, c.mutedErrs = saved, false, 0
	}
}

// ---------- calls ----------

// genericCall checks a call to a generic function: f(args), with the types
// inferred from the arguments, or f[T](args), with the explicit types.
func (c *checker) genericCall(e *ast.CallExpr, g *Func, explicit []ast.Expr) Type {
	var args []Type
	if explicit != nil {
		for _, x := range explicit {
			args = append(args, c.exprType(x))
		}
	} else {
		args = c.infer(e, g)
		if args == nil {
			return Invalid // the arguments were already evaluated in the inference
		}
	}
	inst := c.instFunc(g, args, e.Lparen)
	if inst == nil {
		c.checkArgs(e)
		return Invalid
	}
	return c.userCall(e, inst)
}

// infer infers the type arguments of a call from the arguments.
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
	c.errs = c.errs[:saved] // the arguments are checked again, with the right types
	for _, u := range untyped {
		if tv, ok := u.p.(*TypeVar); ok && vars[tv] && bound[tv] == nil {
			bound[tv] = DefaultType(u.t)
		}
	}
	var args []Type
	for _, v := range g.typeVars {
		t := bound[v.(*TypeVar)]
		if t == nil {
			c.errorf(e.Lparen, "couldn't infer %s in %s: write the types, like %s[int](...)", v, g.Name, g.Name)
			return nil
		}
		args = append(args, t)
	}
	return args
}

// peekType computes an argument's type before knowing the parameter's type.
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

// unify binds the TypeVars of vars that appear in p to the corresponding types of a.
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
		case *Array: // &a, with a [N]T, goes to &[]T
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
	case *FuncType:
		// fn(&C, &K) R against the passed function: C comes from its first parameter
		if at, ok := a.(*FuncType); ok && len(at.Params) == len(pt.Params) {
			for i := range pt.Params {
				unify(pt.Params[i], at.Params[i], vars, bound)
			}
			unify(pt.Result, at.Result, vars, bound)
		}
	}
}

// exprType reads a type written as an expression (the type argument of f[int],
// Pair[int, string]{...}).
func (c *checker) exprType(e ast.Expr) Type {
	if u, ok := e.(*ast.UnaryExpr); ok && (u.Op == token.MUL || u.Op == token.AMP) {
		return c.resolveType(typeExprOf(u), ctxTypeArg)
	}
	switch e := e.(type) {
	case *ast.TypeArg:
		return c.resolveType(e.Type, ctxTypeArg)
	case *ast.Ident:
		return c.resolveType(&ast.TypeExpr{Name: e.Name, P: e.P}, ctxTypeArg)
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return c.resolveType(&ast.TypeExpr{Module: id.Name, Name: e.Sel, P: e.SelPos}, ctxTypeArg)
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		base, args := splitIndex(e)
		var targs []*ast.TypeExpr
		for _, a := range args {
			targs = append(targs, typeExprOf(a))
		}
		switch b := base.(type) {
		case *ast.Ident:
			return c.resolveType(&ast.TypeExpr{Name: b.Name, Args: targs, P: b.P}, ctxTypeArg)
		case *ast.SelectorExpr:
			if id, ok := b.X.(*ast.Ident); ok {
				return c.resolveType(&ast.TypeExpr{Module: id.Name, Name: b.Sel, Args: targs, P: b.SelPos}, ctxTypeArg)
			}
		}
	}
	c.errorf(e.Pos(), "expected a type")
	return Invalid
}

// typeExprOf converts an expression that writes a type into a TypeExpr.
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
		// *Node and &T between brackets are read as expressions: f[*Node](x)
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

// genericFuncOf returns the generic function named by e (f or mod.f), if any.
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
					c.errorf(e.SelPos, "%s.%s isn't public (declare it with pub fn)", m.name, e.Sel)
				}
				return g
			}
		}
	}
	return nil
}

// recvParamNames returns the names of the type parameters of a receiver like
// &Vector[T]: ["T"].
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

// abstractCheck is a generic definition waiting for the abstract check of its body.
type abstractCheck struct {
	fn      *Func
	binding map[string]Type
}

// collectGeneric registers a generic function: the signature with TypeVars, for
// the calls, and the body for the abstract check.
func (c *checker) collectGeneric(d *ast.FuncDecl) {
	if d.Extern {
		c.errorf(d.P, "an extern fn can't be generic")
	}
	if d.Name == "main" {
		c.errorf(d.P, "main can't be generic")
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

// collectGenericMethod registers a method of a generic struct. The receiver
// names the struct's type parameters: fn (v &Vector[T]) size() int.
func (c *checker) collectGenericMethod(d *ast.FuncDecl, s *Struct, named *ast.TypeExpr) {
	if len(named.Args) != len(s.Params) {
		c.errorf(named.P, "the receiver must name the %d type parameter(s) of %s, like %s[T]", len(s.Params), s.Name, s.Name)
		return
	}
	seen := map[string]bool{}
	for _, a := range named.Args {
		if a.Mod != ast.TypeNamed || a.Module != "" || len(a.Args) > 0 || seen[a.Name] {
			c.errorf(a.P, "in the receiver, the type arguments must be new and different names, like %s[T]", s.Name)
			return
		}
		seen[a.Name] = true
	}
	for _, f := range s.Decl.Fields {
		if f.Name == d.Name {
			c.errorf(d.P, "%s already has a field %s; the method needs another name", s.Name, d.Name)
			return
		}
	}
	if prev := s.methodDecls[d.Name]; prev != nil {
		c.errorf(d.P, "method %s.%s already declared on line %d", s.Name, d.Name, prev.P.Line)
		return
	}
	s.methodDecls[d.Name] = d
	// the receiver's constraints: fn (v &Vec[T any]) get() only exists with T any
	var cons []*ast.TypeParam
	for _, a := range named.Args {
		if a.Constraint == "" {
			cons = append(cons, nil)
			continue
		}
		b, iface, ok := splitConstraint(a.Constraint)
		if !ok || (b == "" && c.lookupIface(iface, a.P, false) == nil) {
			c.errorf(a.P, "unknown constraint %q", a.Constraint)
		}
		// the receiver's constraint adds to the struct's: in Map[K owned + Hash],
		// fn (m &Map[K any, V]) asks for K any + Hash
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
	// abstract check: an instance of s with TypeVars instead of the types
	var vars []Type
	for i, a := range named.Args {
		p := s.Params[i]
		if cons[i] != nil {
			p = cons[i] // in the body, T has the receiver's constraint
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
		for k, v := range m.methodBinding {
			binding[k] = v // the method's own type parameters (each[C])
		}
		c.abstracts = append(c.abstracts, abstractCheck{m, binding})
	}
}

// ---------- generic methods ----------
//
//	fn (m &Map[K, V]) each[C any](ctx &C, f fn(&C, &K, &V))
//
// A method can have its own type parameters, besides the struct's. The
// method becomes a template: at the call, its types are inferred from the
// arguments (without the receiver) or written (m.each[Sum](...)), and each
// combination is an ordinary instance, checked and generated like a generic
// function.

// genericMethod prepares m, of declaration d, as a generic method template.
// binding has the struct's types (empty in an ordinary struct).
func (c *checker) genericMethod(m *Func, d *ast.FuncDecl, mod *module, binding map[string]Type, recv *Local) {
	m.typeVars = c.typeVars(d.TypeParams)
	m.instances = map[string]*Func{}
	m.structBinding = binding
	full := map[string]Type{}
	for k, v := range binding {
		full[k] = v
	}
	m.methodBinding = bind(d.TypeParams, m.typeVars)
	for k, v := range m.methodBinding {
		full[k] = v
	}
	c.withScope(mod, full, func() { c.signature(m, d, recv) })
}

// instGenericMethod returns the instance of the generic method m with the types
// args of its parameters.
func (c *checker) instGenericMethod(m *Func, args []Type, pos token.Pos) *Func {
	d := m.Decl
	if !c.checkTypeArgs(m.Name, d.TypeParams, args, pos) {
		return nil
	}
	display, key, mangled := typeList(args)
	if inst, ok := m.instances[key]; ok {
		return inst
	}
	recvT := m.Params[0].Type
	concrete := !abstract(recvT) && !anyAbstract(args)
	decl := d
	if concrete {
		decl = ast.Clone(d)
	}
	inst := &Func{
		Name: m.Name + "[" + display + "]", Result: Void, Decl: decl,
		CName: m.CName + "__" + mangled, File: m.File, Pub: m.Pub, mod: m.mod,
		Method: true, recv: m.recv,
	}
	m.instances[key] = inst
	binding := map[string]Type{}
	for k, v := range m.structBinding {
		binding[k] = v
	}
	for k, v := range bind(d.TypeParams, args) {
		binding[k] = v
	}
	c.withScope(m.mod, binding, func() {
		recv := &Local{Name: d.Recv.Name, CName: "l_" + d.Recv.Name, Type: recvT, Pos: d.Recv.P, Param: true}
		c.signature(inst, decl, recv)
	})
	if concrete {
		c.queue(inst, binding)
	}
	return inst
}

// genericMethodCall checks x.m(args) (or x.m[T](args)) with m a generic
// method.
func (c *checker) genericMethodCall(e *ast.CallExpr, sel *ast.SelectorExpr, t Type, s *Struct, m *Func, explicit []ast.Expr) Type {
	recv := c.receiverArg(sel.X, t, s, m)
	if recv == nil {
		c.checkArgs(e)
		return Invalid
	}
	var args []Type
	if explicit != nil {
		for _, x := range explicit {
			args = append(args, c.exprType(x))
		}
	} else {
		vars := map[*TypeVar]bool{}
		for _, v := range m.typeVars {
			vars[v.(*TypeVar)] = true
		}
		bound := map[*TypeVar]Type{}
		saved := len(c.errs)
		for i, a := range e.Args {
			if i+1 >= len(m.Params) {
				break
			}
			if at := c.peekType(a); !isUntyped(at) {
				unify(m.Params[i+1].Type, at, vars, bound)
			}
		}
		c.errs = c.errs[:saved] // the arguments are checked again, with the right types
		for _, v := range m.typeVars {
			bt := bound[v.(*TypeVar)]
			if bt == nil {
				c.errorf(e.Lparen, "couldn't infer %s in %s: write the types, like %s[%s](...)", v, m.Name, sel.Sel, v)
				c.checkArgs(e)
				return Invalid
			}
			args = append(args, bt)
		}
	}
	inst := c.instGenericMethod(m, args, e.Lparen)
	if inst == nil {
		c.checkArgs(e)
		return Invalid
	}
	c.info.Recv[e] = recv
	c.callArgs(e, inst, inst.Params[1:])
	c.info.Calls[e] = Callee{Func: inst}
	return inst.Result
}

// checkAbstracts checks the body of the current module's generic definitions.
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

// namesType says whether e is the name of a struct (local or mod.Struct), which can
// receive type arguments.
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

// baseOf removes the pointer from ?*T and *T: the type an impl implements.
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
