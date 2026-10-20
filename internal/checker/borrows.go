package checker

import (
	"reflect"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Returned and stored borrows
//
//	fn (v &Vec[T]) at(i int) &T { return &v.items[i] }   // returns part of v
//	x := v.at(0)                                        // x: &T
//	x.field = 3
//	io.print(*x)
//
// A function can return &T when the result comes from a borrow it
// received: the & receiver, in a method, or the only & parameter, in a function. The
// return is checked: &v.items[i] is valid; &local isn't (a dangling pointer
// would be left).
//
// The returned borrow can be used on the spot (*v.at(0), v.at(0).field,
// f(v.at(0))) or stored in a variable. While that variable is used, the
// source stays frozen: it can't be mentioned except through it.
// So there's no way to free, move, grow or pass the source again
// while a piece of it is borrowed. In the same statement where a
// call borrows part of m, m can't appear again.
//
// Borrows can't be stored in structs, slices or fields, nor
// repointed; owners inside a stored borrow can be moved
// out only if they come back before the end of the scope, as in a &T parameter.

// borrowParam returns the index of the parameter the borrow
// returned by fn comes from, or -1.
func (fn *Func) borrowParam() int { return fn.BorrowFrom - 1 }

// setBorrowFrom decides, for a function that returns &T, which parameter the
// result comes from.
func (c *checker) setBorrowFrom(fn *Func, d *ast.FuncDecl) {
	if !isBorrow(fn.Result) {
		return
	}
	if d.Extern {
		c.errorf(d.P, "an extern fn can't return a borrow")
		return
	}
	if fn.Method && isBorrow(fn.Params[0].Type) {
		fn.BorrowFrom = 1 // the receiver
		return
	}
	found := -1
	for i, p := range fn.Params {
		if isBorrow(p.Type) {
			if found >= 0 {
				c.errorf(d.P, "%s returns %s and takes more than one borrow: there's no way to know which one the result comes from (take only one, or make a method with a & receiver)", fn.Name, fn.Result)
				return
			}
			found = i
		}
	}
	if found < 0 {
		c.errorf(d.P, "%s returns %s without taking any borrow: the result would point to something that stops existing", fn.Name, fn.Result)
		return
	}
	fn.BorrowFrom = found + 1
}

// borrowExpr checks an expression used as a borrow (the value of x := ...,
// or of a return &T): &place, &s[a:b], or something that's already a borrow.
// Returns the type.
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

// borrowRoot returns the variable the borrow e comes from (the root of the borrowed
// place, following the borrowed variables up to the source), or nil if it
// comes from a temporary value. view says whether the borrow is a view created on
// the spot (&s[a:b], &a with a an array viewed as a slice), which can't leave the
// function.
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
				return r, true // &a viewed as &[]T
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

// placeRoot returns the variable at the root of a place (p, p.field, s[i], *p).
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
		return c.borrowRoot(e) // &m.get(&k).field
	}
	return nil, false
}

// checkBorrowReturn checks the value of a return in a function that returns &T.
func (c *checker) checkBorrowReturn(s *ast.ReturnStmt, want *Borrow) {
	c.borrowExpr(s.Value, want, "return")
	i := c.fn.borrowParam()
	if i < 0 {
		return // the error was already given in the signature
	}
	src := c.fn.Params[i]
	root, view := c.borrowRoot(s.Value)
	switch {
	case root == nil:
		c.errorf(s.Value.Pos(), "the returned borrow comes from a temporary value; it must come from %s", src.Name)
	case root != src:
		c.errorf(s.Value.Pos(), "the returned borrow comes from %s, which stops existing at the end of the function; it must come from %s", root.Name, src.Name)
	case view:
		c.errorf(s.Value.Pos(), "a sub-slice or an array viewed as a slice can't be returned: the view is created on the spot and vanishes at the end of the function")
	}
}

// declareBorrow registers x := <borrow>: the source and the parent (if x comes from
// another borrowed variable).
func (c *checker) declareBorrow(l *Local, value ast.Expr) {
	root, view := c.borrowRoot(value)
	if root == nil {
		c.errorf(value.Pos(), "%s would store a borrow of a temporary value: store the source in a variable first", l.Name)
		return
	}
	c.info.BorrowSrc[l] = root
	c.info.BorrowView[l] = view
	if parent := c.borrowParent(value); parent != nil {
		c.info.BorrowParent[l] = parent
	}
}

// borrowParent returns the borrowed variable value is derived from
// directly (y := &x.field, with x borrowed), or nil.
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

// ---------- lifetime ----------

// borrowDecl is a borrowed variable: x := ..., or the name of a pattern
// that borrows an owning field. start is where the borrow starts to apply.
type borrowDecl struct {
	l     *Local
	start token.Pos
	pos   token.Pos
}

type borrowUse struct {
	l   *Local
	pos token.Pos
}

// checkBorrowLifetimes checks, in a function, that the source of each stored
// borrow isn't mentioned while it's used, and that a call that
// returns a borrow doesn't share the statement with another mention of the source.
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
			// Circle(r) in a match that only reads: r borrows an owning field
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
	// variables that can see the same object as root
	group := func(root *Local) map[*Local]bool {
		g := map[*Local]bool{root: true}
		if root.Param && isBorrow(root.Type) {
			for _, p := range borrowParams {
				g[p] = true // & parameters can point to the same object
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
				// used in a loop that started before the declaration: the whole loop applies
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
			c.errorf(u.pos, "%s is borrowed by %s (line %d) until line %d: use %s, or finish using %s first",
				u.l.Name, x.Name, d.pos.Line, end.Line, x.Name, x.Name)
		}
	}
	c.sameStatementBorrows(fn, group)
}

// sameStatementBorrows refuses, in a statement with a call that returns a
// borrow of m, another mention of m outside that call: f(m.get(&k), &m)
// would let f change m while holding a piece of it.
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
		parentsOf(stmt, parents) // starting at the statement: the left side of = knows it's a target
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
							c.errorf(id.P, "%s appears again in the statement where %s borrows part of it: use the borrow in a statement of its own (x := ...)", l.Name, exprName(call.Fun))
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

// readAtOnce says whether the borrow returned by call is only read on the spot (*call,
// call.field, call[i] as a value): the value is copied there and the borrow
// ends, so mentioning the source afterwards, in the same statement, is safe. It doesn't
// apply if the place is borrowed (&call.field), receives an assignment or
// is the receiver of another call.
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
	// goes up the chain of fields and indices: call.a.b[i]
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
				return true // used as an index: read
			}
			top = n
			continue
		case *ast.UnaryExpr:
			return n.Op != token.AMP // &call.field stays borrowed
		case *ast.CallExpr:
			return n.Fun != top // call.method(): the receiver stays borrowed
		case *ast.AssignStmt:
			return n.Lhs != top
		case *ast.IncDecStmt:
			return false
		}
		return true
	}
}

// parentsOf notes the parent of each node inside n.
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

// maxPos returns the largest position inside n.
func maxPos(n ast.Node) token.Pos {
	best := n.Pos()
	inspect(n, func(x any) {
		if nd, ok := x.(ast.Node); ok {
			if p := nd.Pos(); best.Before(p) {
				best = p
			}
		}
		if id, ok := x.(*ast.Ident); ok {
			// the end of the name counts (so the declaration ends after it)
			end := token.Pos{Line: id.P.Line, Col: id.P.Col + len(id.Name)}
			if best.Before(end) {
				best = end
			}
		}
	})
	return best
}

// inspect calls f with each AST node of n (by reflection, so as not to depend on
// each node type).
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
