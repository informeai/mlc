package checker

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Enums com dados
//
//	enum Forma {
//	    Circulo(raio float64)
//	    Vazio
//	}
//
// Um valor de enum é uma das variantes, com os dados dela. Constrói-se com o
// nome do enum na frente (Forma.Circulo(2.0), Forma.Vazio) e desmonta-se com
// match, que precisa cobrir todas as variantes:
//
//	match f {
//	    Circulo(r) => ...
//	    Vazio => ...
//	}
//
// Enums podem ser genéricos (enum Resultado[T any] { Ok(valor T); Erro(codigo
// int) }). Nesta versão os dados das variantes não podem ser donos, então um
// enum é um valor comum: copiável e imprimível.

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

// collectEnumNames registra os enums antes das structs, que podem ter campos
// de tipo enum.
func (c *checker) collectEnumNames(f *ast.File) {
	for _, d := range f.Enums {
		switch {
		case predeclared[d.Name] || modules[d.Name]:
			c.errorf(d.P, "o nome %q é reservado", d.Name)
			continue
		case c.mod.enums[d.Name] != nil:
			c.errorf(d.P, "enum %s já declarado na linha %d", d.Name, c.mod.enums[d.Name].Decl.P.Line)
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

// collectEnumVariants resolve as variantes dos enums não genéricos, depois
// das structs.
func (c *checker) collectEnumVariants(f *ast.File) {
	for _, d := range f.Enums {
		en := c.mod.enums[d.Name]
		if en == nil || en.Decl != d {
			continue
		}
		if c.mod.structs[d.Name] != nil {
			c.errorf(d.P, "o nome %s já é usado por uma struct", d.Name)
			continue
		}
		if en.Params == nil {
			c.variants(en)
			c.info.Enums = append(c.info.Enums, en)
		}
	}
}

// variants resolve as variantes de en (com os parâmetros de tipo já ligados,
// numa instância).
func (c *checker) variants(en *Enum) {
	seen := map[string]bool{}
	for i, vd := range en.Decl.Variants {
		if seen[vd.Name] {
			c.errorf(vd.P, "variante %s repetida em %s", vd.Name, en.Name)
			continue
		}
		seen[vd.Name] = true
		v := &Variant{Name: vd.Name, Tag: i, Enum: en}
		fseen := map[string]bool{}
		for _, fd := range vd.Fields {
			if fseen[fd.Name] {
				c.errorf(fd.P, "campo %s repetido em %s", fd.Name, vd.Name)
			}
			fseen[fd.Name] = true
			t := c.resolveType(fd.Type, ctxField)
			if IsOwner(t) || isResource(t) || isBorrow(t) {
				c.errorf(fd.P, "variantes não podem guardar donos nesta versão (%s é %s)", fd.Name, t)
				t = Invalid
			}
			v.Fields = append(v.Fields, &Field{Name: fd.Name, Type: t})
		}
		en.Variants = append(en.Variants, v)
	}
	if len(en.Variants) == 0 {
		c.errorf(en.Decl.P, "o enum %s precisa de pelo menos uma variante", en.Name)
	}
}

// instEnum devolve a instância de um enum genérico.
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

// resolveEnum resolve o tipo escrito t, que nomeia o enum en.
func (c *checker) resolveEnum(en *Enum, t *ast.TypeExpr) Type {
	switch {
	case en.Params != nil && len(t.Args) == 0:
		c.errorf(t.P, "%s é genérico: escreva os argumentos de tipo, como %s[int]", en.Name, en.Name)
		return Invalid
	case en.Params == nil && len(t.Args) > 0:
		c.errorf(t.P, "%s não é genérico", en.Name)
		return Invalid
	case en.Params != nil:
		var args []Type
		for _, a := range t.Args {
			args = append(args, c.resolveType(a, ctxVar))
		}
		return c.instEnum(en, args, t.P)
	}
	return en
}

// enumNamed devolve o enum escrito em x (Forma, Resultado[int], geo.Forma),
// se x nomear um. Um enum genérico sem argumentos volta como a declaração.
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
					c.errorf(x.SelPos, "%s.%s não é público (declare com pub enum)", m.name, x.Sel)
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

// enumCtor checa Forma.Circulo(args) (call não nil) ou Forma.Vazio.
func (c *checker) enumCtor(node ast.Expr, call *ast.CallExpr, en *Enum, sel *ast.SelectorExpr) Type {
	var args []ast.Expr
	if call != nil {
		args = call.Args
	}
	if en.Params != nil {
		// genérico sem argumentos de tipo: do contexto ou dos argumentos
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
		c.errorf(sel.SelPos, "%s não tem variante %s", en.Name, sel.Sel)
		for _, a := range args {
			c.value(a)
		}
		return Invalid
	}
	switch {
	case call == nil && len(v.Fields) > 0:
		c.errorf(sel.SelPos, "%s.%s tem %d campo(s): escreva %s.%s(...)", en.Name, v.Name, len(v.Fields), en.Name, v.Name)
		return Invalid
	case call != nil && len(args) != len(v.Fields):
		c.errorf(call.Lparen, "%s.%s espera %d valor(es), recebeu %d", en.Name, v.Name, len(v.Fields), len(args))
	}
	for i, a := range args {
		if i < len(v.Fields) {
			c.valueAs(a, v.Fields[i].Type, fmt.Sprintf("campo %s de %s", v.Fields[i].Name, v.Name))
		} else {
			c.value(a)
		}
	}
	c.info.EnumCtors[node] = v
	return en
}

// inferEnum acha a instância de um enum genérico usado sem argumentos de
// tipo: pelo tipo esperado (return Resultado.Ok(5) numa função que devolve
// Resultado[int]) ou pelos valores da variante.
func (c *checker) inferEnum(node ast.Expr, en *Enum, sel *ast.SelectorExpr, args []ast.Expr) *Enum {
	if want, ok := c.expected.(*Enum); ok && c.expectedFor == node && want.Origin == en {
		return want
	}
	var vars []Type
	for _, p := range en.Params {
		vars = append(vars, &TypeVar{Name: p.Name, Constraint: p.Constraint})
	}
	abs, ok := c.instEnum(en, vars, sel.SelPos).(*Enum)
	if !ok {
		return nil
	}
	v := abs.Variant(sel.Sel)
	if v == nil {
		c.errorf(sel.SelPos, "%s não tem variante %s", en.Name, sel.Sel)
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
			c.errorf(sel.SelPos, "não foi possível deduzir %s em %s.%s: escreva %s[int].%s(...)", t, en.Name, sel.Sel, en.Name, sel.Sel)
			return nil
		}
		targs = append(targs, b)
	}
	inst, _ := c.instEnum(en, targs, sel.SelPos).(*Enum)
	return inst
}

// checkEnumMatch checa um match sobre enum: cada braço nomeia variantes, e as
// de dados dão nome aos campos (Circulo(r)).
func (c *checker) checkEnumMatch(s *ast.MatchStmt, en *Enum) {
	seen := map[string]token.Pos{}
	var wildcard *ast.Pattern
	unreachable := false
	for _, arm := range s.Arms {
		if wildcard != nil && !unreachable {
			c.errorf(arm.P, "braço depois de _ nunca é executado")
			unreachable = true
		}
		c.pushScope()
		for _, p := range arm.Patterns {
			if p.Wildcard {
				if len(arm.Patterns) > 1 {
					c.errorf(p.P, "_ precisa estar sozinho no braço")
				}
				wildcard = p
				continue
			}
			c.variantPattern(p, en, len(arm.Patterns), seen)
		}
		c.checkStmt(arm.Body)
		c.popScope()
	}
	switch {
	case wildcard != nil && len(seen) == len(en.Variants):
		c.errorf(wildcard.P, "braço _ nunca é executado: todas as variantes de %s já foram cobertas", en.Name)
	case wildcard == nil:
		var missing []string
		for _, v := range en.Variants {
			if _, ok := seen[v.Name]; !ok {
				missing = append(missing, v.Name)
			}
		}
		if len(missing) > 0 {
			c.errorf(s.P, "match sobre %s precisa cobrir todas as variantes (faltam: %s) ou ter um braço _", en.Name, joinNames(missing))
		}
	}
}

func (c *checker) variantPattern(p *ast.Pattern, en *Enum, armSize int, seen map[string]token.Pos) {
	name, pos := "", p.P
	switch {
	case p.Variant != nil:
		name, pos = p.Variant.Name, p.Variant.P
	default:
		id, ok := p.Lit.(*ast.Ident)
		if !ok {
			c.errorf(p.P, "num match sobre %s, os padrões são variantes: Circulo(r), Vazio", en.Name)
			return
		}
		name, pos = id.Name, id.P
	}
	v := en.Variant(name)
	if v == nil {
		c.errorf(pos, "%s não tem variante %s", en.Name, name)
		return
	}
	switch {
	case p.Variant == nil && len(v.Fields) > 0:
		c.errorf(pos, "%s tem %d campo(s): escreva %s(...) dando um nome a cada um (ou _)", name, len(v.Fields), name)
	case p.Variant != nil && len(p.Binds) != len(v.Fields):
		c.errorf(pos, "%s tem %d campo(s), o padrão nomeia %d", name, len(v.Fields), len(p.Binds))
	case p.Variant != nil && armSize > 1:
		c.errorf(pos, "uma variante com campos nomeados precisa estar sozinha no braço")
	}
	if prev, dup := seen[name]; dup {
		c.errorf(pos, "variante %s repetida (já aparece na linha %d)", name, prev.Line)
		return
	}
	seen[name] = pos
	c.info.Patterns[p] = v
	var locals []*Local
	for i, b := range p.Binds {
		if b.Name == "_" || i >= len(v.Fields) {
			locals = append(locals, nil)
			continue
		}
		locals = append(locals, c.declare(b.Name, b.P, v.Fields[i].Type, false))
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
