package checker

import (
	"mlc/internal/ast"
)

// Canais (std/chan)
//
//	rx := chan.new[[]byte](16)   // aloca o canal (16 posições) e devolve a ponta que recebe
//	tx := rx.sender()            // uma ponta que envia, ligada ao mesmo canal
//	tx.send(msg)                 // msg é movida para o canal (bloqueia se cheio)
//	match rx.recv() {            // bloqueia até chegar algo ou o canal fechar
//	    Got(m) => ...            // a posse chega aqui
//	    Closed => ...            // todos os Sender fecharam e o canal esvaziou
//	}
//	tx.close()
//	rx.close()
//
// chan.new e os métodos das pontas são embutidos no compilador; o canal em
// si (buffer em anel, trava, contagem de pontas) fica no runtime. Cada ponta
// (*chan.Sender[T], *chan.Receiver[T]) é um dono, como um handle: precisa de
// close(), não aceita free nem alloc. O runtime conta as pontas, e o último
// close libera o canal (com drop no que sobrou nele). Um send sem nenhum
// Receiver aberto libera o valor (drop) e devolve false.

// Pontas de canal (Struct.ChanEnd).
const (
	ChanSender   = 1
	ChanReceiver = 2
)

// ChanPlan é o que o gerador precisa para uma operação de canal.
type ChanPlan struct {
	Op   string // new, send, recv, sender, clone, close
	Elem Type
	End  int  // a ponta do receptor (nos métodos)
	Recv Type // recv: o enum chan.Recv[T]
}

// chanEnd devolve o tipo da ponta (Sender ou Receiver) de um canal de elem.
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

// chanTypeExpr resolve chan.Sender[T] e chan.Receiver[T] escritos no código.
func (c *checker) chanTypeExpr(t *ast.TypeExpr) Type {
	if len(t.Args) != 1 {
		c.errorf(t.P, "chan.%s recebe o tipo dos valores: chan.%s[int]", t.Name, t.Name)
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

// chanElemOK explica por que valores de t não podem passar por um canal.
func (c *checker) chanElemOK(t Type) string {
	if isBorrow(t) {
		return "um canal não leva empréstimos: os valores passam de uma thread para outra por posse"
	}
	if ok, why := ownedOK(t); !ok {
		if why == "" {
			return "esse tipo não pode passar por um canal"
		}
		return "esse tipo não pode passar por um canal (" + why + ")"
	}
	return ""
}

// chanNew checa chan.new[T](cap).
func (c *checker) chanNew(e *ast.CallExpr, targs []ast.Expr) Type {
	if len(targs) != 1 || len(e.Args) != 1 {
		c.errorf(e.Lparen, "chan.new recebe o tipo dos valores e a capacidade: chan.new[int](16)")
		c.checkArgs(e)
		return Invalid
	}
	elem := c.resolveType(typeExprOf(targs[0]), ctxVar)
	c.valueAs(e.Args[0], Int, "a capacidade de chan.new")
	if elem == Invalid {
		return Invalid
	}
	if msg := c.chanElemOK(elem); msg != "" {
		c.errorf(targs[0].Pos(), "%s", msg)
		return Invalid
	}
	c.recvEnum(elem, targs[0])
	if IsOwner(elem) {
		c.dropPlan(elem, targs[0].Pos()) // o runtime libera o que sobrar no canal
	}
	c.info.Chans[e] = &ChanPlan{Op: "new", Elem: elem}
	c.info.Calls[e] = Callee{Builtin: ChanOp}
	return c.pointerTo(c.chanEnd(ChanReceiver, elem), false)
}

// recvEnum devolve chan.Recv[elem], o resultado de recv.
func (c *checker) recvEnum(elem Type, pos ast.Expr) *Enum {
	m := c.modules["std/chan"]
	en := m.enums["Recv"]
	inst, _ := c.instEnum(en, []Type{elem}, pos.Pos()).(*Enum)
	return inst
}

// chanMethod checa os métodos de uma ponta de canal.
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
		c.errorf(sel.SelPos, "%s não tem método %s (Sender: send, clone, close; Receiver: recv, sender, clone, close)", s.Name, sel.Sel)
		c.checkArgs(e)
		return Invalid
	}
	if len(e.Args) != nargs {
		c.errorf(e.Lparen, "%s.%s espera %d argumento(s), recebeu %d", s.Name, sel.Sel, nargs, len(e.Args))
		c.checkArgs(e)
		return Invalid
	}
	if nargs == 1 {
		c.valueAs(e.Args[0], s.ChanElem, "o valor de send")
	}
	c.info.Chans[e] = plan
	c.info.Calls[e] = Callee{Builtin: ChanOp}
	return result
}
