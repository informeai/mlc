package ast

import "reflect"

var nodeType = reflect.TypeOf((*Node)(nil)).Elem()

// Inspect percorre a árvore a partir de n, em profundidade, e chama f para
// cada nó (comandos, expressões, padrões, tipos). Quando f devolve false, os
// filhos daquele nó não são visitados.
func Inspect(n Node, f func(Node) bool) {
	inspectValue(reflect.ValueOf(n), f)
}

func inspectValue(v reflect.Value, f func(Node) bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return
		}
		if v.Kind() == reflect.Pointer && v.Type().Implements(nodeType) {
			if !f(v.Interface().(Node)) {
				return
			}
		}
		inspectValue(v.Elem(), f)
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			inspectValue(v.Index(i), f)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			inspectValue(v.Field(i), f)
		}
	}
}
