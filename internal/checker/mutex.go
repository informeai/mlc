package checker

import (
	"mlc/internal/ast"
)

// Mutex (std/mutex)
//
//	m := mutex.new(alloc(Conta{saldo: 0}))   // o valor vai para dentro do mutex
//	m2 := m.clone()                           // outro handle, para outra thread
//	g := m.lock()                             // *mutex.Guard[T]: espera a trava
//	c := g.get()                              // &Conta, emprestado do guard
//	c.saldo += 10
//	g.unlock()                                // obrigatório; usar c depois é erro
//	m.close()
//
// mutex.new e os métodos são embutidos no compilador; o mutex fica no
// runtime (pthread), com o valor dentro dele e os handles contados. O
// verificador cobra que cada handle seja fechado (close) e cada guard,
// destravado (unlock). get devolve um empréstimo do guard: enquanto ele é
// usado, o guard fica congelado, então o valor não é tocado depois do unlock.
// Um guard é da thread que travou: não vai para outra thread (contexto de
// spawn, canal), nem para campos ou slices.

// Tipos do mutex (Struct.MutexKind).
const (
	MutexHandle = 1
	MutexGuard  = 2
)

// MutexPlan é o que o gerador precisa para uma operação de mutex.
type MutexPlan struct {
	Op   string // new, lock, clone, close, get, unlock, wait, notify, notify_all
	Elem Type
}

type mutexKey struct {
	kind int
	elem Type
}

// mutexType devolve mutex.Mutex[elem] ou mutex.Guard[elem].
func (c *checker) mutexType(kind int, elem Type) *Struct {
	key := mutexKey{kind, elem}
	if s, ok := c.mutexes[key]; ok {
		return s
	}
	name := "mutex.Mutex["
	if kind == MutexGuard {
		name = "mutex.Guard["
	}
	s := &Struct{Name: name + elem.String() + "]", CName: "mlc_mutex", Methods: map[string]*Func{}, Pub: true, Extern: true,
		MutexKind: kind, MutexElem: elem, mod: c.modules["std/mutex"]}
	c.mutexes[key] = s
	return s
}

func isGuard(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	s, ok := t.(*Struct)
	return ok && s.MutexKind == MutexGuard
}

func isMutexType(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	s, ok := t.(*Struct)
	return ok && s.MutexKind != 0
}

// mutexElemOK explica por que t não pode ficar dentro de um mutex.
func mutexElemOK(t Type) string {
	switch {
	case isBorrow(t):
		return "um mutex não guarda empréstimos: o valor fica dentro dele, por posse"
	case isGuard(t):
		return "um mutex não guarda um guard"
	case isThread(t):
		return "um mutex não guarda uma thread (ela precisa de thread.join)"
	case t == Void || t == NilType || isUntyped(t):
		return "o valor do mutex precisa de um tipo"
	}
	if p, ok := t.(*Pointer); ok && isExternStruct(p.Elem) && !isChanEnd(p.Elem) && !isMutexType(p.Elem) {
		return "um handle de C precisa da função de C que o fecha, e o mutex não saberia qual é"
	}
	return ""
}

// mutexTypeExpr resolve mutex.Mutex[T] e mutex.Guard[T] escritos no código.
func (c *checker) mutexTypeExpr(t *ast.TypeExpr) Type {
	if len(t.Args) != 1 {
		c.errorf(t.P, "mutex.%s recebe o tipo do valor: mutex.%s[int]", t.Name, t.Name)
		return Invalid
	}
	elem := c.resolveType(t.Args[0], ctxVar)
	if elem == Invalid {
		return Invalid
	}
	if msg := mutexElemOK(elem); msg != "" {
		c.errorf(t.P, "%s", msg)
		return Invalid
	}
	kind := MutexHandle
	if t.Name == "Guard" {
		kind = MutexGuard
	}
	return c.mutexType(kind, elem)
}

// mutexNew checa mutex.new(v) e mutex.new[T](v).
func (c *checker) mutexNew(e *ast.CallExpr, targs []ast.Expr) Type {
	if len(e.Args) != 1 || len(targs) > 1 {
		c.errorf(e.Lparen, "mutex.new recebe o valor que o mutex guarda: mutex.new(v) ou mutex.new[T](v)")
		c.checkArgs(e)
		return Invalid
	}
	var elem Type
	if len(targs) == 1 {
		elem = c.resolveType(typeExprOf(targs[0]), ctxVar)
		if elem != Invalid {
			c.valueAs(e.Args[0], elem, "o valor de mutex.new")
		} else {
			c.value(e.Args[0])
		}
	} else {
		elem = c.valueDefault(e.Args[0])
	}
	if elem == Invalid {
		return Invalid
	}
	if msg := mutexElemOK(elem); msg != "" {
		c.errorf(e.Args[0].Pos(), "%s", msg)
		return Invalid
	}
	if IsOwner(elem) {
		c.dropPlan(elem, e.Args[0].Pos()) // o último close libera o valor
	}
	c.info.Mutexes[e] = &MutexPlan{Op: "new", Elem: elem}
	c.info.Calls[e] = Callee{Builtin: MutexOp}
	return c.pointerTo(c.mutexType(MutexHandle, elem), false)
}

// mutexMethod checa os métodos de mutex.Mutex[T] e mutex.Guard[T].
func (c *checker) mutexMethod(e *ast.CallExpr, sel *ast.SelectorExpr, t Type, s *Struct) Type {
	plan := &MutexPlan{Op: sel.Sel, Elem: s.MutexElem}
	callee := Callee{Builtin: MutexOp}
	var result Type = Void
	switch {
	case s.MutexKind == MutexHandle && sel.Sel == "lock":
		result = c.pointerTo(c.mutexType(MutexGuard, s.MutexElem), false)
	case s.MutexKind == MutexHandle && sel.Sel == "clone":
		result = c.pointerTo(s, false)
	case s.MutexKind == MutexHandle && sel.Sel == "close":
	case s.MutexKind == MutexGuard && sel.Sel == "unlock":
	case s.MutexKind == MutexGuard && sel.Sel == "wait":
	case sel.Sel == "notify" || sel.Sel == "notify_all":
	case s.MutexKind == MutexGuard && sel.Sel == "get":
		// empréstimo do guard: como um método fn (g &Guard[T]) get() &T,
		// para o verificador congelar o guard enquanto o empréstimo é usado
		result = c.borrowOf(s.MutexElem)
		callee.Func = &Func{Name: s.Name + ".get", Result: result, Method: true, BorrowFrom: 1, mod: s.mod, Pub: true,
			Params: []*Local{{Name: "g", Type: c.borrowOf(s)}}}
		c.info.Recv[e] = sel.X
	default:
		c.errorf(sel.SelPos, "%s não tem método %s (Mutex: lock, clone, close, notify, notify_all; Guard: get, unlock, wait, notify, notify_all)", s.Name, sel.Sel)
		c.checkArgs(e)
		return Invalid
	}
	if len(e.Args) != 0 {
		c.errorf(e.Lparen, "%s.%s não recebe argumentos", s.Name, sel.Sel)
		c.checkArgs(e)
		return Invalid
	}
	c.info.Mutexes[e] = plan
	c.info.Calls[e] = callee
	return result
}
