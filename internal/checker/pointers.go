package checker

import (
	"fmt"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Ponteiros e empréstimos (docs/memoria.md)
//
//   - *T: ponteiro dono, nunca nil. Quem tem um *T precisa liberá-lo (free)
//     ou passar a posse adiante, em todos os caminhos.
//   - ?*T: ponteiro dono que pode ser nil. Para usar o conteúdo, é preciso
//     checar: dentro de "if q != nil { }", q é tratado como *T.
//   - &T: empréstimo. Só existe como parâmetro; lê e escreve o conteúdo,
//     mas não libera nem passa a posse. Criado com &x numa chamada.
//
// Este arquivo cuida dos tipos; as regras de posse ficam em ownership.go.

type Pointer struct {
	Elem     Type
	Nullable bool
}

func (p *Pointer) String() string {
	if p.Nullable {
		return "?*" + p.Elem.String()
	}
	return "*" + p.Elem.String()
}

type Borrow struct{ Elem Type }

func (b *Borrow) String() string { return "&" + b.Elem.String() }

// NilType é o tipo do literal nil, que só cabe em ?*T.
var NilType = &Basic{"nil"}

// pointerTo devolve sempre o mesmo *Pointer para o mesmo tipo, para que
// tipos possam ser comparados com ==.
func (c *checker) pointerTo(elem Type, nullable bool) *Pointer {
	key := ptrKey{elem, nullable}
	if p, ok := c.pointers[key]; ok {
		return p
	}
	p := &Pointer{Elem: elem, Nullable: nullable}
	c.pointers[key] = p
	return p
}

type ptrKey struct {
	elem     Type
	nullable bool
}

func (c *checker) borrowOf(elem Type) *Borrow {
	if p, ok := elem.(*Pointer); ok {
		// &p, com p ponteiro, empresta o objeto apontado: num genérico com
		// T = ?*X, &T é &X (não existe empréstimo de ponteiro)
		elem = p.Elem
	}
	if b, ok := c.borrows[elem]; ok {
		return b
	}
	b := &Borrow{Elem: elem}
	c.borrows[elem] = b
	return b
}

// IsOwner diz se valores do tipo t carregam posse de memória: ponteiros
// donos e slices.
func IsOwner(t Type) bool {
	switch tt := t.(type) {
	case *Pointer, *Slice:
		return true
	case *Enum:
		return tt.Resource // um enum que guarda donos é dono por valor
	}
	return false
}

// typeCtx diz onde um tipo aparece, porque nem todo tipo vale em todo lugar.
type typeCtx int

const (
	ctxVar        typeCtx = iota // variável local ou constante
	ctxParam                     // parâmetro: aceita &T
	ctxResult                    // retorno de função
	ctxField                     // campo de struct
	ctxElem                      // o T de *T
	ctxBorrowElem                // o T de &T (aceita slices)
	ctxSliceElem                 // o T de []T
	ctxArrayElem                 // o T de [N]T
)

func (c *checker) resolveType(t *ast.TypeExpr, ctx typeCtx) Type {
	for _, a := range t.Args {
		if a.Constraint != "" && !c.inReceiver {
			c.errorf(a.P, "uma restrição (%s %s) só pode aparecer no receptor de um método", a.Name, a.Constraint)
		}
	}
	switch t.Mod {
	case ast.TypeFunc:
		return c.resolveFuncType(t)
	case ast.TypeSlice:
		return c.resolveSlice(t, ctx)
	case ast.TypeArray:
		return c.resolveArray(t, ctx)
	case ast.TypeOptBorrow:
		if ctx != ctxParam || !c.inExternC {
			c.errorf(t.P, "?&T só existe em parâmetros de extern \"C\" fn (um ponteiro que o C aceita como NULL); numa função MLC, use ?*T ou duas funções")
			return Invalid
		}
		inner := c.resolveType(&ast.TypeExpr{Mod: ast.TypeBorrow, Elem: t.Elem, P: t.P}, ctxParam)
		if inner == Invalid {
			return Invalid
		}
		return c.optArgType(inner)
	case ast.TypePointer, ast.TypeNullPtr, ast.TypeBorrow:
		if t.Mod == ast.TypeBorrow && t.Elem != nil && t.Elem.Mod == ast.TypeNullPtr && ctx == ctxParam {
			if c.inExternC {
				return c.outHandleType(t) // &?*X: parâmetro de saída de uma função de C
			}
			c.errorf(t.P, "&?*T só existe em parâmetros de extern \"C\" fn (um handle devolvido pela função de C); numa função MLC, devolva o ?*T")
			return Invalid
		}
		switch {
		case ctx == ctxArrayElem:
			c.errorf(t.P, "arrays de ponteiros ainda não são suportados: os elementos não podem ser donos nem empréstimos")
			return Invalid
		case ctx == ctxSliceElem && t.Mod == ast.TypePointer:
			c.errorf(t.P, "elementos de slice começam zerados (nil), então não podem ser *T: use []?*T")
			return Invalid
		case ctx == ctxSliceElem && t.Mod == ast.TypeBorrow:
			c.errorf(t.P, "&T só pode ser tipo de parâmetro: um empréstimo não pode ser guardado num slice")
			return Invalid
		case ctx == ctxElem || ctx == ctxBorrowElem:
			c.errorf(t.P, "ponteiro para ponteiro não é suportado")
			return Invalid
		case t.Mod == ast.TypeBorrow && ctx != ctxParam && ctx != ctxResult && ctx != ctxVar:
			c.errorf(t.P, "um empréstimo (&T) não pode ser guardado em campos, slices ou arrays: só em parâmetros, variáveis e retornos")
			return Invalid
		}
		elemCtx := ctxElem
		if t.Mod == ast.TypeBorrow {
			elemCtx = ctxBorrowElem
		}
		elem := c.resolveType(t.Elem, elemCtx)
		if elem == Invalid {
			return Invalid
		}
		if isGuard(elem) && (ctx == ctxField || ctx == ctxSliceElem) {
			c.errorf(t.P, "um guard é da thread que travou o mutex: não pode ficar em campos nem em slices (que podem ir para outra thread)")
			return Invalid
		}
		if t.Mod == ast.TypeBorrow {
			return c.borrowOf(elem)
		}
		return c.pointerTo(elem, t.Mod == ast.TypeNullPtr)
	}
	var s *Struct
	switch {
	case t.Module != "" && t.Name == "Thread" && c.mod.uses[t.Module] != nil && c.mod.uses[t.Module].path == "std/thread" && c.mod.uses[t.Module].std:
		th := c.threadTypeExpr(t)
		if th != Invalid && ctx != ctxElem && ctx != ctxBorrowElem {
			c.errorf(t.P, "%s é um handle: só existe atrás de ponteiro (*%s, ?*%s)", th, th, th)
			return Invalid
		}
		return th
	case t.Module != "" && (t.Name == "Mutex" || t.Name == "Guard") && c.mod.uses[t.Module] != nil && c.mod.uses[t.Module].path == "std/mutex" && c.mod.uses[t.Module].std:
		mt := c.mutexTypeExpr(t)
		if mt != Invalid && ctx != ctxElem && ctx != ctxBorrowElem {
			c.errorf(t.P, "%s só existe atrás de ponteiro (*%s, ?*%s)", mt, mt, mt)
			return Invalid
		}
		return mt
	case t.Module != "" && (t.Name == "Sender" || t.Name == "Receiver") && c.mod.uses[t.Module] != nil && c.mod.uses[t.Module].path == "std/chan" && c.mod.uses[t.Module].std:
		ch := c.chanTypeExpr(t)
		if ch != Invalid && ctx != ctxElem && ctx != ctxBorrowElem {
			c.errorf(t.P, "%s é uma ponta de canal: só existe atrás de ponteiro (*%s, ?*%s)", ch, ch, ch)
			return Invalid
		}
		return ch
	case t.Module != "":
		// tipo de outro arquivo: geometria.Ponto
		if m := c.mod.uses[t.Module]; m != nil && m.ifaces[t.Name] != nil {
			c.errorf(t.P, "%s.%s é uma interface: nesta versão, interfaces só servem de restrição de genérico, como fn f[T %s.%s](x &T)", t.Module, t.Name, t.Module, t.Name)
			return Invalid
		}
		if m := c.mod.uses[t.Module]; m != nil && m.enums[t.Name] != nil {
			en := m.enums[t.Name]
			if !en.Pub {
				c.errorf(t.P, "%s.%s não é público (declare com pub enum)", t.Module, t.Name)
				return Invalid
			}
			return c.resolveEnum(en, t)
		}
		st, ok := c.moduleStruct(t.Module, t.Name, t.P).(*Struct)
		if !ok {
			return Invalid
		}
		s = st
	case c.typeParams[t.Name] != nil:
		if len(t.Args) > 0 {
			c.errorf(t.P, "%s é um parâmetro de tipo e não recebe argumentos", t.Name)
		}
		if tv, ok := c.typeParams[t.Name].(*TypeVar); ok && tv.Constraint == "ref" && ctx != ctxBorrowElem {
			c.errorf(t.P, "%s tem a restrição ref e só pode aparecer emprestado (&%s): o tipo pode ter donos e não ser copiável", t.Name, t.Name)
			return Invalid
		}
		return c.typeParams[t.Name]
	case basicTypes[t.Name] != nil:
		if len(t.Args) > 0 {
			c.errorf(t.P, "%s não é genérico", t.Name)
		}
		return basicTypes[t.Name]
	case c.mod.structs[t.Name] != nil:
		s = c.mod.structs[t.Name]
	case c.mod.enums[t.Name] != nil:
		return c.resolveEnum(c.mod.enums[t.Name], t)
	case c.mod.ifaces[t.Name] != nil:
		c.errorf(t.P, "%s é uma interface: nesta versão, interfaces só servem de restrição de genérico, como fn f[T %s](x &T)", t.Name, t.Name)
		return Invalid
	default:
		c.errorf(t.P, "tipo desconhecido %s", t.Name)
		return Invalid
	}
	switch {
	case s.Params != nil && len(t.Args) == 0:
		c.errorf(t.P, "%s é genérica: escreva os argumentos de tipo, como %s[int]", s.Name, s.Name)
		return Invalid
	case s.Params == nil && len(t.Args) > 0:
		c.errorf(t.P, "%s não é genérica", s.Name)
		return Invalid
	case s.Params != nil:
		var args []Type
		for _, a := range t.Args {
			args = append(args, c.resolveType(a, ctxVar))
		}
		inst, ok := c.instStruct(s, args, t.P).(*Struct)
		if !ok {
			return Invalid
		}
		s = inst
	}
	if s.Extern && ctx != ctxElem && ctx != ctxBorrowElem {
		c.errorf(t.P, "%s é um tipo de C opaco: só existe atrás de ponteiro (?*%s, *%s ou &%s)", s.Name, s.Name, s.Name, s.Name)
		return Invalid
	}
	if s.Resource && (ctx == ctxVar || ctx == ctxParam || ctx == ctxResult) {
		c.errorf(t.P, "%s contém ponteiros donos e só pode ser usada através de um ponteiro: use *%s, ?*%s ou &%s", s.Name, s.Name, s.Name, s.Name)
		return Invalid
	}
	return s
}

// assignable checa se um valor do tipo got pode ir para um lugar do tipo want.
// Além de tipos iguais: nil cabe em ?*T, e *T cabe em ?*T.
func (c *checker) assignable(got, want Type, pos token.Pos, context string) {
	if got == Invalid || want == Invalid || got == want {
		return
	}
	wp, wantPtr := want.(*Pointer)
	gp, gotPtr := got.(*Pointer)
	switch {
	case got == NilType && wantPtr && wp.Nullable:
		return
	case got == NilType && isSlice(want):
		c.errorf(pos, "%s: nil não é um slice; o valor zero de %s já é o slice vazio", context, want)
		return
	case got == NilType && wantPtr:
		c.errorf(pos, "%s: nil só cabe em ?*%s, não em %s (um ponteiro *T nunca é nil)", context, wp.Elem, want)
		return
	case gotPtr && wantPtr && gp.Elem == wp.Elem && !gp.Nullable && wp.Nullable:
		return
	case gotPtr && wantPtr && gp.Elem == wp.Elem && gp.Nullable:
		// ?*T onde se espera *T: o verificador de posse exige que o valor
		// não possa ser nil naquele ponto.
		return
	}
	c.errorf(pos, "%s: esperado %s, encontrado %s", context, want, got)
}

func isNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// nilComparison checa x == nil e x != nil. Só ?*T pode ser nil.
func (c *checker) nilComparison(e *ast.BinaryExpr, x, y Type) Type {
	other := x
	if x == NilType {
		other = y
	}
	p, isPtr := other.(*Pointer)
	switch {
	case other == NilType:
		c.errorf(e.OpPos, "comparação entre nil e nil")
	case isPtr && p.Nullable:
		return Bool
	case isPtr || isBorrow(other):
		c.errorf(e.OpPos, "valor do tipo %s nunca é nil", other)
	default:
		c.errorf(e.OpPos, "comparação entre tipos diferentes: %s e %s", x, y)
	}
	return Invalid
}

func isBorrow(t Type) bool {
	_, ok := t.(*Borrow)
	return ok
}

// deref devolve o tipo do conteúdo de um ponteiro ou empréstimo de tipo t,
// para *p e para p.campo. Para ?*T, se o ponteiro pode ser nil naquele ponto
// é decidido pelo verificador de posse, que acompanha o fluxo.
func (c *checker) deref(t Type, pos token.Pos, what string) (Type, bool) {
	switch t := t.(type) {
	case *Pointer:
		return t.Elem, true
	case *Borrow:
		return t.Elem, true
	}
	return t, false
}

// builtinCall checa alloc e free. ok é false se a chamada não é de um deles.
func (c *checker) builtinCall(e *ast.CallExpr, name string) (Type, bool) {
	switch name {
	case "len":
		return c.lenCall(e), true
	case "realloc":
		return c.reallocCall(e), true
	case "take":
		return c.takeCall(e), true
	case "replace":
		return c.replaceCall(e), true
	case "drain":
		c.errorf(e.Lparen, "drain só pode ser usado em for x in drain(s)")
		c.checkArgs(e)
		return Invalid, true
	case "drop":
		return c.dropCall(e), true
	case "alloc":
		if len(e.Args) > 0 {
			if ta, ok := e.Args[0].(*ast.TypeArg); ok {
				return c.allocSlice(e, ta), true
			}
		}
		if len(e.Args) != 1 {
			c.errorf(e.Lparen, "alloc recebe um argumento: um tipo (alloc(Ponto)) ou um valor inicial (alloc(Ponto{x: 1, y: 2}))")
			c.checkArgs(e)
			return Invalid, true
		}
		elem, zero := c.allocArg(e.Args[0])
		if elem == Invalid {
			return Invalid, true
		}
		if isThread(elem) {
			c.errorf(e.Args[0].Pos(), "%s vem de thread.spawn, não de alloc", elem)
			return Invalid, true
		}
		if isExternStruct(elem) {
			c.errorf(e.Args[0].Pos(), "%s é um tipo de C opaco: os valores vêm de funções de C, não de alloc", elem)
			return Invalid, true
		}
		c.info.Calls[e] = Callee{Builtin: Alloc, Elem: elem, Zero: zero}
		return c.pointerTo(elem, false), true
	case "free":
		if len(e.Args) != 1 {
			c.errorf(e.Lparen, "free recebe um argumento")
			c.checkArgs(e)
			return Invalid, true
		}
		switch t := c.value(e.Args[0]); tt := t.(type) {
		case *Pointer:
			if isThread(tt) {
				c.errorf(e.Args[0].Pos(), "%s é uma thread: espere por ela com thread.join, não com free", t)
			} else if isChanEnd(tt) {
				c.errorf(e.Args[0].Pos(), "%s é uma ponta de canal: feche com close(), não com free", t)
			} else if isGuard(tt) {
				c.errorf(e.Args[0].Pos(), "%s é um guard: destrave com unlock(), não com free", t)
			} else if isMutexType(tt) {
				c.errorf(e.Args[0].Pos(), "%s é um mutex: feche com close(), não com free", t)
			} else if isExternStruct(tt.Elem) {
				c.errorf(e.Args[0].Pos(), "%s é um handle de C: libere com a função de C que o fecha, não com free", t)
			}
		case *Slice:
		case *Enum:
			if !tt.Resource {
				c.errorf(e.Args[0].Pos(), "free espera um dono; %s não guarda donos", t)
			} else {
				c.dropPlan(t, e.Args[0].Pos()) // libera os dados da variante que estiver lá
			}
		case *Borrow:
			c.errorf(e.Args[0].Pos(), "não é possível liberar um empréstimo (%s): só o dono libera", t)
		default:
			if t != Invalid {
				c.errorf(e.Args[0].Pos(), "free espera um ponteiro dono, encontrado %s", t)
			}
		}
		c.info.Calls[e] = Callee{Builtin: Free}
		return Void, true
	}
	return nil, false
}

// allocArg resolve o argumento de alloc: um nome de tipo (memória zerada) ou
// um valor inicial.
func (c *checker) allocArg(a ast.Expr) (Type, bool) {
	// handles da biblioteca padrão: thread.Thread[R], chan.Sender[T], ...
	if base, _ := splitIndex(a); base != nil {
		if sel, ok := base.(*ast.SelectorExpr); ok {
			if m := c.userModule(sel.X); m != nil && m.std {
				switch {
				case m.path == "std/thread" && sel.Sel == "Thread":
					c.errorf(a.Pos(), "thread.Thread vem de thread.spawn, não de alloc")
					return Invalid, false
				case m.path == "std/chan" && (sel.Sel == "Sender" || sel.Sel == "Receiver"):
					c.errorf(a.Pos(), "as pontas de canal vêm de chan.new, sender() e clone(), não de alloc")
					return Invalid, false
				case m.path == "std/mutex" && (sel.Sel == "Mutex" || sel.Sel == "Guard"):
					c.errorf(a.Pos(), "um mutex vem de mutex.new (e um guard, de lock()), não de alloc")
					return Invalid, false
				}
			}
		}
	}
	switch a.(type) {
	case *ast.IndexExpr, *ast.IndexListExpr:
		// alloc(Par[int, string]): struct genérica, memória zerada
		if base, _ := splitIndex(a); c.namesType(base) {
			t := c.exprType(a)
			if s, ok := t.(*Struct); ok {
				if field, ok := hasNonNullOwner(s); ok {
					c.errorf(a.Pos(), "alloc(%s) zera a memória, mas %s é um ponteiro *T e não pode ser nil; use alloc(%s{...}) com todos os campos", s.Name, field, s.Name)
					return Invalid, false
				}
				return s, true
			}
			return Invalid, false
		}
	}
	if sel, ok := a.(*ast.SelectorExpr); ok && c.userModule(sel.X) != nil {
		// alloc(geometria.Ponto): struct de outro arquivo, memória zerada
		m := c.userModule(sel.X)
		if s := m.structs[sel.Sel]; s != nil {
			if !s.Pub {
				c.errorf(sel.SelPos, "%s.%s não é pública (declare com pub struct)", m.name, sel.Sel)
				return Invalid, false
			}
			if field, ok := hasNonNullOwner(s); ok {
				c.errorf(a.Pos(), "alloc(%s) zera a memória, mas %s é um ponteiro *T e não pode ser nil; use alloc(%s{...}) com todos os campos", s.Name, field, s.Name)
				return Invalid, false
			}
			return s, true
		}
	}
	if id, ok := a.(*ast.Ident); ok && c.scope.lookup(id.Name) == nil {
		if t, ok := basicTypes[id.Name]; ok {
			return t, true
		}
		if s, ok := c.mod.structs[id.Name]; ok {
			if field, ok := hasNonNullOwner(s); ok {
				c.errorf(a.Pos(), "alloc(%s) zera a memória, mas %s é um ponteiro *T e não pode ser nil; use alloc(%s{...}) com todos os campos", s.Name, field, s.Name)
				return Invalid, false
			}
			return s, true
		}
	}
	if _, isLit := a.(*ast.CompositeLit); isLit {
		c.litOK = true // alloc(No{...}): o literal vai direto para a memória nova
	}
	t := c.valueDefault(a)
	if _, isLit := a.(*ast.CompositeLit); !isLit {
		c.noCopy(t, a.Pos())
	}
	switch t.(type) {
	case *Pointer, *Borrow:
		c.errorf(a.Pos(), "ponteiro para ponteiro não é suportado")
		return Invalid, false
	}
	if isArray(t) {
		c.errorf(a.Pos(), "ponteiro para array não é suportado: use o array direto, ou alloc([]T, n)")
		return Invalid, false
	}
	if t == NilType {
		c.errorf(a.Pos(), "alloc(nil) não faz sentido: passe um tipo ou um valor")
		return Invalid, false
	}
	return t, false
}

// borrowArg checa um argumento passado a um parâmetro &T: "&x" ou um
// empréstimo recebido, repassado como está.
func (c *checker) borrowArg(a ast.Expr, want *Borrow, context string) {
	u, ok := a.(*ast.UnaryExpr)
	if !ok || u.Op != token.AMP {
		t := c.value(a)
		if t == Invalid || t == want {
			return
		}
		if IsOwner(t) || t == want.Elem {
			c.errorf(a.Pos(), "%s: esperado %s; para emprestar, escreva &%s", context, want, exprName(a))
			return
		}
		c.errorf(a.Pos(), "%s: esperado %s, encontrado %s", context, want, t)
		return
	}
	if sl, isSub := u.X.(*ast.SliceExpr); isSub {
		// &s[a:b] empresta parte de um slice
		got := c.subSlice(sl)
		c.info.Types[a] = got
		c.assignable(got, want, a.Pos(), context)
		return
	}
	t := c.borrowPlace(u.X)
	if t == Invalid {
		c.info.Types[a] = Invalid
		return
	}
	if arr, ok := t.(*Array); ok {
		if s, isSl := want.Elem.(*Slice); isSl && s.Elem == arr.Elem {
			// &a, com a [N]T, empresta o array como &[]T
			c.info.Types[a] = want
			return
		}
	}
	got := c.borrowOf(t)
	c.info.Types[a] = got
	c.assignable(got, want, a.Pos(), context)
}

// borrowPlace checa o x de &x e devolve o tipo emprestado. &p, com p *T,
// empresta o conteúdo de p; &s, com s uma struct local, empresta s.
func (c *checker) borrowPlace(x ast.Expr) Type {
	if !isPlace(x) {
		c.errorf(x.Pos(), "só é possível emprestar uma variável, um campo ou um conteúdo (&x, &x.campo, &*p)")
		c.value(x)
		return Invalid
	}
	if id, ok := x.(*ast.Ident); ok {
		if l := c.scope.lookup(id.Name); l != nil && l.IsConst {
			c.errorf(x.Pos(), "não é possível emprestar a constante %s", id.Name)
			return Invalid
		}
	}
	t := c.value(x)
	switch t := t.(type) {
	case *Pointer:
		return t.Elem // se pode ser nil, o verificador de posse acusa
	case *Borrow:
		c.errorf(x.Pos(), "%s já é um empréstimo: passe %s direto, sem &", exprName(x), exprName(x))
		return Invalid
	}
	if t == NilType {
		c.errorf(x.Pos(), "não é possível emprestar nil")
		return Invalid
	}
	return t
}

// isPlace diz se e designa um lugar na memória: variável, campo ou *p.
func isPlace(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name != "nil" && e.Name != "true" && e.Name != "false"
	case *ast.SelectorExpr:
		return isPlace(e.X)
	case *ast.UnaryExpr:
		return e.Op == token.MUL
	case *ast.IndexExpr:
		return isPlace(e.X)
	}
	return false
}

func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel
	case *ast.UnaryExpr:
		return fmt.Sprintf("%s%s", e.Op, exprName(e.X))
	}
	return "x"
}

func isPointerType(t Type) bool {
	_, ok := t.(*Pointer)
	return ok || isBorrow(t)
}

// markUsed marca como usada a variável na raiz de e (p em p.a.b).
func (c *checker) markUsed(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Ident:
		if l := c.info.Locals[e]; l != nil {
			l.Used = true
		}
	case *ast.SelectorExpr:
		c.markUsed(e.X)
	}
}
