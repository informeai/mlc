package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Struct is a struct type declared by the user.
type Struct struct {
	Name    string
	Methods map[string]*Func
	CName   string // name in C, without the mlc_st_ prefix
	File    string
	Pub     bool
	Fields  []*Field
	Decl    *ast.StructDecl

	// Resource: the struct contains owning pointers (directly or in a struct
	// kept by value). Its values only exist behind a pointer:
	// copying would duplicate the owners.
	Resource bool

	// Extern: extern "C" type, an opaque C type (without fields), which only
	// exists behind a pointer, as a handle
	Extern bool

	// CStruct: extern "C" struct T from "h.h", C's struct (CName is C's
	// name, Header the header), with the fields MLC uses; Anon in one of its
	// anonymous nested structs (flags { ... }), which has no name in C
	CStruct bool
	Anon    bool
	Header  string

	// ThreadResult: in a thread.Thread[R], the result's type (Void if it
	// has none); nil in the other structs
	ThreadResult Type

	// ChanEnd: in a chan.Sender[T] or chan.Receiver[T], which end it is
	// (ChanSender, ChanReceiver), and ChanElem the values' type; 0 in the others
	ChanEnd  int
	ChanElem Type

	// MutexKind: in a mutex.Mutex[T] or mutex.Guard[T], which of the two it is
	// (MutexHandle, MutexGuard), and MutexElem the value's type; 0 in the others
	MutexKind int
	MutexElem Type

	// Generics: Params in the generic struct (the declaration); Origin and Args in an
	// instance (Pair[int, string]).
	Params      []*ast.TypeParam
	Origin      *Struct
	Args        []Type
	instances   map[string]*Struct
	methodDecls map[string]*ast.FuncDecl
	// methodConstraints: constraints a method's receiver adds
	// (fn (v &Vec[T any]) get): one per type parameter, nil if none
	methodConstraints map[string][]*ast.TypeParam
	mod               *module
}

type Field struct {
	Name string
	Type Type
}

func (s *Struct) String() string { return s.Name }

func (s *Struct) Field(name string) *Field {
	for _, f := range s.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// collectStructs registers every struct before resolving the fields, so
// declaration order doesn't matter. Then it orders the structs so
// each one comes after the ones it contains, which is the order C requires,
// and rejects a struct that contains itself by value.
func (c *checker) collectStructs(f *ast.File) {
	for _, d := range f.Structs {
		switch {
		case predeclared[d.Name]:
			c.errorf(d.P, "the name %q is reserved", d.Name)
			continue
		case c.mod.structs[d.Name] != nil:
			c.errorf(d.P, "struct %s already declared on line %d", d.Name, c.mod.structs[d.Name].Decl.P.Line)
			continue
		case c.mod.ifaces[d.Name] != nil:
			c.errorf(d.P, "%s was already declared in this file", d.Name)
			continue
		}
		s := &Struct{
			Name: c.qualify(d.Name), Methods: map[string]*Func{}, CName: c.mod.prefix + d.Name, File: c.mod.file, Pub: d.Pub, Decl: d,
			mod: c.mod, Extern: d.Extern,
		}
		if d.Header != "" {
			s.CStruct, s.Header, s.CName = true, d.Header, d.Name // C's own name: the header declares it
			c.addHeader(d.Header)
		}
		if len(d.TypeParams) > 0 {
			// generic: the fields are resolved in each instance
			c.typeVars(d.TypeParams)
			s.Params = d.TypeParams
			s.instances = map[string]*Struct{}
			s.methodDecls = map[string]*ast.FuncDecl{}
		}
		c.mod.structs[d.Name] = s
	}
	for _, d := range f.Structs {
		s := c.mod.structs[d.Name]
		if s == nil || s.Decl != d {
			continue
		}
		if s.CStruct {
			c.cStructFields(s, d.Fields)
			continue
		}
		seen := map[string]bool{}
		for _, fd := range d.Fields {
			if seen[fd.Name] {
				c.errorf(fd.P, "repeated field %s in %s", fd.Name, d.Name)
				continue
			}
			seen[fd.Name] = true
			if s.Params != nil {
				continue
			}
			s.Fields = append(s.Fields, &Field{Name: fd.Name, Type: c.resolveType(fd.Type, ctxField)})
		}
	}

	const (
		unvisited = iota
		visiting
		done
	)
	state := map[*Struct]int{}
	reported := map[*Struct]bool{}
	var path []*Struct
	var visit func(s *Struct) bool
	visit = func(s *Struct) bool {
		switch state[s] {
		case done:
			return true
		case visiting:
			names := []string{}
			start := 0
			for i, p := range path {
				if p == s {
					start = i
				}
			}
			for _, p := range path[start:] {
				names = append(names, p.Name)
			}
			names = append(names, s.Name)
			if reported[s] {
				return false // the same cycle through another field: one error is enough
			}
			reported[s] = true
			c.errorf(s.Decl.P, "struct %s contains itself by value (%s); that will be possible with pointers",
				s.Name, strings.Join(names, " → "))
			return false
		}
		state[s] = visiting
		path = append(path, s)
		ok := true
		for _, f := range s.Fields {
			// [N]T also keeps T by value
			t := f.Type
			for a, isArray := t.(*Array); isArray; a, isArray = t.(*Array) {
				t = a.Elem
			}
			if inner, isStruct := t.(*Struct); isStruct && !visit(inner) {
				// the field stays without a type: the cycle goes away, and the rest of the check
				// (size, owning fields) doesn't go into infinite recursion.
				// Every field is seen: two can close cycles.
				f.Type = Invalid
				ok = false
			}
		}
		path = path[:len(path)-1]
		state[s] = done
		if ok {
			for _, f := range s.Fields {
				if IsOwner(f.Type) || isResource(f.Type) {
					s.Resource = true
				}
			}
			c.info.Structs = append(c.info.Structs, s)
		}
		return ok
	}
	for _, d := range f.Structs {
		if s := c.mod.structs[d.Name]; s != nil && s.Decl == d && s.Params == nil {
			visit(s)
		}
	}
	// Instances of generic structs created in the fields above: the Resource of the
	// structs they contain is only ready now.
	for _, s := range c.mod.structs {
		for _, inst := range s.instances {
			for _, f := range inst.Fields {
				if IsOwner(f.Type) || isResource(f.Type) {
					inst.Resource = true
				}
			}
		}
	}
}

// composite checks "Type{field: value, ...}". Every field must be
// listed; "Type{}" is the zero value written explicitly.
func (c *checker) composite(e *ast.CompositeLit) Type {
	allowed := c.litOK
	c.litOK = false
	var s *Struct
	if len(e.TypeArgs) > 0 || c.genericStruct(e) {
		// generic struct: Pair[int, string]{...}
		te := &ast.TypeExpr{Name: e.Type.Name, P: e.Type.P}
		if e.Module != nil {
			te.Module = e.Module.Name
		}
		for _, a := range e.TypeArgs {
			te.Args = append(te.Args, typeExprOf(a))
		}
		inst, ok := c.resolveType(te, ctxField).(*Struct)
		if !ok {
			for _, fi := range e.Fields {
				c.value(fi.Value)
			}
			return Invalid
		}
		return c.compositeOf(e, inst, allowed)
	}
	if e.Module != nil {
		// a literal of a type from another file: geometry.Point{...}
		if c.userModule(e.Module) == nil {
			c.errorf(e.Module.P, "module %s wasn't imported", e.Module.Name)
			return Invalid
		}
		if t, ok := c.moduleStruct(e.Module.Name, e.Type.Name, e.Type.P).(*Struct); ok {
			s = t
		}
	} else if s = c.mod.structs[e.Type.Name]; s == nil {
		c.errorf(e.Type.P, "%s isn't a struct", e.Type.Name)
	}
	if s == nil {
		for _, fi := range e.Fields {
			c.value(fi.Value)
		}
		return Invalid
	}
	return c.compositeOf(e, s, allowed)
}

// compositeOf checks the fields of s's literal.
func (c *checker) compositeOf(e *ast.CompositeLit, s *Struct, allowed bool) Type {
	if s.Extern {
		c.errorf(e.Type.P, "%s is an opaque C type: the values come from C functions", s.Name)
		return Invalid
	}
	if s.Resource && !allowed {
		c.errorf(e.Type.P, "a literal of %s, which contains owning pointers, can only appear inside alloc(...)", s.Name)
	}
	given := map[string]token.Pos{}
	for _, fi := range e.Fields {
		f := s.Field(fi.Name)
		if f != nil && s.CStruct {
			// a callback field takes a function's name; an anonymous struct has no literal
			if ft, ok := f.Type.(*FuncType); ok {
				given[fi.Name] = fi.P
				c.callbackArg(fi.Value, ft, "field "+fi.Name)
				continue
			}
			if isAnonCStruct(f.Type) {
				c.value(fi.Value)
				c.errorf(fi.P, "%s is an anonymous C struct, which has no literal: set its fields after (x.%s.field = ...)", fi.Name, fi.Name)
				continue
			}
		}
		var vt Type
		if _, isLit := fi.Value.(*ast.CompositeLit); isLit && f != nil && isResource(f.Type) {
			c.litOK = true // a literal nested inside alloc's literal
			vt = c.value(fi.Value)
		} else {
			vt = c.value(fi.Value)
			if isUntyped(vt) && f != nil && isNumeric(f.Type) {
				c.convertUntyped(fi.Value, f.Type)
				vt = f.Type
			}
			c.noCopy(vt, fi.Value.Pos())
		}
		switch {
		case f == nil:
			c.errorf(fi.P, "%s has no field %s", s.Name, fi.Name)
			continue
		case given[fi.Name] != (token.Pos{}):
			c.errorf(fi.P, "repeated field %s in the literal", fi.Name)
			continue
		}
		given[fi.Name] = fi.P
		c.assignable(vt, f.Type, fi.Value.Pos(), "field "+fi.Name)
	}
	if len(e.Fields) > 0 && !s.CStruct { // C's struct has fields MLC doesn't see: the ones left out are zero
		for _, f := range s.Fields {
			if _, ok := given[f.Name]; !ok {
				c.errorf(e.Lbrace, "field %s not initialized (list every field, or use %s{} for the zero value)", f.Name, s.Name)
			}
		}
	}
	return s
}

// selector checks "x.field" when read.
func (c *checker) selector(e *ast.SelectorExpr) Type {
	if en := c.enumNamed(e.X); en != nil {
		return c.enumCtor(e, nil, en, e) // Shape.Empty
	}
	if m := c.userModule(e.X); m != nil {
		return c.moduleConst(e, m)
	}
	saved := c.anonOK
	c.anonOK = true // x.flags.f: the anonymous struct is a path here
	xt := c.value(e.X)
	c.anonOK = saved
	return c.cFieldRead(e, c.field(xt, e))
}

func (c *checker) field(t Type, e *ast.SelectorExpr) Type {
	if t == Invalid {
		return Invalid
	}
	// p.field works through pointers and borrows, as in Go.
	t, _ = c.deref(t, e.X.Pos(), exprName(e.X))
	if t == Invalid {
		return Invalid
	}
	s, ok := t.(*Struct)
	if !ok {
		c.errorf(e.SelPos, "a value of type %s has no fields", t)
		return Invalid
	}
	f := s.Field(e.Sel)
	if f == nil {
		c.errorf(e.SelPos, "%s has no field %s", s.Name, e.Sel)
		return Invalid
	}
	return f.Type
}

func isResource(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.Resource
}

// noCopy refuses to copy a struct value that contains owning pointers.
func (c *checker) noCopy(t Type, pos token.Pos) {
	if isResource(t) {
		c.errorf(pos, "a value of type %s contains owning pointers and can't be copied; use it through a pointer (*%s)", t, t)
	}
}

// hasNonNullOwner says whether t keeps, by value, a *T field, which can't
// start zeroed (nil).
func hasNonNullOwner(t Type) (string, bool) {
	s, ok := t.(*Struct)
	if !ok {
		return "", false
	}
	for _, f := range s.Fields {
		if p, isPtr := f.Type.(*Pointer); isPtr && !p.Nullable {
			return s.Name + "." + f.Name, true
		}
		if name, ok := hasNonNullOwner(f.Type); ok {
			return name, true
		}
	}
	return "", false
}

// genericStruct says whether the literal e is of a (local) generic struct.
func (c *checker) genericStruct(e *ast.CompositeLit) bool {
	if e.Module != nil {
		if m := c.userModule(e.Module); m != nil {
			s := m.structs[e.Type.Name]
			return s != nil && s.Params != nil
		}
		return false
	}
	s := c.mod.structs[e.Type.Name]
	return s != nil && s.Params != nil
}
