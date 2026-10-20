package fuzz

// Fase 5: testes metamórficos. Cada transformação aqui não muda a
// segurança de um programa; se o verificador der um veredito diferente
// para a versão transformada, ele está olhando a forma do código, não o
// significado.

// Variant é um programa transformado.
type Variant struct {
	Kind string
	Prog []*Stmt
}

// Variants devolve as versões transformadas de um programa enumerado:
//
//   - "bloco": o corpo inteiro dentro de um bloco { };
//   - "if invertido": if c { A } else { B } vira if !c { B } else { A };
//   - "else vazio": if c { A } ganha um else { } vazio;
//   - "troca": dois comandos vizinhos que não mexem nas mesmas variáveis
//     trocam de lugar;
//   - "neutro": um comando que não mexe em nada (acc += 1) entra entre dois.
func Variants(ss []*Stmt) []Variant {
	var out []Variant
	out = append(out, Variant{"bloco", []*Stmt{{Block: true, Body: ss}}})
	eachList(ss, func(ns []*Stmt, rebuild func([]*Stmt) []*Stmt) {
		for i, n := range ns {
			if n.If && n.HasElse {
				inv := *n
				inv.Negated = !n.Negated
				inv.Body, inv.Else = n.Else, n.Body
				out = append(out, Variant{"if invertido", rebuild(replaced(ns, i, &inv))})
			}
			if n.If && !n.HasElse {
				e := *n
				e.HasElse = true
				out = append(out, Variant{"else vazio", rebuild(replaced(ns, i, &e))})
			}
			if i+1 < len(ns) && independent(ns[i], ns[i+1]) {
				sw := append([]*Stmt{}, ns...)
				sw[i], sw[i+1] = sw[i+1], sw[i]
				out = append(out, Variant{"troca", rebuild(sw)})
			}
		}
		for i := 0; i <= len(ns); i++ {
			ins := append(append(append([]*Stmt{}, ns[:i]...), &Stmt{Atom: "acc += 1"}), ns[i:]...)
			out = append(out, Variant{"neutro", rebuild(ins)})
		}
	})
	return out
}

func replaced(ns []*Stmt, i int, n *Stmt) []*Stmt {
	c := append([]*Stmt{}, ns...)
	c[i] = n
	return c
}

// eachList chama f com cada lista de comandos do programa (o corpo e os de
// dentro dos blocos), junto com a função que remonta o programa inteiro
// com a lista trocada.
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

// touches diz em que um comando mexe (r conta como as duas variáveis que
// pode emprestar; break e os blocos, como tudo).
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

// hasRef diz se o programa usa empréstimos (r).
func hasRef(ss []*Stmt) bool {
	for _, s := range ss {
		if s.Atom == "acc += r.v" || len(s.Atom) > 4 && s.Atom[:4] == "r :=" || hasRef(s.Body) || hasRef(s.Else) {
			return true
		}
	}
	return false
}
