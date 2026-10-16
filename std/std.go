// Package std embute a biblioteca padrão do MLC (arquivos .mlc) no binário do
// compilador. "import \"std/math\"" lê math.mlc daqui.
package std

import "embed"

//go:embed *.mlc
var FS embed.FS
