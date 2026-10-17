package checker

import (
	"fmt"
	"path"

	"mlc/internal/ast"
	"mlc/internal/token"
	"mlc/std"
)

// Arquivos (módulos)
//
// Cada arquivo .mlc é um módulo. "import \"geometria\"" dá acesso às
// declarações públicas (pub) de geometria.mlc: geometria.area(p),
// geometria.Ponto{...}, geometria.PI. O que não é pub fica privado ao arquivo.
// A biblioteca padrão (import "std/io") também é feita de arquivos .mlc,
// embutidos no compilador.

type module struct {
	name   string // nome usado para qualificar (geometria); "" no principal
	path   string // caminho do import (util/texto); "" no principal
	file   string
	prefix string // prefixo dos nomes no C, para não colidir entre arquivos
	std    bool   // arquivo da biblioteca padrão
	test   bool   // arquivo de teste (só o principal)

	uses     map[string]*module // arquivos importados, pelo nome
	funcs    map[string]*Func
	generics map[string]*Func // funções genéricas (só a definição; as instâncias são criadas no uso)
	structs  map[string]*Struct
	enums    map[string]*Enum
	ifaces   map[string]*Interface
	global   *scope // constantes do topo; pai do escopo de cada função

	pendingConsts map[string]bool // constantes do topo ainda não avaliadas
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

// entry diz se o módulo em checagem é o arquivo principal.
func (c *checker) entry() bool { return c.mod.path == "" }

// qualify devolve o nome de uma declaração como quem importa o vê:
// geometria.Ponto. No arquivo principal, só o nome.
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
			c.errorf(imp.P, "módulo desconhecido %q: o arquivo não foi carregado", imp.Path)
			continue
		}
		switch {
		case c.mod.uses[m.name] == m:
			c.errorf(imp.P, "módulo %q importado duas vezes", imp.Path)
		case predeclared[m.name]:
			c.errorf(imp.P, "o arquivo %s.mlc não pode ser importado: %q é um nome reservado", imp.Path, m.name)
		case c.mod.uses[m.name] != nil:
			c.errorf(imp.P, "dois imports com o mesmo nome %s (%q e %q)", m.name, c.mod.uses[m.name].path, imp.Path)
		default:
			c.mod.uses[m.name] = m
		}
	}
}

// userModule devolve o arquivo importado com o nome de e, se e é um nome de
// módulo que nenhuma variável esconde.
func (c *checker) userModule(e ast.Expr) *module {
	id, ok := e.(*ast.Ident)
	if !ok || c.scope.lookup(id.Name) != nil {
		return nil
	}
	return c.mod.uses[id.Name]
}

// moduleStruct procura a struct pública name no arquivo importado mod.
func (c *checker) moduleStruct(modName string, name string, pos token.Pos) Type {
	m := c.mod.uses[modName]
	if m == nil {
		c.errorf(pos, "módulo %s não foi importado", modName)
		return Invalid
	}
	s := m.structs[name]
	switch {
	case s == nil:
		c.errorf(pos, "%s.%s não existe", modName, name)
		return Invalid
	case !s.Pub:
		c.errorf(pos, "%s.%s não é pública (declare com pub struct)", modName, name)
		return Invalid
	}
	return s
}

// moduleCall checa geometria.area(...).
func (c *checker) moduleCall(e *ast.CallExpr, m *module, sel *ast.SelectorExpr) Type {
	fn := m.funcs[sel.Sel]
	switch {
	case fn == nil:
		c.errorf(sel.SelPos, "%s.%s não existe", m.name, sel.Sel)
	case !fn.Pub:
		c.errorf(sel.SelPos, "%s.%s não é pública (declare com pub fn)", m.name, sel.Sel)
	default:
		return c.userCall(e, fn)
	}
	c.checkArgs(e)
	return Invalid
}

// moduleConst checa geometria.PI usado como valor.
func (c *checker) moduleConst(e *ast.SelectorExpr, m *module) Type {
	l := m.global.names[e.Sel]
	switch {
	case ioBuiltin(m, e.Sel) != "":
		c.errorf(e.SelPos, "%s.%s só pode ser usado como chamada", m.name, e.Sel)
	case l != nil && !l.Pub:
		c.errorf(e.SelPos, "%s.%s não é pública (declare com pub const)", m.name, e.Sel)
	case l != nil:
		c.info.Values[e] = l.Const
		return l.Type
	case m.funcs[e.Sel] != nil && !m.funcs[e.Sel].Pub:
		c.errorf(e.SelPos, "%s.%s não é pública (declare com pub fn)", m.name, e.Sel)
	case m.funcs[e.Sel] != nil:
		return c.funcRef(e, m.funcs[e.Sel])
	case m.structs[e.Sel] != nil:
		c.errorf(e.SelPos, "tipo %s.%s usado como valor (para criar um valor: %s.%s{...})", m.name, e.Sel, m.name, e.Sel)
	default:
		c.errorf(e.SelPos, "%s.%s não existe", m.name, e.Sel)
	}
	return Invalid
}

// StdModule diz se name é um módulo da biblioteca padrão (std/name).
func StdModule(name string) bool {
	_, err := std.FS.ReadFile(name + ".mlc")
	return err == nil
}
