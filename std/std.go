// Package std embeds MLC's standard library (.mlc files) in the compiler's
// binary. "import \"std/math\"" reads math.mlc from here.
package std

import "embed"

//go:embed *.mlc
var FS embed.FS
