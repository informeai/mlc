package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Struct é um tipo struct declarado pelo usuário.
type Struct struct {
	Name    string
	Methods map[string]*Func
	CName   string // nome no C, sem o prefixo mlc_st_
	File    string
	Pub     bool
	Fields  []*Field
	Decl    *ast.StructDecl

	// Resource: a struct contém ponteiros donos (direto ou numa struct
	// guardada por valor). Valores dela só existem atrás de um ponteiro:
	// copiar duplicaria os donos.
	Resource bool

	// Extern: extern "C" type, um tipo opaco de C (sem campos), que só
	// existe atrás de ponteiro, como handle
	Extern bool

	// ThreadResult: num thread.Thread[R], o tipo do resultado (Void se não
	// tem); nil nas outras structs
	ThreadResult Type

	// ChanEnd: num chan.Sender[T] ou chan.Receiver[T], qual ponta é
	// (ChanSender, ChanReceiver), e ChanElem o tipo dos valores; 0 nas outras
	ChanEnd  int
	ChanElem Type

	// MutexKind: num mutex.Mutex[T] ou mutex.Guard[T], qual dos dois é
	// (MutexHandle, MutexGuard), e MutexElem o tipo do valor; 0 nas outras
	MutexKind int
	MutexElem Type

	// Genéricos: Params na struct genérica (a declaração); Origin e Args numa
	// instância (Par[int, string]).
	Params      []*ast.TypeParam
	Origin      *Struct
	Args        []Type
	instances   map[string]*Struct
	methodDecls map[string]*ast.FuncDecl
	// methodConstraints: restrições que o receptor de um método acrescenta
	// (fn (v &Vec[T any]) get): uma por parâmetro de tipo, nil se nenhuma
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

// collectStructs registra todas as structs antes de resolver os campos, para
// que a ordem de declaração não importe. Depois ordena as structs de forma
// que cada uma venha depois das que ela contém, que é a ordem exigida pelo C,
// e rejeita struct que contém a si mesma por valor.
func (c *checker) collectStructs(f *ast.File) {
	for _, d := range f.Structs {
		switch {
		case predeclared[d.Name]:
			c.errorf(d.P, "o nome %q é reservado", d.Name)
			continue
		case c.mod.structs[d.Name] != nil:
			c.errorf(d.P, "struct %s já declarada na linha %d", d.Name, c.mod.structs[d.Name].Decl.P.Line)
			continue
		case c.mod.ifaces[d.Name] != nil:
			c.errorf(d.P, "%s já foi declarado neste arquivo", d.Name)
			continue
		}
		s := &Struct{
			Name: c.qualify(d.Name), Methods: map[string]*Func{}, CName: c.mod.prefix + d.Name, File: c.mod.file, Pub: d.Pub, Decl: d,
			mod: c.mod, Extern: d.Extern,
		}
		if len(d.TypeParams) > 0 {
			// genérica: os campos são resolvidos em cada instância
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
		seen := map[string]bool{}
		for _, fd := range d.Fields {
			if seen[fd.Name] {
				c.errorf(fd.P, "campo %s repetido em %s", fd.Name, d.Name)
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
				return false // o mesmo ciclo por outro campo: um erro basta
			}
			reported[s] = true
			c.errorf(s.Decl.P, "struct %s contém a si mesma por valor (%s); isso vai ser possível com ponteiros",
				s.Name, strings.Join(names, " → "))
			return false
		}
		state[s] = visiting
		path = append(path, s)
		ok := true
		for _, f := range s.Fields {
			// [N]T também guarda T por valor
			t := f.Type
			for a, isArray := t.(*Array); isArray; a, isArray = t.(*Array) {
				t = a.Elem
			}
			if inner, isStruct := t.(*Struct); isStruct && !visit(inner) {
				// o campo fica sem tipo: o ciclo some, e o resto da checagem
				// (tamanho, campos donos) não entra em recursão infinita.
				// Todos os campos são vistos: dois podem fechar ciclos.
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
	// Instâncias de structs genéricas criadas nos campos acima: o Resource das
	// structs que elas contêm só ficou pronto agora.
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

// composite checa "Tipo{campo: valor, ...}". Todos os campos precisam ser
// listados; "Tipo{}" é o valor zero escrito de forma explícita.
func (c *checker) composite(e *ast.CompositeLit) Type {
	allowed := c.litOK
	c.litOK = false
	var s *Struct
	if len(e.TypeArgs) > 0 || c.genericStruct(e) {
		// struct genérica: Par[int, string]{...}
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
		// literal de um tipo de outro arquivo: geometria.Ponto{...}
		if c.userModule(e.Module) == nil {
			c.errorf(e.Module.P, "módulo %s não foi importado", e.Module.Name)
			return Invalid
		}
		if t, ok := c.moduleStruct(e.Module.Name, e.Type.Name, e.Type.P).(*Struct); ok {
			s = t
		}
	} else if s = c.mod.structs[e.Type.Name]; s == nil {
		c.errorf(e.Type.P, "%s não é uma struct", e.Type.Name)
	}
	if s == nil {
		for _, fi := range e.Fields {
			c.value(fi.Value)
		}
		return Invalid
	}
	return c.compositeOf(e, s, allowed)
}

// compositeOf checa os campos do literal de s.
func (c *checker) compositeOf(e *ast.CompositeLit, s *Struct, allowed bool) Type {
	if s.Extern {
		c.errorf(e.Type.P, "%s é um tipo de C opaco: os valores vêm de funções de C", s.Name)
		return Invalid
	}
	if s.Resource && !allowed {
		c.errorf(e.Type.P, "literal de %s, que contém ponteiros donos, só pode aparecer dentro de alloc(...)", s.Name)
	}
	given := map[string]token.Pos{}
	for _, fi := range e.Fields {
		f := s.Field(fi.Name)
		var vt Type
		if _, isLit := fi.Value.(*ast.CompositeLit); isLit && f != nil && isResource(f.Type) {
			c.litOK = true // literal aninhado dentro do literal de alloc
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
			c.errorf(fi.P, "%s não tem campo %s", s.Name, fi.Name)
			continue
		case given[fi.Name] != (token.Pos{}):
			c.errorf(fi.P, "campo %s repetido no literal", fi.Name)
			continue
		}
		given[fi.Name] = fi.P
		c.assignable(vt, f.Type, fi.Value.Pos(), "campo "+fi.Name)
	}
	if len(e.Fields) > 0 {
		for _, f := range s.Fields {
			if _, ok := given[f.Name]; !ok {
				c.errorf(e.Lbrace, "campo %s não inicializado (liste todos os campos, ou use %s{} para o valor zero)", f.Name, s.Name)
			}
		}
	}
	return s
}

// selector checa "x.campo" em leitura.
func (c *checker) selector(e *ast.SelectorExpr) Type {
	if en := c.enumNamed(e.X); en != nil {
		return c.enumCtor(e, nil, en, e) // Forma.Vazio
	}
	if m := c.userModule(e.X); m != nil {
		return c.moduleConst(e, m)
	}
	return c.field(c.value(e.X), e)
}

func (c *checker) field(t Type, e *ast.SelectorExpr) Type {
	if t == Invalid {
		return Invalid
	}
	// p.campo funciona através de ponteiros e empréstimos, como no Go.
	t, _ = c.deref(t, e.X.Pos(), exprName(e.X))
	if t == Invalid {
		return Invalid
	}
	s, ok := t.(*Struct)
	if !ok {
		c.errorf(e.SelPos, "valor do tipo %s não tem campos", t)
		return Invalid
	}
	f := s.Field(e.Sel)
	if f == nil {
		c.errorf(e.SelPos, "%s não tem campo %s", s.Name, e.Sel)
		return Invalid
	}
	return f.Type
}

func isResource(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.Resource
}

// noCopy recusa copiar um valor de struct que contém ponteiros donos.
func (c *checker) noCopy(t Type, pos token.Pos) {
	if isResource(t) {
		c.errorf(pos, "valor do tipo %s contém ponteiros donos e não pode ser copiado; use através de um ponteiro (*%s)", t, t)
	}
}

// hasNonNullOwner diz se t guarda, por valor, um campo *T, que não pode
// começar zerado (nil).
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

// genericStruct diz se o literal e é de uma struct genérica (local).
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
