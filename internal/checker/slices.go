package checker

import (
	"math/big"

	"mlc/internal/ast"
	"mlc/internal/token"
)

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
	if !abstract(elem) { // []T da definição genérica não vai para o C
		c.info.Slices = append(c.info.Slices, s)
	}
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
	}
	elem := c.resolveType(t.Elem, ctxSliceElem)
	if elem == Invalid {
		return Invalid
	}
	if ctx == ctxSliceElem && IsOwner(elem) {
		c.errorf(t.P, "slices de slices de donos ainda não são suportados ([][]T só com T simples)")
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
	c.intValue(e.Args[1], "o tamanho em alloc")
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
	if a, ok := arrayElem(t); ok {
		c.info.Calls[e] = Callee{Builtin: Len}
		if isPath(e.Args[0]) {
			c.info.Values[e] = big.NewInt(a.Len) // conhecido na compilação
		}
		return Int
	}
	if _, ok := sliceElem(t); !ok && t != String && t != Invalid {
		c.errorf(e.Args[0].Pos(), "len se aplica a slices, arrays e strings, não a %s", t)
	}
	c.info.Calls[e] = Callee{Builtin: Len}
	return Int
}

// index checa s[i] em leitura: elemento de slice, ou byte de string (int).
func (c *checker) index(e *ast.IndexExpr) Type {
	bt := c.value(e.X)
	c.indexValue(e.Index)
	if a, ok := arrayElem(bt); ok {
		c.checkArrayIndex(a, e.Index)
		return a.Elem
	}
	if elem, ok := sliceElem(bt); ok {
		return elem
	}
	switch bt {
	case String:
		return Byte
	case Invalid:
		return Invalid
	}
	c.errorf(e.Lbrack, "valor do tipo %s não pode ser indexado", bt)
	return Invalid
}

func (c *checker) indexValue(i ast.Expr) {
	c.intValue(i, "o índice")
}

// targetIndex checa s[i] como destino de atribuição.
func (c *checker) targetIndex(e *ast.IndexExpr) Type {
	bt := c.value(e.X)
	c.indexValue(e.Index)
	if a, ok := arrayElem(bt); ok {
		c.checkArrayIndex(a, e.Index)
		return a.Elem
	}
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
	iter := s.Iter
	drain := false
	if call, ok := iter.(*ast.CallExpr); ok {
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "drain" && c.scope.lookup("drain") == nil {
			drain = true
			if len(call.Args) != 1 {
				c.errorf(call.Lparen, "drain recebe um slice: for x in drain(s)")
				c.checkArgs(call)
				iter = nil
			} else {
				iter = call.Args[0]
				c.info.Drains[s] = iter
			}
		}
	}
	t, elem, ok := Type(Invalid), Type(Invalid), false
	if iter != nil {
		t = c.value(iter)
		elem, ok = sliceElem(t)
		if a, isArr := arrayElem(t); isArr {
			elem, ok = a.Elem, true
			if drain {
				c.errorf(iter.Pos(), "drain serve para slices de donos; para percorrer %s, use for x in a", t)
			}
		}
	}
	switch {
	case t == Invalid:
		elem = Invalid
	case !ok:
		c.errorf(iter.Pos(), "for in percorre slices, encontrado %s", t)
		elem = Invalid
	case !isPath(iter):
		c.errorf(iter.Pos(), "for in percorre uma variável ou campo: guarde o slice em uma variável primeiro")
	case drain && s.Var.Name == "_":
		c.errorf(s.Var.P, "o elemento tirado por drain precisa de um nome, para ser liberado ou movido")
	case drain && !IsOwner(elem) && !c.genericContext():
		c.errorf(iter.Pos(), "drain serve para slices de donos; para percorrer %s, use for x in s", t)
	case !drain && (IsOwner(elem) || ownedVar(elem)):
		c.errorf(iter.Pos(), "os elementos de %s são donos e não podem ser copiados: use for x in drain(s) para esvaziar o slice", t)
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

// reallocCall checa realloc(s, n): consome o slice s e devolve um slice novo
// com n elementos. Os elementos que já existiam são copiados; os novos vêm
// zerados.
func (c *checker) reallocCall(e *ast.CallExpr) Type {
	if len(e.Args) != 2 {
		c.errorf(e.Lparen, "realloc recebe o slice e o novo tamanho: s = realloc(s, n)")
		c.checkArgs(e)
		return Invalid
	}
	t := c.value(e.Args[0])
	c.intValue(e.Args[1], "o tamanho em realloc")
	switch {
	case t == Invalid:
		return Invalid
	case isBorrow(t):
		c.errorf(e.Args[0].Pos(), "realloc precisa do dono do slice: um empréstimo (%s) não pode ser realocado", t)
		return Invalid
	case !isSlice(t):
		c.errorf(e.Args[0].Pos(), "realloc se aplica a slices, não a %s", t)
		return Invalid
	}
	c.info.Calls[e] = Callee{Builtin: Realloc, Elem: t}
	return t
}

// subSlice checa s[a:b] e devolve o tipo do empréstimo: &[]T. Um sub-slice
// não é dono de nada: enxerga a memória de s.
func (c *checker) subSlice(e *ast.SliceExpr) Type {
	bt := c.value(e.X)
	for _, b := range []ast.Expr{e.Low, e.High} {
		if b != nil {
			c.indexValue(b)
		}
	}
	if bt == Invalid {
		return Invalid
	}
	if b, ok := bt.(*Borrow); ok {
		bt = b.Elem
	}
	if a, ok := bt.(*Array); ok {
		return c.borrowOf(c.sliceOf(a.Elem)) // parte de um array, vista como slice
	}
	s, ok := bt.(*Slice)
	if !ok {
		c.errorf(e.Lbrack, "sub-slice se aplica a slices e arrays, não a %s", bt)
		return Invalid
	}
	return c.borrowOf(s)
}

// isPath diz se e é uma variável ou um campo (p, p.a.b), sem índices.
func isPath(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return isPath(e.X)
	}
	return false
}

// ownerElem checa o argumento &s[i] de take e replace e devolve o tipo do
// elemento, que precisa ser dono.
func (c *checker) ownerElem(a ast.Expr, fn string) Type {
	u, ok := a.(*ast.UnaryExpr)
	var ix *ast.IndexExpr
	ok = ok && u.Op == token.AMP // !s[i] e -s[i] não são empréstimos
	if ok {
		ix, ok = u.X.(*ast.IndexExpr)
	}
	if !ok {
		c.errorf(a.Pos(), "%s recebe um elemento emprestado: %s(&s[i]...)", fn, fn)
		c.value(a)
		return Invalid
	}
	t := c.targetIndex(ix)
	c.info.Types[ix] = t
	c.info.Types[a] = t
	if t != Invalid && !IsOwner(t) && !c.genericContext() {
		c.errorf(a.Pos(), "%s serve para elementos donos; os elementos aqui são %s (use s[i] direto)", fn, t)
		return Invalid
	}
	return t
}

// takeCall checa take(&s[i]): devolve o elemento e deixa vazio no lugar.
func (c *checker) takeCall(e *ast.CallExpr) Type {
	if len(e.Args) != 1 {
		c.errorf(e.Lparen, "take recebe um elemento: take(&s[i])")
		c.checkArgs(e)
		return Invalid
	}
	t := c.ownerElem(e.Args[0], "take")
	c.info.Calls[e] = Callee{Builtin: Take, Elem: t}
	return t
}

// replaceCall checa replace(&s[i], v): põe v e devolve o elemento antigo.
func (c *checker) replaceCall(e *ast.CallExpr) Type {
	if len(e.Args) != 2 {
		c.errorf(e.Lparen, "replace recebe o elemento e o valor novo: replace(&s[i], v)")
		c.checkArgs(e)
		return Invalid
	}
	t := c.ownerElem(e.Args[0], "replace")
	if t != Invalid {
		c.valueAs(e.Args[1], t, "o valor de replace")
	} else {
		c.value(e.Args[1])
	}
	c.info.Calls[e] = Callee{Builtin: Replace, Elem: t}
	return t
}

// isSlicePtrElem diz se t é um slice de ponteiros (para []([]?*T), que não
// existe hoje, mas mantém io.print correto se vier a existir).
func isSlicePtrElem(t Type) bool {
	e, ok := sliceElem(t)
	return ok && isPointerType(e)
}
