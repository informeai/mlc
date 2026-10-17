package checker

import (
	"reflect"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Empréstimos devolvidos e guardados
//
//	fn (v &Vec[T]) at(i int) &T { return &v.items[i] }   // devolve parte de v
//	x := v.at(0)                                        // x: &T
//	x.campo = 3
//	io.print(*x)
//
// Uma função pode devolver &T quando o resultado vem de um empréstimo que ela
// recebeu: o receptor &, num método, ou o único parâmetro &, numa função. O
// return é conferido: &v.items[i] vale; &local, não (sobraria um ponteiro
// pendente).
//
// O empréstimo devolvido pode ser usado na hora (*v.at(0), v.at(0).campo,
// f(v.at(0))) ou guardado numa variável. Enquanto essa variável é usada, a
// origem fica congelada: não pode ser mencionada a não ser através dela.
// Assim não dá para liberar, mover, crescer ou passar de novo a origem
// enquanto um pedaço dela está emprestado. Na mesma instrução em que uma
// chamada empresta parte de m, m não pode aparecer de novo.
//
// Empréstimos não podem ser guardados em structs, slices ou campos, nem
// reapontados; donos dentro de um empréstimo guardado podem ser movidos para
// fora só se voltarem antes do fim do escopo, como num parâmetro &T.

// borrowParam devolve o índice do parâmetro de onde vem o empréstimo
// devolvido por fn, ou -1.
func (fn *Func) borrowParam() int { return fn.BorrowFrom - 1 }

// setBorrowFrom decide, para uma função que devolve &T, de qual parâmetro o
// resultado vem.
func (c *checker) setBorrowFrom(fn *Func, d *ast.FuncDecl) {
	if !isBorrow(fn.Result) {
		return
	}
	if d.Extern {
		c.errorf(d.P, "extern fn não pode devolver empréstimo")
		return
	}
	if fn.Method && isBorrow(fn.Params[0].Type) {
		fn.BorrowFrom = 1 // o receptor
		return
	}
	found := -1
	for i, p := range fn.Params {
		if isBorrow(p.Type) {
			if found >= 0 {
				c.errorf(d.P, "%s devolve %s e recebe mais de um empréstimo: não dá para saber de qual o resultado vem (receba só um, ou faça um método com receptor &)", fn.Name, fn.Result)
				return
			}
			found = i
		}
	}
	if found < 0 {
		c.errorf(d.P, "%s devolve %s sem receber nenhum empréstimo: o resultado apontaria para algo que deixa de existir", fn.Name, fn.Result)
		return
	}
	fn.BorrowFrom = found + 1
}

// borrowExpr checa uma expressão usada como empréstimo (o valor de x := ...,
// ou de um return &T): &lugar, &s[a:b], ou algo que já é um empréstimo.
// Devolve o tipo.
func (c *checker) borrowExpr(e ast.Expr, want *Borrow, context string) Type {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AMP {
		if want != nil {
			c.borrowArg(e, want, context)
			return c.info.Types[e]
		}
		t := c.peekType(e)
		if b, ok := t.(*Borrow); ok {
			c.borrowArg(e, b, context)
		}
		c.info.Types[e] = t
		return t
	}
	if want != nil {
		c.valueAs(e, want, context)
		return want
	}
	return c.value(e)
}

// borrowRoot devolve a variável de onde vem o empréstimo e (a raiz do lugar
// emprestado, seguindo as variáveis emprestadas até a origem), ou nil se ele
// vem de um valor temporário. view diz se o empréstimo é uma visão criada na
// hora (&s[a:b], &a com a um array visto como slice), que não pode sair da
// função.
func (c *checker) borrowRoot(e ast.Expr) (root *Local, view bool) {
	switch e := e.(type) {
	case *ast.Ident:
		l := c.info.Locals[e]
		if l == nil {
			return nil, false
		}
		if src := c.info.BorrowSrc[l]; src != nil {
			return src, c.info.BorrowView[l]
		}
		return l, false
	case *ast.UnaryExpr:
		switch e.Op {
		case token.AMP:
			if _, isSub := e.X.(*ast.SliceExpr); isSub {
				r, _ := c.placeRoot(e.X)
				return r, true
			}
			if isArray(c.info.Types[e.X]) && !isArray(elemOf(c.info.Types[e])) {
				r, _ := c.placeRoot(e.X)
				return r, true // &a visto como &[]T
			}
			return c.placeRoot(e.X)
		case token.MUL:
			return c.placeRoot(e.X)
		}
	case *ast.CallExpr:
		callee, ok := c.info.Calls[e]
		if !ok || callee.Func == nil || callee.Func.borrowParam() < 0 {
			return nil, false
		}
		args := c.info.CallArgs(e)
		i := callee.Func.borrowParam()
		if i >= len(args) {
			return nil, false
		}
		return c.borrowRoot(args[i])
	case *ast.SelectorExpr, *ast.IndexExpr, *ast.SliceExpr:
		return c.placeRoot(e)
	}
	return nil, false
}

// placeRoot devolve a variável na raiz de um lugar (p, p.campo, s[i], *p).
func (c *checker) placeRoot(e ast.Expr) (*Local, bool) {
	switch e := e.(type) {
	case *ast.Ident:
		return c.borrowRoot(e)
	case *ast.SelectorExpr:
		return c.placeRoot(e.X)
	case *ast.IndexExpr:
		return c.placeRoot(e.X)
	case *ast.SliceExpr:
		return c.placeRoot(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.MUL {
			return c.placeRoot(e.X)
		}
	case *ast.CallExpr:
		return c.borrowRoot(e) // &m.get(&k).campo
	}
	return nil, false
}

// checkBorrowReturn confere o valor de um return numa função que devolve &T.
func (c *checker) checkBorrowReturn(s *ast.ReturnStmt, want *Borrow) {
	c.borrowExpr(s.Value, want, "retorno")
	i := c.fn.borrowParam()
	if i < 0 {
		return // o erro já foi dado na assinatura
	}
	src := c.fn.Params[i]
	root, view := c.borrowRoot(s.Value)
	switch {
	case root == nil:
		c.errorf(s.Value.Pos(), "o empréstimo devolvido vem de um valor temporário; ele precisa vir de %s", src.Name)
	case root != src:
		c.errorf(s.Value.Pos(), "o empréstimo devolvido vem de %s, que deixa de existir no fim da função; ele precisa vir de %s", root.Name, src.Name)
	case view:
		c.errorf(s.Value.Pos(), "não dá para devolver um sub-slice ou um array visto como slice: a visão é criada na hora e some no fim da função")
	}
}

// declareBorrow registra x := <empréstimo>: a origem e o pai (se x vem de
// outra variável emprestada).
func (c *checker) declareBorrow(l *Local, value ast.Expr) {
	root, view := c.borrowRoot(value)
	if root == nil {
		c.errorf(value.Pos(), "%s guardaria um empréstimo de um valor temporário: guarde a origem numa variável primeiro", l.Name)
		return
	}
	c.info.BorrowSrc[l] = root
	c.info.BorrowView[l] = view
	if parent := c.borrowParent(value); parent != nil {
		c.info.BorrowParent[l] = parent
	}
}

// borrowParent devolve a variável emprestada de que value é derivado
// diretamente (y := &x.campo, com x emprestada), ou nil.
func (c *checker) borrowParent(e ast.Expr) *Local {
	var walk func(e ast.Expr) *Local
	walk = func(e ast.Expr) *Local {
		switch e := e.(type) {
		case *ast.Ident:
			if l := c.info.Locals[e]; l != nil && c.info.BorrowSrc[l] != nil {
				return l
			}
		case *ast.UnaryExpr:
			return walk(e.X)
		case *ast.SelectorExpr:
			return walk(e.X)
		case *ast.IndexExpr:
			return walk(e.X)
		case *ast.SliceExpr:
			return walk(e.X)
		case *ast.CallExpr:
			if callee, ok := c.info.Calls[e]; ok && callee.Func != nil && callee.Func.borrowParam() >= 0 {
				args := c.info.CallArgs(e)
				if i := callee.Func.borrowParam(); i < len(args) {
					return walk(args[i])
				}
			}
		}
		return nil
	}
	return walk(e)
}

// ---------- tempo de vida ----------

// borrowDecl é uma variável emprestada: x := ..., ou o nome de um padrão
// que empresta um campo dono. start é onde o empréstimo passa a valer.
type borrowDecl struct {
	l     *Local
	start token.Pos
	pos   token.Pos
}

type borrowUse struct {
	l   *Local
	pos token.Pos
}

// checkBorrowLifetimes confere, numa função, que a origem de cada empréstimo
// guardado não é mencionada enquanto ele é usado, e que uma chamada que
// devolve um empréstimo não divide a instrução com outra menção da origem.
func (c *checker) checkBorrowLifetimes(fn *Func) {
	if fn.Decl == nil || fn.Decl.Body == nil {
		return
	}
	var uses []borrowUse
	var loops []*ast.ForStmt
	var decls []borrowDecl
	inspect(fn.Decl.Body, func(n any) {
		switch n := n.(type) {
		case *ast.Ident:
			if l := c.info.Locals[n]; l != nil {
				uses = append(uses, borrowUse{l, n.P})
			}
		case *ast.ForStmt:
			loops = append(loops, n)
		case *ast.VarDecl:
			if l := c.info.Decls[n]; l != nil && c.info.BorrowSrc[l] != nil {
				decls = append(decls, borrowDecl{l, maxPos(n.Value), n.P})
			}
		case *ast.Pattern:
			// Circulo(r) num match que só lê: r empresta um campo dono
			for _, l := range c.info.PatternBinds[n] {
				if l != nil && c.info.BorrowSrc[l] != nil {
					decls = append(decls, borrowDecl{l, l.Pos, l.Pos})
				}
			}
		}
	})
	var borrowParams []*Local
	for _, p := range fn.Params {
		if isBorrow(p.Type) {
			borrowParams = append(borrowParams, p)
		}
	}
	// variáveis que podem enxergar o mesmo objeto que root
	group := func(root *Local) map[*Local]bool {
		g := map[*Local]bool{root: true}
		if root.Param && isBorrow(root.Type) {
			for _, p := range borrowParams {
				g[p] = true // parâmetros & podem apontar para o mesmo objeto
			}
		}
		for _, d := range decls {
			if g[c.info.BorrowSrc[d.l]] {
				g[d.l] = true
			}
		}
		return g
	}
	descends := func(l, from *Local) bool {
		for p := c.info.BorrowParent[l]; p != nil; p = c.info.BorrowParent[p] {
			if p == from {
				return true
			}
		}
		return false
	}
	for _, d := range decls {
		x := d.l
		start := d.start
		end := start
		for _, u := range uses {
			if u.l != x || !start.Before(u.pos) {
				continue
			}
			e := u.pos
			for _, lp := range loops {
				// usada num laço que começou antes da declaração: vale o laço todo
				if within(u.pos, lp.P, lp.Body.End) && !within(d.pos, lp.P, lp.Body.End) && e.Before(lp.Body.End) {
					e = lp.Body.End
				}
			}
			if end.Before(e) {
				end = e
			}
		}
		g := group(c.info.BorrowSrc[x])
		for _, u := range uses {
			if !g[u.l] || u.l == x || descends(u.l, x) || !start.Before(u.pos) || end.Before(u.pos) {
				continue
			}
			c.errorf(u.pos, "%s está emprestada a %s (linha %d) até a linha %d: use %s, ou termine de usar %s antes",
				u.l.Name, x.Name, d.pos.Line, end.Line, x.Name, x.Name)
		}
	}
	c.sameStatementBorrows(fn, group)
}

// sameStatementBorrows recusa, numa instrução com uma chamada que devolve um
// empréstimo de m, outra menção de m fora dessa chamada: f(m.get(&k), &m)
// deixaria f mudar m enquanto segura um pedaço dela.
func (c *checker) sameStatementBorrows(fn *Func, group func(*Local) map[*Local]bool) {
	check := func(stmt any, exprs ...ast.Node) {
		var calls []*ast.CallExpr
		for _, e := range exprs {
			if e == nil {
				continue
			}
			inspect(e, func(n any) {
				if call, ok := n.(*ast.CallExpr); ok {
					if callee, ok := c.info.Calls[call]; ok && callee.Func != nil && callee.Func.borrowParam() >= 0 {
						calls = append(calls, call)
					}
				}
			})
		}
		parents := map[any]any{}
		parentsOf(stmt, parents) // a partir da instrução: o lado esquerdo de = sabe que é alvo
		for _, call := range calls {
			root, _ := c.borrowRoot(call)
			if root == nil || readAtOnce(call, parents) {
				continue
			}
			g := group(root)
			inside := map[token.Pos]bool{}
			inspect(call, func(n any) {
				if id, ok := n.(*ast.Ident); ok {
					inside[id.P] = true
				}
			})
			for _, e := range exprs {
				if e == nil {
					continue
				}
				inspect(e, func(n any) {
					if id, ok := n.(*ast.Ident); ok && !inside[id.P] {
						if l := c.info.Locals[id]; l != nil && g[l] {
							c.errorf(id.P, "%s aparece de novo na instrução em que %s empresta parte dela: use o empréstimo numa instrução só para ele (x := ...)", l.Name, exprName(call.Fun))
						}
					}
				})
			}
		}
	}
	inspect(fn.Decl.Body, func(n any) {
		switch s := n.(type) {
		case *ast.ExprStmt:
			check(s, s.X)
		case *ast.AssignStmt:
			check(s, s.Lhs, s.Rhs)
		case *ast.VarDecl:
			if s.Value != nil {
				check(s, s.Value)
			}
		case *ast.ReturnStmt:
			if s.Value != nil {
				check(s, s.Value)
			}
		case *ast.IfStmt:
			check(s.Cond, s.Cond)
		case *ast.ForStmt:
			if s.Cond != nil {
				check(s.Cond, s.Cond)
			}
		case *ast.MatchStmt:
			check(s.Subject, s.Subject)
		case *ast.IncDecStmt:
			check(s, s.X)
		}
	})
}

// readAtOnce diz se o empréstimo devolvido por call é só lido na hora (*call,
// call.campo, call[i] como valor): o valor é copiado ali e o empréstimo
// acaba, então mencionar a origem depois, na mesma instrução, é seguro. Não
// vale se o lugar for emprestado (&call.campo), receber uma atribuição ou
// for o receptor de outra chamada.
func readAtOnce(call *ast.CallExpr, parents map[any]any) bool {
	var top any = call
	p := parents[top]
	switch q := p.(type) {
	case *ast.UnaryExpr:
		if q.Op != token.MUL {
			return false
		}
		top = q
	case *ast.SelectorExpr, *ast.IndexExpr:
	default:
		return false
	}
	// sobe pela cadeia de campos e índices: call.a.b[i]
	for {
		next := parents[top]
		switch n := next.(type) {
		case *ast.SelectorExpr:
			if n.X != top {
				return true
			}
			top = n
			continue
		case *ast.IndexExpr:
			if n.X != top {
				return true // usado como índice: lido
			}
			top = n
			continue
		case *ast.UnaryExpr:
			return n.Op != token.AMP // &call.campo continua emprestado
		case *ast.CallExpr:
			return n.Fun != top // call.metodo(): o receptor fica emprestado
		case *ast.AssignStmt:
			return n.Lhs != top
		case *ast.IncDecStmt:
			return false
		}
		return true
	}
}

// parentsOf anota o pai de cada nó dentro de n.
func parentsOf(n any, parents map[any]any) {
	var walk func(v reflect.Value, parent any)
	walk = func(v reflect.Value, parent any) {
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				return
			}
			node := v.Interface()
			if parent != nil {
				parents[node] = parent
			}
			walk(v.Elem(), node)
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), parent)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i), parent)
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), parent)
			}
		}
	}
	walk(reflect.ValueOf(n), nil)
}

func within(p, from, to token.Pos) bool { return !p.Before(from) && !to.Before(p) }

// maxPos devolve a maior posição dentro de n.
func maxPos(n ast.Node) token.Pos {
	best := n.Pos()
	inspect(n, func(x any) {
		if nd, ok := x.(ast.Node); ok {
			if p := nd.Pos(); best.Before(p) {
				best = p
			}
		}
		if id, ok := x.(*ast.Ident); ok {
			// o fim do nome conta (para a declaração terminar depois dele)
			end := token.Pos{Line: id.P.Line, Col: id.P.Col + len(id.Name)}
			if best.Before(end) {
				best = end
			}
		}
	})
	return best
}

// inspect chama f com cada nó da AST de n (por reflexão, para não depender de
// cada tipo de nó).
func inspect(n any, f func(any)) {
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return
			}
			if v.Kind() == reflect.Pointer {
				f(v.Interface())
			}
			walk(v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(n))
}
