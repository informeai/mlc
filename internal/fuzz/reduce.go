package fuzz

// Reduce encolhe um programa enquanto keep continuar valendo: tira um
// comando de cada vez (e troca um bloco pelos comandos de dentro), até
// nenhuma tentativa funcionar. O comando com o bug injetado nunca sai. O
// resultado é o menor programa que ainda reproduz o achado.
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

// shrinks devolve as variações de p com um comando a menos ou um bloco
// aberto, das maiores reduções para as menores.
func shrinks(p *Program) []*Program {
	var out []*Program
	var walk func(path []int, ns []*Node)
	walk = func(path []int, ns []*Node) {
		// sequências de comandos vizinhos (um par declaração e uso só sai
		// junto), das maiores para as menores
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

// cut copia p sem os w nós a partir de path.
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

// edit copia p trocando o nó em path (índices alternados: posição na lista,
// depois 0 para o corpo ou 1 para o else) pelos nós de with.
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
