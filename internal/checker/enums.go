package checker

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Enums with data
//
//	enum Shape {
//	    Circle(radius float64)
//	    Empty
//	}
//
// An enum value is one of the variants, with its data. It's built with the
// enum's name in front (Shape.Circle(2.0), Shape.Empty) and taken apart with
// match, which must cover every variant:
//
//	match f {
//	    Circle(r) => ...
//	    Empty => ...
//	}
//
// Enums can be generic (enum Result[T any] { Ok(value T); Err(code
// int) }). Variants with owning data make the enum an owner (see Resource); otherwise an
// enum is an ordinary value: copyable and printable.

type Enum struct {
	Name     string
	CName    string
	File     string
	Pub      bool
	Decl     *ast.EnumDecl
	Variants []*Variant

	Params    []*ast.TypeParam
	Origin    *Enum
	Args      []Type
	instances map[string]*Enum
	mod       *module

	// Resource: some variant holds owners (Err(msg []byte)). The enum becomes
	// an owner by value: assigning, passing or returning moves, and the checker
	// requires it to be consumed (match, free or move).
	Resource bool
}

type Variant struct {
	Name   string
	Fields []*Field
	Tag    int
	Enum   *Enum
}

func (e *Enum) String() string { return e.Name }

func (e *Enum) Variant(name string) *Variant {
	for _, v := range e.Variants {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// collectEnumNames registers the enums before the structs, which can have fields
// of an enum type.
func (c *checker) collectEnumNames(f *ast.File) {
	for _, d := range f.Enums {
		switch {
		case predeclared[d.Name]:
			c.errorf(d.P, "the name %q is reserved", d.Name)
			continue
		case c.mod.enums[d.Name] != nil:
			c.errorf(d.P, "enum %s already declared on line %d", d.Name, c.mod.enums[d.Name].Decl.P.Line)
			continue
		}
		en := &Enum{
			Name: c.qualify(d.Name), CName: c.mod.prefix + d.Name, File: c.mod.file, Pub: d.Pub, Decl: d, mod: c.mod,
		}
		if len(d.TypeParams) > 0 {
			c.typeVars(d.TypeParams)
			en.Params = d.TypeParams
			en.instances = map[string]*Enum{}
		}
		c.mod.enums[d.Name] = en
	}
}

// collectEnumVariants resolves the variants of the non-generic enums, after
// the structs.
func (c *checker) collectEnumVariants(f *ast.File) {
	for _, d := range f.Enums {
		en := c.mod.enums[d.Name]
		if en == nil || en.Decl != d {
			continue
		}
		if c.mod.structs[d.Name] != nil {
			c.errorf(d.P, "the name %s is already used by a struct", d.Name)
			continue
		}
		if en.Params == nil {
			c.variants(en)
			c.info.Enums = append(c.info.Enums, en)
		}
	}
}

// variants resolves the variants of en (with the type parameters already bound,
// in an instance).
func (c *checker) variants(en *Enum) {
	seen := map[string]bool{}
	for i, vd := range en.Decl.Variants {
		if seen[vd.Name] {
			c.errorf(vd.P, "repeated variant %s in %s", vd.Name, en.Name)
			continue
		}
		seen[vd.Name] = true
		v := &Variant{Name: vd.Name, Tag: i, Enum: en}
		fseen := map[string]bool{}
		for _, fd := range vd.Fields {
			if fseen[fd.Name] {
				c.errorf(fd.P, "repeated field %s in %s", fd.Name, vd.Name)
			}
			fseen[fd.Name] = true
			t := c.resolveType(fd.Type, ctxField)
			switch tt := t.(type) {
			case *Borrow:
				c.errorf(fd.P, "a variant can't hold a borrow (%s is %s)", fd.Name, t)
				t = Invalid
			case *Pointer:
				// the enum's zero value is the first variant zeroed: *T has no zero
				if !tt.Nullable {
					c.errorf(fd.P, "a variant holds ?*T, not *T (the enum's zero value is nil): use ?%s", t)
					t = Invalid
				} else if ok, why := ownedPointee(tt.Elem); !ok {
					c.errorf(fd.P, "%s: %s", fd.Name, why)
					t = Invalid
				} else {
					c.requireFree(t, fd.P, en.Name)
				}
			case *Slice:
				if ok, why := ownedElem(tt.Elem); !ok {
					c.errorf(fd.P, "%s: %s", fd.Name, why)
					t = Invalid
				} else {
					c.requireFree(t, fd.P, en.Name)
				}
			default:
				if isResource(t) {
					c.errorf(fd.P, "a struct with owners only exists behind a pointer: use ?*%s", t)
					t = Invalid
				}
			}
			if IsOwner(t) {
				en.Resource = true // the variant holds an owner
			}
			v.Fields = append(v.Fields, &Field{Name: fd.Name, Type: t})
		}
		en.Variants = append(en.Variants, v)
	}
	if len(en.Variants) == 0 {
		c.errorf(en.Decl.P, "the enum %s needs at least one variant", en.Name)
	}
}

// instEnum returns the instance of a generic enum.
func (c *checker) instEnum(en *Enum, args []Type, pos token.Pos) Type {
	if !c.checkTypeArgs(en.Name, en.Params, args, pos) {
		return Invalid
	}
	display, key, mangled := typeList(args)
	if inst, ok := en.instances[key]; ok {
		return inst
	}
	inst := &Enum{
		Name: en.Name + "[" + display + "]", CName: en.CName + "__" + mangled,
		File: en.File, Pub: en.Pub, Decl: en.Decl, Origin: en, Args: args, mod: en.mod,
	}
	en.instances[key] = inst
	c.withScope(en.mod, bind(en.Params, args), func() { c.variants(inst) })
	if !anyAbstract(args) {
		c.info.Enums = append(c.info.Enums, inst)
	}
	return inst
}

// resolveEnum resolves the written type t, which names the enum en.
func (c *checker) resolveEnum(en *Enum, t *ast.TypeExpr) Type {
	switch {
	case en.Params != nil && len(t.Args) == 0:
		c.errorf(t.P, "%s is generic: write the type arguments, like %s[int]", en.Name, en.Name)
		return Invalid
	case en.Params == nil && len(t.Args) > 0:
		c.errorf(t.P, "%s isn't generic", en.Name)
		return Invalid
	case en.Params != nil:
		var args []Type
		for _, a := range t.Args {
			args = append(args, c.resolveType(a, ctxTypeArg))
		}
		return c.instEnum(en, args, t.P)
	}
	return en
}

// enumNamed returns the enum written in x (Shape, Result[int], geo.Shape),
// if x names one. A generic enum without arguments comes back as the declaration.
func (c *checker) enumNamed(x ast.Expr) *Enum {
	switch x := x.(type) {
	case *ast.Ident:
		if c.scope.lookup(x.Name) == nil {
			return c.mod.enums[x.Name]
		}
	case *ast.SelectorExpr:
		if m := c.userModule(x.X); m != nil {
			if en := m.enums[x.Sel]; en != nil {
				if !en.Pub {
					c.errorf(x.SelPos, "%s.%s isn't public (declare it with pub enum)", m.name, x.Sel)
				}
				return en
			}
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		base, args := splitIndex(x)
		en := c.enumNamed(base)
		if en == nil || en.Params == nil {
			return nil
		}
		var targs []Type
		for _, a := range args {
			targs = append(targs, c.exprType(a))
		}
		inst, _ := c.instEnum(en, targs, x.Pos()).(*Enum)
		return inst
	}
	return nil
}

// enumCtor checks Shape.Circle(args) (call not nil) or Shape.Empty.
func (c *checker) enumCtor(node ast.Expr, call *ast.CallExpr, en *Enum, sel *ast.SelectorExpr) Type {
	var args []ast.Expr
	if call != nil {
		args = call.Args
	}
	if en.Params != nil {
		// generic without type arguments: from the context or from the arguments
		en = c.inferEnum(node, en, sel, args)
		if en == nil {
			for _, a := range args {
				c.value(a)
			}
			return Invalid
		}
	}
	v := en.Variant(sel.Sel)
	if v == nil {
		c.errorf(sel.SelPos, "%s has no variant %s", en.Name, sel.Sel)
		for _, a := range args {
			c.value(a)
		}
		return Invalid
	}
	switch {
	case call == nil && len(v.Fields) > 0:
		c.errorf(sel.SelPos, "%s.%s has %d field(s): write %s.%s(...)", en.Name, v.Name, len(v.Fields), en.Name, v.Name)
		return Invalid
	case call != nil && len(args) != len(v.Fields):
		c.errorf(call.Lparen, "%s.%s expects %d value(s), got %d", en.Name, v.Name, len(v.Fields), len(args))
	}
	for i, a := range args {
		if i < len(v.Fields) {
			c.valueAs(a, v.Fields[i].Type, fmt.Sprintf("field %s of %s", v.Fields[i].Name, v.Name))
		} else {
			c.value(a)
		}
	}
	c.info.EnumCtors[node] = v
	return en
}

// inferEnum finds the instance of a generic enum used without type
// arguments: by the expected type (return Result.Ok(5) in a function that returns
// Result[int]) or by the variant's values.
func (c *checker) inferEnum(node ast.Expr, en *Enum, sel *ast.SelectorExpr, args []ast.Expr) *Enum {
	if want, ok := c.expected.(*Enum); ok && c.expectedFor == node && want.Origin == en {
		return want
	}
	var vars []Type
	for _, p := range en.Params {
		vars = append(vars, c.newTypeVar(p.Name, p))
	}
	abs, ok := c.instEnum(en, vars, sel.SelPos).(*Enum)
	if !ok {
		return nil
	}
	v := abs.Variant(sel.Sel)
	if v == nil {
		c.errorf(sel.SelPos, "%s has no variant %s", en.Name, sel.Sel)
		return nil
	}
	set := map[*TypeVar]bool{}
	for _, t := range vars {
		set[t.(*TypeVar)] = true
	}
	bound := map[*TypeVar]Type{}
	saved := len(c.errs)
	for i, a := range args {
		if i < len(v.Fields) {
			at := c.value(a)
			if isUntyped(at) {
				at = DefaultType(at)
			}
			unify(v.Fields[i].Type, at, set, bound)
		}
	}
	c.errs = c.errs[:saved]
	var targs []Type
	for _, t := range vars {
		b := bound[t.(*TypeVar)]
		if b == nil {
			c.errorf(sel.SelPos, "couldn't infer %s in %s.%s: write %s[int].%s(...)", t, en.Name, sel.Sel, en.Name, sel.Sel)
			return nil
		}
		targs = append(targs, b)
	}
	inst, _ := c.instEnum(en, targs, sel.SelPos).(*Enum)
	return inst
}

// checkEnumMatch checks a match on an enum: each arm names variants, and the
// ones with data name the fields (Circle(r)).
func (c *checker) checkEnumMatch(s *ast.MatchStmt, en *Enum) {
	seen := map[string]token.Pos{}
	var wildcard *ast.Pattern
	unreachable := false
	// An owning enum is consumed by the match when the subject is an owning variable
	// or a temporary value: the owning fields go out to the pattern's names.
	// Otherwise (*e, a field, a borrow), the match only reads, and the owning fields
	// come borrowed.
	mode := matchCopy
	if IsOwner(en) {
		mode = matchBorrow
		if c.consumableSubject(s.Subject) {
			mode = matchTake
			c.info.MatchTakes[s] = true
		}
	}
	for _, arm := range s.Arms {
		if wildcard != nil && !unreachable {
			c.errorf(arm.P, "an arm after _ is never executed")
			unreachable = true
		}
		c.pushScope()
		for _, p := range arm.Patterns {
			if p.Wildcard {
				if len(arm.Patterns) > 1 {
					c.errorf(p.P, "_ must be alone in the arm")
				}
				wildcard = p
				continue
			}
			c.variantPattern(p, en, len(arm.Patterns), seen, mode, s.Subject)
		}
		c.checkStmt(arm.Body)
		c.popScope()
	}
	if wildcard != nil && mode == matchTake {
		// the _ would consume variants without naming their owners
		for _, v := range en.Variants {
			if _, ok := seen[v.Name]; !ok && variantOwns(v) {
				c.errorf(wildcard.P, "the _ arm would take out the variant %s, which holds owners, without freeing them: handle %s in its own arm", v.Name, v.Name)
			}
		}
	}
	switch {
	case wildcard != nil && len(seen) == len(en.Variants):
		c.errorf(wildcard.P, "the _ arm is never executed: every variant of %s is already covered", en.Name)
	case wildcard == nil:
		var missing []string
		for _, v := range en.Variants {
			if _, ok := seen[v.Name]; !ok {
				missing = append(missing, v.Name)
			}
		}
		if len(missing) > 0 {
			c.errorf(s.P, "a match on %s must cover every variant (missing: %s) or have a _ arm", en.Name, joinNames(missing))
		}
	}
}

// modes of a match on an enum
const (
	matchCopy   = iota // enum without owners: the fields are copied
	matchTake          // owning enum consumed: the owning fields go out to the names
	matchBorrow        // owning enum only read: the owning fields come borrowed
)

// consumableSubject says whether a match's subject can be consumed: a
// whole owning variable or a temporary value (a call, a constructor).
func (c *checker) consumableSubject(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		l := c.info.Locals[e]
		return l != nil && !isBorrow(l.Type)
	case *ast.CallExpr:
		return true
	case *ast.SelectorExpr:
		_, ctor := c.info.EnumCtors[e] // Shape.Empty
		return ctor
	}
	return false
}

func variantOwns(v *Variant) bool {
	for _, f := range v.Fields {
		if IsOwner(f.Type) {
			return true
		}
	}
	return false
}

func (c *checker) variantPattern(p *ast.Pattern, en *Enum, armSize int, seen map[string]token.Pos, mode int, subject ast.Expr) {
	name, pos := "", p.P
	switch {
	case p.Variant != nil:
		name, pos = p.Variant.Name, p.Variant.P
	default:
		id, ok := p.Lit.(*ast.Ident)
		if !ok {
			c.errorf(p.P, "in a match on %s, the patterns are variants: Circle(r), Empty", en.Name)
			return
		}
		name, pos = id.Name, id.P
	}
	v := en.Variant(name)
	if v == nil {
		c.errorf(pos, "%s has no variant %s", en.Name, name)
		return
	}
	switch {
	case p.Variant == nil && len(v.Fields) > 0:
		c.errorf(pos, "%s has %d field(s): write %s(...) giving each one a name (or _)", name, len(v.Fields), name)
	case p.Variant != nil && len(p.Binds) != len(v.Fields):
		c.errorf(pos, "%s has %d field(s), the pattern names %d", name, len(v.Fields), len(p.Binds))
	case p.Variant != nil && armSize > 1:
		c.errorf(pos, "a variant with named fields must be alone in the arm")
	}
	if prev, dup := seen[name]; dup {
		c.errorf(pos, "repeated variant %s (already appears on line %d)", name, prev.Line)
		return
	}
	seen[name] = pos
	c.info.Patterns[p] = v
	var locals []*Local
	for i, b := range p.Binds {
		if i < len(v.Fields) && b.Name == "_" && mode == matchTake && IsOwner(v.Fields[i].Type) {
			c.errorf(b.P, "the field %s of %s is an owner: give it a name (to free or move it), instead of _", v.Fields[i].Name, v.Name)
		}
		if b.Name == "_" || i >= len(v.Fields) {
			locals = append(locals, nil)
			continue
		}
		ft := v.Fields[i].Type
		if mode == matchBorrow && IsOwner(ft) {
			// the match only reads: the owning field comes borrowed
			l := c.declare(b.Name, b.P, c.borrowOf(ft), false)
			if root, _ := c.placeRoot(subject); root != nil {
				c.info.BorrowSrc[l] = root
			} else if root, _ := c.borrowRoot(subject); root != nil {
				c.info.BorrowSrc[l] = root
			}
			c.info.BindBorrows[l] = true
			locals = append(locals, l)
			continue
		}
		locals = append(locals, c.declare(b.Name, b.P, ft, false))
	}
	c.info.PatternBinds[p] = locals
}

func joinNames(names []string) string {
	s := ""
	for i, n := range names {
		if i > 0 {
			s += ", "
		}
		s += n
	}
	return s
}

func isEnum(t Type) bool {
	_, ok := t.(*Enum)
	return ok
}
