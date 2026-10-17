package checker

import (
	"mlc/internal/ast"
	"mlc/internal/token"
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
// Valem os tipos sem dono e os donos que têm valor zero e sabem se liberar:
// ?*X (X sem donos, ou com um método fn (x *X) free()) e []E, com cada
// elemento E também assim (donos aninhados: [][]byte, []?*No). A existência
// do free é conferida depois (requireFree), quando os métodos já existem.
func ownedOK(t Type) (bool, string) {
	switch t := t.(type) {
	case *Pointer:
		if !t.Nullable {
			return false, "*T não tem valor zero e as coleções precisam de posições vazias: use ?" + t.String()
		}
		return ownedPointee(t.Elem)
	case *Slice:
		return ownedElem(t.Elem)
	case *Borrow:
		return false, "empréstimos não podem ser argumentos de tipo"
	}
	if isResource(t) {
		return false, "uma struct com donos não pode ser copiada: use ?*" + t.String()
	}
	if t == Void || t == NilType || t == Invalid || isUntyped(t) {
		return false, ""
	}
	return true, ""
}

// ownedPointee confere o tipo apontado por um ?*X usado como T owned.
func ownedPointee(t Type) (bool, string) {
	if IsOwner(t) {
		return false, "ponteiro para dono não é suportado"
	}
	return true, ""
}

// ownedElem confere o elemento de um []E usado como T owned: precisa ter
// valor zero (as posições novas vêm zeradas) e saber se liberar.
func ownedElem(t Type) (bool, string) {
	switch t := t.(type) {
	case *Pointer:
		if !t.Nullable {
			return false, "elementos de slice começam zerados: use []?" + t.String()
		}
		return ownedPointee(t.Elem)
	case *Slice:
		return ownedElem(t.Elem)
	}
	if isResource(t) {
		return false, "structs com donos só existem atrás de ponteiro: use []?*" + t.String()
	}
	return true, ""
}

// freeNeed é uma struct com donos que apareceu dentro de um T owned: ela
// precisa de um método fn (x *X) free() (conferido depois da coleta).
type freeNeed struct {
	s   *Struct
	pos token.Pos
	mod *module
	in  string // onde apareceu, para a mensagem
}

// requireFree anota as structs com donos alcançadas por t (como ?*X ou
// dentro de []?*X), que precisam saber se liberar.
func (c *checker) requireFree(t Type, pos token.Pos, in string) {
	switch t := t.(type) {
	case *Pointer:
		if s, ok := t.Elem.(*Struct); ok && s.Resource {
			c.freeNeeds = append(c.freeNeeds, freeNeed{s, pos, c.mod, in})
		}
	case *Slice:
		c.requireFree(t.Elem, pos, in)
	}
}

// checkFreeNeeds confere, com todos os métodos coletados, que cada struct
// com donos usada dentro de um T owned tem o método free.
func (c *checker) checkFreeNeeds() {
	saved := c.mod
	for _, n := range c.freeNeeds {
		if c.freeMethod(n.s) == nil {
			if c.freeMissing[n.s] {
				continue // um erro por struct basta
			}
			c.freeMissing[n.s] = true
			c.mod = n.mod
			c.errorf(n.pos, "%s tem campos donos e não sabe se liberar dentro de %s: declare fn (x *%s) free() liberando os campos e o próprio x", n.s, n.in, n.s.Name)
		}
	}
	c.mod = saved
	c.freeNeeds = nil
}

// freeMethod devolve o método fn (x *X) free() de s (instanciando-o, numa
// struct genérica), ou nil.
func (c *checker) freeMethod(s *Struct) *Func {
	m := s.Methods["free"]
	if m == nil && s.Origin != nil {
		if d := s.Origin.methodDecls["free"]; d != nil {
			m = c.instMethod(s, "free")
		}
	}
	if m == nil || m.recv != recvOwner || len(m.Params) != 1 || m.Result != Void {
		return nil
	}
	return m
}

// dropPlan registra o que drop(x) faz com um valor concreto do tipo t: nada
// (sem dono), free (dono simples), x.free() (struct que sabe se liberar) ou
// liberar cada elemento e depois o slice. Os tipos com drop não trivial vão
// para o gerador, que escreve uma função mlc_drop_ para cada um.
func (c *checker) dropPlan(t Type, pos token.Pos) {
	if abstract(t) || !IsOwner(t) || c.info.DropTypes[typeKey(t)] != nil {
		return
	}
	switch tt := t.(type) {
	case *Pointer:
		if s, ok := tt.Elem.(*Struct); ok && s.Resource {
			m := c.freeMethod(s)
			if m == nil {
				if !c.freeMissing[s] { // senão, o erro já saiu onde o tipo foi usado
					c.errorf(pos, "drop de %s: %s tem campos donos e precisa de fn (x *%s) free()", t, s, s.Name)
				}
				return
			}
			c.info.DropFree[s] = m
		}
	case *Slice:
		c.dropPlan(tt.Elem, pos)
	}
	c.info.DropTypes[typeKey(t)] = t
	c.info.DropOrder = append(c.info.DropOrder, t)
}

// deepDrop diz se drop de um valor do tipo t libera outros donos além do
// próprio valor (então é um move para a função de drop, e não um free).
func deepDrop(t Type) bool {
	switch tt := t.(type) {
	case *Pointer:
		return isResource(tt.Elem)
	case *Slice:
		return IsOwner(tt.Elem)
	}
	return false
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
		builtin, _, _ := splitConstraint(p.Constraint)
		candidates := []Type{Int}
		if builtin == "owned" {
			hasOwned = true
			candidates = []Type{c.sliceOf(Byte), Int}
		}
		var pick Type
		for _, cand := range candidates {
			if c.satisfiesParam(cand, p) {
				pick = cand
				break
			}
		}
		if pick == nil {
			return nil // nenhum tipo simples atende (uma interface sem impl conhecido)
		}
		args = append(args, pick)
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
				reason = " (" + s.Args[i].String() + " é dono, e o método copia): use os métodos que movem a posse, como take ou put"
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
	default:
		c.dropPlan(t, e.Args[0].Pos())
	}
	c.info.Calls[e] = Callee{Builtin: Drop}
	return Void
}
