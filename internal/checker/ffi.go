package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Funções de C (FFI)
//
//	link "sqlite3"
//	extern "C" type Banco                                  // handle opaco de C
//	extern "C" fn abre(nome string) ?*Banco                  // um handle (ou NULL)
//	extern "C" fn strlen(s string) uint64
//	extern "C" fn memcmp(a &[]byte, b &[]byte, n uint64) int32
//	extern "C" fn fclose(f *Arquivo) int32                 // consome o handle
//
// Qualquer arquivo pode declarar funções de C. A fronteira é conferida: só
// passam números, bool, string (um literal, terminado em zero), &[]T (vai o
// ponteiro para os dados), &T e handles de C (*X e ?*X, com X um extern
// "C" type). Nada que seja dono de memória do MLC atravessa: o C não pode
// liberar nem guardar memória do MLC. A assinatura precisa bater com a do C
// (int do C é int32; size_t é uint64), e funções variádicas (printf) não
// são aceitas.
//
// Um handle de C é um dono como os outros: o verificador cobra que ele seja
// passado a uma função de C que o recebe por posse (*X), não deixa usar free
// nele nem ver o conteúdo. Uma função de C que devolve um handle devolve ?*X,
// porque pode devolver NULL.
//
// Um parâmetro &?*X (OutHandle) é um handle devolvido por ponteiro, como em
// int sqlite3_open(const char *, sqlite3 **): quem chama passa &db, com db
// uma variável ?*X que não tem handle (nil ou já fechada), e depois da
// chamada db pode ter um handle novo, que o verificador obriga a fechar.

// OutHandle é o tipo &?*X de um parâmetro de saída de uma função de C: ela
// escreve um handle novo (ou NULL) na variável emprestada.
type OutHandle struct{ Ptr *Pointer }

func (o *OutHandle) String() string { return "&" + o.Ptr.String() }

func isOutHandle(t Type) bool {
	_, ok := t.(*OutHandle)
	return ok
}

// outHandleType resolve &?*X na assinatura de uma função de C.
func (c *checker) outHandleType(t *ast.TypeExpr) Type {
	elem := c.resolveType(t.Elem.Elem, ctxElem)
	if elem == Invalid {
		return Invalid
	}
	if !isExternStruct(elem) {
		c.errorf(t.P, "&?*T só serve para handles de C (?*X, com X um extern \"C\" type): a função de C escreve o handle na variável")
		return Invalid
	}
	p := c.pointerTo(elem, true)
	if oh, ok := c.outHandles[p]; ok {
		return oh
	}
	oh := &OutHandle{Ptr: p}
	c.outHandles[p] = oh
	return oh
}

// outArg checa o argumento de um parâmetro &?*X: &v, com v uma variável ou
// campo do tipo ?*X.
func (c *checker) outArg(a ast.Expr, oh *OutHandle, context string) {
	u, ok := a.(*ast.UnaryExpr)
	if !ok || u.Op != token.AMP {
		c.value(a)
		c.errorf(a.Pos(), "%s: esperado %s; passe a variável que recebe o handle com &: f(&h)", context, oh)
		return
	}
	switch u.X.(type) {
	case *ast.Ident, *ast.SelectorExpr:
	default:
		c.peekType(a)
		c.errorf(a.Pos(), "%s: o handle precisa ir para uma variável ou campo ?*X: f(&h)", context)
		return
	}
	t := c.value(u.X)
	if t != Invalid && t != oh.Ptr {
		c.errorf(a.Pos(), "%s: esperado %s, encontrado &%s (declare var h %s = nil)", context, oh, t, oh.Ptr)
	}
	c.info.Types[a] = oh
}

// cScalar diz se t passa direto para o C.
func cScalar(t Type) bool {
	return isNumeric(t) || t == Bool
}

// cStructOK diz se uma struct do MLC pode ser emprestada ao C: só campos
// escalares (o C vê a mesma disposição na memória).
func cStructOK(s *Struct) bool {
	if s.Resource || s.Origin != nil {
		return false
	}
	for _, f := range s.Fields {
		if !cScalar(f.Type) {
			return false
		}
	}
	return true
}

func isExternStruct(t Type) bool {
	s, ok := t.(*Struct)
	return ok && s.Extern
}

// cParamOK explica por que t não pode ser parâmetro de uma função de C, ou
// devolve "".
func cParamOK(t Type) string {
	switch tt := t.(type) {
	case *OutHandle:
		return ""
	case *Borrow:
		switch e := tt.Elem.(type) {
		case *Slice:
			if cScalar(e.Elem) {
				return ""
			}
			return "um slice emprestado ao C precisa ter elementos numéricos ou bool"
		case *Struct:
			if e.Extern || cStructOK(e) {
				return ""
			}
			return "uma struct emprestada ao C só pode ter campos numéricos ou bool"
		}
		if cScalar(tt.Elem) {
			return ""
		}
		return "esse empréstimo não tem representação em C"
	case *Pointer:
		if isExternStruct(tt.Elem) {
			return "" // a função de C recebe a posse do handle
		}
		return "memória do MLC não pode ser entregue ao C (ele não saberia liberá-la); empreste com &"
	case *Slice:
		return "um slice dono não pode ser entregue ao C; empreste com &[]T"
	}
	if cScalar(t) || t == String {
		return ""
	}
	return "esse tipo não tem representação em C"
}

// cResultOK explica por que t não pode ser retorno de uma função de C, ou
// devolve "".
func cResultOK(t Type) string {
	if t == Void || cScalar(t) {
		return ""
	}
	if p, ok := t.(*Pointer); ok && isExternStruct(p.Elem) {
		if !p.Nullable {
			return "uma função de C pode devolver NULL: o retorno precisa ser ?" + t.String()
		}
		return ""
	}
	return "uma função de C só devolve números, bool ou um handle (?*X, com X um extern \"C\" type)"
}

// collectExternC registra uma função de C declarada com extern "C" fn.
func (c *checker) collectExternC(fn *Func, d *ast.FuncDecl) {
	if len(d.TypeParams) > 0 || d.Recv != nil {
		c.errorf(d.P, "uma função de C não pode ser genérica nem método")
		return
	}
	for i, p := range fn.Params {
		if p.Type == Invalid {
			continue // o tipo já deu erro
		}
		if why := cParamOK(p.Type); why != "" {
			c.errorf(d.Params[i].P, "parâmetro %s de %s (%s): %s", p.Name, d.Name, p.Type, why)
		}
	}
	if why := cResultOK(fn.Result); why != "" {
		c.errorf(d.P, "retorno de %s (%s): %s", d.Name, fn.Result, why)
	}
	fn.CSym = d.Name
	fn.CName = "mlc_c_" + d.Name
	c.mod.funcs[d.Name] = fn
	for _, prev := range c.info.CExterns {
		if prev.CSym == d.Name {
			return // a mesma função de C declarada em dois arquivos
		}
	}
	c.info.CExterns = append(c.info.CExterns, fn)
}

// collectLinks registra as bibliotecas de C pedidas com link "nome".
func (c *checker) collectLinks(f *ast.File) {
	for _, l := range f.Links {
		if l.Lib == "" || strings.ContainsAny(l.Lib, "/\\ ") || strings.HasPrefix(l.Lib, "-") {
			c.errorf(l.P, "link %q: escreva só o nome da biblioteca, como link \"sqlite3\" (para -lsqlite3)", l.Lib)
			continue
		}
		dup := false
		for _, prev := range c.info.Links {
			dup = dup || prev == l.Lib
		}
		if !dup {
			c.info.Links = append(c.info.Links, l.Lib)
		}
	}
}
