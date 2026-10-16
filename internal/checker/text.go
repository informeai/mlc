package checker

import "mlc/internal/ast"

// Texto e io.print
//
// Texto é montado num buffer []byte que quem programa aloca com alloc e
// cresce com realloc. As funções de texto (std/str) e de entrada e saída
// (std/io) nunca alocam: escrevem no buffer emprestado, devolvem quantos bytes
// usaram e dão panic se não couber. São funções comuns da biblioteca padrão
// (extern fn ou MLC), checadas como qualquer outra.
//
// A exceção é io.print, embutido no compilador: aceita qualquer quantidade de
// argumentos, de qualquer tipo, e o gerador emite uma chamada tipada por
// argumento.

// ioPrint checa io.print(a, b, ...).
func (c *checker) ioPrint(e *ast.CallExpr) Type {
	for _, a := range e.Args {
		t := c.valueDefault(a)
		elem, isSl := sliceElem(t)
		switch {
		case isPointerType(t) || t == NilType:
			c.errorf(a.Pos(), "io.print não imprime ponteiros (%s); imprima o conteúdo: *p ou p.campo", t)
		case isSl && (isPointerType(elem) || isSlicePtrElem(elem)):
			c.errorf(a.Pos(), "io.print não imprime %s: os elementos são ponteiros", t)
		default:
			c.noCopy(t, a.Pos())
		}
	}
	c.info.Calls[e] = Callee{Builtin: IoPrint}
	return Void
}
