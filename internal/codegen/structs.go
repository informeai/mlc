package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Structs viram "struct mlc_st_<Nome>" com campos f_<nome>. O prefixo evita
// colisão com palavras reservadas do C (um campo pode se chamar default).
// Para cada struct também é gerada mlc_eq_<Nome> (==), em ordem de
// dependência: uma struct só usa as já definidas.

func structCType(s *checker.Struct) string { return "mlc_st_" + s.CName }

func (g *gen) structs() {
	for _, s := range g.info.Structs {
		g.raw("typedef struct %s %s;\n", structCType(s), structCType(s))
	}
	for _, a := range g.info.Arrays {
		g.raw("typedef struct %s %s;\n", arrayCType(a), arrayCType(a))
	}
	for _, e := range g.info.Enums {
		g.raw("typedef struct %s %s;\n", enumCType(e), enumCType(e))
	}
	if len(g.info.Structs)+len(g.info.Arrays)+len(g.info.Enums) > 0 {
		g.raw("\n")
	}
	// Os slices vêm antes das structs e dos arrays: uma struct pode ter campo
	// slice, e o slice só precisa do nome (typedef) do tipo dos elementos.
	g.sliceTypedefs()
	// Structs e arrays guardam uns aos outros por valor: cada um é definido
	// depois dos que ele contém.
	done := map[checker.Type]bool{}
	for _, s := range g.info.Structs {
		g.valueType(s, done)
	}
	for _, a := range g.info.Arrays {
		g.valueType(a, done)
	}
	for _, e := range g.info.Enums {
		g.valueType(e, done)
	}
}

// valueType define a struct ou o array t (e antes, os que t contém por valor),
// com as funções de igualdade e impressão.
func (g *gen) valueType(t checker.Type, done map[checker.Type]bool) {
	if done[t] {
		return
	}
	done[t] = true
	switch t := t.(type) {
	case *checker.Struct:
		for _, f := range t.Fields {
			if isValueType(f.Type) {
				g.valueType(f.Type, done)
			}
		}
		if !t.Extern { // tipo de C opaco: só o typedef, sem corpo
			g.structDef(t)
		}
	case *checker.Array:
		if isValueType(t.Elem) {
			g.valueType(t.Elem, done)
		}
		g.arrayDef(t)
	case *checker.Enum:
		for _, v := range t.Variants {
			for _, f := range v.Fields {
				if isValueType(f.Type) {
					g.valueType(f.Type, done)
				}
			}
		}
		g.enumDef(t)
	}
}

func (g *gen) structDef(s *checker.Struct) {
	g.file = s.File
	g.lineDirective(s.Decl.P)
	g.raw("struct %s {\n", structCType(s))
	for _, f := range s.Fields {
		g.raw("    %s f_%s;\n", cType(f.Type), f.Name)
	}
	if len(s.Fields) == 0 {
		// Struct vazia não é C padrão: um campo de preenchimento mantém
		// o tipo válido e "{0}" como valor zero.
		g.raw("    char mlc_empty;\n")
	}
	g.raw("};\n\n")
	if !s.Resource { // struct com ponteiros donos não é comparada
		g.structEq(s)
	}
}

func (g *gen) structEq(s *checker.Struct) {
	var conds []string
	for _, f := range s.Fields {
		a, b := "a.f_"+f.Name, "b.f_"+f.Name
		conds = append(conds, eqExpr(f.Type, a, b))
	}
	body := "true"
	if len(conds) > 0 {
		body = strings.Join(conds, " && ")
	}
	t := structCType(s)
	g.raw("static bool mlc_eq_%s(%s a, %s b) {\n    return %s;\n}\n\n", s.CName, t, t, body)
}

// eqExpr compara dois valores do tipo t em C.
func eqExpr(t checker.Type, a, b string) string {
	if en, ok := t.(*checker.Enum); ok {
		return fmt.Sprintf("mlc_eq_%s(%s, %s)", mangle(en), a, b)
	}
	if arr, ok := t.(*checker.Array); ok {
		return fmt.Sprintf("mlc_eq_%s(%s, %s)", mangle(arr), a, b)
	}
	switch t := t.(type) {
	case *checker.Struct:
		return fmt.Sprintf("mlc_eq_%s(%s, %s)", t.CName, a, b)
	}
	if t == checker.String {
		return fmt.Sprintf("mlc_string_eq(%s, %s)", a, b)
	}
	return fmt.Sprintf("%s == %s", a, b)
}

func (g *gen) composite(e *ast.CompositeLit) string {
	s := g.info.Types[e].(*checker.Struct)
	if len(e.Fields) == 0 {
		return zeroValue(s)
	}
	values := make([]ast.Expr, len(e.Fields))
	for i, fi := range e.Fields {
		values[i] = fi.Value
	}
	pre, vals := g.operands(values)
	inits := make([]string, len(e.Fields))
	for i, fi := range e.Fields {
		inits[i] = fmt.Sprintf(".f_%s = %s", fi.Name, vals[i])
	}
	return sequence(pre, fmt.Sprintf("((%s){%s})", structCType(s), strings.Join(inits, ", ")))
}
