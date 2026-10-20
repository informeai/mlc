package fuzz

import "fmt"

// O modelo de referência: um interpretador abstrato que segue todos os
// caminhos de um programa enumerado (os dois lados de cada if, o laço com
// qualquer número de voltas) e julga cada caminho sozinho. É bem menor que
// o verificador, e por isso erra de outro jeito: onde os dois discordam, um
// deles está errado, e o exemplo já é mínimo.
//
// A semântica é a que o MLC pretende:
//   - x e y (*Box) começam vivos; q (?*Box) começa nil ou vivo (quem chama
//     decide), e todos precisam terminar liberados ou movidos;
//   - usar, mover ou liberar exige a variável viva (liberar um q nil pode);
//   - sobrescrever uma variável viva vaza;
//   - um empréstimo (r := &x) é exclusivo: do momento em que é feito até o
//     último uso dele no caminho, x não pode ser tocado (nem lido). Fora
//     disso, x é livre: o empréstimo acaba no último uso de cada caminho.
//
// O estado de um caminho é pequeno (o estado das três variáveis e do
// empréstimo), então caminhos que chegam ao mesmo estado se fundem, e o laço
// é calculado até o ponto fixo: o conjunto de estados no começo da volta
// para de crescer.

type mstate struct {
	vars    [3]state // x, y, q
	ref     int      // a variável emprestada em r, -1 se nenhuma
	touched bool     // a origem de r foi tocada depois do empréstimo
	bad     string   // a primeira violação no caminho
}

// mset é um conjunto de estados (os caminhos que chegam a um ponto).
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

// touch registra um toque em v: se v está emprestada, o empréstimo não
// pode mais ser usado.
func (s *mstate) touch(v int) {
	if s.ref == v {
		s.touched = true
	}
}

// need exige v viva (ou nil, se nilOK).
func (s *mstate) need(v int, what string, nilOK bool) {
	s.touch(v)
	switch st := s.vars[v]; {
	case st == live, st == null && nilOK:
	case st == null:
		s.fail("%s de %s, que é nil", what, varNames[v])
	default:
		s.fail("%s de %s, já liberada ou movida", what, varNames[v])
	}
}

// overwrite atribui um valor novo a v: o antigo não pode estar vivo.
func (s *mstate) overwrite(v int, to state) {
	s.touch(v)
	if s.vars[v] == live {
		s.fail("vazamento: %s ainda viva é sobrescrita", varNames[v])
	}
	s.vars[v] = to
}

func (s mstate) atom(a string) mstate {
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
		s.need(x, "uso", false)
	case "acc += y.v":
		s.need(y, "uso", false)
	case "use q":
		s.touch(q)
		if s.vars[q] == dead {
			s.fail("uso de q, já liberada ou movida")
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
		s.need(v, "empréstimo", false)
		s.ref, s.touched = v, false
	case "acc += r.v":
		switch {
		case s.ref < 0:
			s.fail("uso de r sem empréstimo")
		case s.touched:
			s.fail("%s tocada enquanto emprestada a r", varNames[s.ref])
		}
	default:
		panic("modelo: comando desconhecido " + a)
	}
	return s
}

// runModel roda a lista a partir de cada estado e devolve os estados de
// quem terminou normalmente e de quem saiu por break.
func runModel(ss []*Stmt, in mset) (normal, broke mset) {
	cur, broke := in, mset{}
	for _, st := range ss {
		next := mset{}
		for s := range cur {
			if s.bad != "" {
				next[s] = true // o caminho já falhou: segue parado
				continue
			}
			switch {
			case st.Atom == "break":
				broke[s] = true
			case st.Atom != "":
				next[s.atom(st.Atom)] = true
			case st.If:
				n, b := runModel(st.Body, mset{s: true})
				next.add(n)
				broke.add(b)
				if st.HasElse {
					n, b = runModel(st.Else, mset{s: true})
					next.add(n)
					broke.add(b)
				} else {
					next[s] = true
				}
			case st.Loop:
				next.add(loop(st.Body, s))
			}
		}
		cur = next
	}
	return cur, broke
}

// loop calcula os estados na saída do laço: o conjunto dos estados no
// começo de uma volta cresce até parar (ponto fixo); de cada um, a
// condição pode dar falso. Um break também sai.
func loop(body []*Stmt, s mstate) mset {
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
		n, b := runModel(body, in)
		exits.add(b)
		frontier = mset{}
		for h := range n {
			if !heads[h] {
				heads[h] = true
				frontier[h] = true
			}
		}
	}
	exits.add(heads) // a condição deu falso no começo de qualquer volta
	return exits
}

// start são os estados iniciais: q nil ou vivo.
func start() mset {
	return mset{
		{vars: [3]state{live, live, null}, ref: -1}: true,
		{vars: [3]state{live, live, live}, ref: -1}: true,
	}
}

// Verdict é o veredito do modelo: "" se todo caminho é seguro, ou o que deu
// errado num caminho inseguro.
func Verdict(ss []*Stmt) string {
	final, _ := runModel(ss, start())
	bad := ""
	for s := range final {
		if s.bad == "" {
			for v, st := range s.vars {
				if st == live {
					s.fail("vazamento: %s ainda viva no fim", varNames[v])
				}
			}
		}
		if s.bad != "" && (bad == "" || s.bad < bad) {
			bad = s.bad // o menor, para o relatório não depender da ordem do mapa
		}
	}
	return bad
}

// Epilogue devolve os free que fecham o programa: uma variável que termina
// viva em todo caminho seguro é liberada no fim (q também quando pode ser
// nil, porque free de nil não faz nada). Uma que termina viva só em alguns
// caminhos fica como está: o programa vaza de qualquer jeito.
func Epilogue(ss []*Stmt) []*Stmt {
	final, _ := runModel(ss, start())
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
