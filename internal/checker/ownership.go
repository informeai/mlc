package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Ownership checker (docs/memory.md)
//
// Runs after type checking, once per function. It follows the state of
// each owning path: a variable (p) or a pointer field reached from
// it (p.next, p.next.next, l.head). Possible states:
//
//	sLive: owns memory and must free it or pass it along
//	sNull: is nil (only ?*T)
//	sDead: was freed or moved; can't be used
//
// and a fourth bit, sOpaque, which says what's known about the fields of the pointed-to
// object: without it, the object came out of alloc(T) and its pointer fields are
// nil; with it, the object came from outside (a parameter, a function) and each field can
// be an owner or nil.
//
// Where two paths join, the states are merged. It's an error to: use what may
// be sDead; use the pointee of what may be nil; free twice;
// overwrite what may be sLive; leave the scope of a variable that may
// be sLive (a leak); free an object whose fields may still be owners.
//
// Loops: the body is analyzed until the state at the start of the iteration stabilizes
// (fixed point), with errors muted; then, one last pass with the
// stable state reports the errors. That way the second iteration onward is also
// checked: freeing inside the loop what came from outside becomes a double free, and
// allocating without freeing becomes an overwrite.

type ownState uint8

const (
	sLive ownState = 1 << iota
	sNull
	sDead
	sOpaque

	sObligation = sLive | sNull | sDead
)

// closerOf returns the name of the method that consumes a standard library
// handle (a *T receiver, without other parameters): close, or wait in sync.Group.
// hasFreeMethod says whether t is *S with S having a free method that consumes the
// pointer (fn (x *S) free()).
func hasFreeMethod(t Type) bool {
	p, ok := t.(*Pointer)
	if !ok {
		return false
	}
	st, ok := p.Elem.(*Struct)
	if !ok {
		return false
	}
	m := st.Methods["free"]
	if m == nil || !m.Method || len(m.Params) != 1 {
		return false
	}
	_, owner := m.Params[0].Type.(*Pointer)
	return owner
}

func closerOf(s *Struct) string {
	if s.Methods["close"] != nil {
		return "close"
	}
	for name, m := range s.Methods {
		if _, ok := m.Params[0].Type.(*Pointer); ok && m.Method && len(m.Params) == 1 {
			return name
		}
	}
	return "close"
}

// pathKey identifies a path: the root variable and the fields from it
// ("" for the variable itself, "next.next" for p.next.next).
type pathKey struct {
	root   *Local
	fields string
}

func (k pathKey) isRoot() bool { return k.fields == "" }

func (k pathKey) child(rel string) pathKey {
	if k.fields == "" {
		return pathKey{k.root, rel}
	}
	return pathKey{k.root, k.fields + "." + rel}
}

func (k pathKey) parent() pathKey {
	i := strings.LastIndexByte(k.fields, '.')
	if i < 0 {
		return pathKey{k.root, ""}
	}
	return pathKey{k.root, k.fields[:i]}
}

// under says whether k is strictly below p.
func (k pathKey) under(p pathKey) bool {
	if k.root != p.root || k.fields == p.fields {
		return false
	}
	return p.fields == "" || strings.HasPrefix(k.fields, p.fields+".")
}

// rel returns k's fields relative to p (k must be below p).
func (k pathKey) rel(p pathKey) string {
	if p.fields == "" {
		return k.fields
	}
	return k.fields[len(p.fields)+1:]
}

func (k pathKey) String() string {
	if k.fields == "" {
		return k.root.Name
	}
	return k.root.Name + "." + k.fields
}

func elemOf(t Type) Type {
	switch t := t.(type) {
	case *Pointer:
		return t.Elem
	case *Borrow:
		return t.Elem
	}
	return t
}

// pathType returns the type of the path k.
func pathType(k pathKey) Type {
	t := k.root.Type
	if k.fields == "" {
		return t
	}
	for _, f := range strings.Split(k.fields, ".") {
		t = elemOf(t).(*Struct).Field(f).Type
	}
	return t
}

// objectOf returns the path of the object that contains the field k: the nearest
// pointer prefix (structs kept by value are part of the same
// object).
func objectOf(k pathKey) pathKey {
	p := k.parent()
	for !p.isRoot() && !isPointerType(pathType(p)) {
		p = p.parent()
	}
	return p
}

// ownerFields lists the owning fields of an object of type t, including those of
// structs kept by value ("next", "node.next").
func ownerFields(t Type, prefix string) []string {
	s, ok := t.(*Struct)
	if !ok {
		return nil
	}
	var out []string
	for _, f := range s.Fields {
		switch {
		case IsOwner(f.Type):
			out = append(out, prefix+f.Name)
		case isResource(f.Type):
			out = append(out, ownerFields(f.Type, prefix+f.Name+".")...)
		}
	}
	return out
}

// ownFlow is the state of each path at a point of the code. nil means
// an unreachable point (after return, break or continue).
type ownFlow map[pathKey]ownState

func (f ownFlow) clone() ownFlow {
	if f == nil {
		return nil
	}
	g := make(ownFlow, len(f))
	for k, v := range f {
		g[k] = v
	}
	return g
}

func (f ownFlow) has(root *Local) bool {
	_, ok := f[pathKey{root, ""}]
	return ok
}

// get returns k's state, deducing that of fields that weren't touched from
// the object that contains them.
func (f ownFlow) get(k pathKey) ownState {
	if v, ok := f[k]; ok {
		return v
	}
	if k.isRoot() {
		return 0
	}
	obj := f.get(objectOf(k))
	if obj == 0 {
		return 0
	}
	if obj&sOpaque == 0 {
		return sNull // object out of alloc(T): pointer fields zeroed
	}
	if p, ok := pathType(k).(*Pointer); ok && !p.Nullable {
		return sLive | sOpaque
	}
	return sLive | sNull | sOpaque
}

// merge joins two execution paths. The paths whose root variable exists on
// both sides stay: the others were declared in a scope that closed.
func merge(a, b ownFlow) ownFlow {
	if a == nil {
		return b.clone()
	}
	if b == nil {
		return a.clone()
	}
	m := ownFlow{}
	for _, src := range []ownFlow{a, b} {
		for k := range src {
			if a.has(k.root) && b.has(k.root) {
				m[k] = a.get(k) | b.get(k)
			}
		}
	}
	return m
}

// ownValue is an owning value in transit: its state and that of the fields that
// come along (relative to the value), to install at the destination.
type ownValue struct {
	st   ownState
	sub  map[string]ownState
	gone map[string]token.Pos
}

type ownLoop struct {
	stmt      *ast.ForStmt
	exits     []ownFlow // states at the breaks
	continues []ownFlow // states at the continues, already after the post statement
	depth     int
}

type ownChecker struct {
	c        *checker
	fn       *Func
	st       ownFlow
	scopes   [][]*Local
	loops    []*ownLoop
	borrows  []*Local              // &T parameters
	origin   map[pathKey]token.Pos // where the path received the current memory
	gone     map[pathKey]token.Pos // where the path was freed or moved
	drains   []pathKey             // slices being emptied by for in drain(s)
	pending  *[]pendingMove        // moves of the arguments of the call being evaluated
	reported map[pathKey]bool      // use errors already reported
	muted    int                   // greater than zero: errors muted (loop stabilizing)
}

func (c *checker) checkOwnership(fn *Func) {
	o := &ownChecker{
		c: c, fn: fn, st: ownFlow{},
		origin: map[pathKey]token.Pos{}, gone: map[pathKey]token.Pos{},
		reported: map[pathKey]bool{},
	}
	o.push()
	for _, p := range fn.Params {
		switch t := p.Type.(type) {
		case *Pointer:
			s := sLive | sOpaque
			if t.Nullable {
				s |= sNull
			}
			o.declare(p, ownValue{st: s}, p.Pos)
		case *Slice:
			// A received slice is an owner: it must be freed or passed
			// along. sOpaque: if the elements are owners, they may have memory.
			o.declare(p, ownValue{st: sLive | sOpaque}, p.Pos)
		case *Borrow:
			// A borrow is never nil nor freed here; what matters
			// are the fields of the borrowed object.
			o.st[pathKey{p, ""}] = sLive | sOpaque
			o.borrows = append(o.borrows, p)
		}
	}
	for _, s := range fn.Decl.Body.Stmts {
		o.stmt(s)
	}
	if o.st != nil {
		o.borrowsComplete(fn.Decl.Body.End)
	}
	o.pop(fn.Decl.Body.End, "at the end of the function")
}

func (o *ownChecker) push() { o.scopes = append(o.scopes, nil) }

func (o *ownChecker) pop(pos token.Pos, where string) {
	top := o.scopes[len(o.scopes)-1]
	o.leaks(top, pos, where)
	for k := range o.st {
		for _, l := range top {
			if k.root == l {
				delete(o.st, k)
			}
		}
	}
	o.scopes = o.scopes[:len(o.scopes)-1]
}

func (o *ownChecker) declare(l *Local, v ownValue, pos token.Pos) {
	k := pathKey{l, ""}
	o.install(k, v)
	o.origin[k] = pos
	o.scopes[len(o.scopes)-1] = append(o.scopes[len(o.scopes)-1], l)
}

// install puts the value v at the path k, with the fields that come along.
func (o *ownChecker) install(k pathKey, v ownValue) {
	o.dropUnder(k)
	o.st[k] = v.st
	for rel, s := range v.sub {
		o.st[k.child(rel)] = s
	}
	for rel, pos := range v.gone {
		o.gone[k.child(rel)] = pos
	}
}

// extract takes the owning value and the fields tracked below it out of k.
func (o *ownChecker) extract(k pathKey) ownValue {
	v := ownValue{st: o.st.get(k), sub: map[string]ownState{}, gone: map[string]token.Pos{}}
	for p, s := range o.st {
		if p.under(k) {
			v.sub[p.rel(k)] = s
			if pos, ok := o.gone[p]; ok {
				v.gone[p.rel(k)] = pos
			}
		}
	}
	return v
}

func (o *ownChecker) dropUnder(k pathKey) {
	for p := range o.st {
		if p.under(k) {
			delete(o.st, p)
		}
	}
}

func (o *ownChecker) kill(k pathKey, pos token.Pos) {
	o.dropUnder(k)
	o.st[k] = sDead
	o.gone[k] = pos
}

// leaks reports the variables of ls that may still have memory when leaving through pos.
func (o *ownChecker) leaks(ls []*Local, pos token.Pos, where string) {
	if o.st == nil {
		return
	}
	for _, l := range ls {
		k := pathKey{l, ""}
		if isBorrow(l.Type) {
			o.borrowedFieldsBack(l, pos)
			continue
		}
		s := o.st.get(k) & sObligation
		if s&sLive == 0 {
			continue
		}
		if p, ok := l.Type.(*Pointer); ok && isExternStruct(p.Elem) {
			if isThread(p) {
				o.errorf(pos, "leak: the thread %s (line %d) may not have been waited for %s; pass it to thread.join",
					l.Name, o.origin[k].Line, where)
			} else if isGuard(p) {
				o.errorf(pos, "leak: the guard %s (line %d) may still be locked %s; unlock it with %s.unlock()",
					l.Name, o.origin[k].Line, where, l.Name)
			} else if isMutexType(p) {
				o.errorf(pos, "leak: the mutex %s (line %d) may still be open %s; close it with %s.close()",
					l.Name, o.origin[k].Line, where, l.Name)
			} else if st := p.Elem.(*Struct); st.mod != nil && st.mod.std && st.ChanEnd == 0 {
				// a standard library handle (atomic.Int): closes with close(),
				// or with the method that consumes it (sync.Group: wait())
				o.errorf(pos, "leak: %s (%s, line %d) may still be open %s; close it with %s.%s()",
					l.Name, st.Name, o.origin[k].Line, where, l.Name, closerOf(st))
			} else if isChanEnd(p) {
				o.errorf(pos, "leak: the channel end %s (line %d) may still be open %s; close it with %s.close()",
					l.Name, o.origin[k].Line, where, l.Name)
			} else {
				o.errorf(pos, "leak: the C handle %s (line %d) may still be open %s; pass it to the C function that closes it",
					l.Name, o.origin[k].Line, where)
			}
			if o.muted == 0 {
				o.st[k] = sDead
			}
			continue
		}
		if s == sLive {
			how := "free(" + l.Name + ")"
			if hasFreeMethod(l.Type) {
				how = l.Name + ".free()" // vec, map, arena...: free also frees what's inside
			}
			o.errorf(pos, "leak: %s still owns the memory (line %d) %s; free it with %s or pass ownership along",
				l.Name, o.origin[k].Line, where, how)
		} else {
			o.errorf(pos, "leak: %s may still own the memory (line %d) %s, depending on the path",
				l.Name, o.origin[k].Line, where)
		}
		if o.muted == 0 {
			o.st[k] = sDead // avoids repeating the error further on
		}
	}
}

func (o *ownChecker) leaksFrom(depth int, pos token.Pos, where string) {
	for i := len(o.scopes) - 1; i >= depth; i-- {
		o.leaks(o.scopes[i], pos, where)
	}
}

// borrowsComplete requires that no field of a borrowed object is left without a
// value when leaving the function: the lender expects the whole object back.
// borrowedFieldsBack checks, at the end of a borrowed variable's scope,
// that the fields moved out through it came back.
func (o *ownChecker) borrowedFieldsBack(l *Local, pos token.Pos) {
	for k := range o.st {
		if k.root == l && !k.isRoot() && o.st.get(k)&sDead != 0 {
			o.errorf(pos, "%s was moved out of the borrow (line %d) and must receive a value before the end of the scope of %s",
				k, o.gone[k].Line, l.Name)
			if o.muted == 0 {
				o.st[k] = sNull
			}
		}
	}
}

// borrowValue evaluates the value of x := <borrow>: &place checks the place
// (like a borrowed argument); the rest is read.
func (o *ownChecker) borrowValue(e ast.Expr) {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AMP {
		if obj, ok := o.borrowArg(e); ok {
			_ = obj
		}
		return
	}
	o.read(e)
}

func (o *ownChecker) borrowsComplete(pos token.Pos) {
	for k := range o.st {
		for _, b := range o.borrows {
			if k.root == b && !k.isRoot() && o.st.get(k)&sDead != 0 {
				o.errorf(pos, "%s was moved out of the borrow (line %d) and must receive a value before leaving the function",
					k, o.gone[k].Line)
				if o.muted == 0 {
					o.st[k] = sNull
				}
			}
		}
	}
}

// ---------- Statements ----------

func (o *ownChecker) stmt(s ast.Stmt) {
	if o.st == nil {
		return // unreachable code
	}
	info := o.c.info
	switch s := s.(type) {
	case *ast.Block:
		o.push()
		for _, st := range s.Stmts {
			o.stmt(st)
		}
		o.pop(s.End, "at the end of the block")
	case *ast.VarDecl:
		l := info.Decls[s]
		if isBorrow(l.Type) {
			// x := v.at(0): like a &T parameter, never nil nor freed; the
			// owning fields can leave and come back before the end of the scope
			if s.Value != nil {
				o.borrowValue(s.Value)
			}
			o.st[pathKey{l, ""}] = sLive | sOpaque
			o.scopes[len(o.scopes)-1] = append(o.scopes[len(o.scopes)-1], l)
			return
		}
		if !IsOwner(l.Type) {
			if s.Value != nil {
				o.read(s.Value)
			}
			return
		}
		v, pos := ownValue{st: sNull}, s.P
		if s.Value != nil {
			v, pos = o.consume(s.Value, l.Type, "the declaration of "+s.Name), s.Value.Pos()
		}
		o.declare(l, v, pos)
	case *ast.ConstDecl:
	case *ast.AssignStmt:
		if ix, isIndex := s.Lhs.(*ast.IndexExpr); isIndex && IsOwner(info.Types[s.Lhs]) {
			o.errorf(ix.Lbrack, "an owning element can't be overwritten directly (the old value would leak): use old := replace(&s[i], v)")
			o.consume(s.Rhs, nil, "")
			o.target(s.Lhs)
			return
		}
		if s.Op == token.ASSIGN {
			if k, ok := o.pathOf(s.Lhs); ok && IsOwner(pathType(k)) {
				o.assignOwner(k, s)
				return
			}
		}
		// The right side comes first: in p.x = f(p), f can free p.
		o.read(s.Rhs)
		o.target(s.Lhs)
	case *ast.IncDecStmt:
		o.target(s.X)
	case *ast.ExprStmt:
		o.read(s.X)
		if IsOwner(info.Types[s.X]) {
			o.errorf(s.X.Pos(), "leak: the owning pointer returned here is discarded; store it in a variable")
		}
	case *ast.ReturnStmt:
		if s.Value != nil {
			if t := info.Types[s.Value]; IsOwner(t) || t == NilType {
				o.consume(s.Value, o.fn.Result, "the return value of "+o.fn.Name)
			} else {
				o.read(s.Value)
			}
		}
		o.borrowsComplete(s.P)
		o.leaksFrom(0, s.P, "at the return")
		o.st = nil
	case *ast.IfStmt:
		o.read(s.Cond)
		base := o.st
		o.st = base.clone()
		o.apply(o.facts(s.Cond, true))
		o.stmt(s.Then)
		thenOut := o.st
		o.st = base.clone()
		o.apply(o.facts(s.Cond, false))
		if s.Else != nil {
			o.stmt(s.Else)
		}
		o.st = merge(thenOut, o.st)
	case *ast.ForStmt:
		o.forStmt(s)
	case *ast.MatchStmt:
		if info.MatchTakes[s] {
			o.consume(s.Subject, nil, "o match") // the owning fields go out to the names
		} else {
			o.read(s.Subject)
		}
		base := o.st
		var out ownFlow
		for _, arm := range s.Arms {
			o.st = base.clone()
			o.push()
			for _, p := range arm.Patterns {
				for _, l := range info.PatternBinds[p] {
					switch {
					case l == nil:
					case isBorrow(l.Type):
						// a borrowed field (a match that only reads): like x := v.at(0)
						o.st[pathKey{l, ""}] = sLive | sOpaque
						o.scopes[len(o.scopes)-1] = append(o.scopes[len(o.scopes)-1], l)
					case IsOwner(l.Type):
						// a taken field (a match that consumes): it must be freed or moved
						st := sLive | sOpaque
						if pt, ok := l.Type.(*Pointer); ok && pt.Nullable {
							st |= sNull
						}
						o.declare(l, ownValue{st: st}, l.Pos)
					}
				}
			}
			o.stmt(arm.Body)
			o.pop(arm.Body.Pos(), "at the end of the match arm")
			out = merge(out, o.st)
		}
		o.st = out
	case *ast.BranchStmt:
		loop := o.loops[len(o.loops)-1]
		where := "at the break"
		if s.Tok == token.CONTINUE {
			where = "at the continue"
		}
		o.leaksFrom(loop.depth, s.P, where)
		if s.Tok == token.BREAK {
			loop.exits = append(loop.exits, o.st.clone())
		} else {
			if loop.stmt.Post != nil {
				o.stmt(loop.stmt.Post)
			}
			loop.continues = append(loop.continues, o.st.clone())
		}
		o.st = nil
	default:
		panic("ownership: unknown statement")
	}
}

// assignOwner handles "k = value" with k an owning variable or field.
func (o *ownChecker) assignOwner(k pathKey, s *ast.AssignStmt) {
	v := o.consume(s.Rhs, pathType(k), "the assignment to "+k.String())
	if !k.isRoot() {
		o.reach(k, s.Lhs.Pos()) // the object that receives the field must exist
	}
	if cur := o.st.get(k); cur&sLive != 0 {
		maybe := ""
		if cur&sObligation != sLive {
			maybe = " (on some path)"
		}
		line := o.origin[k].Line
		if line == 0 {
			o.errorf(s.OpPos, "leak: %s may own memory%s and would be overwritten; free or move the old value first", k, maybe)
		} else {
			o.errorf(s.OpPos, "leak: %s still owns the memory (line %d)%s and would be overwritten; free it or pass ownership first",
				k, line, maybe)
		}
	}
	o.install(k, v)
	o.origin[k] = s.Rhs.Pos()
}

func (o *ownChecker) forStmt(s *ast.ForStmt) {
	o.push() // the init's scope
	if s.Init != nil {
		o.stmt(s.Init)
	}
	loop := &ownLoop{stmt: s, depth: len(o.scopes)}
	o.loops = append(o.loops, loop)

	// Fixed point: joins the state at the end of the iteration to the one at the start until it stops
	// changing. The states only grow and the number of paths is finite.
	entry := o.st
	for i := 0; i < 100; i++ {
		o.muted++
		back := o.loopBody(s, loop, entry)
		o.muted--
		next := merge(entry, back)
		if sameFlow(next, entry) {
			break
		}
		entry = next
	}
	o.loopBody(s, loop, entry) // final pass, with errors
	o.loops = o.loops[:len(o.loops)-1]

	var out ownFlow
	switch {
	case s.Cond != nil:
		o.st = entry.clone() // the condition became false
		o.read(s.Cond)
		o.apply(o.facts(s.Cond, false))
		out = o.st
	case s.Iter != nil:
		o.st = entry.clone() // the elements ran out
		iter := o.iterOf(s)
		o.sliceAccess(iter)
		if k, ok := o.pathOf(iter); ok && o.c.info.Drains[s] != nil && o.st.get(k) != 0 {
			// a complete drain: every element left the slice
			o.st[k] = o.st.get(k) &^ sOpaque
		}
		out = o.st
	}
	for _, ex := range loop.exits {
		out = merge(out, ex)
	}
	o.st = out
	o.pop(s.Body.End, "at the end of the for")
}

// loopBody analyzes an iteration from entry and returns the state the
// next iteration starts with (the end of the body and the continues, after the post statement).
func (o *ownChecker) loopBody(s *ast.ForStmt, loop *ownLoop, entry ownFlow) ownFlow {
	loop.exits, loop.continues = nil, nil
	o.st = entry.clone()
	if s.Cond != nil {
		o.read(s.Cond)
		o.apply(o.facts(s.Cond, true))
	}
	if s.Iter != nil {
		o.sliceAccess(o.iterOf(s)) // each iteration reads the slice again
	}
	if drained := o.c.info.Drains[s]; drained != nil && o.st != nil {
		o.drainBody(s, drained)
	} else {
		o.stmt(s.Body)
	}
	if o.st != nil && s.Post != nil {
		o.stmt(s.Post)
	}
	back := o.st
	for _, c := range loop.continues {
		back = merge(back, c)
	}
	return back
}

// sameFlow compares two states, including the deduced fields.
func sameFlow(a, b ownFlow) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	for _, pair := range [][2]ownFlow{{a, b}, {b, a}} {
		for k := range pair[0] {
			if pair[0].get(k) != pair[1].get(k) {
				return false
			}
		}
	}
	return true
}

func (o *ownChecker) errorf(pos token.Pos, format string, args ...any) {
	if o.muted == 0 {
		o.c.errorf(pos, format, args...)
	}
}

// ---------- Conditions on nil ----------

type nilFact struct {
	k      pathKey
	notNil bool
}

// facts returns what the condition, being truth, guarantees about ?*T paths:
// "p != nil", "p.next == nil", combined with &&, || and !.
func (o *ownChecker) facts(cond ast.Expr, truth bool) []nilFact {
	switch e := cond.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case token.EQL, token.NEQ:
			x, y := e.X, e.Y
			if isNil(x) {
				x, y = y, x
			}
			if !isNil(y) {
				return nil
			}
			k, ok := o.pathOf(x)
			if !ok || !IsOwner(pathType(k)) {
				return nil
			}
			return []nilFact{{k, (e.Op == token.NEQ) == truth}}
		case token.LAND:
			if truth {
				return append(o.facts(e.X, true), o.facts(e.Y, true)...)
			}
		case token.LOR:
			if !truth {
				return append(o.facts(e.X, false), o.facts(e.Y, false)...)
			}
		}
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return o.facts(e.X, !truth)
		}
	}
	return nil
}

func (o *ownChecker) apply(fs []nilFact) {
	if o.st == nil {
		return
	}
	for _, f := range fs {
		s := o.st.get(f.k)
		if f.notNil {
			o.st[f.k] = s &^ sNull
		} else {
			o.st[f.k] = s & (sNull | sOpaque)
		}
	}
}

// ---------- Paths and uses ----------

// pathOf recognizes expressions that are paths: an owning or borrowed variable,
// followed by fields.
func (o *ownChecker) pathOf(e ast.Expr) (pathKey, bool) {
	switch e := e.(type) {
	case *ast.Ident:
		l := o.c.info.Locals[e]
		if l != nil && (IsOwner(l.Type) || isBorrow(l.Type)) {
			return pathKey{l, ""}, true
		}
	case *ast.SelectorExpr:
		if k, ok := o.pathOf(e.X); ok {
			return k.child(e.Sel), true
		}
	}
	return pathKey{}, false
}

// check reports the use of k if it may be freed, or, if deref, if it may
// be nil.
func (o *ownChecker) check(k pathKey, pos token.Pos, deref bool) {
	if o.reported[k] {
		return
	}
	s := o.st.get(k)
	switch {
	case s&sDead != 0:
		o.markReported(k)
		if s&sObligation == sDead {
			o.errorf(pos, "%s was already freed or moved (line %d)", k, o.gone[k].Line)
		} else {
			o.errorf(pos, "%s may have already been freed or moved (line %d), depending on the path", k, o.gone[k].Line)
		}
	case deref && s&sNull != 0 && !isBorrow(pathType(k)) && !isSlice(pathType(k)):
		// (an empty slice can be indexed: the bounds check handles it)
		o.markReported(k)
		o.errorf(pos, "%s may be nil here: check with if %s != nil before using the pointee", k, k)
	}
}

// reach checks the pointers that must be followed to get to k.
func (o *ownChecker) reach(k pathKey, pos token.Pos) {
	if k.isRoot() {
		return
	}
	p := pathKey{k.root, ""}
	for _, f := range strings.Split(k.fields, ".") {
		if isPointerType(pathType(p)) {
			o.check(p, pos, true)
		}
		p = p.child(f)
	}
}

// deref checks that the pointee of e (a pointer or borrow) can be used.
func (o *ownChecker) deref(e ast.Expr) {
	k, ok := o.pathOf(e)
	if ix, isIndex := e.(*ast.IndexExpr); !ok && isIndex && IsOwner(o.c.info.Types[e]) {
		// The index is only known at run time: the checker doesn't
		// know whether s[i] is nil. The element must leave the slice to be used.
		o.errorf(ix.Lbrack, "s[i] may be nil and can't be used directly: take it out with take(&s[i]) and put it back with replace(&s[i], v)")
		o.read(e)
		return
	}
	if !ok {
		o.lostTemporary(e)
		o.read(e)
		return
	}
	o.reach(k, e.Pos())
	o.check(k, e.Pos(), true)
}

// complete requires that no field tracked below k is freed:
// an object only leaves whole (moved, borrowed, returned).
func (o *ownChecker) complete(k pathKey, pos token.Pos, verb string) {
	for p := range o.st {
		if p.under(k) && o.st.get(p)&sDead != 0 && !o.reported[p] {
			o.markReported(p)
			o.errorf(pos, "%s can't be %s: %s was moved out (line %d) and didn't receive a new value", k, verb, p, o.gone[p].Line)
		}
	}
}

// read evaluates an expression in which owning paths are only used.
func (o *ownChecker) read(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Ident:
		if k, ok := o.pathOf(e); ok {
			o.check(k, e.P, false)
		}
	case *ast.SelectorExpr:
		if isPointerType(o.c.info.Types[e.X]) {
			o.deref(e.X)
		} else {
			o.read(e.X)
		}
		if k, ok := o.pathOf(e); ok && IsOwner(pathType(k)) {
			o.check(k, e.SelPos, false)
		}
	case *ast.UnaryExpr:
		if e.Op == token.MUL {
			o.deref(e.X)
			return
		}
		o.read(e.X)
	case *ast.BinaryExpr:
		o.read(e.X)
		if e.Op != token.LAND && e.Op != token.LOR {
			o.read(e.Y)
			return
		}
		// Short-circuit: the right side only runs if the left one allowed it.
		skipped := o.st.clone()
		o.apply(o.facts(e.X, e.Op == token.LAND))
		o.read(e.Y)
		o.st = merge(skipped, o.st)
	case *ast.IndexExpr:
		o.sliceAccess(e.X)
		o.read(e.Index)
	case *ast.CompositeLit:
		for _, f := range e.Fields {
			o.read(f.Value)
		}
	case *ast.CallExpr:
		o.call(e)
	}
}

// lostTemporary reports *f() or f().field with f returning an owner.
func (o *ownChecker) lostTemporary(base ast.Expr) {
	// Only calls produce temporary owners; s[i] and p.field are places.
	if _, isCall := base.(*ast.CallExpr); isCall && IsOwner(o.c.info.Types[base]) {
		o.errorf(base.Pos(), "leak: the temporary owning pointer is lost here; store it in a variable first")
	}
}

// target evaluates the left side of an assignment that isn't an owning path.
func (o *ownChecker) target(e ast.Expr) {
	switch e := e.(type) {
	case *ast.IndexExpr:
		o.sliceAccess(e.X)
		o.read(e.Index)
	case *ast.SelectorExpr:
		if isPointerType(o.c.info.Types[e.X]) {
			o.deref(e.X)
		} else {
			o.target(e.X)
		}
	case *ast.UnaryExpr:
		o.deref(e.X)
	}
}

// consume evaluates an expression whose owning value goes to a place of type
// dest (a variable, field, return value, *T parameter).
func (o *ownChecker) consume(e ast.Expr, dest Type, what string) ownValue {
	var v ownValue
	switch {
	case isNil(e):
		v = ownValue{st: sNull}
	default:
		if k, ok := o.pathOf(e); ok && IsOwner(pathType(k)) {
			o.reach(k, e.Pos())
			o.check(k, e.Pos(), false)
			o.complete(k, e.Pos(), "moved")
			v = o.extract(k)
			v.st &^= sDead
			o.move(k, e.Pos())
			if p, ok := dest.(*Pointer); ok && !p.Nullable && v.st&sNull != 0 {
				o.errorf(e.Pos(), "%s may be nil here, but %s expects %s; check with if %s != nil first", k, what, dest, k)
			}
			return v
		}
		if ix, isIndex := e.(*ast.IndexExpr); isIndex {
			o.errorf(ix.Lbrack, "an owning element can't be copied or moved directly: use take(&s[i]) (leaves an empty slot)")
			o.read(e)
			return ownValue{st: sLive | sNull | sOpaque}
		}
		if call, ok := e.(*ast.CallExpr); ok && o.c.info.Calls[call].Builtin == Alloc {
			return o.allocValue(call)
		}
		if call, ok := e.(*ast.CallExpr); ok && o.c.info.Calls[call].Builtin == Realloc {
			// realloc keeps the elements: the new slice inherits what's known about them
			var old ownValue
			o.deferMoves(func() {
				old = o.consume(call.Args[0], o.c.info.Calls[call].Elem, "realloc")
				o.read(call.Args[1])
			})
			return ownValue{st: sLive | old.st&sOpaque}
		}
		o.read(e)
		v = ownValue{st: sLive | sOpaque}
		if p, ok := o.c.info.Types[e].(*Pointer); ok && p.Nullable {
			v.st |= sNull
		}
	}
	if p, ok := dest.(*Pointer); ok && !p.Nullable && v.st&sNull != 0 {
		o.errorf(e.Pos(), "the value may be nil here, but %s expects %s", what, dest)
	}
	return v
}

// allocValue handles alloc(T) (nil fields) and alloc(T{...}) (the literal's fields).
func (o *ownChecker) allocValue(call *ast.CallExpr) ownValue {
	callee := o.c.info.Calls[call]
	if callee.Zero {
		return ownValue{st: sLive}
	}
	if callee.Slice {
		o.read(call.Args[1]) // the size
		return ownValue{st: sLive}
	}
	if lit, ok := call.Args[0].(*ast.CompositeLit); ok {
		v := ownValue{st: sLive, sub: map[string]ownState{}}
		o.literalFields(lit, "", v.sub)
		return v
	}
	o.read(call.Args[0])
	return ownValue{st: sLive}
}

// literalFields evaluates the fields of a literal inside alloc, keeping the
// state of the owning fields in sub. Omitted fields (an empty literal) are nil.
func (o *ownChecker) literalFields(lit *ast.CompositeLit, prefix string, sub map[string]ownState) {
	s := o.c.info.Types[lit].(*Struct)
	for _, fi := range lit.Fields {
		f := s.Field(fi.Name)
		switch inner, isLit := fi.Value.(*ast.CompositeLit); {
		case IsOwner(f.Type):
			v := o.consume(fi.Value, f.Type, "the field "+f.Name)
			sub[prefix+f.Name] = v.st
			for rel, st := range v.sub {
				sub[prefix+f.Name+"."+rel] = st
			}
		case isLit && isResource(f.Type):
			o.literalFields(inner, prefix+f.Name+".", sub)
		default:
			o.read(fi.Value)
		}
	}
}

func (o *ownChecker) call(e *ast.CallExpr) {
	callee := o.c.info.Calls[e]
	if v, isCtor := o.c.info.EnumCtors[e]; isCtor {
		o.deferMoves(func() {
			for i, a := range e.Args {
				if i < len(v.Fields) && IsOwner(v.Fields[i].Type) {
					o.consume(a, v.Fields[i].Type, "the variant "+v.Name) // the owner goes into the enum
				} else {
					o.read(a)
				}
			}
		})
		return
	}
	switch callee.Builtin {
	case Free:
		o.free(e.Args[0])
		return
	case Drop:
		switch t := o.c.info.Types[e.Args[0]]; {
		case deepDrop(t):
			o.consume(e.Args[0], nil, "drop") // the drop function frees the inside
		case IsOwner(t):
			o.free(e.Args[0]) // drop of a simple owner is free
		default:
			o.read(e.Args[0])
		}
		return
	case Alloc:
		o.allocValue(e)
		return
	case Realloc:
		// the old slice is consumed; the result is a new owner
		o.deferMoves(func() {
			o.consume(e.Args[0], callee.Elem, "realloc")
			o.read(e.Args[1])
		})
		return
	case Take, Replace:
		o.deferMoves(func() { o.takeReplace(e, callee) })
		return
	case Convert:
		o.read(e.Args[0])
		return
	case ThreadSpawn:
		plan := o.c.info.Spawns[e]
		o.deferMoves(func() {
			o.read(e.Args[0])
			if IsOwner(plan.Ctx) {
				o.consume(e.Args[1], plan.Ctx, "the context of thread.spawn") // goes to the thread
			} else {
				o.read(e.Args[1])
			}
			o.read(e.Args[2])
		})
		return
	case ThreadJoin:
		p := o.c.info.Types[e.Args[0]].(*Pointer)
		o.consume(e.Args[0], o.c.pointerTo(p.Elem, false), "thread.join")
		return
	case ChanOp:
		o.chanOp(e)
		return
	case MutexOp:
		o.mutexOp(e)
		return
	case IoPrint, Len:
		for _, a := range e.Args {
			o.read(a)
		}
		if plan := o.c.info.Prints[e]; plan != nil && plan.Dst != nil {
			o.formatAlias(plan)
		}
		if plan := o.c.info.Prints[e]; plan != nil && plan.Recv != nil {
			o.read(plan.Recv) // the Writer of w.printf
		}
		return
	}

	if callee.Indirect {
		o.read(e.Fun) // the variable or the field with the fn value
	}
	// In the same call, an object can't be borrowed and moved.
	borrowedRoots := map[*Local]bool{}
	var touched []pathKey // borrowed objects: the function can change the fields
	var outs []pathKey    // &h of &?*X parameters: they receive a handle after the call
	o.deferMoves(func() { o.userArgs(e, callee, borrowedRoots, &touched, &outs) })
	for _, k := range outs {
		o.install(k, ownValue{st: sLive | sNull})
		o.origin[k] = e.Lparen
	}
	// After the call, the fields of the borrowed objects may have changed
	// (the function kept the object whole, but there's no knowing how).
	for _, obj := range touched {
		if o.st.get(obj) != 0 {
			o.dropUnder(obj)
			o.st[obj] = o.st.get(obj) | sOpaque
		}
	}
}

// userArgs evaluates the arguments of a user function call, left
// to right. The moves only apply after all of them (deferMoves).
func (o *ownChecker) userArgs(e *ast.CallExpr, callee Callee, borrowedRoots map[*Local]bool, touched *[]pathKey, outs *[]pathKey) {
	for i, a := range o.c.info.CallArgs(e) { // in a method, the receiver comes first
		context := "argument " + itoa(i+1) + " of " + callee.Func.Name
		pt := callee.Func.Params[i].Type
		switch {
		case isOutHandle(pt):
			o.outArg(a, callee.Func.Name, borrowedRoots, outs)
		case isOptArg(pt):
			switch {
			case isNil(a):
			case isOutHandle(pt.(*OptArg).Inner):
				o.outArg(a, callee.Func.Name, borrowedRoots, outs)
			default:
				if obj, ok := o.borrowArg(a); ok {
					borrowedRoots[obj.root] = true
					*touched = append(*touched, obj)
				}
			}
		case IsOwner(pt): // *T, ?*T and []T: the function takes ownership
			if k, ok := o.pathOf(a); ok && borrowedRoots[k.root] {
				o.errorf(a.Pos(), "%s is borrowed and moved in the same call", k.root.Name)
				o.kill(k, a.Pos())
				continue
			}
			o.consume(a, pt, context)
		case isBorrow(pt):
			if obj, ok := o.borrowArg(a); ok {
				if o.movingRoot(obj.root) {
					o.errorf(a.Pos(), "%s is borrowed and moved in the same call", obj.root.Name)
				}
				borrowedRoots[obj.root] = true
				*touched = append(*touched, obj)
			}
		default:
			o.read(a)
		}
	}
}

// borrowArg checks an argument passed to a &T parameter and returns the borrowed
// object, if it's tracked.
func (o *ownChecker) borrowArg(a ast.Expr) (pathKey, bool) {
	u, ok := a.(*ast.UnaryExpr)
	if !ok || u.Op != token.AMP {
		// a received borrow, passed along
		k, ok := o.pathOf(a)
		if ok {
			o.complete(k, a.Pos(), "borrowed")
			o.notDraining(k, a.Pos())
		}
		o.read(a)
		return k, ok
	}
	if sl, isSub := u.X.(*ast.SliceExpr); isSub {
		// &s[a:b] borrows part of the slice: it counts as borrowing the slice
		o.sliceAccess(sl.X)
		for _, b := range []ast.Expr{sl.Low, sl.High} {
			if b != nil {
				o.read(b)
			}
		}
		return o.pathOf(sl.X)
	}
	if ix, isIndex := u.X.(*ast.IndexExpr); isIndex {
		// &s[i] borrows an element: it counts as borrowing the slice
		if _, isPtr := o.c.info.Types[ix].(*Pointer); isPtr {
			o.errorf(ix.Lbrack, "s[i] may be nil and can't be borrowed directly: take it out with take(&s[i]) and put it back with replace(&s[i], v)")
		}
		o.sliceAccess(ix.X)
		o.read(ix.Index)
		return o.pathOf(ix.X)
	}
	k, ok := o.pathOf(u.X)
	if !ok {
		o.read(u.X) // a local struct: it has no owning fields
		return pathKey{}, false
	}
	var obj pathKey
	if isPointerType(pathType(k)) || IsOwner(pathType(k)) {
		// &p borrows the object pointed to by p; &s, the slice s; &e, the enum
		// with owners e (none of them can have been freed or moved)
		o.reach(k, u.X.Pos())
		o.check(k, u.X.Pos(), true)
		obj = k
	} else {
		// &p.node borrows a struct kept inside p's object
		o.reach(k, u.X.Pos())
		obj = objectOf(k)
	}
	o.notDraining(obj, u.X.Pos())
	o.complete(obj, u.X.Pos(), "borrowed")
	return obj, true
}

// free handles free(x): x can't be freed, and no owning field of the
// object can still have memory.
func (o *ownChecker) free(arg ast.Expr) {
	k, ok := o.pathOf(arg)
	if !ok || !IsOwner(pathType(k)) {
		v := o.consume(arg, nil, "free")
		if v.st&sOpaque != 0 && isOwnerSlice(o.c.info.Types[arg]) {
			o.errorf(arg.Pos(), "free of a temporary slice whose elements may be owners: store it in a variable and empty it with for x in drain(s)")
		}
		if v.st&sOpaque != 0 && len(ownerFields(elemOf(o.c.info.Types[arg]), "")) > 0 {
			o.errorf(arg.Pos(), "free of a temporary object with owning fields: store it in a variable and free the fields first")
		}
		return
	}
	o.reach(k, arg.Pos())
	if s := o.st.get(k); s&sDead != 0 {
		maybe := ""
		if s&sObligation != sDead {
			maybe = " on some path"
		}
		o.errorf(arg.Pos(), "%s freed twice: it was already freed or moved%s (line %d)", k, maybe, o.gone[k].Line)
		o.kill(k, arg.Pos())
		return
	}
	if s := o.st.get(k); s&sLive != 0 && s&sOpaque != 0 && isOwnerSlice(pathType(k)) {
		o.errorf(arg.Pos(), "free(%s): the elements of %s may still own memory; empty it first with for x in drain(%s)", k, k, k)
	}
	if o.st.get(k)&sLive != 0 {
		for _, rel := range ownerFields(elemOf(pathType(k)), "") {
			fk := k.child(rel)
			if fs := o.st.get(fk); fs&sLive != 0 {
				o.errorf(arg.Pos(), "free(%s): %s may still own memory; free or move %s first", k, fk, fk)
			}
		}
	}
	o.kill(k, arg.Pos())
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// markReported avoids repeating k's error, but only outside a loop's
// fixed point: there the errors are muted and must show up in the final pass.
func (o *ownChecker) markReported(k pathKey) {
	if o.muted == 0 {
		o.reported[k] = true
	}
}

// sliceAccess checks the slice used in s[i], len(s) or for x in s: it can't
// be freed. Empty is fine: the bounds check handles it.
func (o *ownChecker) sliceAccess(e ast.Expr) {
	k, ok := o.pathOf(e)
	if !ok {
		o.lostTemporary(e)
		o.read(e)
		return
	}
	if t := pathType(k); !IsOwner(t) && !isBorrow(t) {
		// an array kept in a field: it's a value, only the path to it matters
		o.reach(k, e.Pos())
		return
	}
	o.reach(k, e.Pos())
	o.check(k, e.Pos(), true)
}

func isOwnerSlice(t Type) bool {
	e, ok := sliceElem(t)
	return ok && IsOwner(e)
}

// iterOf returns the slice walked by a for in: s, in "for x in s" or in
// "for x in drain(s)".
func (o *ownChecker) iterOf(s *ast.ForStmt) ast.Expr {
	if d := o.c.info.Drains[s]; d != nil {
		return d
	}
	return s.Iter
}

// drainBody analyzes an iteration of "for x in drain(s)": x receives the element,
// which leaves the slice, and must be freed or moved before the end of the iteration.
func (o *ownChecker) drainBody(s *ast.ForStmt, drained ast.Expr) {
	k, ok := o.pathOf(drained)
	if ok {
		o.drains = append(o.drains, k)
	}
	o.push()
	if l := o.c.info.ForVars[s]; l != nil && IsOwner(l.Type) {
		// the taken element is an owner (in a generic, T can be int: then there's no ownership)
		o.declare(l, ownValue{st: sLive | sNull | sOpaque}, s.Var.P)
	}
	o.stmt(s.Body)
	o.pop(s.Body.End, "at the end of the for iteration (the element taken out by drain)")
	if ok {
		o.drains = o.drains[:len(o.drains)-1]
	}
}

// notDraining refuses to borrow or put elements into a slice that a
// for in drain is emptying: the elements already taken would come back.
func (o *ownChecker) notDraining(k pathKey, pos token.Pos) {
	for _, d := range o.drains {
		if d == k {
			o.errorf(pos, "%s is being emptied by a for in drain(%s) and can't be borrowed or receive elements", k, k)
			return
		}
	}
}

// takeReplace handles take(&s[i]) and replace(&s[i], v). The old element leaves
// as an ordinary owner. Putting an owner with replace makes the slice go back to "may have
// owning elements".
func (o *ownChecker) takeReplace(e *ast.CallExpr, callee Callee) {
	ix := e.Args[0].(*ast.UnaryExpr).X.(*ast.IndexExpr)
	o.sliceAccess(ix.X)
	o.read(ix.Index)
	if callee.Builtin != Replace {
		return
	}
	v := o.consume(e.Args[1], callee.Elem, "the value of replace")
	if k, ok := o.pathOf(ix.X); ok {
		o.notDraining(k, ix.X.Pos())
		if s := o.st.get(k); s != 0 && v.st&sLive != 0 {
			o.st[k] = s | sOpaque
		}
	}
}

// pendingMove is a move made by an argument: it only applies when the call
// happens, after all the arguments. So f(s, len(s)) reads s before
// passing it along, as in the generated C.
type pendingMove struct {
	k   pathKey
	pos token.Pos
}

// deferMoves evaluates f keeping the moves for the end.
func (o *ownChecker) deferMoves(f func()) {
	saved := o.pending
	var list []pendingMove
	o.pending = &list
	f()
	o.pending = saved
	for _, m := range list {
		o.move(m.k, m.pos)
	}
}

// move takes ownership away from k: right away, or at the end of the arguments of the call being
// evaluated. Moving the same thing twice in the call is an error.
func (o *ownChecker) move(k pathKey, pos token.Pos) {
	if o.pending == nil {
		o.kill(k, pos)
		return
	}
	for _, m := range *o.pending {
		if m.k == k || k.under(m.k) || m.k.under(k) {
			o.errorf(pos, "%s is moved twice in the same call", k)
			return
		}
	}
	*o.pending = append(*o.pending, pendingMove{k, pos})
}

// movingRoot says whether some already evaluated argument of the call moves something from root.
func (o *ownChecker) movingRoot(root *Local) bool {
	if o.pending == nil {
		return false
	}
	for _, m := range *o.pending {
		if m.k.root == root {
			return true
		}
	}
	return false
}

// formatAlias refuses str.format(&b, "{}", b): b's text would be read
// while b is overwritten. Values copied whole (numbers, structs without
// slices) were already computed before the write and don't count.
func (o *ownChecker) formatAlias(plan *PrintPlan) {
	dst := rootLocal(o.c, plan.Dst)
	if dst == nil {
		return
	}
	for i, a := range plan.Args {
		switch plan.ArgTypes[i].(type) {
		case *Slice, *Borrow:
			if rootLocal(o.c, a) == dst {
				o.errorf(a.Pos(), "%s is the destination of str.format and also a value of the format: the text would be read while it's overwritten", dst.Name)
			}
		}
	}
}

// rootLocal returns the variable e comes out of (b in &b[2:], p.name, *x), or nil.
func rootLocal(c *checker, e ast.Expr) *Local {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return c.info.Locals[x]
		case *ast.UnaryExpr:
			e = x.X
		case *ast.SliceExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		default:
			return nil
		}
	}
}

// outArg checks &h passed to a &?*X parameter: h can't hold a handle
// (the C function would write another over it), and it receives the new one after the
// call (outs).
func (o *ownChecker) outArg(a ast.Expr, fn string, borrowedRoots map[*Local]bool, outs *[]pathKey) {
	u, ok := a.(*ast.UnaryExpr)
	if !ok {
		return
	}
	k, ok := o.pathOf(u.X)
	if !ok {
		return
	}
	if !k.isRoot() {
		o.reach(k, u.X.Pos())
	}
	if o.st.get(k)&sLive != 0 {
		o.errorf(a.Pos(), "leak: %s may hold a handle, and %s would write another over it; close the old one first", k, fn)
	}
	for _, prev := range *outs {
		if prev == k {
			o.errorf(a.Pos(), "%s receives two handles in the same call", k)
		}
	}
	if borrowedRoots[k.root] || o.movingRoot(k.root) {
		o.errorf(a.Pos(), "%s is used in two ways in the same call", k.root.Name)
	}
	*outs = append(*outs, k)
}

// chanOp handles chan.new and the ends' methods: close consumes the end; send
// moves the value into the channel; the others only use the end.
func (o *ownChecker) chanOp(e *ast.CallExpr) {
	plan := o.c.info.Chans[e]
	if plan.Op == "new" {
		o.read(e.Args[0])
		return
	}
	recv := e.Fun.(*ast.SelectorExpr).X
	if plan.Op == "close" {
		end := o.c.info.Types[recv]
		if b, ok := end.(*Borrow); ok {
			o.errorf(recv.Pos(), "close needs the channel end (ownership), and %s is a borrow (%s)", exprName(recv), b)
			return
		}
		o.consume(recv, o.c.pointerTo(structBehind(end), false), "close")
		return
	}
	o.deferMoves(func() {
		o.read(recv)
		if plan.Op == "send" {
			if IsOwner(plan.Elem) {
				o.consume(e.Args[0], plan.Elem, "the value of send") // goes into the channel
			} else {
				o.read(e.Args[0])
			}
		}
	})
}

// mutexOp handles mutex.new and the methods of Mutex and Guard: close and unlock
// consume; new moves the value inside the mutex; the others only use.
func (o *ownChecker) mutexOp(e *ast.CallExpr) {
	plan := o.c.info.Mutexes[e]
	if plan.Op == "new" {
		if IsOwner(plan.Elem) {
			o.consume(e.Args[0], plan.Elem, "the value of mutex.new") // goes inside the mutex
		} else {
			o.read(e.Args[0])
		}
		return
	}
	recv := e.Fun.(*ast.SelectorExpr).X
	if plan.Op == "close" || plan.Op == "unlock" {
		t := o.c.info.Types[recv]
		if b, ok := t.(*Borrow); ok {
			o.errorf(recv.Pos(), "%s needs the owner, and %s is a borrow (%s)", plan.Op, exprName(recv), b)
			return
		}
		o.consume(recv, o.c.pointerTo(structBehind(t), false), plan.Op)
		return
	}
	o.read(recv)
}
