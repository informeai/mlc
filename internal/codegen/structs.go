package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Structs viram "struct mlc_st_<Nome>" com campos f_<nome>. O prefixo evita
// colisão com palavras reservadas do C (um campo pode se chamar default).
// Para cada struct também são geradas mlc_eq_<Nome> (==) e mlc_print_<Nome>
// (io.print), em ordem de dependência: uma struct só usa as já definidas.

func structCType(s *checker.Struct) string { return "mlc_st_" + s.CName }

func (g *gen) structs() {
	for _, s := range g.info.Structs {
		g.raw("typedef struct %s %s;\n", structCType(s), structCType(s))
	}
	for _, a := range g.info.Arrays {
		g.raw("typedef struct %s %s;\n", arrayCType(a), arrayCType(a))
	}
	if len(g.info.Structs)+len(g.info.Arrays) > 0 {
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
	g.slicePrints()
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
			if isStruct(f.Type) || isArray(f.Type) {
				g.valueType(f.Type, done)
			}
		}
		g.structDef(t)
	case *checker.Array:
		if isStruct(t.Elem) || isArray(t.Elem) {
			g.valueType(t.Elem, done)
		}
		g.arrayDef(t)
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
	if !s.Resource { // struct com ponteiros donos não é comparada nem impressa
		g.structEq(s)
		g.structPrint(s)
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

// structPrint imprime no formato {campo1 campo2}, como o %v do Go.
func (g *gen) structPrint(s *checker.Struct) {
	g.raw("static void mlc_print_%s(%s v) {\n", s.CName, structCType(s))
	g.raw("    mlc_io_print_str(MLC_STR_LIT(\"{\", 1));\n")
	for i, f := range s.Fields {
		if i > 0 {
			g.raw("    mlc_io_print_sep();\n")
		}
		g.raw("    %s;\n", printCall(f.Type, "v.f_"+f.Name))
	}
	g.raw("    mlc_io_print_str(MLC_STR_LIT(\"}\", 1));\n}\n\n")
}

// eqExpr compara dois valores do tipo t em C.
func eqExpr(t checker.Type, a, b string) string {
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

// printCall imprime um valor do tipo t, sem separador nem quebra de linha.
func printCall(t checker.Type, v string) string {
	if a, ok := t.(*checker.Array); ok {
		return fmt.Sprintf("mlc_print_%s(%s)", mangle(a), v)
	}
	if s, ok := printNumber(t, v); ok {
		return s
	}
	switch t := t.(type) {
	case *checker.Slice:
		return fmt.Sprintf("mlc_print_%s(%s)", sliceCType(t), v)
	case *checker.Struct:
		return fmt.Sprintf("mlc_print_%s(%s)", t.CName, v)
	}
	switch t {
	case checker.Int:
		return "mlc_io_print_int(" + v + ")"
	case checker.Byte:
		return "mlc_io_print_int((int64_t)(" + v + "))"
	case checker.Bool:
		return "mlc_io_print_bool(" + v + ")"
	case checker.String:
		return "mlc_io_print_str(" + v + ")"
	}
	panic(fmt.Sprintf("codegen: tipo sem impressão: %s", t))
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
