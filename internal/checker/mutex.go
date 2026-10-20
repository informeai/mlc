package checker

import (
	"mlc/internal/ast"
)

// Mutex (std/mutex)
//
//	m := mutex.new(alloc(Account{balance: 0}))  // the value goes inside the mutex
//	m2 := m.clone()                           // another handle, for another thread
//	g := m.lock()                             // *mutex.Guard[T]: waits for the lock
//	c := g.get()                              // &Account, borrowed from the guard
//	c.balance += 10
//	g.unlock()                                // required; using c afterwards is an error
//	m.close()
//
// mutex.new and the methods are built into the compiler; the mutex lives in the
// runtime (pthread), with the value inside it and the handles counted. The
// checker requires each handle to be closed (close) and each guard,
// unlocked (unlock). get returns a borrow of the guard: while it's
// used, the guard stays frozen, so the value isn't touched after the unlock.
// A guard belongs to the thread that locked: it doesn't go to another thread (spawn's
// context, a channel), nor into fields or slices.

// Mutex types (Struct.MutexKind).
const (
	MutexHandle = 1
	MutexGuard  = 2
)

// MutexPlan is what the generator needs for a mutex operation.
type MutexPlan struct {
	Op   string // new, lock, clone, close, get, unlock, wait, notify, notify_all
	Elem Type
}

type mutexKey struct {
	kind int
	elem Type
}

// mutexType returns mutex.Mutex[elem] or mutex.Guard[elem].
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

// mutexElemOK explains why t can't be inside a mutex.
func mutexElemOK(t Type) string {
	switch {
	case isBorrow(t):
		return "a mutex doesn't hold borrows: the value stays inside it, by ownership"
	case isGuard(t):
		return "a mutex doesn't hold a guard"
	case isThread(t):
		return "a mutex doesn't hold a thread (it needs thread.join)"
	case t == Void || t == NilType || isUntyped(t):
		return "the mutex's value needs a type"
	}
	if p, ok := t.(*Pointer); ok && isExternStruct(p.Elem) && !isChanEnd(p.Elem) && !isMutexType(p.Elem) {
		return "a C handle needs the C function that closes it, and the mutex wouldn't know which one"
	}
	return ""
}

// mutexTypeExpr resolves mutex.Mutex[T] and mutex.Guard[T] written in the code.
func (c *checker) mutexTypeExpr(t *ast.TypeExpr) Type {
	if len(t.Args) != 1 {
		c.errorf(t.P, "mutex.%s takes the type of the value: mutex.%s[int]", t.Name, t.Name)
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

// mutexNew checks mutex.new(v) and mutex.new[T](v).
func (c *checker) mutexNew(e *ast.CallExpr, targs []ast.Expr) Type {
	if len(e.Args) != 1 || len(targs) > 1 {
		c.errorf(e.Lparen, "mutex.new takes the value the mutex holds: mutex.new(v) or mutex.new[T](v)")
		c.checkArgs(e)
		return Invalid
	}
	var elem Type
	if len(targs) == 1 {
		elem = c.resolveType(typeExprOf(targs[0]), ctxVar)
		if elem != Invalid {
			c.valueAs(e.Args[0], elem, "the value of mutex.new")
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
		c.dropPlan(elem, e.Args[0].Pos()) // the last close frees the value
	}
	c.info.Mutexes[e] = &MutexPlan{Op: "new", Elem: elem}
	c.info.Calls[e] = Callee{Builtin: MutexOp}
	return c.pointerTo(c.mutexType(MutexHandle, elem), false)
}

// mutexMethod checks the methods of mutex.Mutex[T] and mutex.Guard[T].
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
		// a borrow of the guard: like a method fn (g &Guard[T]) get() &T,
		// so the checker freezes the guard while the borrow is used
		result = c.borrowOf(s.MutexElem)
		callee.Func = &Func{Name: s.Name + ".get", Result: result, Method: true, BorrowFrom: 1, mod: s.mod, Pub: true,
			Params: []*Local{{Name: "g", Type: c.borrowOf(s)}}}
		c.info.Recv[e] = sel.X
	default:
		c.errorf(sel.SelPos, "%s has no method %s (Mutex: lock, clone, close, notify, notify_all; Guard: get, unlock, wait, notify, notify_all)", s.Name, sel.Sel)
		c.checkArgs(e)
		return Invalid
	}
	if len(e.Args) != 0 {
		c.errorf(e.Lparen, "%s.%s takes no arguments", s.Name, sel.Sel)
		c.checkArgs(e)
		return Invalid
	}
	c.info.Mutexes[e] = plan
	c.info.Calls[e] = callee
	return result
}
