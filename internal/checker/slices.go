package checker

import "mlc/internal/ast"

// Slices (Marco 6)
//
// []T é dono da memória dos elementos, como *T: criado com alloc([]T, n),
// com n elementos zerados, e liberado com free(s). O valor zero (var s []T)
// é o slice vazio, que não precisa de free. s[i] tem checagem de limites em
// tempo de execução; len(s) dá o tamanho; for x in s percorre cópias dos
// elementos. &[]T empresta o slice: lê e escreve elementos, sem liberar.
//
// Nesta versão os elementos não podem ser donos ([]*No, [][]int): a posse
// por elemento exigiria rastrear índices.

type Slice struct{ Elem Type }

func (s *Slice) String() string { return "[]" + s.Elem.String() }

func (c *checker) sliceOf(elem Type) *Slice {
	if s, ok := c.slices[elem]; ok {
		return s
	}
	s := &Slice{Elem: elem}
	c.slices[elem] = s
	c.info.Slices = append(c.info.Slices, s)
	return s
}

func isSlice(t Type) bool {
	_, ok := t.(*Slice)
	return ok
}

// sliceElem devolve o tipo dos elementos de um slice ou de um empréstimo de
// slice.
func sliceElem(t Type) (Type, bool) {
	if b, ok := t.(*Borrow); ok {
		t = b.Elem
	}
	if s, ok := t.(*Slice); ok {
		return s.Elem, true
	}
	return nil, false
}

func (c *checker) resolveSlice(t *ast.TypeExpr, ctx typeCtx) Type {
	switch ctx {
	case ctxElem:
		c.errorf(t.P, "ponteiro para slice não é suportado: o slice já é dono da memória (use []T, ou &[]T para emprestar)")
		return Invalid
	case ctxSliceElem:
		c.errorf(t.P, "slices de slices ainda não são suportados (os elementos não podem ser donos)")
		return Invalid
	}
	elem := c.resolveType(t.Elem, ctxSliceElem)
	if elem == Invalid {
		return Invalid
	}
	if isResource(elem) {
		c.errorf(t.Elem.P, "slices de %s ainda não são suportados: %s contém ponteiros donos, e os elementos não podem ser donos", elem, elem)
		return Invalid
	}
	return c.sliceOf(elem)
}

// allocSlice checa alloc([]T, n).
func (c *checker) allocSlice(e *ast.CallExpr, ta *ast.TypeArg) Type {
	t := c.resolveType(ta.Type, ctxVar)
	if len(e.Args) != 2 {
		c.errorf(e.Lparen, "alloc de slice precisa do tamanho: alloc(%s, n)", t)
		return Invalid
	}
	if nt := c.value(e.Args[1]); nt != Int && nt != Invalid {
		c.errorf(e.Args[1].Pos(), "o tamanho em alloc precisa ser int, encontrado %s", nt)
	}
	if t == Invalid {
		return Invalid
	}
	if !isSlice(t) {
		c.errorf(ta.Type.P, "alloc(T, n) serve para slices; para %s use alloc(%s)", t, t)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: Alloc, Elem: t, Slice: true}
	return t
}

// lenCall checa len(x), para slices e strings.
func (c *checker) lenCall(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "len recebe um argumento")
		c.checkArgs(e)
		return Invalid
	}
	t := c.value(e.Args[0])
	if _, ok := sliceElem(t); !ok && t != String && t != Invalid {
		c.errorf(e.Args[0].Pos(), "len se aplica a slices e strings, não a %s", t)
	}
	c.info.Calls[e] = Callee{Builtin: Len}
	return Int
}

// index checa s[i] em leitura: elemento de slice, ou byte de string (int).
func (c *checker) index(e *ast.IndexExpr) Type {
	bt := c.value(e.X)
	c.indexValue(e.Index)
	if elem, ok := sliceElem(bt); ok {
		return elem
	}
	switch bt {
	case String:
		return Int
	case Invalid:
		return Invalid
	}
	c.errorf(e.Lbrack, "valor do tipo %s não pode ser indexado", bt)
	return Invalid
}

func (c *checker) indexValue(i ast.Expr) {
	if t := c.value(i); t != Int && t != Invalid {
		c.errorf(i.Pos(), "o índice precisa ser int, encontrado %s", t)
	}
}

// targetIndex checa s[i] como destino de atribuição.
func (c *checker) targetIndex(e *ast.IndexExpr) Type {
	bt := c.value(e.X)
	c.indexValue(e.Index)
	if elem, ok := sliceElem(bt); ok {
		return elem
	}
	switch bt {
	case String:
		c.errorf(e.Lbrack, "strings não podem ser alteradas")
	case Invalid:
	default:
		c.errorf(e.Lbrack, "valor do tipo %s não pode ser indexado", bt)
	}
	return Invalid
}

// checkForIn checa "for x in s { }": s é uma variável ou campo slice, e x
// recebe uma cópia de cada elemento. O nome _ descarta o elemento.
func (c *checker) checkForIn(s *ast.ForStmt) {
	c.pushScope()
	t := c.value(s.Iter)
	elem, ok := sliceElem(t)
	switch {
	case t == Invalid:
		elem = Invalid
	case !ok:
		c.errorf(s.Iter.Pos(), "for in percorre slices, encontrado %s", t)
		elem = Invalid
	case !isPlace(s.Iter):
		c.errorf(s.Iter.Pos(), "for in percorre uma variável ou campo: guarde o slice em uma variável primeiro")
	}
	if s.Var.Name != "_" {
		c.info.ForVars[s] = c.declare(s.Var.Name, s.Var.P, elem, false)
	}
	c.loops = append(c.loops, s)
	c.checkBlock(s.Body)
	c.loops = c.loops[:len(c.loops)-1]
	c.popScope()
}

func (c *checker) noSliceCompare(e *ast.BinaryExpr, t Type) bool {
	if isSlice(t) {
		c.errorf(e.OpPos, "slices não podem ser comparados com ==; compare os elementos")
		return true
	}
	return false
}
