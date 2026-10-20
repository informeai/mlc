package checker

import (
	"fmt"
	"math/big"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Stack
//
// Arrays and structs with arrays live on the stack, and a thread's stack has the
// size asked for in thread.spawn (64 KB with thread.STACK_64KB). A buffer
// larger than the stack can't go unnoticed: in C, the function's frame skips
// the guard page and the writes land in something else's memory.
//
// So the checker estimates each function's stack: the frame (the local
// variables, the parameters and the large temporary values) plus the largest
// stack among the functions it calls directly. At compile time, it refuses a
// thread.spawn (or a main) whose function doesn't fit in the requested stack. Recursion and
// calls through fn values don't enter the count (there's no way to know the
// depth): for them there's the generator's check, which before
// entering a function with a large frame checks whether it fits in what's left of the
// thread's stack and, if it doesn't, stops with a panic instead of corrupting.

// BigFrame is the frame size from which the generator checks the stack
// before entering the function. Below it, the frame is smaller than the guard
// page, and an overflow always lands on it.
const BigFrame = 2048

// MainStack is the main thread's stack (the macOS and Linux default).
const MainStack = 8 << 20

// StackReserve is the part of the stack the check leaves free: what C
// uses besides MLC's variables, and the panic that reports the overflow. The runtime
// uses the same calculation (mlc_stack_init).
func StackReserve(stack int64) int64 {
	r := stack / 8
	if r < 4096 {
		r = 4096
	}
	if r > 32768 {
		r = 32768
	}
	return r
}

// stackUse is a function's estimated stack and what weighs most in it.
type stackUse struct {
	bytes   int64
	biggest *stackItem
}

// stackItem is the largest variable (or temporary value) on the deepest path.
type stackItem struct {
	name string // a variable, or "" for a temporary value
	typ  Type
	fn   *Func
	size int64
}

// spawnSite is a thread.spawn, kept to check the stack at the end.
type spawnSite struct {
	call *ast.CallExpr
	mod  *module
}

// checkStacks computes each function's frame (Func.Frame, used by the
// generator) and checks the thread.spawn calls and main.
func (c *checker) checkStacks() {
	for _, fn := range c.info.Funcs {
		if fn.Decl != nil && fn.Decl.Body != nil {
			fn.Frame, fn.frameTop = c.frame(fn)
		}
	}
	uses := map[*Func]*stackUse{}
	for _, s := range c.spawnSites {
		fn := c.info.FuncRefs[s.call.Args[0]]
		v, ok := c.info.Values[s.call.Args[2]].(*big.Int)
		if fn == nil || fn.Decl == nil || !ok || !v.IsInt64() {
			continue // fn value or computed size: the generator's check remains
		}
		if v.Int64() < 16384 {
			continue // below the minimum, thread.spawn panics
		}
		c.mod = s.mod
		c.fitStack(fn, v.Int64(), s.call.Args[2].Pos(), uses, true)
	}
	if m := c.info.Main; m != nil {
		c.mod = m.mod
		c.fitStack(m, MainStack, m.Decl.P, uses, false)
	}
	for _, t := range c.info.Tests {
		c.mod = t.mod
		c.fitStack(t, MainStack, t.Decl.P, uses, false)
	}
}

// fitStack refuses fn if the estimated stack doesn't fit in stack.
func (c *checker) fitStack(fn *Func, stack int64, pos token.Pos, uses map[*Func]*stackUse, thread bool) {
	u := c.stackOf(fn, uses, map[*Func]bool{})
	room := stack - StackReserve(stack)
	if u.bytes <= room {
		return
	}
	what := "the variable " + u.biggest.name
	if u.biggest.name == "" {
		what = "a temporary value"
	}
	where := fmt.Sprintf("%s %s (%s) in %s", what, u.biggest.typ, kb(u.biggest.size), u.biggest.fn.Name)
	fix := "put the buffer on the heap"
	if a, ok := u.biggest.typ.(*Array); ok {
		fix = fmt.Sprintf("put the buffer on the heap with alloc([]%s, %d)", a.Elem, a.Len)
	}
	if !thread {
		c.errorf(pos, "%s needs about %s of stack, and the main thread has %s (the heaviest is %s): %s",
			fn.Name, kb(u.bytes), kb(stack), where, fix)
		return
	}
	c.errorf(pos, "the thread of %s needs about %s of stack, and thread.spawn asks for %s (the heaviest is %s): %s, or ask for a larger stack%s",
		fn.Name, kb(u.bytes), kb(stack), where, fix, suggestStack(u.bytes))
}

// suggestStack suggests the smallest std/thread constant n fits in.
func suggestStack(n int64) string {
	for _, s := range []struct {
		name string
		size int64
	}{{"STACK_64KB", 64 << 10}, {"STACK_256KB", 256 << 10}, {"STACK_1MB", 1 << 20}, {"STACK_8MB", 8 << 20}} {
		if n <= s.size-StackReserve(s.size) {
			return " (thread." + s.name + ")"
		}
	}
	return ""
}

// stackOf is fn's frame plus the largest stack among the functions it
// calls directly. A recursive call doesn't add again (open marks the
// functions on the current path).
func (c *checker) stackOf(fn *Func, uses map[*Func]*stackUse, open map[*Func]bool) *stackUse {
	if u, ok := uses[fn]; ok {
		return u
	}
	open[fn] = true
	var deepest *stackUse
	for _, callee := range c.callees(fn) {
		if open[callee] {
			continue
		}
		if u := c.stackOf(callee, uses, open); deepest == nil || u.bytes > deepest.bytes {
			deepest = u
		}
	}
	delete(open, fn)
	u := &stackUse{bytes: fn.Frame, biggest: fn.frameTop}
	if deepest != nil {
		u.bytes += deepest.bytes
		if u.biggest == nil || deepest.biggest != nil && deepest.biggest.size > u.biggest.size {
			u.biggest = deepest.biggest
		}
	}
	if u.biggest == nil {
		u.biggest = &stackItem{name: "?", typ: Void, fn: fn}
	}
	uses[fn] = u
	return u
}

// callees are the MLC functions fn calls directly (without fn values).
func (c *checker) callees(fn *Func) []*Func {
	if fn.Decl == nil || fn.Decl.Body == nil {
		return nil
	}
	var out []*Func
	seen := map[*Func]bool{}
	ast.Inspect(fn.Decl.Body, func(n ast.Node) bool {
		if e, ok := n.(*ast.CallExpr); ok {
			k := c.info.Calls[e]
			if f := k.Func; f != nil && !k.Indirect && f.Decl != nil && f.Decl.Body != nil && !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
		return true
	})
	return out
}

// frame estimates fn's frame: parameters and local variables (all of them, as
// if they lived at the same time) and the large values C keeps in
// temporaries (call results and literals that don't go straight into
// a variable).
func (c *checker) frame(fn *Func) (int64, *stackItem) {
	var total int64
	var top *stackItem
	add := func(name string, t Type) {
		if t == nil || t == Invalid || t == Void {
			return
		}
		n := sizeOf(t)
		total += n
		if top == nil || n > top.size {
			top = &stackItem{name: name, typ: t, fn: fn, size: n}
		}
	}
	for _, p := range fn.Params {
		add(p.Name, p.Type)
	}
	direct := map[ast.Expr]bool{} // a declaration's value: it's the variable itself
	ast.Inspect(fn.Decl.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.VarDecl:
			if l := c.info.Decls[n]; l != nil {
				add(l.Name, l.Type)
			}
			if n.Value != nil {
				direct[n.Value] = true
			}
		case *ast.ForStmt:
			if l := c.info.ForVars[n]; l != nil {
				add(l.Name, l.Type)
			}
		case *ast.Pattern:
			for _, l := range c.info.PatternBinds[n] {
				if l != nil { // _ declares nothing
					add(l.Name, l.Type)
				}
			}
		case *ast.CallExpr, *ast.CompositeLit, *ast.ArrayLit:
			e := n.(ast.Expr)
			if t := c.info.Types[e]; !direct[e] && t != nil && sizeOf(t) >= 256 {
				add("", t)
			}
		}
		return true
	})
	return total, top
}

// kb writes a size in bytes in a readable way.
func kb(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f MB", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
