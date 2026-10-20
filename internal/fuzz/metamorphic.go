package fuzz

// Phase 5: metamorphic tests. Each transformation here doesn't change a
// program's safety; if the checker gives a different verdict
// for the transformed version, it's looking at the shape of the code, not at its
// meaning.

// Variant is a transformed program.
type Variant struct {
	Kind string
	Prog []*Stmt
}

// Variants returns the transformed versions of an enumerated program:
//
//   - "block": the whole body inside a block { };
//   - "inverted if": if c { A } else { B } becomes if !c { B } else { A };
//   - "empty else": if c { A } gets an empty else { };
//   - "swap": two neighboring statements that don't touch the same variables
//     swap places;
//   - "neutral": a statement that touches nothing (acc += 1) goes between two.
func Variants(ss []*Stmt) []Variant {
	var out []Variant
	out = append(out, Variant{"block", []*Stmt{{Block: true, Body: ss}}})
	eachList(ss, func(ns []*Stmt, rebuild func([]*Stmt) []*Stmt) {
		for i, n := range ns {
			if n.If && n.HasElse {
				inv := *n
				inv.Negated = !n.Negated
				inv.Body, inv.Else = n.Else, n.Body
				out = append(out, Variant{"inverted if", rebuild(replaced(ns, i, &inv))})
			}
			if n.If && !n.HasElse {
				e := *n
				e.HasElse = true
				out = append(out, Variant{"empty else", rebuild(replaced(ns, i, &e))})
			}
			if i+1 < len(ns) && independent(ns[i], ns[i+1]) {
				sw := append([]*Stmt{}, ns...)
				sw[i], sw[i+1] = sw[i+1], sw[i]
				out = append(out, Variant{"swap", rebuild(sw)})
			}
		}
		for i := 0; i <= len(ns); i++ {
			ins := append(append(append([]*Stmt{}, ns[:i]...), &Stmt{Atom: "acc += 1"}), ns[i:]...)
			out = append(out, Variant{"neutral", rebuild(ins)})
		}
	})
	return out
}

func replaced(ns []*Stmt, i int, n *Stmt) []*Stmt {
	c := append([]*Stmt{}, ns...)
	c[i] = n
	return c
}

// eachList calls f with each statement list of the program (the body and the ones
// inside blocks), along with the function that rebuilds the whole program
// with the list replaced.
func eachList(ss []*Stmt, f func(ns []*Stmt, rebuild func([]*Stmt) []*Stmt)) {
	var walk func(ns []*Stmt, rebuild func([]*Stmt) []*Stmt)
	walk = func(ns []*Stmt, rebuild func([]*Stmt) []*Stmt) {
		f(ns, rebuild)
		for i, n := range ns {
			i, n := i, n
			walk(n.Body, func(body []*Stmt) []*Stmt {
				c := *n
				c.Body = body
				return rebuild(replaced(ns, i, &c))
			})
			if n.HasElse {
				walk(n.Else, func(els []*Stmt) []*Stmt {
					c := *n
					c.Else = els
					return rebuild(replaced(ns, i, &c))
				})
			}
		}
	}
	walk(ss, func(ns []*Stmt) []*Stmt { return ns })
}

// touches says what a statement touches (r counts as the two variables it
// can borrow; break and blocks, as everything).
func touches(s *Stmt) map[string]bool {
	all := map[string]bool{"x": true, "y": true, "q": true, "r": true}
	switch s.Atom {
	case "free(x)", "x = alloc(Box{v: 1})", "acc += x.v":
		return map[string]bool{"x": true}
	case "free(y)", "acc += y.v":
		return map[string]bool{"y": true}
	case "free(q)", "q = alloc(Box{v: 1})", "use q":
		return map[string]bool{"q": true}
	case "y = x", "x = y":
		return map[string]bool{"x": true, "y": true}
	case "q = x":
		return map[string]bool{"q": true, "x": true}
	case "r := &x":
		return map[string]bool{"r": true, "x": true}
	case "r := &y":
		return map[string]bool{"r": true, "y": true}
	case "acc += r.v":
		return map[string]bool{"r": true, "x": true, "y": true}
	}
	return all
}

func independent(a, b *Stmt) bool {
	if a.Atom == "" || b.Atom == "" || a.Atom == "break" || b.Atom == "break" {
		return false
	}
	tb := touches(b)
	for v := range touches(a) {
		if tb[v] {
			return false
		}
	}
	return true
}

// hasRef says whether the program uses borrows (r).
func hasRef(ss []*Stmt) bool {
	for _, s := range ss {
		if s.Atom == "acc += r.v" || len(s.Atom) > 4 && s.Atom[:4] == "r :=" || hasRef(s.Body) || hasRef(s.Else) {
			return true
		}
	}
	return false
}
