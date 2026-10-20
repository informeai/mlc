package fuzz

// Reduce shrinks a program while keep still holds: it removes one
// statement at a time (and replaces a block with the statements inside), until
// no attempt works. The statement with the injected bug never leaves. The
// result is the smallest program that still reproduces the finding.
func Reduce(p *Program, keep func(*Program) bool) *Program {
	cur := p
	for changed := true; changed; {
		changed = false
		for _, cand := range shrinks(cur) {
			if keep(cand) {
				cur, changed = cand, true
				break
			}
		}
	}
	return cur
}

// shrinks returns the variations of p with one statement fewer or one block
// opened, from the largest reductions to the smallest.
func shrinks(p *Program) []*Program {
	var out []*Program
	var walk func(path []int, ns []*Node)
	walk = func(path []int, ns []*Node) {
		// sequences of neighboring statements (a declaration and use pair only leaves
		// together), from the largest to the smallest
		for w := 4; w >= 2; w-- {
			for i := 0; i+w <= len(ns); i++ {
				if !anyBug(ns[i : i+w]) {
					out = append(out, cut(p, append(append([]int{}, path...), i), w))
				}
			}
		}
		for i, n := range ns {
			here := append(append([]int{}, path...), i)
			if !n.Bug && !hasBug(n) {
				out = append(out, edit(p, here, nil))
			}
			if n.Close {
				out = append(out, edit(p, here, append(append([]*Node{}, n.Body...), n.Else...)))
				walk(append(here, 0), n.Body)
				walk(append(here, 1), n.Else)
			}
		}
	}
	walk(nil, p.Body)
	return out
}

func anyBug(ns []*Node) bool {
	for _, n := range ns {
		if n.Bug || hasBug(n) {
			return true
		}
	}
	return false
}

// cut copies p without the w nodes starting at path.
func cut(p *Program, path []int, w int) *Program {
	q := *p
	q.Body = cutList(p.Body, path, w)
	return &q
}

func cutList(ns []*Node, path []int, w int) []*Node {
	i := path[0]
	if len(path) == 1 {
		return append(append([]*Node{}, ns[:i]...), ns[i+w:]...)
	}
	out := append([]*Node{}, ns[:i]...)
	n := *ns[i]
	if path[1] == 0 {
		n.Body = cutList(n.Body, path[2:], w)
	} else {
		n.Else = cutList(n.Else, path[2:], w)
	}
	out = append(out, &n)
	return append(out, ns[i+1:]...)
}

func hasBug(n *Node) bool {
	for _, c := range append(append([]*Node{}, n.Body...), n.Else...) {
		if c.Bug || hasBug(c) {
			return true
		}
	}
	return false
}

// edit copies p replacing the node at path (alternating indices: position in the list,
// then 0 for the body or 1 for the else) with the nodes of with.
func edit(p *Program, path []int, with []*Node) *Program {
	q := *p
	q.Body = editList(p.Body, path, with)
	return &q
}

func editList(ns []*Node, path []int, with []*Node) []*Node {
	i := path[0]
	out := append([]*Node{}, ns[:i]...)
	if len(path) == 1 {
		out = append(out, with...)
		return append(out, ns[i+1:]...)
	}
	n := *ns[i]
	if path[1] == 0 {
		n.Body = editList(n.Body, path[2:], with)
	} else {
		n.Else = editList(n.Else, path[2:], with)
	}
	out = append(out, &n)
	return append(out, ns[i+1:]...)
}
