package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Enums viram "struct { int32_t tag; union { struct {...} v_Variante; ... } u; }":
// tag diz qual variante o valor é, e a union guarda os campos dela. O valor
// zero é a primeira variante com os campos zerados.

func enumCType(e *checker.Enum) string { return "mlc_en_" + e.CName }

func isEnum(t checker.Type) bool {
	_, ok := t.(*checker.Enum)
	return ok
}

func (g *gen) enumDef(e *checker.Enum) {
	t := enumCType(e)
	g.file = e.File
	g.lineDirective(e.Decl.P)
	g.raw("struct %s {\n    int32_t tag;\n    union {\n        char mlc_empty;\n", t)
	for _, v := range e.Variants {
		if len(v.Fields) == 0 {
			continue
		}
		g.raw("        struct {")
		for _, f := range v.Fields {
			g.raw(" %s f_%s;", cType(f.Type), f.Name)
		}
		g.raw(" } v_%s;\n", v.Name)
	}
	g.raw("    } u;\n};\n\n")

	if e.Resource {
		return // enum que guarda donos não é comparado com ==
	}
	// igualdade: mesma variante e mesmos campos
	g.raw("static bool mlc_eq_%s(%s x, %s y) {\n    if (x.tag != y.tag) return false;\n    switch (x.tag) {\n", mangle(e), t, t)
	for _, v := range e.Variants {
		if len(v.Fields) == 0 {
			continue
		}
		var conds []string
		for _, f := range v.Fields {
			field := fmt.Sprintf(".u.v_%s.f_%s", v.Name, f.Name)
			conds = append(conds, eqExpr(f.Type, "x"+field, "y"+field))
		}
		g.raw("    case %d: return %s;\n", v.Tag, strings.Join(conds, " && "))
	}
	g.raw("    }\n    return true;\n}\n\n")
}

// enumCtor traduz Forma.Circulo(r) ou Forma.Vazio.
func (g *gen) enumCtor(e ast.Expr, v *checker.Variant) string {
	t := enumCType(g.info.Types[e].(*checker.Enum))
	call, ok := e.(*ast.CallExpr)
	if !ok || len(v.Fields) == 0 {
		return fmt.Sprintf("((%s){.tag = %d})", t, v.Tag)
	}
	pre, vals := g.operands(call.Args)
	var inits []string
	for i, f := range v.Fields {
		inits = append(inits, fmt.Sprintf(".f_%s = %s", f.Name, vals[i]))
	}
	return sequence(pre, fmt.Sprintf("((%s){.tag = %d, .u.v_%s = {%s}})", t, v.Tag, v.Name, strings.Join(inits, ", ")))
}

// matchEnum traduz um match sobre enum: switch na tag; num braço com dados,
// cada nome recebe uma cópia do campo.
func (g *gen) matchEnum(s *ast.MatchStmt) {
	subject := g.expr(s.Subject)
	st := g.typeOf(s.Subject)
	if b, ok := st.(*checker.Borrow); ok {
		st, subject = b.Elem, "(*"+subject+")" // match e, com e &Enum
	}
	en := st.(*checker.Enum)
	tmp := g.newName("mlc_subject")
	g.line("{")
	g.indent++
	g.line("%s %s = %s;", enumCType(en), tmp, subject)
	hasWildcard := false
	for _, arm := range s.Arms {
		for _, p := range arm.Patterns {
			hasWildcard = hasWildcard || p.Wildcard
		}
	}
	g.line("switch (%s.tag) {", tmp)
	g.switches++
	for i, arm := range s.Arms {
		var labels []string
		var binds *ast.Pattern
		for _, p := range arm.Patterns {
			if p.Wildcard {
				labels = append(labels, "default:")
				continue
			}
			v := g.info.Patterns[p].(*checker.Variant)
			labels = append(labels, fmt.Sprintf("case %d:", v.Tag))
			if len(p.Binds) > 0 {
				binds = p
			}
		}
		if i == len(s.Arms)-1 && !hasWildcard {
			// o checker garante que todas as variantes estão cobertas: sem
			// default, o C acharia que a função pode terminar sem return
			labels = append(labels, "default:")
		}
		g.line("%s {", strings.Join(labels, " "))
		g.indent++
		if binds != nil {
			v := g.info.Patterns[binds].(*checker.Variant)
			for i, l := range g.info.PatternBinds[binds] {
				if l == nil {
					continue
				}
				field := fmt.Sprintf("%s.u.v_%s.f_%s", tmp, v.Name, v.Fields[i].Name)
				switch {
				case g.info.BindBorrows[l] && isPointer(v.Fields[i].Type):
					// campo ?*X emprestado como &X: o objeto apontado (nil dá panic)
					g.line("%s %s = MLC_NONNULL(%s);", cType(l.Type), l.CName, field)
				case g.info.BindBorrows[l]:
					g.line("%s %s = &%s;", cType(l.Type), l.CName, field)
				default:
					g.line("%s %s = %s;", cType(l.Type), l.CName, field)
				}
			}
		}
		g.indent--
		g.armBody(arm.Body)
		g.indent++
		g.line("break;")
		g.indent--
		g.line("}")
	}
	g.switches--
	g.line("}")
	g.indent--
	g.line("}")
}

// isValueType diz se t é guardado por valor e precisa estar definido antes de
// quem o contém: struct, array ou enum.
func isValueType(t checker.Type) bool {
	return isStruct(t) || isArray(t) || isEnum(t)
}
