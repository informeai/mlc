package checker

import (
	"strings"

	"mlc/internal/ast"
	"mlc/internal/token"
)

// Verificador de posse (docs/memoria.md)
//
// Roda depois da checagem de tipos, uma vez por função. Acompanha o estado de
// cada caminho dono: uma variável (p) ou um campo ponteiro alcançado a partir
// dela (p.prox, p.prox.prox, l.cabeca). Estados possíveis:
//
//	sLive: é dona de memória e precisa liberá-la ou passá-la adiante
//	sNull: vale nil (só ?*T)
//	sDead: foi liberada ou movida; não pode ser usada
//
// e um quarto bit, sOpaque, que diz o que se sabe dos campos do objeto
// apontado: sem ele, o objeto saiu de alloc(T) e seus campos ponteiro valem
// nil; com ele, o objeto veio de fora (parâmetro, função) e cada campo pode
// ser dono ou nil.
//
// Onde dois caminhos se juntam, os estados se unem. É erro: usar o que pode
// estar sDead; usar o conteúdo do que pode ser nil; liberar duas vezes;
// sobrescrever o que pode estar sLive; sair do escopo de uma variável que pode
// estar sLive (vazamento); liberar um objeto cujos campos ainda podem ser donos.
//
// Laços: o corpo é analisado até o estado do começo da volta estabilizar
// (ponto fixo), com os erros silenciados; depois, uma última passada com o
// estado estável reporta os erros. Assim a segunda volta em diante também é
// verificada: liberar dentro do laço o que veio de fora vira free duplo, e
// alocar sem liberar vira sobrescrita.

type ownState uint8

const (
	sLive ownState = 1 << iota
	sNull
	sDead
	sOpaque

	sObligation = sLive | sNull | sDead
)

// pathKey identifica um caminho: a variável raiz e os campos a partir dela
// ("" para a própria variável, "prox.prox" para p.prox.prox).
type pathKey struct {
	root   *Local
	fields string
}

func (k pathKey) isRoot() bool { return k.fields == "" }

func (k pathKey) child(rel string) pathKey {
	if k.fields == "" {
		return pathKey{k.root, rel}
	}
	return pathKey{k.root, k.fields + "." + rel}
}

func (k pathKey) parent() pathKey {
	i := strings.LastIndexByte(k.fields, '.')
	if i < 0 {
		return pathKey{k.root, ""}
	}
	return pathKey{k.root, k.fields[:i]}
}

// under diz se k fica estritamente abaixo de p.
func (k pathKey) under(p pathKey) bool {
	if k.root != p.root || k.fields == p.fields {
		return false
	}
	return p.fields == "" || strings.HasPrefix(k.fields, p.fields+".")
}

// rel devolve os campos de k relativos a p (k precisa estar abaixo de p).
func (k pathKey) rel(p pathKey) string {
	if p.fields == "" {
		return k.fields
	}
	return k.fields[len(p.fields)+1:]
}

func (k pathKey) String() string {
	if k.fields == "" {
		return k.root.Name
	}
	return k.root.Name + "." + k.fields
}

func elemOf(t Type) Type {
	switch t := t.(type) {
	case *Pointer:
		return t.Elem
	case *Borrow:
		return t.Elem
	}
	return t
}

// pathType devolve o tipo do caminho k.
func pathType(k pathKey) Type {
	t := k.root.Type
	if k.fields == "" {
		return t
	}
	for _, f := range strings.Split(k.fields, ".") {
		t = elemOf(t).(*Struct).Field(f).Type
	}
	return t
}

// objectOf devolve o caminho do objeto que contém o campo k: o prefixo
// ponteiro mais próximo (structs guardadas por valor fazem parte do mesmo
// objeto).
func objectOf(k pathKey) pathKey {
	p := k.parent()
	for !p.isRoot() && !isPointerType(pathType(p)) {
		p = p.parent()
	}
	return p
}

// ownerFields lista os campos donos de um objeto do tipo t, inclusive os de
// structs guardadas por valor ("prox", "no.prox").
func ownerFields(t Type, prefix string) []string {
	s, ok := t.(*Struct)
	if !ok {
		return nil
	}
	var out []string
	for _, f := range s.Fields {
		switch {
		case IsOwner(f.Type):
			out = append(out, prefix+f.Name)
		case isResource(f.Type):
			out = append(out, ownerFields(f.Type, prefix+f.Name+".")...)
		}
	}
	return out
}

// ownFlow é o estado de cada caminho num ponto do código. nil significa
// ponto inalcançável (depois de return, break ou continue).
type ownFlow map[pathKey]ownState

func (f ownFlow) clone() ownFlow {
	if f == nil {
		return nil
	}
	g := make(ownFlow, len(f))
	for k, v := range f {
		g[k] = v
	}
	return g
}

func (f ownFlow) has(root *Local) bool {
	_, ok := f[pathKey{root, ""}]
	return ok
}

// get devolve o estado de k, deduzindo o de campos que não foram tocados a
// partir do objeto que os contém.
func (f ownFlow) get(k pathKey) ownState {
	if v, ok := f[k]; ok {
		return v
	}
	if k.isRoot() {
		return 0
	}
	obj := f.get(objectOf(k))
	if obj == 0 {
		return 0
	}
	if obj&sOpaque == 0 {
		return sNull // objeto saído de alloc(T): campos ponteiro zerados
	}
	if p, ok := pathType(k).(*Pointer); ok && !p.Nullable {
		return sLive | sOpaque
	}
	return sLive | sNull | sOpaque
}

// merge junta dois caminhos de execução. Ficam os caminhos cuja variável
// raiz existe nos dois lados: as outras foram declaradas num escopo que fechou.
func merge(a, b ownFlow) ownFlow {
	if a == nil {
		return b.clone()
	}
	if b == nil {
		return a.clone()
	}
	m := ownFlow{}
	for _, src := range []ownFlow{a, b} {
		for k := range src {
			if a.has(k.root) && b.has(k.root) {
				m[k] = a.get(k) | b.get(k)
			}
		}
	}
	return m
}

// ownValue é um valor dono em trânsito: o estado dele e o dos campos que
// vêm junto (relativos ao valor), para instalar no destino.
type ownValue struct {
	st   ownState
	sub  map[string]ownState
	gone map[string]token.Pos
}

type ownLoop struct {
	stmt      *ast.ForStmt
	exits     []ownFlow // estados nos break
	continues []ownFlow // estados nos continue, já depois do pós
	depth     int
}

type ownChecker struct {
	c        *checker
	fn       *Func
	st       ownFlow
	scopes   [][]*Local
	loops    []*ownLoop
	borrows  []*Local              // parâmetros &T
	origin   map[pathKey]token.Pos // onde o caminho recebeu a memória atual
	gone     map[pathKey]token.Pos // onde o caminho foi liberado ou movido
	drains   []pathKey             // slices sendo esvaziados por for in drain(s)
	pending  *[]pendingMove        // moves dos argumentos da chamada em avaliação
	reported map[pathKey]bool      // erros de uso já reportados
	muted    int                   // maior que zero: erros silenciados (laço estabilizando)
}

func (c *checker) checkOwnership(fn *Func) {
	o := &ownChecker{
		c: c, fn: fn, st: ownFlow{},
		origin: map[pathKey]token.Pos{}, gone: map[pathKey]token.Pos{},
		reported: map[pathKey]bool{},
	}
	o.push()
	for _, p := range fn.Params {
		switch t := p.Type.(type) {
		case *Pointer:
			s := sLive | sOpaque
			if t.Nullable {
				s |= sNull
			}
			o.declare(p, ownValue{st: s}, p.Pos)
		case *Slice:
			// Um slice recebido é dono: precisa ser liberado ou passado
			// adiante. sOpaque: se os elementos são donos, podem ter memória.
			o.declare(p, ownValue{st: sLive | sOpaque}, p.Pos)
		case *Borrow:
			// Um empréstimo nunca é nil nem liberado aqui; o que interessa
			// são os campos do objeto emprestado.
			o.st[pathKey{p, ""}] = sLive | sOpaque
			o.borrows = append(o.borrows, p)
		}
	}
	for _, s := range fn.Decl.Body.Stmts {
		o.stmt(s)
	}
	if o.st != nil {
		o.borrowsComplete(fn.Decl.Body.End)
	}
	o.pop(fn.Decl.Body.End, "no fim da função")
}

func (o *ownChecker) push() { o.scopes = append(o.scopes, nil) }

func (o *ownChecker) pop(pos token.Pos, where string) {
	top := o.scopes[len(o.scopes)-1]
	o.leaks(top, pos, where)
	for k := range o.st {
		for _, l := range top {
			if k.root == l {
				delete(o.st, k)
			}
		}
	}
	o.scopes = o.scopes[:len(o.scopes)-1]
}

func (o *ownChecker) declare(l *Local, v ownValue, pos token.Pos) {
	k := pathKey{l, ""}
	o.install(k, v)
	o.origin[k] = pos
	o.scopes[len(o.scopes)-1] = append(o.scopes[len(o.scopes)-1], l)
}

// install coloca o valor v no caminho k, com os campos que vêm junto.
func (o *ownChecker) install(k pathKey, v ownValue) {
	o.dropUnder(k)
	o.st[k] = v.st
	for rel, s := range v.sub {
		o.st[k.child(rel)] = s
	}
	for rel, pos := range v.gone {
		o.gone[k.child(rel)] = pos
	}
}

// extract tira de k o valor dono e os campos rastreados abaixo dele.
func (o *ownChecker) extract(k pathKey) ownValue {
	v := ownValue{st: o.st.get(k), sub: map[string]ownState{}, gone: map[string]token.Pos{}}
	for p, s := range o.st {
		if p.under(k) {
			v.sub[p.rel(k)] = s
			if pos, ok := o.gone[p]; ok {
				v.gone[p.rel(k)] = pos
			}
		}
	}
	return v
}

func (o *ownChecker) dropUnder(k pathKey) {
	for p := range o.st {
		if p.under(k) {
			delete(o.st, p)
		}
	}
}

func (o *ownChecker) kill(k pathKey, pos token.Pos) {
	o.dropUnder(k)
	o.st[k] = sDead
	o.gone[k] = pos
}

// leaks reporta as variáveis de ls que ainda podem ter memória ao sair por pos.
func (o *ownChecker) leaks(ls []*Local, pos token.Pos, where string) {
	if o.st == nil {
		return
	}
	for _, l := range ls {
		k := pathKey{l, ""}
		if isBorrow(l.Type) {
			o.borrowedFieldsBack(l, pos)
			continue
		}
		s := o.st.get(k) & sObligation
		if s&sLive == 0 {
			continue
		}
		if p, ok := l.Type.(*Pointer); ok && isExternStruct(p.Elem) {
			o.errorf(pos, "vazamento: o handle de C %s (linha %d) pode ainda estar aberto %s; passe-o à função de C que o fecha",
				l.Name, o.origin[k].Line, where)
			if o.muted == 0 {
				o.st[k] = sDead
			}
			continue
		}
		if s == sLive {
			o.errorf(pos, "vazamento: %s ainda é dona da memória (linha %d) %s; libere com free(%s) ou passe a posse adiante",
				l.Name, o.origin[k].Line, where, l.Name)
		} else {
			o.errorf(pos, "vazamento: %s pode ainda ser dona da memória (linha %d) %s, dependendo do caminho",
				l.Name, o.origin[k].Line, where)
		}
		if o.muted == 0 {
			o.st[k] = sDead // evita repetir o erro adiante
		}
	}
}

func (o *ownChecker) leaksFrom(depth int, pos token.Pos, where string) {
	for i := len(o.scopes) - 1; i >= depth; i-- {
		o.leaks(o.scopes[i], pos, where)
	}
}

// borrowsComplete exige que nenhum campo de objeto emprestado fique sem
// valor ao sair da função: quem emprestou espera o objeto inteiro de volta.
// borrowedFieldsBack confere, no fim do escopo de uma variável emprestada,
// que os campos movidos para fora através dela voltaram.
func (o *ownChecker) borrowedFieldsBack(l *Local, pos token.Pos) {
	for k := range o.st {
		if k.root == l && !k.isRoot() && o.st.get(k)&sDead != 0 {
			o.errorf(pos, "%s foi movida para fora do empréstimo (linha %d) e precisa receber um valor antes do fim do escopo de %s",
				k, o.gone[k].Line, l.Name)
			if o.muted == 0 {
				o.st[k] = sNull
			}
		}
	}
}

// borrowValue avalia o valor de x := <empréstimo>: &lugar confere o lugar
// (como um argumento emprestado); o resto é lido.
func (o *ownChecker) borrowValue(e ast.Expr) {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AMP {
		if obj, ok := o.borrowArg(e); ok {
			_ = obj
		}
		return
	}
	o.read(e)
}

func (o *ownChecker) borrowsComplete(pos token.Pos) {
	for k := range o.st {
		for _, b := range o.borrows {
			if k.root == b && !k.isRoot() && o.st.get(k)&sDead != 0 {
				o.errorf(pos, "%s foi movida para fora do empréstimo (linha %d) e precisa receber um valor antes de sair da função",
					k, o.gone[k].Line)
				if o.muted == 0 {
					o.st[k] = sNull
				}
			}
		}
	}
}

// ---------- Comandos ----------

func (o *ownChecker) stmt(s ast.Stmt) {
	if o.st == nil {
		return // código inalcançável
	}
	info := o.c.info
	switch s := s.(type) {
	case *ast.Block:
		o.push()
		for _, st := range s.Stmts {
			o.stmt(st)
		}
		o.pop(s.End, "no fim do bloco")
	case *ast.VarDecl:
		l := info.Decls[s]
		if isBorrow(l.Type) {
			// x := v.at(0): como um parâmetro &T, nunca nil nem liberado; os
			// campos donos podem sair e voltar antes do fim do escopo
			if s.Value != nil {
				o.borrowValue(s.Value)
			}
			o.st[pathKey{l, ""}] = sLive | sOpaque
			o.scopes[len(o.scopes)-1] = append(o.scopes[len(o.scopes)-1], l)
			return
		}
		if !IsOwner(l.Type) {
			if s.Value != nil {
				o.read(s.Value)
			}
			return
		}
		v, pos := ownValue{st: sNull}, s.P
		if s.Value != nil {
			v, pos = o.consume(s.Value, l.Type, "a declaração de "+s.Name), s.Value.Pos()
		}
		o.declare(l, v, pos)
	case *ast.ConstDecl:
	case *ast.AssignStmt:
		if ix, isIndex := s.Lhs.(*ast.IndexExpr); isIndex && IsOwner(info.Types[s.Lhs]) {
			o.errorf(ix.Lbrack, "um elemento dono não pode ser sobrescrito direto (o valor antigo vazaria): use velho := replace(&s[i], v)")
			o.consume(s.Rhs, nil, "")
			o.target(s.Lhs)
			return
		}
		if s.Op == token.ASSIGN {
			if k, ok := o.pathOf(s.Lhs); ok && IsOwner(pathType(k)) {
				o.assignOwner(k, s)
				return
			}
		}
		// O lado direito vem antes: em p.x = f(p), f pode liberar p.
		o.read(s.Rhs)
		o.target(s.Lhs)
	case *ast.IncDecStmt:
		o.target(s.X)
	case *ast.ExprStmt:
		o.read(s.X)
		if IsOwner(info.Types[s.X]) {
			o.errorf(s.X.Pos(), "vazamento: o ponteiro dono devolvido aqui é descartado; guarde em uma variável")
		}
	case *ast.ReturnStmt:
		if s.Value != nil {
			if t := info.Types[s.Value]; IsOwner(t) || t == NilType {
				o.consume(s.Value, o.fn.Result, "o retorno de "+o.fn.Name)
			} else {
				o.read(s.Value)
			}
		}
		o.borrowsComplete(s.P)
		o.leaksFrom(0, s.P, "no return")
		o.st = nil
	case *ast.IfStmt:
		o.read(s.Cond)
		base := o.st
		o.st = base.clone()
		o.apply(o.facts(s.Cond, true))
		o.stmt(s.Then)
		thenOut := o.st
		o.st = base.clone()
		o.apply(o.facts(s.Cond, false))
		if s.Else != nil {
			o.stmt(s.Else)
		}
		o.st = merge(thenOut, o.st)
	case *ast.ForStmt:
		o.forStmt(s)
	case *ast.MatchStmt:
		o.read(s.Subject)
		base := o.st
		var out ownFlow
		for _, arm := range s.Arms {
			o.st = base.clone()
			o.push()
			o.stmt(arm.Body)
			o.pop(arm.Body.Pos(), "no fim do braço do match")
			out = merge(out, o.st)
		}
		o.st = out
	case *ast.BranchStmt:
		loop := o.loops[len(o.loops)-1]
		where := "no break"
		if s.Tok == token.CONTINUE {
			where = "no continue"
		}
		o.leaksFrom(loop.depth, s.P, where)
		if s.Tok == token.BREAK {
			loop.exits = append(loop.exits, o.st.clone())
		} else {
			if loop.stmt.Post != nil {
				o.stmt(loop.stmt.Post)
			}
			loop.continues = append(loop.continues, o.st.clone())
		}
		o.st = nil
	default:
		panic("ownership: comando desconhecido")
	}
}

// assignOwner trata "k = valor" com k uma variável ou campo dono.
func (o *ownChecker) assignOwner(k pathKey, s *ast.AssignStmt) {
	v := o.consume(s.Rhs, pathType(k), "a atribuição a "+k.String())
	if !k.isRoot() {
		o.reach(k, s.Lhs.Pos()) // o objeto que recebe o campo precisa existir
	}
	if cur := o.st.get(k); cur&sLive != 0 {
		maybe := ""
		if cur&sObligation != sLive {
			maybe = " (em algum caminho)"
		}
		line := o.origin[k].Line
		if line == 0 {
			o.errorf(s.OpPos, "vazamento: %s pode ser dona de memória%s e seria sobrescrita; libere ou mova o valor antigo antes", k, maybe)
		} else {
			o.errorf(s.OpPos, "vazamento: %s ainda é dona da memória (linha %d)%s e seria sobrescrita; libere ou passe a posse antes",
				k, line, maybe)
		}
	}
	o.install(k, v)
	o.origin[k] = s.Rhs.Pos()
}

func (o *ownChecker) forStmt(s *ast.ForStmt) {
	o.push() // escopo do init
	if s.Init != nil {
		o.stmt(s.Init)
	}
	loop := &ownLoop{stmt: s, depth: len(o.scopes)}
	o.loops = append(o.loops, loop)

	// Ponto fixo: junta o estado do fim da volta ao do começo até parar de
	// mudar. Os estados só crescem e o número de caminhos é finito.
	entry := o.st
	for i := 0; i < 100; i++ {
		o.muted++
		back := o.loopBody(s, loop, entry)
		o.muted--
		next := merge(entry, back)
		if sameFlow(next, entry) {
			break
		}
		entry = next
	}
	o.loopBody(s, loop, entry) // passada final, com erros
	o.loops = o.loops[:len(o.loops)-1]

	var out ownFlow
	switch {
	case s.Cond != nil:
		o.st = entry.clone() // a condição ficou falsa
		o.read(s.Cond)
		o.apply(o.facts(s.Cond, false))
		out = o.st
	case s.Iter != nil:
		o.st = entry.clone() // os elementos acabaram
		iter := o.iterOf(s)
		o.sliceAccess(iter)
		if k, ok := o.pathOf(iter); ok && o.c.info.Drains[s] != nil && o.st.get(k) != 0 {
			// drain completo: todos os elementos saíram do slice
			o.st[k] = o.st.get(k) &^ sOpaque
		}
		out = o.st
	}
	for _, ex := range loop.exits {
		out = merge(out, ex)
	}
	o.st = out
	o.pop(s.Body.End, "no fim do for")
}

// loopBody analisa uma volta a partir de entry e devolve o estado com que a
// próxima volta começa (fim do corpo e continues, depois do pós).
func (o *ownChecker) loopBody(s *ast.ForStmt, loop *ownLoop, entry ownFlow) ownFlow {
	loop.exits, loop.continues = nil, nil
	o.st = entry.clone()
	if s.Cond != nil {
		o.read(s.Cond)
		o.apply(o.facts(s.Cond, true))
	}
	if s.Iter != nil {
		o.sliceAccess(o.iterOf(s)) // cada volta lê o slice de novo
	}
	if drained := o.c.info.Drains[s]; drained != nil && o.st != nil {
		o.drainBody(s, drained)
	} else {
		o.stmt(s.Body)
	}
	if o.st != nil && s.Post != nil {
		o.stmt(s.Post)
	}
	back := o.st
	for _, c := range loop.continues {
		back = merge(back, c)
	}
	return back
}

// sameFlow compara dois estados, incluindo os campos deduzidos.
func sameFlow(a, b ownFlow) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	for _, pair := range [][2]ownFlow{{a, b}, {b, a}} {
		for k := range pair[0] {
			if pair[0].get(k) != pair[1].get(k) {
				return false
			}
		}
	}
	return true
}

func (o *ownChecker) errorf(pos token.Pos, format string, args ...any) {
	if o.muted == 0 {
		o.c.errorf(pos, format, args...)
	}
}

// ---------- Condições sobre nil ----------

type nilFact struct {
	k      pathKey
	notNil bool
}

// facts devolve o que a condição, sendo truth, garante sobre caminhos ?*T:
// "p != nil", "p.prox == nil", combinados com &&, || e !.
func (o *ownChecker) facts(cond ast.Expr, truth bool) []nilFact {
	switch e := cond.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case token.EQL, token.NEQ:
			x, y := e.X, e.Y
			if isNil(x) {
				x, y = y, x
			}
			if !isNil(y) {
				return nil
			}
			k, ok := o.pathOf(x)
			if !ok || !IsOwner(pathType(k)) {
				return nil
			}
			return []nilFact{{k, (e.Op == token.NEQ) == truth}}
		case token.LAND:
			if truth {
				return append(o.facts(e.X, true), o.facts(e.Y, true)...)
			}
		case token.LOR:
			if !truth {
				return append(o.facts(e.X, false), o.facts(e.Y, false)...)
			}
		}
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return o.facts(e.X, !truth)
		}
	}
	return nil
}

func (o *ownChecker) apply(fs []nilFact) {
	if o.st == nil {
		return
	}
	for _, f := range fs {
		s := o.st.get(f.k)
		if f.notNil {
			o.st[f.k] = s &^ sNull
		} else {
			o.st[f.k] = s & (sNull | sOpaque)
		}
	}
}

// ---------- Caminhos e usos ----------

// pathOf reconhece expressões que são caminhos: uma variável dona ou
// empréstimo, seguida de campos.
func (o *ownChecker) pathOf(e ast.Expr) (pathKey, bool) {
	switch e := e.(type) {
	case *ast.Ident:
		l := o.c.info.Locals[e]
		if l != nil && (IsOwner(l.Type) || isBorrow(l.Type)) {
			return pathKey{l, ""}, true
		}
	case *ast.SelectorExpr:
		if k, ok := o.pathOf(e.X); ok {
			return k.child(e.Sel), true
		}
	}
	return pathKey{}, false
}

// check reporta o uso de k se ele pode estar liberado, ou, se deref, se pode
// ser nil.
func (o *ownChecker) check(k pathKey, pos token.Pos, deref bool) {
	if o.reported[k] {
		return
	}
	s := o.st.get(k)
	switch {
	case s&sDead != 0:
		o.markReported(k)
		if s&sObligation == sDead {
			o.errorf(pos, "%s já foi liberada ou movida (linha %d)", k, o.gone[k].Line)
		} else {
			o.errorf(pos, "%s pode já ter sido liberada ou movida (linha %d), dependendo do caminho", k, o.gone[k].Line)
		}
	case deref && s&sNull != 0 && !isBorrow(pathType(k)) && !isSlice(pathType(k)):
		// (um slice vazio pode ser indexado: a checagem de limites resolve)
		o.markReported(k)
		o.errorf(pos, "%s pode ser nil aqui: verifique com if %s != nil antes de usar o conteúdo", k, k)
	}
}

// reach checa os ponteiros que precisam ser seguidos para chegar a k.
func (o *ownChecker) reach(k pathKey, pos token.Pos) {
	if k.isRoot() {
		return
	}
	p := pathKey{k.root, ""}
	for _, f := range strings.Split(k.fields, ".") {
		if isPointerType(pathType(p)) {
			o.check(p, pos, true)
		}
		p = p.child(f)
	}
}

// deref checa que o conteúdo de e (um ponteiro ou empréstimo) pode ser usado.
func (o *ownChecker) deref(e ast.Expr) {
	k, ok := o.pathOf(e)
	if ix, isIndex := e.(*ast.IndexExpr); !ok && isIndex && IsOwner(o.c.info.Types[e]) {
		// O índice só é conhecido em tempo de execução: o verificador não
		// sabe se s[i] é nil. O elemento precisa sair do slice para ser usado.
		o.errorf(ix.Lbrack, "s[i] pode ser nil e não pode ser usado direto: tire com take(&s[i]) e devolva com replace(&s[i], v)")
		o.read(e)
		return
	}
	if !ok {
		o.lostTemporary(e)
		o.read(e)
		return
	}
	o.reach(k, e.Pos())
	o.check(k, e.Pos(), true)
}

// complete exige que nenhum campo rastreado abaixo de k esteja liberado:
// um objeto só sai inteiro (movido, emprestado, devolvido).
func (o *ownChecker) complete(k pathKey, pos token.Pos, verb string) {
	for p := range o.st {
		if p.under(k) && o.st.get(p)&sDead != 0 && !o.reported[p] {
			o.markReported(p)
			o.errorf(pos, "%s não pode ser %s: %s foi movida para fora (linha %d) e não recebeu valor novo", k, verb, p, o.gone[p].Line)
		}
	}
}

// read avalia uma expressão em que caminhos donos são só usados.
func (o *ownChecker) read(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Ident:
		if k, ok := o.pathOf(e); ok {
			o.check(k, e.P, false)
		}
	case *ast.SelectorExpr:
		if isPointerType(o.c.info.Types[e.X]) {
			o.deref(e.X)
		} else {
			o.read(e.X)
		}
		if k, ok := o.pathOf(e); ok && IsOwner(pathType(k)) {
			o.check(k, e.SelPos, false)
		}
	case *ast.UnaryExpr:
		if e.Op == token.MUL {
			o.deref(e.X)
			return
		}
		o.read(e.X)
	case *ast.BinaryExpr:
		o.read(e.X)
		if e.Op != token.LAND && e.Op != token.LOR {
			o.read(e.Y)
			return
		}
		// Curto-circuito: o lado direito só roda se o esquerdo deixou.
		skipped := o.st.clone()
		o.apply(o.facts(e.X, e.Op == token.LAND))
		o.read(e.Y)
		o.st = merge(skipped, o.st)
	case *ast.IndexExpr:
		o.sliceAccess(e.X)
		o.read(e.Index)
	case *ast.CompositeLit:
		for _, f := range e.Fields {
			o.read(f.Value)
		}
	case *ast.CallExpr:
		o.call(e)
	}
}

// lostTemporary reporta *f() ou f().campo com f devolvendo um dono.
func (o *ownChecker) lostTemporary(base ast.Expr) {
	// Só chamadas produzem donos temporários; s[i] e p.campo são lugares.
	if _, isCall := base.(*ast.CallExpr); isCall && IsOwner(o.c.info.Types[base]) {
		o.errorf(base.Pos(), "vazamento: o ponteiro dono temporário é perdido aqui; guarde em uma variável primeiro")
	}
}

// target avalia o lado esquerdo de uma atribuição que não é um caminho dono.
func (o *ownChecker) target(e ast.Expr) {
	switch e := e.(type) {
	case *ast.IndexExpr:
		o.sliceAccess(e.X)
		o.read(e.Index)
	case *ast.SelectorExpr:
		if isPointerType(o.c.info.Types[e.X]) {
			o.deref(e.X)
		} else {
			o.target(e.X)
		}
	case *ast.UnaryExpr:
		o.deref(e.X)
	}
}

// consume avalia uma expressão cujo valor dono vai para um lugar do tipo
// dest (variável, campo, retorno, parâmetro *T).
func (o *ownChecker) consume(e ast.Expr, dest Type, what string) ownValue {
	var v ownValue
	switch {
	case isNil(e):
		v = ownValue{st: sNull}
	default:
		if k, ok := o.pathOf(e); ok && IsOwner(pathType(k)) {
			o.reach(k, e.Pos())
			o.check(k, e.Pos(), false)
			o.complete(k, e.Pos(), "movida")
			v = o.extract(k)
			v.st &^= sDead
			o.move(k, e.Pos())
			if p, ok := dest.(*Pointer); ok && !p.Nullable && v.st&sNull != 0 {
				o.errorf(e.Pos(), "%s pode ser nil aqui, mas %s espera %s; verifique com if %s != nil antes", k, what, dest, k)
			}
			return v
		}
		if ix, isIndex := e.(*ast.IndexExpr); isIndex {
			o.errorf(ix.Lbrack, "um elemento dono não pode ser copiado nem movido direto: use take(&s[i]) (deixa vazio no lugar)")
			o.read(e)
			return ownValue{st: sLive | sNull | sOpaque}
		}
		if call, ok := e.(*ast.CallExpr); ok && o.c.info.Calls[call].Builtin == Alloc {
			return o.allocValue(call)
		}
		if call, ok := e.(*ast.CallExpr); ok && o.c.info.Calls[call].Builtin == Realloc {
			// realloc mantém os elementos: o slice novo herda o que se sabe deles
			var old ownValue
			o.deferMoves(func() {
				old = o.consume(call.Args[0], o.c.info.Calls[call].Elem, "realloc")
				o.read(call.Args[1])
			})
			return ownValue{st: sLive | old.st&sOpaque}
		}
		o.read(e)
		v = ownValue{st: sLive | sOpaque}
		if p, ok := o.c.info.Types[e].(*Pointer); ok && p.Nullable {
			v.st |= sNull
		}
	}
	if p, ok := dest.(*Pointer); ok && !p.Nullable && v.st&sNull != 0 {
		o.errorf(e.Pos(), "o valor pode ser nil aqui, mas %s espera %s", what, dest)
	}
	return v
}

// allocValue trata alloc(T) (campos nil) e alloc(T{...}) (campos do literal).
func (o *ownChecker) allocValue(call *ast.CallExpr) ownValue {
	callee := o.c.info.Calls[call]
	if callee.Zero {
		return ownValue{st: sLive}
	}
	if callee.Slice {
		o.read(call.Args[1]) // o tamanho
		return ownValue{st: sLive}
	}
	if lit, ok := call.Args[0].(*ast.CompositeLit); ok {
		v := ownValue{st: sLive, sub: map[string]ownState{}}
		o.literalFields(lit, "", v.sub)
		return v
	}
	o.read(call.Args[0])
	return ownValue{st: sLive}
}

// literalFields avalia os campos de um literal dentro de alloc, guardando o
// estado dos campos donos em sub. Campos omitidos (literal vazio) valem nil.
func (o *ownChecker) literalFields(lit *ast.CompositeLit, prefix string, sub map[string]ownState) {
	s := o.c.info.Types[lit].(*Struct)
	for _, fi := range lit.Fields {
		f := s.Field(fi.Name)
		switch inner, isLit := fi.Value.(*ast.CompositeLit); {
		case IsOwner(f.Type):
			v := o.consume(fi.Value, f.Type, "o campo "+f.Name)
			sub[prefix+f.Name] = v.st
			for rel, st := range v.sub {
				sub[prefix+f.Name+"."+rel] = st
			}
		case isLit && isResource(f.Type):
			o.literalFields(inner, prefix+f.Name+".", sub)
		default:
			o.read(fi.Value)
		}
	}
}

func (o *ownChecker) call(e *ast.CallExpr) {
	callee := o.c.info.Calls[e]
	if _, isCtor := o.c.info.EnumCtors[e]; isCtor {
		for _, a := range e.Args {
			o.read(a) // dados de variante não são donos
		}
		return
	}
	switch callee.Builtin {
	case Free:
		o.free(e.Args[0])
		return
	case Drop:
		switch t := o.c.info.Types[e.Args[0]]; {
		case deepDrop(t):
			o.consume(e.Args[0], nil, "drop") // a função de drop libera o interior
		case IsOwner(t):
			o.free(e.Args[0]) // drop de um dono simples é free
		default:
			o.read(e.Args[0])
		}
		return
	case Alloc:
		o.allocValue(e)
		return
	case Realloc:
		// o slice antigo é consumido; o resultado é um dono novo
		o.deferMoves(func() {
			o.consume(e.Args[0], callee.Elem, "realloc")
			o.read(e.Args[1])
		})
		return
	case Take, Replace:
		o.deferMoves(func() { o.takeReplace(e, callee) })
		return
	case Convert:
		o.read(e.Args[0])
		return
	case IoPrint, Len:
		for _, a := range e.Args {
			o.read(a)
		}
		return
	}

	if callee.Indirect {
		o.read(e.Fun) // a variável ou o campo com o valor fn
	}
	// Numa mesma chamada, um objeto não pode ser emprestado e movido.
	borrowedRoots := map[*Local]bool{}
	var touched []pathKey // objetos emprestados: a função pode mudar os campos
	o.deferMoves(func() { o.userArgs(e, callee, borrowedRoots, &touched) })
	// Depois da chamada, os campos dos objetos emprestados podem ter mudado
	// (a função manteve o objeto inteiro, mas não se sabe como).
	for _, obj := range touched {
		if o.st.get(obj) != 0 {
			o.dropUnder(obj)
			o.st[obj] = o.st.get(obj) | sOpaque
		}
	}
}

// userArgs avalia os argumentos de uma chamada de função do usuário, da
// esquerda para a direita. Os moves só valem depois de todos (deferMoves).
func (o *ownChecker) userArgs(e *ast.CallExpr, callee Callee, borrowedRoots map[*Local]bool, touched *[]pathKey) {
	for i, a := range o.c.info.CallArgs(e) { // num método, o receptor vem primeiro
		context := "o argumento " + itoa(i+1) + " de " + callee.Func.Name
		pt := callee.Func.Params[i].Type
		switch {
		case IsOwner(pt): // *T, ?*T e []T: a função recebe a posse
			if k, ok := o.pathOf(a); ok && borrowedRoots[k.root] {
				o.errorf(a.Pos(), "%s é emprestada e movida na mesma chamada", k.root.Name)
				o.kill(k, a.Pos())
				continue
			}
			o.consume(a, pt, context)
		case isBorrow(pt):
			if obj, ok := o.borrowArg(a); ok {
				if o.movingRoot(obj.root) {
					o.errorf(a.Pos(), "%s é emprestada e movida na mesma chamada", obj.root.Name)
				}
				borrowedRoots[obj.root] = true
				*touched = append(*touched, obj)
			}
		default:
			o.read(a)
		}
	}
}

// borrowArg checa um argumento passado a um parâmetro &T e devolve o objeto
// emprestado, se ele é rastreado.
func (o *ownChecker) borrowArg(a ast.Expr) (pathKey, bool) {
	u, ok := a.(*ast.UnaryExpr)
	if !ok || u.Op != token.AMP {
		// empréstimo recebido, repassado
		k, ok := o.pathOf(a)
		if ok {
			o.complete(k, a.Pos(), "emprestada")
			o.notDraining(k, a.Pos())
		}
		o.read(a)
		return k, ok
	}
	if sl, isSub := u.X.(*ast.SliceExpr); isSub {
		// &s[a:b] empresta parte do slice: conta como emprestar o slice
		o.sliceAccess(sl.X)
		for _, b := range []ast.Expr{sl.Low, sl.High} {
			if b != nil {
				o.read(b)
			}
		}
		return o.pathOf(sl.X)
	}
	if ix, isIndex := u.X.(*ast.IndexExpr); isIndex {
		// &s[i] empresta um elemento: conta como emprestar o slice
		if _, isPtr := o.c.info.Types[ix].(*Pointer); isPtr {
			o.errorf(ix.Lbrack, "s[i] pode ser nil e não pode ser emprestado direto: tire com take(&s[i]) e devolva com replace(&s[i], v)")
		}
		o.sliceAccess(ix.X)
		o.read(ix.Index)
		return o.pathOf(ix.X)
	}
	k, ok := o.pathOf(u.X)
	if !ok {
		o.read(u.X) // struct local: não tem campos donos
		return pathKey{}, false
	}
	var obj pathKey
	if isPointerType(pathType(k)) || isSlice(pathType(k)) {
		// &p empresta o objeto apontado por p; &s, o slice s (que não pode
		// ter sido liberado)
		o.reach(k, u.X.Pos())
		o.check(k, u.X.Pos(), true)
		obj = k
	} else {
		// &p.no empresta uma struct guardada dentro do objeto de p
		o.reach(k, u.X.Pos())
		obj = objectOf(k)
	}
	o.notDraining(obj, u.X.Pos())
	o.complete(obj, u.X.Pos(), "emprestada")
	return obj, true
}

// free trata free(x): x não pode estar liberada, e nenhum campo dono do
// objeto pode ainda ter memória.
func (o *ownChecker) free(arg ast.Expr) {
	k, ok := o.pathOf(arg)
	if !ok || !IsOwner(pathType(k)) {
		v := o.consume(arg, nil, "free")
		if v.st&sOpaque != 0 && isOwnerSlice(o.c.info.Types[arg]) {
			o.errorf(arg.Pos(), "free de um slice temporário cujos elementos podem ser donos: guarde em uma variável e esvazie com for x in drain(s)")
		}
		if v.st&sOpaque != 0 && len(ownerFields(elemOf(o.c.info.Types[arg]), "")) > 0 {
			o.errorf(arg.Pos(), "free de um objeto temporário com campos donos: guarde em uma variável e libere os campos antes")
		}
		return
	}
	o.reach(k, arg.Pos())
	if s := o.st.get(k); s&sDead != 0 {
		maybe := ""
		if s&sObligation != sDead {
			maybe = " em algum caminho"
		}
		o.errorf(arg.Pos(), "%s liberada duas vezes: já foi liberada ou movida%s (linha %d)", k, maybe, o.gone[k].Line)
		o.kill(k, arg.Pos())
		return
	}
	if s := o.st.get(k); s&sLive != 0 && s&sOpaque != 0 && isOwnerSlice(pathType(k)) {
		o.errorf(arg.Pos(), "free(%s): os elementos de %s podem ainda ser donos de memória; esvazie antes com for x in drain(%s)", k, k, k)
	}
	if o.st.get(k)&sLive != 0 {
		for _, rel := range ownerFields(elemOf(pathType(k)), "") {
			fk := k.child(rel)
			if fs := o.st.get(fk); fs&sLive != 0 {
				o.errorf(arg.Pos(), "free(%s): %s ainda pode ser dona de memória; libere ou mova %s antes", k, fk, fk)
			}
		}
	}
	o.kill(k, arg.Pos())
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// markReported evita repetir o erro de k, mas só fora do ponto fixo de um
// laço: lá os erros estão silenciados e precisam aparecer na passada final.
func (o *ownChecker) markReported(k pathKey) {
	if o.muted == 0 {
		o.reported[k] = true
	}
}

// sliceAccess checa o slice usado em s[i], len(s) ou for x in s: ele não
// pode estar liberado. Vazio pode: a checagem de limites resolve.
func (o *ownChecker) sliceAccess(e ast.Expr) {
	k, ok := o.pathOf(e)
	if !ok {
		o.lostTemporary(e)
		o.read(e)
		return
	}
	if t := pathType(k); !IsOwner(t) && !isBorrow(t) {
		// array guardado num campo: é um valor, só o caminho até ele importa
		o.reach(k, e.Pos())
		return
	}
	o.reach(k, e.Pos())
	o.check(k, e.Pos(), true)
}

func isOwnerSlice(t Type) bool {
	e, ok := sliceElem(t)
	return ok && IsOwner(e)
}

// iterOf devolve o slice percorrido por um for in: s, em "for x in s" ou em
// "for x in drain(s)".
func (o *ownChecker) iterOf(s *ast.ForStmt) ast.Expr {
	if d := o.c.info.Drains[s]; d != nil {
		return d
	}
	return s.Iter
}

// drainBody analisa uma volta de "for x in drain(s)": x recebe o elemento,
// que sai do slice, e precisa ser liberado ou movido antes do fim da volta.
func (o *ownChecker) drainBody(s *ast.ForStmt, drained ast.Expr) {
	k, ok := o.pathOf(drained)
	if ok {
		o.drains = append(o.drains, k)
	}
	o.push()
	if l := o.c.info.ForVars[s]; l != nil && IsOwner(l.Type) {
		// o elemento tirado é dono (num genérico, T pode ser int: aí não há posse)
		o.declare(l, ownValue{st: sLive | sNull | sOpaque}, s.Var.P)
	}
	o.stmt(s.Body)
	o.pop(s.Body.End, "no fim da volta do for (o elemento tirado por drain)")
	if ok {
		o.drains = o.drains[:len(o.drains)-1]
	}
}

// notDraining recusa emprestar ou pôr elementos num slice que um
// for in drain está esvaziando: os elementos já tirados voltariam.
func (o *ownChecker) notDraining(k pathKey, pos token.Pos) {
	for _, d := range o.drains {
		if d == k {
			o.errorf(pos, "%s está sendo esvaziada por um for in drain(%s) e não pode ser emprestada nem receber elementos", k, k)
			return
		}
	}
}

// takeReplace trata take(&s[i]) e replace(&s[i], v). O elemento antigo sai
// como um dono comum. Pôr um dono com replace faz o slice voltar a "pode ter
// elementos donos".
func (o *ownChecker) takeReplace(e *ast.CallExpr, callee Callee) {
	ix := e.Args[0].(*ast.UnaryExpr).X.(*ast.IndexExpr)
	o.sliceAccess(ix.X)
	o.read(ix.Index)
	if callee.Builtin != Replace {
		return
	}
	v := o.consume(e.Args[1], callee.Elem, "o valor de replace")
	if k, ok := o.pathOf(ix.X); ok {
		o.notDraining(k, ix.X.Pos())
		if s := o.st.get(k); s != 0 && v.st&sLive != 0 {
			o.st[k] = s | sOpaque
		}
	}
}

// pendingMove é um move feito por um argumento: só vale quando a chamada
// acontece, depois de todos os argumentos. Assim f(s, len(s)) lê s antes de
// passá-la adiante, como no C gerado.
type pendingMove struct {
	k   pathKey
	pos token.Pos
}

// deferMoves avalia f guardando os moves para o fim.
func (o *ownChecker) deferMoves(f func()) {
	saved := o.pending
	var list []pendingMove
	o.pending = &list
	f()
	o.pending = saved
	for _, m := range list {
		o.move(m.k, m.pos)
	}
}

// move tira a posse de k: na hora, ou no fim dos argumentos da chamada em
// avaliação. Mover a mesma coisa duas vezes na chamada é erro.
func (o *ownChecker) move(k pathKey, pos token.Pos) {
	if o.pending == nil {
		o.kill(k, pos)
		return
	}
	for _, m := range *o.pending {
		if m.k == k || k.under(m.k) || m.k.under(k) {
			o.errorf(pos, "%s é movida duas vezes na mesma chamada", k)
			return
		}
	}
	*o.pending = append(*o.pending, pendingMove{k, pos})
}

// movingRoot diz se algum argumento já avaliado da chamada move algo de root.
func (o *ownChecker) movingRoot(root *Local) bool {
	if o.pending == nil {
		return false
	}
	for _, m := range *o.pending {
		if m.k.root == root {
			return true
		}
	}
	return false
}
