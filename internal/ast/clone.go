package ast

import "reflect"

// Clone devolve uma cópia profunda de uma árvore sintática. Cada instância de
// uma função genérica é checada e gerada a partir da própria cópia, porque as
// informações do checker são guardadas por nó.
func Clone[T any](n T) T {
	return cloneValue(reflect.ValueOf(n)).Interface().(T)
}

func cloneValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		c := reflect.New(v.Elem().Type())
		c.Elem().Set(cloneValue(v.Elem()))
		return c
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		c := reflect.New(v.Type()).Elem()
		c.Set(cloneValue(v.Elem()))
		return c
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			c.Index(i).Set(cloneValue(v.Index(i)))
		}
		return c
	case reflect.Struct:
		c := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			c.Field(i).Set(cloneValue(v.Field(i)))
		}
		return c
	}
	return v
}
