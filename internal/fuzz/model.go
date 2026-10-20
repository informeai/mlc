package fuzz

import (
	"fmt"
	"strings"
)

// The reference model: an abstract interpreter that follows every
// path of an enumerated program (both sides of each if, the loop with
// any number of iterations) and judges each path alone. It's much smaller than
// the checker, and so it errs in a different way: where the two disagree, one
// of them is wrong, and the example is already minimal.
//
// The semantics is the one MLC intends:
//   - x and y (*Box) start live; q (?*Box) starts nil or live (the caller
//     decides), and all of them must end freed or moved;
//   - using, moving or freeing requires the variable to be live (freeing a nil q is fine);
//   - overwriting a live variable leaks;
//   - a borrow (r := &x) is exclusive: from the moment it's made until its
//     last use on the path, x can't be touched (nor read). Outside
//     that, x is free: the borrow ends at the last use on each path.
//
// Each declaration r := &v is a borrow of its own: an r declared inside
// a block hides the outer one only until the end of the block, and then the outer one is
// valid again (the uses are bound to the declaration by scope, before running).
//
// A path's state is small (the state of the three variables and of the
// borrows), so paths that reach the same state merge, and the
// loop is computed to a fixed point: the set of states at the start of the
// iteration stops growing.

const maxRefs = 8

type mref struct {
	src     int8 // the borrowed variable, -1 if the declaration hasn't run yet
	touched bool // the source was touched after the borrow
}

type mstate struct {
	vars [3]state // x, y, q
	refs [maxRefs]mref
	bad  string // the first violation on the path
}

// model binds each r (declaration and use) to the declaration's number.
type model struct {
	ref map[*Stmt]int
}

// newModel resolves the program's r by scope.
func newModel(ss []*Stmt) *model {
	m := &model{ref: map[*Stmt]int{}}
	next := 0
	var resolve func(ss []*Stmt, visible int)
	resolve = func(ss []*Stmt, visible int) {
		for _, st := range ss {
			switch {
			case strings.HasPrefix(st.Atom, "r := "):
				if next == maxRefs {
					panic("model: too many declarations of r")
				}
				visible = next
				m.ref[st] = next
				next++
			case st.Atom == "acc += r.v":
				m.ref[st] = visible
			}
			resolve(st.Body, visible)
			resolve(st.Else, visible)
		}
	}
	resolve(ss, -1)
	return m
}

// mset is a set of states (the paths that reach a point).
type mset map[mstate]bool

func (s mset) add(o mset) {
	for k := range o {
		s[k] = true
	}
}

func (s *mstate) fail(format string, args ...any) {
	if s.bad == "" {
		s.bad = fmt.Sprintf(format, args...)
	}
}

var varNames = [3]string{"x", "y", "q"}

// touch records a touch on v: v's borrows can no longer be
// used.
func (s *mstate) touch(v int) {
	for i := range s.refs {
		if int(s.refs[i].src) == v {
			s.refs[i].touched = true
		}
	}
}

// need requires v to be live (or nil, if nilOK).
func (s *mstate) need(v int, what string, nilOK bool) {
	s.touch(v)
	switch st := s.vars[v]; {
	case st == live, st == null && nilOK:
	case st == null:
		s.fail("%s of %s, which is nil", what, varNames[v])
	default:
		s.fail("%s of %s, already freed or moved", what, varNames[v])
	}
}

// overwrite assigns a new value to v: the old one can't be live.
func (s *mstate) overwrite(v int, to state) {
	s.touch(v)
	if s.vars[v] == live {
		s.fail("leak: %s still live is overwritten", varNames[v])
	}
	s.vars[v] = to
}

func (s mstate) atom(a string, ref int) mstate {
	const x, y, q = 0, 1, 2
	switch a {
	case "free(x)", "free(y)":
		v := x
		if a == "free(y)" {
			v = y
		}
		s.need(v, "free", false)
		s.vars[v] = dead
	case "free(q)":
		s.need(q, "free", true)
		s.vars[q] = dead
	case "x = alloc(Box{v: 1})":
		s.overwrite(x, live)
	case "q = alloc(Box{v: 1})":
		s.overwrite(q, live)
	case "acc += x.v":
		s.need(x, "use", false)
	case "acc += y.v":
		s.need(y, "use", false)
	case "use q":
		s.touch(q)
		if s.vars[q] == dead {
			s.fail("use of q, already freed or moved")
		}
	case "y = x", "x = y", "q = x":
		from, to := x, y
		switch a {
		case "x = y":
			from, to = y, x
		case "q = x":
			from, to = x, q
		}
		s.need(from, "move", false)
		s.vars[from] = dead
		s.overwrite(to, live)
	case "r := &x", "r := &y":
		v := x
		if a == "r := &y" {
			v = y
		}
		s.need(v, "borrow", false)
		s.refs[ref] = mref{src: int8(v)}
	case "acc += r.v":
		switch {
		case ref < 0 || s.refs[ref].src < 0:
			s.fail("use of r without a borrow")
		case s.refs[ref].touched:
			s.fail("%s touched while borrowed by r", varNames[s.refs[ref].src])
		}
	case "acc += 1":
		// touches nothing (the neutral statement of the metamorphic tests)
	default:
		panic("model: unknown statement " + a)
	}
	return s
}

// runModel runs the list from each state and returns the states of
// whoever finished normally and of whoever left through break.
func (m *model) run(ss []*Stmt, in mset) (normal, broke mset) {
	cur, broke := in, mset{}
	for _, st := range ss {
		next := mset{}
		for s := range cur {
			if s.bad != "" {
				next[s] = true // the path already failed: stays stopped
				continue
			}
			switch {
			case st.Atom == "break":
				broke[s] = true
			case st.Atom != "":
				ref, ok := m.ref[st]
				if !ok {
					ref = -1
				}
				next[s.atom(st.Atom, ref)] = true
			case st.If:
				n, b := m.run(st.Body, mset{s: true})
				next.add(n)
				broke.add(b)
				if st.HasElse {
					n, b = m.run(st.Else, mset{s: true})
					next.add(n)
					broke.add(b)
				} else {
					next[s] = true
				}
			case st.Loop:
				next.add(m.loop(st.Body, s))
			case st.Block:
				n, b := m.run(st.Body, mset{s: true})
				next.add(n)
				broke.add(b)
			}
		}
		cur = next
	}
	return cur, broke
}

// loop computes the states at the loop's exit: the set of states at the
// start of an iteration grows until it stops (fixed point); from each one, the
// condition can be false. A break also leaves.
func (m *model) loop(body []*Stmt, s mstate) mset {
	heads := mset{s: true}
	exits := mset{}
	frontier := mset{s: true}
	for len(frontier) > 0 {
		in := mset{}
		for h := range frontier {
			if h.bad == "" {
				in[h] = true
			}
		}
		n, b := m.run(body, in)
		exits.add(b)
		frontier = mset{}
		for h := range n {
			if !heads[h] {
				heads[h] = true
				frontier[h] = true
			}
		}
	}
	exits.add(heads) // the condition was false at the start of some iteration
	return exits
}

// start are the initial states: q nil or live.
func start() mset {
	a := mstate{vars: [3]state{live, live, null}}
	for i := range a.refs {
		a.refs[i].src = -1
	}
	b := a
	b.vars[2] = live
	return mset{a: true, b: true}
}

// Verdict is the model's verdict: "" if every path is safe, or what went
// wrong on an unsafe path.
func Verdict(ss []*Stmt) string {
	final, _ := newModel(ss).run(ss, start())
	bad := ""
	for s := range final {
		if s.bad == "" {
			for v, st := range s.vars {
				if st == live {
					s.fail("leak: %s still live at the end", varNames[v])
				}
			}
		}
		if s.bad != "" && (bad == "" || s.bad < bad) {
			bad = s.bad // the smallest, so the report doesn't depend on the map's order
		}
	}
	return bad
}

// Epilogue returns the frees that close the program: a variable that ends
// live on every safe path is freed at the end (q too when it may be
// nil, because free of nil does nothing). One that ends live only on some
// paths stays as it is: the program leaks either way.
func Epilogue(ss []*Stmt) []*Stmt {
	final, _ := newModel(ss).run(ss, start())
	var out []*Stmt
	for v := range varNames {
		some, all := false, true
		for s := range final {
			if s.bad != "" {
				continue
			}
			st := s.vars[v]
			some = some || st == live
			all = all && (st == live || (v == 2 && st == null))
		}
		if some && all {
			out = append(out, &Stmt{Atom: "free(" + varNames[v] + ")"})
		}
	}
	return out
}
