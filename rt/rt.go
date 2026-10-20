// Package rt embeds the C runtime in the mlc binary, so the compiler
// works without depending on external files.
package rt

import _ "embed"

//go:embed c/mlc_runtime.h
var Header string

//go:embed c/mlc_runtime.c
var Source string
