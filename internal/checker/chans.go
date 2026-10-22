package checker

import (
	"mlc/internal/ast"
)

// Channels (std/chan)
//
//	rx := chan.new[[]byte](16)   // allocates the channel (16 slots) and returns the receiving end
//	tx := rx.sender()            // an end that sends, tied to the same channel
//	tx.send(msg)                 // msg is moved into the channel (blocks if full)
//	tx.try_send(msg)             // never blocks: Sent, or Full(msg) / Closed(msg) back
//	match rx.recv() {            // blocks until something arrives or the channel closes
//	    Got(m) => ...            // ownership arrives here
//	    Closed => ...            // every Sender closed and the channel is empty
//	}
//	tx.close()
//	rx.close()
//
// chan.new and the ends' methods are built into the compiler; the channel
// itself (ring buffer, lock, end count) lives in the runtime. Each end
// (*chan.Sender[T], *chan.Receiver[T]) is an owner, like a handle: it needs a
// close(), and doesn't accept free or alloc. The runtime counts the ends, and the last
// close frees the channel (with drop on whatever was left in it). A send with no
// Receiver open frees the value (drop) and returns false.

// Channel ends (Struct.ChanEnd).
const (
	ChanSender   = 1
	ChanReceiver = 2
)

// ChanPlan is what the generator needs for a channel operation.
type ChanPlan struct {
	Op   string // new, send, try_send, recv, sender, clone, close
	Elem Type
	End  int  // the receiver's end (in methods)
	Recv Type // recv: the chan.Recv[T] enum; try_send: chan.Try[T]
}

// chanEnd returns the type of a channel end (Sender or Receiver) for elem.
func (c *checker) chanEnd(end int, elem Type) *Struct {
	key := chanKey{end, elem}
	if s, ok := c.chans[key]; ok {
		return s
	}
	name := "chan.Sender["
	if end == ChanReceiver {
		name = "chan.Receiver["
	}
	s := &Struct{Name: name + elem.String() + "]", CName: "mlc_chan", Methods: map[string]*Func{}, Pub: true, Extern: true,
		ChanEnd: end, ChanElem: elem, mod: c.modules["std/chan"]}
	c.chans[key] = s
	return s
}

type chanKey struct {
	end  int
	elem Type
}

func isChanEnd(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	s, ok := t.(*Struct)
	return ok && s.ChanEnd != 0
}

// chanTypeExpr resolves chan.Sender[T] and chan.Receiver[T] written in the code.
func (c *checker) chanTypeExpr(t *ast.TypeExpr) Type {
	if len(t.Args) != 1 {
		c.errorf(t.P, "chan.%s takes the type of the values: chan.%s[int]", t.Name, t.Name)
		return Invalid
	}
	elem := c.resolveType(t.Args[0], ctxVar)
	if elem == Invalid {
		return Invalid
	}
	if msg := c.chanElemOK(elem); msg != "" {
		c.errorf(t.P, "%s", msg)
		return Invalid
	}
	end := ChanSender
	if t.Name == "Receiver" {
		end = ChanReceiver
	}
	return c.chanEnd(end, elem)
}

// chanElemOK explains why values of t can't go through a channel.
func (c *checker) chanElemOK(t Type) string {
	if isBorrow(t) {
		return "a channel doesn't carry borrows: values pass from one thread to another by ownership"
	}
	if ok, why := ownedOK(t); !ok {
		if why == "" {
			return "that type can't go through a channel"
		}
		return "that type can't go through a channel (" + why + ")"
	}
	return ""
}

// chanNew checks chan.new[T](cap).
func (c *checker) chanNew(e *ast.CallExpr, targs []ast.Expr) Type {
	if len(targs) != 1 || len(e.Args) != 1 {
		c.errorf(e.Lparen, "chan.new takes the type of the values and the capacity: chan.new[int](16)")
		c.checkArgs(e)
		return Invalid
	}
	elem := c.resolveType(typeExprOf(targs[0]), ctxVar)
	c.valueAs(e.Args[0], Int, "the capacity of chan.new")
	if elem == Invalid {
		return Invalid
	}
	if msg := c.chanElemOK(elem); msg != "" {
		c.errorf(targs[0].Pos(), "%s", msg)
		return Invalid
	}
	c.recvEnum(elem, targs[0])
	if IsOwner(elem) {
		c.dropPlan(elem, targs[0].Pos()) // the runtime frees whatever is left in the channel
	}
	c.info.Chans[e] = &ChanPlan{Op: "new", Elem: elem}
	c.info.Calls[e] = Callee{Builtin: ChanOp}
	return c.pointerTo(c.chanEnd(ChanReceiver, elem), false)
}

// recvEnum returns chan.Recv[elem], the result of recv.
func (c *checker) recvEnum(elem Type, pos ast.Expr) *Enum {
	return c.chanEnum("Recv", elem, pos)
}

// chanEnum returns chan.Recv[elem] or chan.Try[elem].
func (c *checker) chanEnum(name string, elem Type, pos ast.Expr) *Enum {
	m := c.modules["std/chan"]
	en := m.enums[name]
	inst, _ := c.instEnum(en, []Type{elem}, pos.Pos()).(*Enum)
	return inst
}

// chanMethod checks the methods of a channel end.
func (c *checker) chanMethod(e *ast.CallExpr, sel *ast.SelectorExpr, t Type, s *Struct) Type {
	if isBorrow(t) {
		t = c.pointerTo(s, false)
	}
	plan := &ChanPlan{Op: sel.Sel, Elem: s.ChanElem, End: s.ChanEnd}
	nargs := 0
	var result Type
	switch {
	case sel.Sel == "send" && s.ChanEnd == ChanSender:
		nargs, result = 1, Bool
	case sel.Sel == "try_send" && s.ChanEnd == ChanSender:
		en := c.chanEnum("Try", s.ChanElem, e)
		nargs, plan.Recv, result = 1, en, en
	case sel.Sel == "recv" && s.ChanEnd == ChanReceiver:
		en := c.recvEnum(s.ChanElem, e)
		plan.Recv, result = en, en
	case sel.Sel == "sender" && s.ChanEnd == ChanReceiver:
		result = c.pointerTo(c.chanEnd(ChanSender, s.ChanElem), false)
	case sel.Sel == "clone":
		result = c.pointerTo(s, false)
	case sel.Sel == "close":
		result = Void
	default:
		c.errorf(sel.SelPos, "%s has no method %s (Sender: send, try_send, clone, close; Receiver: recv, sender, clone, close)", s.Name, sel.Sel)
		c.checkArgs(e)
		return Invalid
	}
	if len(e.Args) != nargs {
		c.errorf(e.Lparen, "%s.%s expects %d argument(s), got %d", s.Name, sel.Sel, nargs, len(e.Args))
		c.checkArgs(e)
		return Invalid
	}
	if nargs == 1 {
		c.valueAs(e.Args[0], s.ChanElem, "the value of "+sel.Sel)
	}
	c.info.Chans[e] = plan
	c.info.Calls[e] = Callee{Builtin: ChanOp}
	return result
}
