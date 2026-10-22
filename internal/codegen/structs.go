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

func structCType(s *checker.Struct) string {
	if s.CStruct {
		return s.CName // extern "C" struct: the header's type
	}
	return "mlc_st_" + s.CName
}

// fieldCName is the C name of the field name of a value of type t (a struct,
// or a pointer or borrow of one): f_name, or C's own name in a C struct.
func fieldCName(t checker.Type, name string) string {
	if isCStructBase(t) {
		return name
	}
	return "f_" + name
}

// isCStructBase says whether t is (a pointer or borrow of) a C struct.
func isCStructBase(t checker.Type) bool {
	switch tt := t.(type) {
	case *checker.Pointer:
		t = tt.Elem
	case *checker.Borrow:
		t = tt.Elem
	}
	s, ok := t.(*checker.Struct)
	return ok && s.CStruct
}

// cFieldValue is the value written into the C struct field place (an lvalue)
// of type ft: a string's pointer (a literal, which lives as long as the
// program), a callback's C wrapper, or the value itself. The wrapper takes
// the context as the MLC type (Ticks *) where C's field says void *: the
// same pointer in the ABI, converted to the field's own type.
func (g *gen) cFieldValue(place string, ft checker.Type, v ast.Expr) string {
	switch ft.(type) {
	case *checker.FuncType:
		return fmt.Sprintf("(__typeof__(%s))%s", place, callbackName(g.info.FuncRefs[v]))
	}
	if ft == checker.String {
		return "(" + g.expr(v) + ").ptr"
	}
	return g.expr(v)
}

func (g *gen) structs() {
	for _, s := range g.info.Structs {
		if s.CStruct {
			continue // declared by its header
		}
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
		if t.CStruct {
			return // the header defines it
		}
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
	if s.CStruct {
		// C's struct: the fields named, the others zero
		values := make([]ast.Expr, len(e.Fields))
		for i, fi := range e.Fields {
			values[i] = fi.Value
		}
		pre, vals := g.operands(values)
		inits := make([]string, len(e.Fields))
		for i, fi := range e.Fields {
			switch ft := s.Field(fi.Name).Type; {
			case isFuncType(ft):
				place := fmt.Sprintf("((%s *)0)->%s", structCType(s), fi.Name) // only for __typeof__
				inits[i] = "." + fi.Name + " = " + g.cFieldValue(place, ft, fi.Value)
			case ft == checker.String:
				inits[i] = "." + fi.Name + " = (" + vals[i] + ").ptr"
			default:
				inits[i] = "." + fi.Name + " = " + vals[i]
			}
		}
		return sequence(pre, fmt.Sprintf("((%s){%s})", structCType(s), strings.Join(inits, ", ")))
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
