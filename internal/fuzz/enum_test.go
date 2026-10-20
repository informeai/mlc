package fuzz

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"mlc/internal/driver"
)

// enumResult é a comparação de um tamanho.
type enumResult struct {
	programs, skipped, agreeSafe, agreeUnsafe int
	holes                                     []string            // o verificador aceita, o modelo diz inseguro
	fp                                        map[string][]string // mensagem do verificador → programas seguros recusados
}

// compareSize julga todos os programas de tamanho n.
func compareSize(n int) *enumResult {
	r := &enumResult{fp: map[string][]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan []*Stmt)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ss := range work {
				ss = append(append([]*Stmt{}, ss...), Epilogue(ss)...)
				src := EnumSource(ss)
				_, _, err := driver.Check("enum.mlc", src, noImports, driver.Options{})
				model := Verdict(ss)
				mu.Lock()
				r.programs++
				switch {
				case err != nil && !ownershipError.MatchString(err.Error()):
					r.skipped++ // erro de tipo (r fora do escopo, r declarada duas vezes...)
				case err == nil && model == "":
					r.agreeSafe++
				case err != nil && model != "":
					r.agreeUnsafe++
				case err == nil:
					r.holes = append(r.holes, fmt.Sprintf("o modelo diz: %s\n%s", model, render(ss, 1)))
				default:
					m := normalize.ReplaceAllString(strings.TrimPrefix(firstLine(err.Error()), "enum.mlc:"), "N")
					r.fp[m] = append(r.fp[m], firstLine(err.Error())+"\n"+render(ss, 1))
				}
				mu.Unlock()
			}
		}()
	}
	Enumerate(n, func(ss []*Stmt) { work <- ss }) // um por vez: o tamanho 6 tem milhões
	close(work)
	wg.Wait()
	return r
}

func (r *enumResult) fpCount() int {
	n := 0
	for _, v := range r.fp {
		n += len(v)
	}
	return n
}

// A cada go test: todos os programas até o tamanho 3 (uns milhares). O
// verificador não pode aceitar nenhum que o modelo diz inseguro.
func TestEnumerateSmall(t *testing.T) {
	for n := 1; n <= 3; n++ {
		r := compareSize(n)
		for _, h := range r.holes {
			t.Errorf("tamanho %d: o verificador aceitou um programa inseguro; %s", n, h)
		}
		t.Logf("tamanho %d: %d programas, %d com erro de tipo, %d seguros aceitos, %d inseguros recusados, %d buracos, %d falsos positivos",
			n, r.programs, r.skipped, r.agreeSafe, r.agreeUnsafe, len(r.holes), r.fpCount())
	}
}

// TestEnumerate é o make enum: todos os tamanhos até MLC_ENUM (padrão 5),
// com o relatório dos buracos e o catálogo dos falsos positivos em
// MLC_ENUM_OUT (padrão /tmp/mlc-enum).
func TestEnumerate(t *testing.T) {
	s := os.Getenv("MLC_ENUM")
	if s == "" {
		t.Skip("rode com MLC_ENUM=<tamanho> (make enum)")
	}
	max, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("MLC_ENUM_OUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "mlc-enum")
	}
	os.RemoveAll(out)
	os.MkdirAll(out, 0o755)
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-8s %10s %10s %14s %16s %8s %16s\n", "tamanho", "programas", "erro tipo", "seguros/aceitos", "inseguros/recus.", "buracos", "falsos positivos")
	fps := map[string][]string{}
	holes := 0
	var holeText strings.Builder
	for n := 1; n <= max; n++ {
		r := compareSize(n)
		fmt.Fprintf(&b, "%-8d %10d %10d %14d %16d %8d %16d\n", n, r.programs, r.skipped, r.agreeSafe, r.agreeUnsafe, len(r.holes), r.fpCount())
		for _, h := range r.holes {
			holes++
			fmt.Fprintf(&holeText, "\nBURACO (tamanho %d): %s", n, h)
		}
		for k, v := range r.fp {
			fps[k] = append(fps[k], v...)
		}
	}
	b.WriteString(holeText.String())
	if len(fps) > 0 {
		var ks []string
		for k := range fps {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool { return len(fps[ks[i]]) > len(fps[ks[j]]) })
		fmt.Fprintf(&b, "\nFalsos positivos (programas seguros que o verificador recusa), pela mensagem:\n")
		for i, k := range ks {
			fmt.Fprintf(&b, "\n%5d  %s\n", len(fps[k]), k)
			// o menor exemplo de cada um
			ex := fps[k]
			sort.Slice(ex, func(a, c int) bool { return len(ex[a]) < len(ex[c]) })
			fmt.Fprintf(&b, "       exemplo: %s\n", strings.ReplaceAll(strings.TrimSpace(ex[0]), "\n", "\n       "))
			file := filepath.Join(out, fmt.Sprintf("falso_positivo_%02d.txt", i+1))
			os.WriteFile(file, []byte(k+"\n\n"+strings.Join(ex, "\n---\n")), 0o644)
		}
	}
	fmt.Fprintf(&b, "\nexemplos em %s\n", out)
	t.Log(b.String())
	os.WriteFile(filepath.Join(out, "relatorio.txt"), []byte(b.String()), 0o644)
	if holes > 0 {
		t.Errorf("%d buraco(s) no verificador", holes)
	}
}

// O modelo trata cada r := &v como um empréstimo próprio: o r de dentro de
// um bloco esconde o de fora só até o fim do bloco (achado na enumeração
// do tamanho 6, quando o modelo tinha um r só e acusava buracos falsos).
func TestModelShadowedRef(t *testing.T) {
	a := func(s string) *Stmt { return &Stmt{Atom: s} }
	prog := []*Stmt{
		a("r := &x"),
		{If: true, Body: []*Stmt{a("r := &y"), a("acc += r.v")}},
		a("free(y)"),    // o r de fora empresta x, não y
		a("acc += r.v"), // o r de fora
		a("free(x)"),
		a("free(q)"),
	}
	if v := Verdict(prog); v != "" {
		t.Errorf("seguro, mas o modelo diz: %s", v)
	}
	prog[2] = a("free(x)") // agora sim: x é tocada antes do último uso do r de fora
	prog[4] = a("free(y)")
	if v := Verdict(prog); v == "" {
		t.Error("inseguro, mas o modelo aceitou")
	}
}
