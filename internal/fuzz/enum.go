package fuzz

import (
	"strings"
)

// Fase 4: enumeração exaustiva com um modelo de referência.
//
// Enumerate gera todos os programas até um tamanho, a partir de um alfabeto
// pequeno de comandos sobre três variáveis donas (os parâmetros x e y, *Box,
// e q, ?*Box), um empréstimo r, if e laço com condições que o verificador
// não conhece (flip()). Cada programa é julgado pelo verificador e pelo
// modelo de referência (model.go), e os dois vereditos são comparados.

// Stmt é um comando do alfabeto: um átomo, um if ou um laço.
type Stmt struct {
	Atom    string // o comando (um dos Atoms), ou "" num bloco
	If      bool   // if flip() { Body } [else { Else }]
	Loop    bool   // for flip() { Body }
	Body    []*Stmt
	Else    []*Stmt
	HasElse bool
	// formas usadas só pelos testes metamórficos (metamorphic.go)
	Block   bool // { Body }: um bloco solto
	Negated bool // if !flip(): a mesma escolha desconhecida
}

// Atoms são os comandos simples. A semântica de cada um está em model.go.
var Atoms = []string{
	"free(x)",
	"free(y)",
	"free(q)",
	"x = alloc(Box{v: 1})",
	"q = alloc(Box{v: 1})",
	"acc += x.v",
	"acc += y.v",
	"use q", // if q != nil { acc += q.v }
	"y = x",
	"x = y",
	"q = x",
	"r := &x",
	"r := &y",
	"acc += r.v",
	"break",
}

// size é o tamanho de uma lista: cada comando conta 1, mais o que tem
// dentro.
func size(ss []*Stmt) int {
	n := 0
	for _, s := range ss {
		n += 1 + size(s.Body) + size(s.Else)
	}
	return n
}

// Enumerate chama visit com cada corpo de função de tamanho exatamente n.
// Programas que só diferem pela troca de x com y são visitados uma vez, e os
// que nem passariam nos tipos por causa de r (usada sem estar declarada, ou
// declarada duas vezes no mesmo escopo) não são gerados.
func Enumerate(n int, visit func([]*Stmt)) {
	lists(n, false, rNone, false, func(ss []*Stmt) {
		if canonical(ss) {
			visit(ss)
		}
	})
}

// A visibilidade de r num ponto do programa.
const (
	rNone  = iota // não declarada
	rOuter        // declarada num escopo de fora (pode ser declarada de novo aqui)
	rHere         // declarada neste escopo
)

// lists gera todas as listas de comandos de tamanho n; inLoop permite
// break; pending diz que r foi declarada nesta lista e ainda não foi usada
// (a lista precisa usá-la antes de acabar).
func lists(n int, inLoop bool, rv int, pending bool, visit func([]*Stmt)) {
	if n == 0 {
		if !pending {
			visit(nil)
		}
		return
	}
	// o primeiro comando tem tamanho k, o resto n-k
	for k := 1; k <= n; k++ {
		stmts(k, inLoop, rv, func(first *Stmt) {
			next, still := rv, pending
			switch {
			case strings.HasPrefix(first.Atom, "r := "):
				next, still = rHere, true
			case still && usesR([]*Stmt{first}):
				still = false
			}
			lists(n-k, inLoop, next, still, func(rest []*Stmt) {
				visit(append([]*Stmt{first}, rest...))
			})
		})
	}
}

// usesR diz se a lista usa o r de fora (antes de declarar outro).
func usesR(ss []*Stmt) bool {
	for _, s := range ss {
		switch {
		case strings.HasPrefix(s.Atom, "r := "):
			return false
		case s.Atom == "acc += r.v":
			return true
		case usesR(s.Body) || usesR(s.Else):
			return true
		}
	}
	return false
}

// stmts gera todos os comandos de tamanho n.
func stmts(n int, inLoop bool, rv int, visit func(*Stmt)) {
	inner := rv
	if inner == rHere {
		inner = rOuter // dentro de um bloco, r de fora pode ser declarada de novo
	}
	if n == 1 {
		for _, a := range Atoms {
			switch {
			case a == "break" && !inLoop,
				a == "acc += r.v" && rv == rNone,
				strings.HasPrefix(a, "r := ") && rv == rHere:
				continue
			}
			visit(&Stmt{Atom: a})
		}
	}
	if n >= 2 {
		// if sem else, e laço: o corpo tem n-1
		lists(n-1, inLoop, inner, false, func(body []*Stmt) {
			visit(&Stmt{If: true, Body: body})
		})
		lists(n-1, true, inner, false, func(body []*Stmt) {
			visit(&Stmt{Loop: true, Body: body})
		})
	}
	if n >= 3 {
		// if com else: os dois lados somam n-1, nenhum vazio
		for a := 1; a <= n-2; a++ {
			lists(a, inLoop, inner, false, func(then []*Stmt) {
				lists(n-1-a, inLoop, inner, false, func(els []*Stmt) {
					visit(&Stmt{If: true, Body: then, Else: els, HasElse: true})
				})
			})
		}
	}
}

// canonical diz se o programa é o representante da troca de x com y (o
// que menciona x antes de y, ou o que não muda com a troca).
func canonical(ss []*Stmt) bool {
	src := render(ss, 0)
	swapped := strings.NewReplacer("x", "\x00", "y", "x").Replace(src)
	swapped = strings.ReplaceAll(swapped, "\x00", "y")
	return src <= swapped
}

// EnumSource escreve o programa completo.
func EnumSource(ss []*Stmt) []byte {
	var b strings.Builder
	b.WriteString(enumHeader)
	b.WriteString(render(ss, 1))
	b.WriteString(enumFooter)
	return []byte(b.String())
}

const enumHeader = `struct Box {
    v int
}

extern "C" fn rand() int32

fn flip() bool {
    return rand() % 2 == 0
}

fn sink(n int) {
}

fn f(x *Box, y *Box, q ?*Box) {
    acc := 0
`

const enumFooter = `    sink(acc)
}

fn main() {
    f(alloc(Box{v: 1}), alloc(Box{v: 2}), nil)
}
`

func render(ss []*Stmt, depth int) string {
	var b strings.Builder
	ind := strings.Repeat("    ", depth)
	for _, s := range ss {
		switch {
		case s.Atom == "use q":
			b.WriteString(ind + "if q != nil {\n" + ind + "    acc += q.v\n" + ind + "}\n")
		case s.Atom != "":
			b.WriteString(ind + s.Atom + "\n")
		case s.Block:
			b.WriteString(ind + "{\n" + render(s.Body, depth+1) + ind + "}\n")
		case s.If:
			cond := "flip()"
			if s.Negated {
				cond = "!flip()"
			}
			b.WriteString(ind + "if " + cond + " {\n" + render(s.Body, depth+1))
			if s.HasElse {
				b.WriteString(ind + "} else {\n" + render(s.Else, depth+1))
			}
			b.WriteString(ind + "}\n")
		case s.Loop:
			b.WriteString(ind + "for flip() {\n" + render(s.Body, depth+1) + ind + "}\n")
		}
	}
	return b.String()
}
