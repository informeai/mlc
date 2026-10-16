// Package rt embute o runtime em C no binário do mlc, para que o compilador
// funcione sem depender de arquivos externos.
package rt

import _ "embed"

//go:embed c/mlc_runtime.h
var Header string

//go:embed c/mlc_runtime.c
var Source string
