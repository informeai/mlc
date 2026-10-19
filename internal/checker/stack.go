package checker

import (
	"fmt"
	"math/big"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Pilha
//
// Arrays e structs com arrays vivem na pilha, e a pilha de uma thread tem o
// tamanho pedido no thread.spawn (64 KB com thread.STACK_64KB). Um buffer
// maior que a pilha não pode ficar sem aviso: no C, o quadro da função pula
// a página de guarda e as escritas caem na memória de outra coisa.
//
// Por isso o checker estima a pilha de cada função: o quadro (as variáveis
// locais, os parâmetros e os valores temporários grandes) mais a maior
// pilha entre as funções que ela chama direto. Na compilação, recusa um
// thread.spawn (ou um main) cuja função não cabe na pilha pedida. Recursão e
// chamadas por valor fn não entram na conta (não dá para saber a
// profundidade): para elas fica a conferência do gerador, que antes de
// entrar numa função de quadro grande confere se ele cabe no que resta da
// pilha da thread e, se não couber, para com panic em vez de corromper.

// BigFrame é o tamanho de quadro a partir do qual o gerador confere a pilha
// antes de entrar na função. Abaixo disso, o quadro é menor que a página de
// guarda, e um estouro sempre cai nela.
const BigFrame = 2048

// MainStack é a pilha da thread principal (o padrão do macOS e do Linux).
const MainStack = 8 << 20

// StackReserve é a parte da pilha que a conferência deixa livre: o que o C
// usa além das variáveis do MLC, e o panic que avisa do estouro. O runtime
// usa a mesma conta (mlc_stack_init).
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

// stackUse é a pilha estimada de uma função e o que mais pesa nela.
type stackUse struct {
	bytes   int64
	biggest *stackItem
}

// stackItem é a maior variável (ou valor temporário) no caminho mais fundo.
type stackItem struct {
	name string // variável, ou "" para um valor temporário
	typ  Type
	fn   *Func
	size int64
}

// spawnSite é um thread.spawn, guardado para conferir a pilha no fim.
type spawnSite struct {
	call *ast.CallExpr
	mod  *module
}

// checkStacks calcula o quadro de cada função (Func.Frame, usado pelo
// gerador) e confere os thread.spawn e o main.
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
			continue // valor fn ou tamanho calculado: fica a conferência do gerador
		}
		if v.Int64() < 16384 {
			continue // abaixo do mínimo, o thread.spawn dá panic
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

// fitStack recusa fn se a pilha estimada não couber em stack.
func (c *checker) fitStack(fn *Func, stack int64, pos token.Pos, uses map[*Func]*stackUse, thread bool) {
	u := c.stackOf(fn, uses, map[*Func]bool{})
	room := stack - StackReserve(stack)
	if u.bytes <= room {
		return
	}
	what := "a variável " + u.biggest.name
	if u.biggest.name == "" {
		what = "um valor temporário"
	}
	where := fmt.Sprintf("%s %s (%s) em %s", what, u.biggest.typ, kb(u.biggest.size), u.biggest.fn.Name)
	fix := "ponha o buffer no heap"
	if a, ok := u.biggest.typ.(*Array); ok {
		fix = fmt.Sprintf("ponha o buffer no heap com alloc([]%s, %d)", a.Elem, a.Len)
	}
	if !thread {
		c.errorf(pos, "%s precisa de cerca de %s de pilha, e a thread principal tem %s (o maior peso é %s): %s",
			fn.Name, kb(u.bytes), kb(stack), where, fix)
		return
	}
	c.errorf(pos, "a thread de %s precisa de cerca de %s de pilha, e thread.spawn pede %s (o maior peso é %s): %s, ou peça uma pilha maior%s",
		fn.Name, kb(u.bytes), kb(stack), where, fix, suggestStack(u.bytes))
}

// suggestStack sugere a menor constante de std/thread em que n cabe.
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

// stackOf é o quadro de fn mais a maior pilha entre as funções que ela
// chama direto. Uma chamada recursiva não soma de novo (open marca as
// funções no caminho atual).
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

// callees são as funções do MLC que fn chama direto (sem valor fn).
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

// frame estima o quadro de fn: parâmetros e variáveis locais (todas, como
// se vivessem ao mesmo tempo) e os valores grandes que o C guarda em
// temporárias (resultados de chamadas e literais que não vão direto para
// uma variável).
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
	direct := map[ast.Expr]bool{} // valor de uma declaração: é a própria variável
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
				if l != nil { // _ não declara nada
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

// kb escreve um tamanho em bytes de forma legível.
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
