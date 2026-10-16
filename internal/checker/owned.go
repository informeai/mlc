package checker

import (
	"mlc/internal/ast"
)

// Donos em genéricos
//
//	struct Vec[T owned] { items []T; count int }
//	fn (v &Vec[T]) push(x T) { ... }        // x é movido para dentro
//	fn (v &Vec[T any]) get(i int) T { ... } // só quando T é copiável
//
// A restrição owned aceita os tipos sem dono e os donos simples: ?*X e []X,
// com X sem donos dentro. Dentro do corpo, um valor do tipo T é tratado como
// um dono: atribuir, passar ou devolver move; usar depois de mover é erro;
// todo valor precisa de destino (mover, guardar ou drop(x)). drop(x) libera x
// se for dono (free) e não faz nada se não for.
//
// A posse é verificada nas instâncias concretas, como em todo genérico. Para
// os erros aparecerem já na definição, cada genérico com um parâmetro owned
// ganha uma instância de verificação com T = []byte (um dono com valor zero),
// que passa pelo verificador de posse; os erros dela dizem "verificado com T
// dono".

// ownedVar diz se t é um parâmetro de tipo com a restrição owned.
func ownedVar(t Type) bool {
	tv, ok := t.(*TypeVar)
	return ok && tv.Constraint == "owned"
}

// ownedOK diz se t pode ser argumento de um parâmetro owned, e por que não.
func ownedOK(t Type) (bool, string) {
	switch t := t.(type) {
	case *Pointer:
		if !t.Nullable {
			return false, "*T não tem valor zero e as coleções precisam de posições vazias: use ?" + t.String()
		}
		if IsOwner(t.Elem) || isResource(t.Elem) || hasOwnerFields(t.Elem) {
			return false, "donos dentro de donos ainda não podem ser argumentos de tipo"
		}
		return true, ""
	case *Slice:
		if IsOwner(t.Elem) || isResource(t.Elem) {
			return false, "donos dentro de donos ainda não podem ser argumentos de tipo"
		}
		return true, ""
	case *Borrow:
		return false, "empréstimos não podem ser argumentos de tipo"
	}
	if isResource(t) {
		return false, "structs com donos ainda não podem ser argumentos de tipo"
	}
	if t == Void || t == NilType || t == Invalid || isUntyped(t) {
		return false, ""
	}
	return true, ""
}

// hasOwnerFields diz se t é uma struct com campos donos.
func hasOwnerFields(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.Resource
}

// genericContext diz se o código checado está dentro de um genérico (na
// definição ou numa instância): lá, take, replace, drain e drop valem também
// para elementos que não são donos (T pode ser int numa instância).
func (c *checker) genericContext() bool { return c.typeParams != nil }

// probeArgs escolhe os tipos da instância de verificação de posse: []byte
// para os parâmetros owned e int para os outros. Devolve nil se não houver
// parâmetro owned, ou se algum parâmetro pedir uma interface (não há como
// escolher um tipo que a implemente).
func (c *checker) probeArgs(params []*ast.TypeParam) []Type {
	hasOwned := false
	var args []Type
	for _, p := range params {
		switch {
		case p.Constraint == "owned":
			hasOwned = true
			args = append(args, c.sliceOf(Byte))
		case constraints[p.Constraint]:
			args = append(args, Int)
		default:
			return nil
		}
	}
	if !hasOwned {
		return nil
	}
	return args
}

// probeOwned cria as instâncias de verificação dos genéricos com parâmetros
// owned declarados no arquivo.
func (c *checker) probeOwned(f *ast.File) {
	for _, d := range f.Funcs {
		if d.Recv != nil || len(d.TypeParams) == 0 {
			continue
		}
		g := c.mod.generics[d.Name]
		args := c.probeArgs(d.TypeParams)
		if g == nil || args == nil {
			continue
		}
		if inst := c.instFunc(g, args, d.P); inst != nil {
			c.probes[inst] = true
		}
	}
	for _, sd := range f.Structs {
		s := c.mod.structs[sd.Name]
		if s == nil || s.Params == nil {
			continue
		}
		args := c.probeArgs(s.Params)
		if args == nil {
			continue
		}
		inst, ok := c.instStruct(s, args, sd.P).(*Struct)
		if !ok {
			continue
		}
		for name := range s.methodDecls {
			if msg := c.methodConstraintError(inst, name); msg != "" {
				continue // método só para T copiável: não vale com T dono
			}
			if m := c.instMethod(inst, name); m != nil {
				c.probes[m] = true
			}
		}
	}
}

// methodConstraintError explica por que o método name não existe na
// instância s (o receptor pede uma restrição que os argumentos não atendem),
// ou devolve "".
func (c *checker) methodConstraintError(s *Struct, name string) string {
	if s.Origin == nil {
		return ""
	}
	cons := s.Origin.methodConstraints[name]
	for i, p := range cons {
		if p == nil || i >= len(s.Args) {
			continue
		}
		if !c.satisfiesParam(s.Args[i], p) {
			reason := ""
			if IsOwner(s.Args[i]) {
				reason = " (" + s.Args[i].String() + " é dono): use take, put ou pop"
			}
			return s.Name + "." + name + " exige " + p.Name + " " + p.Constraint + reason
		}
	}
	return ""
}

// dropCall checa drop(x): x é consumido; se for dono, é liberado.
func (c *checker) dropCall(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "drop recebe um valor: drop(x)")
		c.checkArgs(e)
		return Void
	}
	t := c.value(e.Args[0])
	switch {
	case t == Invalid:
	case !c.genericContext():
		c.errorf(e.Lparen, "drop só existe em código genérico, para um T owned; fora dele, use free")
	case isBorrow(t):
		c.errorf(e.Args[0].Pos(), "drop recebe o dono, não um empréstimo (%s)", t)
	}
	c.info.Calls[e] = Callee{Builtin: Drop}
	return Void
}
