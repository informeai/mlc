package checker

import (
	"fmt"
	"math/big"

	"mlc/internal/ast"
)

// Arrays [N]T
//
// Um array tem tamanho fixo, conhecido na compilação, e é um valor: vive na
// pilha ou dentro de uma struct, sem alloc nem free. Atribuir copia. &a
// empresta o array como &[]T, para usar com funções de slice e de texto.
// Nesta versão os elementos não podem ser donos.

type Array struct {
	Elem Type
	Len  int64
}

func (a *Array) String() string { return fmt.Sprintf("[%d]%s", a.Len, a.Elem) }

type arrKey struct {
	elem Type
	n    int64
}

// maxArrayBytes limita o tamanho de um array: ele pode ir para a pilha, que
// costuma ter 8 MB. Para mais, o caminho é alloc([]T, n).
const maxArrayBytes = 1 << 20

func (c *checker) arrayOf(elem Type, n int64) *Array {
	key := arrKey{elem, n}
	if a, ok := c.arrays[key]; ok {
		return a
	}
	a := &Array{Elem: elem, Len: n}
	c.arrays[key] = a
	if !abstract(elem) {
		c.info.Arrays = append(c.info.Arrays, a)
	}
	return a
}

func isArray(t Type) bool {
	_, ok := t.(*Array)
	return ok
}

// arrayElem devolve o tipo dos elementos de um array ou de um empréstimo de
// array.
func arrayElem(t Type) (*Array, bool) {
	if b, ok := t.(*Borrow); ok {
		t = b.Elem
	}
	a, ok := t.(*Array)
	return a, ok
}

func (c *checker) resolveArray(t *ast.TypeExpr, ctx typeCtx) Type {
	if ctx == ctxElem {
		c.errorf(t.P, "ponteiro para array não é suportado: use o array direto ([N]T), ou alloc([]T, n) para memória no heap")
		return Invalid
	}
	n, ok := c.arrayLen(t.Len)
	elem := c.resolveType(t.Elem, ctxArrayElem)
	if !ok || elem == Invalid {
		return Invalid
	}
	if IsOwner(elem) || isResource(elem) {
		c.errorf(t.Elem.P, "arrays de %s ainda não são suportados: os elementos não podem ser donos", elem)
		return Invalid
	}
	if size := sizeOf(elem) * n; size > maxArrayBytes {
		c.errorf(t.P, "array grande demais (%d bytes; o limite é %d, para não estourar a pilha): use alloc([]%s, %d)", size, maxArrayBytes, elem, n)
		return Invalid
	}
	return c.arrayOf(elem, n)
}

// arrayLen avalia o tamanho de [N]T: uma constante inteira maior que zero.
func (c *checker) arrayLen(e ast.Expr) (int64, bool) {
	if c.scope == nil {
		// tipos de campos e de parâmetros: só as constantes do topo valem
		c.scope = c.mod.global
		defer func() { c.scope = nil }()
	}
	t := c.value(e)
	if t == Invalid {
		return 0, false
	}
	v, isConst := c.info.Values[e].(*big.Int)
	if !isConst || (!isInteger(t) && t != UntypedInt) {
		c.errorf(e.Pos(), "o tamanho de um array precisa ser uma constante inteira")
		return 0, false
	}
	if v.Sign() <= 0 || !v.IsInt64() {
		c.errorf(e.Pos(), "o tamanho de um array precisa ser maior que zero, encontrado %s", v)
		return 0, false
	}
	return v.Int64(), true
}

// sizeOf estima quantos bytes um valor do tipo t ocupa (sem alinhamento).
func sizeOf(t Type) int64 {
	switch t := t.(type) {
	case *Enum:
		var max int64
		for _, v := range t.Variants {
			var n int64
			for _, f := range v.Fields {
				n += sizeOf(f.Type)
			}
			if n > max {
				max = n
			}
		}
		return max + 8
	case *Array:
		return sizeOf(t.Elem) * t.Len
	case *Struct:
		var n int64
		for _, f := range t.Fields {
			n += sizeOf(f.Type)
		}
		return n
	case *Pointer, *Borrow:
		return 8
	case *Slice:
		return 16
	}
	switch t {
	case Int8, Byte, Bool:
		return 1
	case Int16, Uint16:
		return 2
	case Int32, Uint32, Float32:
		return 4
	case String:
		return 16
	}
	return 8
}

// arrayLit checa "[3]int{1, 2, 3}": todos os elementos, ou nenhum (valor zero).
func (c *checker) arrayLit(e *ast.ArrayLit) Type {
	t := c.resolveType(e.Type, ctxVar)
	a, ok := t.(*Array)
	if !ok {
		for _, el := range e.Elems {
			c.value(el)
		}
		return Invalid
	}
	if n := int64(len(e.Elems)); n != 0 && n != a.Len {
		c.errorf(e.Lbrace, "o literal de %s tem %d elemento(s): liste os %d, ou use %s{} para o valor zero", a, n, a.Len, a)
	}
	for i, el := range e.Elems {
		c.valueAs(el, a.Elem, fmt.Sprintf("elemento %d", i))
	}
	return a
}

// checkArrayIndex recusa na compilação um índice constante fora do array.
func (c *checker) checkArrayIndex(a *Array, i ast.Expr) {
	if v, ok := c.info.Values[i].(*big.Int); ok && (v.Sign() < 0 || v.Cmp(big.NewInt(a.Len)) >= 0) {
		c.errorf(i.Pos(), "índice %s fora dos limites de %s (0 a %d)", v, a, a.Len-1)
	}
}
