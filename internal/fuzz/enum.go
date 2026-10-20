package fuzz

import (
	"strings"
)

// Phase 4: exhaustive enumeration with a reference model.
//
// Enumerate generates every program up to a size, from a small alphabet
// of statements over three owning variables (the parameters x and y, *Box,
// and q, ?*Box), a borrow r, if and a loop with conditions the checker
// doesn't know (flip()). Each program is judged by the checker and by the
// reference model (model.go), and the two verdicts are compared.

// Stmt is a statement of the alphabet: an atom, an if or a loop.
type Stmt struct {
	Atom    string // the statement (one of the Atoms), or "" in a block
	If      bool   // if flip() { Body } [else { Else }]
	Loop    bool   // for flip() { Body }
	Body    []*Stmt
	Else    []*Stmt
	HasElse bool
	// forms used only by the metamorphic tests (metamorphic.go)
	Block   bool // { Body }: a loose block
	Negated bool // if !flip(): the same unknown choice
}

// Atoms are the simple statements. The semantics of each one is in model.go.
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

// size is the size of a list: each statement counts 1, plus what it has
// inside.
func size(ss []*Stmt) int {
	n := 0
	for _, s := range ss {
		n += 1 + size(s.Body) + size(s.Else)
	}
	return n
}

// Enumerate calls visit with each function body of size exactly n.
// Programs that only differ by swapping x with y are visited once, and the ones
// that wouldn't even pass the types because of r (used without being declared, or
// declared twice in the same scope) aren't generated.
func Enumerate(n int, visit func([]*Stmt)) {
	lists(n, false, rNone, false, func(ss []*Stmt) {
		if canonical(ss) {
			visit(ss)
		}
	})
}

// The visibility of r at a point of the program.
const (
	rNone  = iota // not declared
	rOuter        // declared in an outer scope (can be declared again here)
	rHere         // declared in this scope
)

// lists generates every statement list of size n; inLoop allows
// break; pending says r was declared in this list and hasn't been used yet
// (the list must use it before it ends).
func lists(n int, inLoop bool, rv int, pending bool, visit func([]*Stmt)) {
	if n == 0 {
		if !pending {
			visit(nil)
		}
		return
	}
	// the first statement has size k, the rest n-k
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

// usesR says whether the list uses the outer r (before declaring another).
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

// stmts generates every statement of size n.
func stmts(n int, inLoop bool, rv int, visit func(*Stmt)) {
	inner := rv
	if inner == rHere {
		inner = rOuter // inside a block, the outer r can be declared again
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
		// if without else, and a loop: the body has n-1
		lists(n-1, inLoop, inner, false, func(body []*Stmt) {
			visit(&Stmt{If: true, Body: body})
		})
		lists(n-1, true, inner, false, func(body []*Stmt) {
			visit(&Stmt{Loop: true, Body: body})
		})
	}
	if n >= 3 {
		// if with else: both sides add up to n-1, neither empty
		for a := 1; a <= n-2; a++ {
			lists(a, inLoop, inner, false, func(then []*Stmt) {
				lists(n-1-a, inLoop, inner, false, func(els []*Stmt) {
					visit(&Stmt{If: true, Body: then, Else: els, HasElse: true})
				})
			})
		}
	}
}

// canonical says whether the program is the representative of the x-y swap (the
// one that mentions x before y, or the one the swap doesn't change).
func canonical(ss []*Stmt) bool {
	src := render(ss, 0)
	swapped := strings.NewReplacer("x", "\x00", "y", "x").Replace(src)
	swapped = strings.ReplaceAll(swapped, "\x00", "y")
	return src <= swapped
}

// EnumSource writes the complete program.
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
