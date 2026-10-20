package codegen

import (
	"fmt"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
)

// Structs become "struct mlc_st_<Name>" with fields f_<name>. The prefix avoids
// collisions with C keywords (a field can be called default).
// For each struct, mlc_eq_<Name> (==) is also generated, in
// dependency order: a struct only uses the ones already defined.

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
	// Slices come before structs and arrays: a struct can have a slice
	// field, and the slice only needs the name (typedef) of the element type.
	g.sliceTypedefs()
	// Structs and arrays hold each other by value: each one is defined
	// after the ones it contains.
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

// valueType defines the struct or array t (and before it, the ones t contains by value),
// with the equality and printing functions.
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
		if !t.Extern { // opaque C type: only the typedef, no body
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
		// An empty struct isn't standard C: a padding field keeps
		// the type valid and "{0}" as the zero value.
		g.raw("    char mlc_empty;\n")
	}
	g.raw("};\n\n")
	if !s.Resource { // a struct with owning pointers isn't compared
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

// eqExpr compares two values of type t in C.
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
