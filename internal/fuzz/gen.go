package fuzz

import (
	"fmt"
	"math/rand"
	"strings"
)

// Fase 3: programas aleatórios focados em posse.
//
// O gerador acompanha o estado de cada variável (viva, liberada, nil) e, por
// construção, sabe se o programa tem bug de posse:
//
//   - um programa válido precisa ser aceito pelo verificador (se não for, é
//     um candidato a falso positivo, um caso em que o verificador é
//     conservador demais);
//   - um programa com um bug injetado (uso depois de free, free duplo,
//     vazamento, empréstimo pendurado, struct liberada com campo vivo,
//     enum lido depois de free) precisa ser recusado.
//
// As condições dos if são conhecidas (one == 1 é sempre verdade, one == 2
// nunca), e os laços rodam pelo menos uma vez. O bug só é injetado em
// código que roda, e sempre de um jeito que a execução vê: lendo a memória
// liberada, liberando duas vezes, deixando alocação no fim. Assim, se o
// verificador aceitar um programa com bug, o oráculo (AddressSanitizer e o
// contador de alocações) confirma o buraco.

// Program é um programa gerado.
type Program struct {
	Seed  int64
	Body  []*Node // o corpo de main, sem as declarações fixas
	Bug   string  // o bug injetado ("" num programa válido)
	Lines int     // comandos gerados
}

// Node é um comando de uma linha, ou um bloco (if, for, match) com filhos.
type Node struct {
	Line    string  // o comando, ou o cabeçalho do bloco ("if one == 1 {")
	Body    []*Node // os comandos do bloco
	Else    []*Node // o else de um if
	HasElse bool
	Close   bool // bloco: fecha com "}"
	Bug     bool // o comando com o bug injetado
}

// header são as declarações fixas de todo programa gerado.
const header = `import "std/io"

struct Box {
    v int
}

struct Holder {
    b ?*Box
    s []byte
}

enum Bag {
    Empty
    Full(s []byte)
}

fn peek(b &Box) int {
    return b.v
}

fn size(s &[]byte) int {
    return len(s)
}

fn eat_box(b *Box) int {
    n := b.v
    free(b)
    return n
}

fn eat_bytes(s []byte) int {
    n := len(s)
    free(s)
    return n
}

fn make_box(v int) *Box {
    return alloc(Box{v: v})
}

fn pass_box(b *Box) *Box {
    b.v = b.v + 1
    return b
}

fn bag_peek(e &Bag) int {
    match *e {
        Empty => return 0
        Full(t) => return int(t[0])
    }
}

fn main() {
    acc := 0
    one := 1
`

// Source escreve o programa em MLC.
func (p *Program) Source() []byte {
	var b strings.Builder
	what := "válido"
	if p.Bug != "" {
		what = "com bug injetado: " + p.Bug
	}
	fmt.Fprintf(&b, "// gerado pelo make fuzz (semente %d), %s\n", p.Seed, what)
	b.WriteString(header)
	writeNodes(&b, p.Body, 1)
	b.WriteString("    io.print(acc, one)\n}\n")
	return []byte(b.String())
}

func writeNodes(b *strings.Builder, ns []*Node, depth int) {
	ind := strings.Repeat("    ", depth)
	for _, n := range ns {
		b.WriteString(ind + n.Line + "\n")
		if !n.Close {
			continue
		}
		writeNodes(b, n.Body, depth+1)
		if n.HasElse {
			b.WriteString(ind + "} else {\n")
			writeNodes(b, n.Else, depth+1)
		}
		b.WriteString(ind + "}\n")
	}
}

// ---------- o estado das variáveis ----------

type kind int

const (
	kBox    kind = iota // *Box
	kBytes              // []byte
	kOpt                // ?*Box
	kHolder             // *Holder (campos b ?*Box e s []byte)
	kBag                // Bag (enum com um []byte)
	kRef                // &Box
)

type state int

const (
	live  state = iota
	dead        // liberada ou movida
	null        // ?*Box nil, ou o campo b de um Holder
	empty       // o campo s de um Holder vindo de Holder{}: slice vazio
)

type gvar struct {
	name   string
	kind   kind
	st     state
	b, s   state // os campos de um Holder
	full   bool  // Bag.Full (bag_peek lê a memória do slice)
	src    *gvar // kRef: a variável emprestada
	srcVer int   // kRef: a versão de src quando o empréstimo foi feito
	ver    int   // muda a cada move, free ou atribuição
}

// env é o estado durante a geração: as variáveis visíveis, por escopo.
type env struct {
	scopes [][]*gvar
}

func (e *env) clone() *env {
	m := map[*gvar]*gvar{}
	c := &env{}
	for _, sc := range e.scopes {
		var ns []*gvar
		for _, v := range sc {
			nv := *v
			m[v] = &nv
			ns = append(ns, &nv)
		}
		c.scopes = append(c.scopes, ns)
	}
	for _, sc := range c.scopes {
		for _, v := range sc {
			if v.src != nil {
				v.src = m[v.src]
			}
		}
	}
	return c
}

func (e *env) all() []*gvar {
	var out []*gvar
	for _, sc := range e.scopes {
		out = append(out, sc...)
	}
	return out
}

// pairs devolve, para cada variável de e, a correspondente em o (um clone).
func (e *env) pairs(o *env) [][2]*gvar {
	var out [][2]*gvar
	for i, sc := range e.scopes {
		for j, v := range sc {
			out = append(out, [2]*gvar{v, o.scopes[i][j]})
		}
	}
	return out
}

// ---------- o gerador ----------

type gen struct {
	r      *rand.Rand
	n      int  // próximo número de variável
	budget int  // comandos que ainda podem ser gerados
	bug    bool // ainda falta injetar o bug
	p      *Program
}

// Generate gera um programa. Com withBug, um único bug é injetado (se
// houver uma chance de injetá-lo em código que roda).
func Generate(seed int64, withBug bool) *Program {
	g := &gen{r: rand.New(rand.NewSource(seed)), budget: 8 + int(seed%5)*6, bug: withBug, p: &Program{Seed: seed}}
	e := &env{scopes: [][]*gvar{nil}}
	body := g.block(e, 0, true)
	if g.bug {
		// não houve chance no caminho: um vazamento no fim
		body = append(body, g.inject(e, true)...)
	}
	body = append(body, g.closeScope(e, true)...)
	g.p.Body = body
	return g.p
}

func (g *gen) name(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

func (g *gen) k() int { return 1 + g.r.Intn(9) }

func line(s string) *Node { return &Node{Line: s} }

// block gera comandos até acabar o orçamento ou o sorteio parar. exec diz
// se o bloco roda.
func (g *gen) block(e *env, depth int, exec bool) []*Node {
	var out []*Node
	for g.budget > 0 {
		g.budget--
		if exec && g.bug && g.r.Intn(12) == 0 {
			out = append(out, g.inject(e, false)...)
			continue
		}
		switch c := g.r.Intn(20); {
		case c < 2 && depth < 2:
			out = append(out, g.ifStmt(e, depth, exec)...)
		case c < 3 && depth < 2:
			out = append(out, g.forStmt(e, depth, exec)...)
		case c < 7:
			out = append(out, g.declare(e)...)
		default:
			out = append(out, g.op(e)...)
		}
		if depth > 0 && g.r.Intn(4) == 0 {
			break
		}
	}
	return out
}

// declare cria uma variável no escopo atual.
func (g *gen) declare(e *env) []*Node {
	top := len(e.scopes) - 1
	add := func(v *gvar) { e.scopes[top] = append(e.scopes[top], v) }
	switch g.r.Intn(9) {
	case 0, 1:
		v := &gvar{name: g.name("b"), kind: kBox}
		add(v)
		if g.r.Intn(2) == 0 {
			return []*Node{line(fmt.Sprintf("%s := alloc(Box{v: %d})", v.name, g.k()))}
		}
		return []*Node{line(fmt.Sprintf("%s := make_box(%d)", v.name, g.k()))}
	case 2, 3:
		v := &gvar{name: g.name("s"), kind: kBytes}
		add(v)
		return []*Node{line(fmt.Sprintf("%s := alloc([]byte, %d)", v.name, g.k()))}
	case 4:
		v := &gvar{name: g.name("q"), kind: kOpt, st: null}
		add(v)
		return []*Node{line("var " + v.name + " ?*Box")}
	case 5:
		v := &gvar{name: g.name("h"), kind: kHolder, b: null, s: empty}
		if x, s := g.pick(e, kBox, live), g.pick(e, kBytes, live); x != nil && s != nil && g.r.Intn(2) == 0 {
			g.kill(x)
			g.kill(s)
			v.b, v.s = live, live
			add(v)
			return []*Node{line(fmt.Sprintf("%s := alloc(Holder{b: %s, s: %s})", v.name, x.name, s.name))}
		}
		add(v)
		return []*Node{line(v.name + " := alloc(Holder{})")}
	case 6:
		v := &gvar{name: g.name("e"), kind: kBag}
		if s := g.pick(e, kBytes, live); s != nil && g.r.Intn(3) > 0 {
			g.kill(s)
			v.full = true
			add(v)
			return []*Node{line(fmt.Sprintf("%s := Bag.Full(%s)", v.name, s.name))}
		}
		add(v)
		return []*Node{line(v.name + " := Bag.Empty")}
	case 7:
		x := g.pick(e, kBox, live)
		if x == nil {
			return nil
		}
		x.ver++ // um empréstimo novo encerra os anteriores (não há dois vivos)
		v := &gvar{name: g.name("r"), kind: kRef, src: x, srcVer: x.ver}
		add(v)
		return []*Node{line(v.name + " := &" + x.name), line("acc += " + v.name + ".v")}
	default:
		// move para uma variável nova
		x := g.pickAny(e, live, kBox, kBytes, kBag)
		if x == nil {
			return nil
		}
		v := &gvar{name: g.name(string(x.name[0])), kind: x.kind, full: x.full}
		g.kill(x)
		add(v)
		return []*Node{line(v.name + " := " + x.name)}
	}
}

// op é uma operação sobre uma variável que já existe, válida no estado dela.
func (g *gen) op(e *env) []*Node {
	vs := e.all()
	if len(vs) == 0 {
		return g.declare(e)
	}
	v := vs[g.r.Intn(len(vs))]
	n := v.name
	if v.kind != kRef {
		// enquanto um empréstimo de v ainda vai ser usado, v fica congelado
		// (até para leitura): usar v encerra os empréstimos dele
		v.ver++
	}
	switch v.kind {
	case kBox:
		if v.st == dead {
			g.touch(v, live)
			if g.r.Intn(2) == 0 {
				return []*Node{line(fmt.Sprintf("%s = make_box(%d)", n, g.k()))}
			}
			return []*Node{line(fmt.Sprintf("%s = alloc(Box{v: %d})", n, g.k()))}
		}
		switch g.r.Intn(8) {
		case 0:
			return []*Node{line("acc += peek(&" + n + ")")}
		case 1:
			return []*Node{line("acc += " + n + ".v")}
		case 2:
			g.touch(v, live)
			return []*Node{line(n + ".v = acc % 100")}
		case 3:
			g.touch(v, live)
			return []*Node{line(n + " = pass_box(" + n + ")")}
		case 4:
			g.kill(v)
			return []*Node{line("acc += eat_box(" + n + ")")}
		case 5:
			if q := g.pickOpt(e); q != nil {
				g.kill(v)
				g.touch(q, live)
				return []*Node{line(q.name + " = " + n)}
			}
			return []*Node{line("acc += " + n + ".v")}
		case 6:
			if h := g.pickHolderField(e, "b"); h != nil {
				g.kill(v)
				h.b = live
				h.ver++
				return []*Node{line(h.name + ".b = " + n)}
			}
			return []*Node{line("acc += peek(&" + n + ")")}
		default:
			g.kill(v)
			return []*Node{line("free(" + n + ")")}
		}
	case kBytes:
		if v.st == dead {
			g.touch(v, live)
			return []*Node{line(fmt.Sprintf("%s = alloc([]byte, %d)", n, g.k()))}
		}
		switch g.r.Intn(7) {
		case 0:
			return []*Node{line("acc += size(&" + n + ")")}
		case 1:
			return []*Node{line("acc += int(" + n + "[0])")}
		case 2:
			g.touch(v, live)
			return []*Node{line(n + "[0] = byte(acc % 256)")}
		case 3:
			g.touch(v, live)
			return []*Node{line(fmt.Sprintf("%s = realloc(%s, %d)", n, n, g.k()))}
		case 4:
			g.kill(v)
			return []*Node{line("acc += eat_bytes(" + n + ")")}
		default:
			g.kill(v)
			return []*Node{line("free(" + n + ")")}
		}
	case kOpt:
		switch {
		case v.st == dead:
			if g.r.Intn(2) == 0 {
				g.touch(v, null)
				return []*Node{line(n + " = nil")}
			}
			g.touch(v, live)
			return []*Node{line(fmt.Sprintf("%s = make_box(%d)", n, g.k()))}
		case g.r.Intn(3) == 0:
			g.kill(v)
			return []*Node{line("free(" + n + ")")}
		case v.st == null && g.r.Intn(2) == 0:
			g.touch(v, live)
			return []*Node{line(fmt.Sprintf("%s = make_box(%d)", n, g.k()))}
		}
		return []*Node{{Line: "if " + n + " != nil {", Close: true, Body: []*Node{line("acc += " + n + ".v")}}}
	case kHolder:
		if v.st == dead {
			return nil // um Holder liberado não volta (os campos teriam de ser refeitos)
		}
		switch g.r.Intn(7) {
		case 0:
			if v.b != dead {
				return []*Node{{Line: "if " + n + ".b != nil {", Close: true, Body: []*Node{line("acc += " + n + ".b.v")}}}
			}
		case 1:
			if v.s == dead {
				v.s = live
				v.ver++
				return []*Node{line(fmt.Sprintf("%s.s = alloc([]byte, %d)", n, g.k()))}
			}
			return []*Node{line("acc += size(&" + n + ".s)")}
		case 2:
			if v.s == live || v.s == empty {
				v.s = dead
				v.ver++
				return []*Node{line("free(" + n + ".s)")}
			}
		case 3:
			if v.b == live || v.b == null {
				v.b = dead
				v.ver++
				return []*Node{line("free(" + n + ".b)")}
			}
		case 4:
			if v.b != dead {
				q := &gvar{name: g.name("q"), kind: kOpt, st: v.b}
				v.b = dead
				v.ver++
				e.scopes[len(e.scopes)-1] = append(e.scopes[len(e.scopes)-1], q)
				return []*Node{line(q.name + " := " + n + ".b")}
			}
		case 5:
			if v.b == dead {
				v.b = null
				v.ver++
				return []*Node{line(n + ".b = nil")}
			}
		default:
			if v.b != live && v.s != live {
				g.kill(v)
				return []*Node{line("free(" + n + ")")}
			}
		}
		if v.s != dead {
			return []*Node{line("acc += size(&" + n + ".s)")}
		}
		return nil
	case kBag:
		if v.st == dead {
			g.touch(v, live)
			v.full = false
			return []*Node{line(n + " = Bag.Empty")}
		}
		switch g.r.Intn(4) {
		case 0, 1:
			return []*Node{line("acc += bag_peek(&" + n + ")")}
		case 2:
			g.kill(v)
			return []*Node{line("free(" + n + ")")}
		default:
			g.kill(v)
			t := g.name("t")
			return []*Node{{Line: "match " + n + " {", Close: true, Body: []*Node{
				line("Empty => {}"),
				{Line: "Full(" + t + ") => {", Close: true, Body: []*Node{line("acc += int(" + t + "[0])"), line("free(" + t + ")")}},
			}}}
		}
	case kRef:
		if v.src.st == live && v.src.ver == v.srcVer {
			return []*Node{line("acc += " + n + ".v")}
		}
	}
	return nil
}

// ifStmt gera um if com a condição conhecida. As variáveis de fora terminam
// os dois lados no mesmo estado: o lado em que uma ficou viva a libera.
func (g *gen) ifStmt(e *env, depth int, exec bool) []*Node {
	taken := g.r.Intn(2) == 0
	cond := "one == 1"
	if !taken {
		cond = "one == 2"
	}
	n := &Node{Line: "if " + cond + " {", Close: true}
	te := e.clone()
	te.scopes = append(te.scopes, nil)
	n.Body = g.block(te, depth+1, exec && taken)
	n.Body = append(n.Body, g.closeScope(te, exec && taken)...)
	te.scopes = te.scopes[:len(te.scopes)-1]
	if g.r.Intn(2) == 0 {
		n.HasElse = true
		ee := e.clone()
		ee.scopes = append(ee.scopes, nil)
		n.Else = g.block(ee, depth+1, exec && !taken)
		n.Else = append(n.Else, g.closeScope(ee, exec && !taken)...)
		ee.scopes = ee.scopes[:len(ee.scopes)-1]
		g.reconcile(te, ee, &n.Body, &n.Else)
		g.adopt(e, te)
	} else {
		var none []*Node
		g.reconcile(te, e.clone(), &n.Body, &none)
		if len(none) > 0 {
			n.HasElse, n.Else = true, none
		}
		g.adopt(e, te)
	}
	return []*Node{n}
}

// forStmt gera um laço que roda de 2 a 4 vezes, às vezes com break na
// última volta. As variáveis de fora terminam cada volta no estado em que
// entraram.
func (g *gen) forStmt(e *env, depth int, exec bool) []*Node {
	i := g.name("i")
	k := 2 + g.r.Intn(3)
	n := &Node{Line: fmt.Sprintf("for %s := 0; %s < %d; %s++ {", i, i, k, i), Close: true}
	if g.r.Intn(3) == 0 {
		n.Body = append(n.Body, &Node{Line: fmt.Sprintf("if %s == %d {", i, k-1), Close: true, Body: []*Node{line("break")}})
	}
	// um Holder com o campo s vazio (de Holder{}) não volta a ficar vazio
	// depois de uma volta que o reconstrói: antes do laço, s ganha memória
	var pre []*Node
	for _, v := range e.all() {
		if v.kind == kHolder && v.st == live && v.s == empty {
			pre = append(pre, line("free("+v.name+".s)"), line(fmt.Sprintf("%s.s = alloc([]byte, %d)", v.name, g.k())))
			v.s = live
			v.ver++
		}
	}
	be := e.clone()
	be.scopes = append(be.scopes, nil)
	// os empréstimos de fora não são usados dentro do laço: a origem pode
	// mudar numa volta e o empréstimo seria usado na seguinte
	for _, v := range be.all() {
		if v.kind == kRef {
			v.srcVer = -1
		}
	}
	n.Body = append(n.Body, g.block(be, depth+1, exec)...)
	n.Body = append(n.Body, g.closeScope(be, exec)...)
	be.scopes = be.scopes[:len(be.scopes)-1]
	start := e.clone()
	var none []*Node
	g.reconcileTo(be, start, &n.Body, &none)
	// um empréstimo de fora cuja origem mudou no laço não vale mais depois
	for _, pr := range e.pairs(be) {
		if pr[0].ver != pr[1].ver {
			pr[0].ver = pr[1].ver + 1000
		}
	}
	return append(pre, n)
}

// reconcile iguala o estado das variáveis de fora no fim dos dois lados de
// um if: uma que terminou diferente é liberada (ou fica nil) nos dois.
func (g *gen) reconcile(a, b *env, an, bn *[]*Node) {
	for _, pr := range a.pairs(b) {
		x, y := pr[0], pr[1]
		if !same(x, y) {
			*an, *bn = append(*an, g.settle(x)...), append(*bn, g.settle(y)...)
		}
		if x.ver != y.ver {
			// mudou em algum lado: um empréstimo dela feito antes do if não vale
			v := max(x.ver, y.ver) + 1
			x.ver, y.ver = v, v
		}
	}
}

func same(x, y *gvar) bool {
	switch x.kind {
	case kRef:
		return true
	case kHolder:
		return x.st == y.st && x.b == y.b && x.s == y.s
	case kBag:
		return x.st == y.st && x.full == y.full
	}
	return x.st == y.st
}

// reconcileTo leva as variáveis de a (o fim de uma volta do laço) ao estado
// de to (o começo), acrescentando os comandos a an.
func (g *gen) reconcileTo(a, to *env, an, _ *[]*Node) {
	for _, pr := range to.pairs(a) {
		want, x := pr[0], pr[1]
		*an = append(*an, g.restore(x, want)...)
	}
}

// settle leva uma variável de fora a um estado que não depende do caminho:
// liberada (ou nil, para ?*Box), com os campos de um Holder liberados.
func (g *gen) settle(v *gvar) []*Node {
	var out []*Node
	switch v.kind {
	case kOpt:
		if v.st == live {
			out = append(out, line("free("+v.name+")"))
		}
		if v.st != null {
			out = append(out, line(v.name+" = nil"))
			g.touch(v, null)
		}
	case kHolder:
		if v.st == dead {
			return nil
		}
		if v.b == live || v.b == null {
			out = append(out, line("free("+v.name+".b)"))
		}
		if v.s == live || v.s == empty {
			out = append(out, line("free("+v.name+".s)"))
		}
		out = append(out, line("free("+v.name+")"))
		v.b, v.s = dead, dead
		g.kill(v)
	case kRef:
		v.srcVer = -1
	default:
		if v.st == live {
			out = append(out, line("free("+v.name+")"))
			g.kill(v)
		}
	}
	return out
}

// restore leva x ao estado de want (o mesmo do começo da volta).
func (g *gen) restore(x, want *gvar) []*Node {
	var out []*Node
	switch x.kind {
	case kRef:
		return nil
	case kHolder:
		if want.st == dead {
			return nil // já entrou liberado: nada dentro o reviveu
		}
		if x.st == dead {
			// liberado dentro da volta: refeito vazio
			out = append(out, line(x.name+" = alloc(Holder{})"))
			x.st, x.b, x.s = live, null, empty
		}
		if x.b == live {
			out = append(out, line("free("+x.name+".b)"))
		}
		if x.b != null {
			out = append(out, line(x.name+".b = nil"))
		}
		if want.b == live {
			out = append(out, line(fmt.Sprintf("%s.b = make_box(%d)", x.name, g.k())))
		}
		// want.s nunca é empty (forStmt normaliza antes do laço)
		if x.s != want.s {
			if x.s == live || x.s == empty {
				out = append(out, line("free("+x.name+".s)"))
			}
			if want.s == live {
				out = append(out, line(fmt.Sprintf("%s.s = alloc([]byte, %d)", x.name, g.k())))
			}
		}
		x.b, x.s = want.b, want.s
		return out
	}
	if x.st == live && want.st != live {
		out = append(out, line("free("+x.name+")"))
	}
	if x.kind == kOpt && want.st == null && x.st != null {
		out = append(out, line(x.name+" = nil"))
	}
	if want.st == live && x.st != live {
		switch x.kind {
		case kBox, kOpt:
			out = append(out, line(fmt.Sprintf("%s = make_box(%d)", x.name, g.k())))
		case kBytes:
			out = append(out, line(fmt.Sprintf("%s = alloc([]byte, %d)", x.name, g.k())))
		case kBag:
			if want.full {
				out = append(out, line(fmt.Sprintf("%s = Bag.Full(alloc([]byte, %d))", x.name, g.k())))
			} else {
				out = append(out, line(x.name+" = Bag.Empty"))
			}
		}
	}
	x.st = want.st
	return out
}

// adopt copia para e o estado das variáveis de o (um clone de e).
func (g *gen) adopt(e, o *env) {
	for _, pr := range e.pairs(o) {
		src := pr[0].src
		*pr[0] = *pr[1]
		pr[0].src = src
	}
}

// closeScope libera as variáveis vivas do escopo de dentro, no fim do
// bloco (um ?*Box nil também passa pelo free, que não faz nada: assim toda
// variável declarada é usada).
func (g *gen) closeScope(e *env, exec bool) []*Node {
	top := len(e.scopes) - 1
	var out []*Node
	for _, v := range e.scopes[top] {
		if v.kind == kOpt && v.st == null {
			out = append(out, line("free("+v.name+")"))
			g.kill(v)
			continue
		}
		out = append(out, g.settle(v)...)
	}
	return out
}

// ---------- bugs ----------

// inject escreve um bug que a execução vê, no ponto atual (que roda).
func (g *gen) inject(e *env, atEnd bool) []*Node {
	bug := func(s string) *Node { return &Node{Line: s, Bug: true} }
	kinds := []string{"vazamento"}
	if g.pick(e, kBox, live) != nil {
		kinds = append(kinds, "uso depois de free", "free duplo", "empréstimo depois de free", "uso depois de mover e liberar")
	}
	if g.pick(e, kBytes, live) != nil {
		kinds = append(kinds, "byte lido depois de free")
	}
	for _, v := range e.all() {
		if v.kind == kBag && v.st == live && v.full {
			kinds = append(kinds, "enum lido depois de free")
			break
		}
	}
	for _, v := range e.all() {
		if v.kind == kHolder && v.st == live && (v.b == live || v.s == live) {
			kinds = append(kinds, "struct liberada com campo vivo")
			break
		}
	}
	if atEnd {
		kinds = kinds[:1]
	}
	k := kinds[g.r.Intn(len(kinds))]
	g.bug = false
	g.p.Bug = k
	switch k {
	case "uso depois de free":
		x := g.pick(e, kBox, live)
		g.kill(x)
		return []*Node{line("free(" + x.name + ")"), bug("acc += " + x.name + ".v")}
	case "free duplo":
		x := g.pick(e, kBox, live)
		g.kill(x)
		return []*Node{line("free(" + x.name + ")"), bug("free(" + x.name + ")")}
	case "empréstimo depois de free":
		x := g.pick(e, kBox, live)
		r := g.name("r")
		g.kill(x)
		return []*Node{line(r + " := &" + x.name), line("acc += " + r + ".v"), line("free(" + x.name + ")"), bug("acc += " + r + ".v")}
	case "uso depois de mover e liberar":
		x := g.pick(e, kBox, live)
		y := g.name("b")
		g.kill(x)
		return []*Node{line(y + " := " + x.name), line("free(" + y + ")"), bug("acc += " + x.name + ".v")}
	case "byte lido depois de free":
		s := g.pick(e, kBytes, live)
		g.kill(s)
		return []*Node{line("free(" + s.name + ")"), bug("acc += int(" + s.name + "[0])")}
	case "enum lido depois de free":
		for _, v := range e.all() {
			if v.kind == kBag && v.st == live && v.full {
				g.kill(v)
				return []*Node{line("free(" + v.name + ")"), bug("acc += bag_peek(&" + v.name + ")")}
			}
		}
	case "struct liberada com campo vivo":
		for _, v := range e.all() {
			if v.kind == kHolder && v.st == live && (v.b == live || v.s == live) {
				var out []*Node
				// o campo que não vaza é liberado antes, para o vazamento ser um só
				if v.b == live {
					if v.s == live || v.s == empty {
						out = append(out, line("free("+v.name+".s)"))
					}
				} else if v.b == null {
					out = append(out, line("free("+v.name+".b)"))
				}
				g.kill(v)
				return append(out, bug("free("+v.name+")"))
			}
		}
	}
	// vazamento: uma alocação que nunca é liberada
	v := g.name("vaza")
	return []*Node{bug(fmt.Sprintf("%s := alloc(Box{v: %d})", v, g.k())), line("acc += " + v + ".v")}
}

// ---------- ajudantes ----------

func (g *gen) pick(e *env, k kind, st state) *gvar {
	var c []*gvar
	for _, v := range e.all() {
		if v.kind == k && v.st == st {
			c = append(c, v)
		}
	}
	if len(c) == 0 {
		return nil
	}
	return c[g.r.Intn(len(c))]
}

func (g *gen) pickAny(e *env, st state, ks ...kind) *gvar {
	var c []*gvar
	for _, v := range e.all() {
		for _, k := range ks {
			if v.kind == k && v.st == st {
				c = append(c, v)
			}
		}
	}
	if len(c) == 0 {
		return nil
	}
	return c[g.r.Intn(len(c))]
}

// pickOpt acha um ?*Box que pode receber um valor (nil ou liberado).
func (g *gen) pickOpt(e *env) *gvar {
	var c []*gvar
	for _, v := range e.all() {
		if v.kind == kOpt && v.st != live {
			c = append(c, v)
		}
	}
	if len(c) == 0 {
		return nil
	}
	return c[g.r.Intn(len(c))]
}

// pickHolderField acha um Holder vivo cujo campo b pode receber um valor.
func (g *gen) pickHolderField(e *env, _ string) *gvar {
	for _, v := range e.all() {
		if v.kind == kHolder && v.st == live && v.b != live {
			return v
		}
	}
	return nil
}

func (g *gen) kill(v *gvar) { g.touch(v, dead) }

func (g *gen) touch(v *gvar, st state) {
	v.st = st
	v.ver++
}
