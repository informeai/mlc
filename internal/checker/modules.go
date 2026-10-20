package checker

import (
	"fmt"
	"path"

	"mlc/internal/ast"
	"mlc/internal/token"
	"mlc/std"
)

// Files (modules)
//
// Each .mlc file is a module. "import \"geometry\"" gives access to the
// public (pub) declarations of geometry.mlc: geometry.area(p),
// geometry.Point{...}, geometry.PI. What isn't pub stays private to the file.
// The standard library (import "std/io") is also made of .mlc files,
// built into the compiler.

type module struct {
	name   string // name used to qualify (geometry); "" in the main file
	path   string // import path (util/text); "" in the main file
	file   string
	prefix string // prefix of the names in C, so they don't collide between files
	std    bool   // a standard library file
	test   bool   // a test file (only the main one)

	uses     map[string]*module // imported files, by name
	funcs    map[string]*Func
	generics map[string]*Func // generic functions (only the definition; the instances are created on use)
	structs  map[string]*Struct
	enums    map[string]*Enum
	ifaces   map[string]*Interface
	global   *scope // top-level constants; parent of each function's scope

	pendingConsts map[string]bool // top-level constants not evaluated yet
}

func (c *checker) newModule(src Source, i int) *module {
	m := &module{
		path: src.Path, file: src.File,
		std: src.Std, test: src.Test,
		uses:  map[string]*module{},
		funcs: map[string]*Func{}, structs: map[string]*Struct{},
		generics:      map[string]*Func{},
		enums:         map[string]*Enum{},
		ifaces:        map[string]*Interface{},
		global:        &scope{names: map[string]*Local{}},
		pendingConsts: map[string]bool{},
	}
	if src.Path != "" {
		m.name = path.Base(src.Path)
		m.prefix = fmt.Sprintf("m%d_%s__", i, m.name)
		c.modules[src.Path] = m
	}
	return m
}

// entry says whether the module being checked is the main file.
func (c *checker) entry() bool { return c.mod.path == "" }

// qualify returns a declaration's name as the importer sees it:
// geometry.Point. In the main file, just the name.
func (c *checker) qualify(name string) string {
	if c.mod.name == "" {
		return name
	}
	return c.mod.name + "." + name
}

func (c *checker) collectImports(f *ast.File) {
	for _, imp := range f.Imports {
		m := c.modules[imp.Path]
		if m == nil {
			c.errorf(imp.P, "unknown module %q: the file wasn't loaded", imp.Path)
			continue
		}
		switch {
		case c.mod.uses[m.name] == m:
			c.errorf(imp.P, "module %q imported twice", imp.Path)
		case predeclared[m.name]:
			c.errorf(imp.P, "the file %s.mlc can't be imported: %q is a reserved name", imp.Path, m.name)
		case c.mod.uses[m.name] != nil:
			c.errorf(imp.P, "two imports with the same name %s (%q and %q)", m.name, c.mod.uses[m.name].path, imp.Path)
		default:
			c.mod.uses[m.name] = m
		}
	}
}

// userModule returns the imported file with e's name, if e is a module
// name no variable hides.
func (c *checker) userModule(e ast.Expr) *module {
	id, ok := e.(*ast.Ident)
	if !ok || c.scope.lookup(id.Name) != nil {
		return nil
	}
	return c.mod.uses[id.Name]
}

// moduleStruct looks for the public struct name in the imported file mod.
func (c *checker) moduleStruct(modName string, name string, pos token.Pos) Type {
	m := c.mod.uses[modName]
	if m == nil {
		c.errorf(pos, "module %s wasn't imported", modName)
		return Invalid
	}
	s := m.structs[name]
	switch {
	case s == nil:
		c.errorf(pos, "%s.%s doesn't exist", modName, name)
		return Invalid
	case !s.Pub:
		c.errorf(pos, "%s.%s isn't public (declare it with pub struct)", modName, name)
		return Invalid
	}
	return s
}

// moduleCall checks geometry.area(...).
func (c *checker) moduleCall(e *ast.CallExpr, m *module, sel *ast.SelectorExpr) Type {
	fn := m.funcs[sel.Sel]
	switch {
	case fn == nil:
		c.errorf(sel.SelPos, "%s.%s doesn't exist", m.name, sel.Sel)
	case !fn.Pub:
		c.errorf(sel.SelPos, "%s.%s isn't public (declare it with pub fn)", m.name, sel.Sel)
	default:
		return c.userCall(e, fn)
	}
	c.checkArgs(e)
	return Invalid
}

// moduleConst checks geometry.PI used as a value.
func (c *checker) moduleConst(e *ast.SelectorExpr, m *module) Type {
	l := m.global.names[e.Sel]
	switch {
	case ioBuiltin(m, e.Sel) != "" || threadBuiltin(m, e.Sel) != "":
		c.errorf(e.SelPos, "%s.%s can only be used as a call", m.name, e.Sel)
	case l != nil && !l.Pub:
		c.errorf(e.SelPos, "%s.%s isn't public (declare it with pub const)", m.name, e.Sel)
	case l != nil:
		c.info.Values[e] = l.Const
		return l.Type
	case m.funcs[e.Sel] != nil && !m.funcs[e.Sel].Pub:
		c.errorf(e.SelPos, "%s.%s isn't public (declare it with pub fn)", m.name, e.Sel)
	case m.funcs[e.Sel] != nil:
		return c.funcRef(e, m.funcs[e.Sel])
	case m.structs[e.Sel] != nil:
		c.errorf(e.SelPos, "type %s.%s used as a value (to create a value: %s.%s{...})", m.name, e.Sel, m.name, e.Sel)
	default:
		c.errorf(e.SelPos, "%s.%s doesn't exist", m.name, e.Sel)
	}
	return Invalid
}

// StdModule says whether name is a standard library module (std/name).
func StdModule(name string) bool {
	_, err := std.FS.ReadFile(name + ".mlc")
	return err == nil
}
