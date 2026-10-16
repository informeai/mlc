package checker

import "mlc/internal/ast"

// Texto (Marco 7)
//
// Texto é montado num buffer []byte que quem programa aloca com alloc e
// cresce com realloc. As funções de texto e de entrada e saída nunca alocam:
// escrevem no buffer emprestado, devolvem quantos bytes usaram e dão panic se
// não couber. Assim toda alocação de texto aparece no código.
//
// As funções nativas são declaradas como funções comuns (com parâmetros &T),
// então a checagem de tipos e o verificador de posse valem para elas sem
// regra especial. No C, viram chamadas ao runtime com arquivo e linha no fim,
// para os panics apontarem para o .mlc.

// nativeSig descreve uma função nativa: parâmetros e retorno, com "bytes"
// para &[]byte, "int" para int, "string", "bool", "&int" e "" para nada.
type nativeSig struct {
	name   string
	cname  string
	params []string
	result string
}

var nativeSigs = map[string][]nativeSig{
	"str": {
		{"copy", "mlc_str_copy", []string{"bytes", "string"}, "int"},
		{"copy_bytes", "mlc_str_copy_bytes", []string{"bytes", "bytes"}, "int"},
		{"put_int", "mlc_str_put_int", []string{"bytes", "int"}, "int"},
		{"int_len", "mlc_str_int_len", []string{"int"}, "int"},
		{"eq", "mlc_str_eq", []string{"bytes", "string"}, "bool"},
		{"eq_bytes", "mlc_str_eq_bytes", []string{"bytes", "bytes"}, "bool"},
		{"parse_int", "mlc_str_parse_int", []string{"bytes", "&int"}, "bool"},
		{"put_float", "mlc_str_put_float", []string{"bytes", "float64"}, "int"},
		{"parse_float", "mlc_str_parse_float", []string{"bytes", "&float64"}, "bool"},
	},
	"io": {
		{"write", "mlc_io_write", []string{"bytes"}, ""},
		{"write_line", "mlc_io_write_line", []string{"bytes"}, ""},
		{"read_line", "mlc_io_read_line", []string{"bytes"}, "int"},
	},
}

// paramNames dá nomes legíveis aos parâmetros das funções nativas, usados nas
// mensagens de erro.
var paramNames = []string{"a", "b", "c", "d"}

// nativeFunc devolve a função nativa module.name, ou nil.
func (c *checker) nativeFunc(module, name string) *Func {
	key := module + "." + name
	if fn, ok := c.natives[key]; ok {
		return fn
	}
	for _, sig := range nativeSigs[module] {
		if sig.name != name {
			continue
		}
		fn := &Func{Name: key, Native: sig.cname, Result: c.nativeType(sig.result)}
		for i, p := range sig.params {
			fn.Params = append(fn.Params, &Local{Name: paramNames[i], Type: c.nativeType(p), Param: true})
		}
		c.natives[key] = fn
		return fn
	}
	return nil
}

func (c *checker) nativeType(s string) Type {
	switch s {
	case "bytes":
		return c.borrowOf(c.sliceOf(Byte))
	case "&int":
		return c.borrowOf(Int)
	case "&float64":
		return c.borrowOf(Float64)
	case "float64":
		return Float64
	case "int":
		return Int
	case "bool":
		return Bool
	case "string":
		return String
	case "":
		return Void
	}
	panic("checker: tipo nativo desconhecido " + s)
}

func (c *checker) moduleFunc(e *ast.CallExpr, module, name string) (Type, bool) {
	fn := c.nativeFunc(module, name)
	if fn == nil {
		return nil, false
	}
	return c.userCall(e, fn), true
}
