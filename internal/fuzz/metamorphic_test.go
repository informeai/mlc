package fuzz

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"

	"mlc/internal/driver"
)

// metaResult junta as divergências de uma rodada.
type metaResult struct {
	programs, variants int
	byKind             map[string]int      // divergências por transformação
	known              int                 // divergências em programas com empréstimo (o caso conhecido)
	unexpected         []string            // divergências sem empréstimo: inesperadas
	holes              []string            // a versão transformada é aceita e o modelo diz insegura
	modelDiff          []string            // o modelo deu vereditos diferentes: bug no modelo ou na transformação
	examples           map[string][]string // exemplos das divergências conhecidas, por transformação
}

func checkVerdict(ss []*Stmt) (accepted, typeErr bool) {
	_, _, err := driver.Check("enum.mlc", EnumSource(ss), noImports, driver.Options{})
	if err == nil {
		return true, false
	}
	return false, !ownershipError.MatchString(err.Error())
}

// metamorphic compara cada programa até o tamanho max com as versões
// transformadas.
func metamorphic(max int) *metaResult {
	r := &metaResult{byKind: map[string]int{}, examples: map[string][]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan []*Stmt)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ss := range work {
				ep := Epilogue(ss)
				full := append(append([]*Stmt{}, ss...), ep...)
				acc, terr := checkVerdict(full)
				if terr {
					continue
				}
				model := Verdict(full) == ""
				vs := Variants(ss)
				var local []func()
				for _, v := range vs {
					vf := append(append([]*Stmt{}, v.Prog...), ep...)
					vacc, vterr := checkVerdict(vf)
					vmodel := Verdict(vf) == ""
					v := v
					switch {
					case vmodel != model:
						local = append(local, func() {
							r.modelDiff = append(r.modelDiff, fmt.Sprintf("%s:\n%s---\n%s", v.Kind, render(full, 1), render(vf, 1)))
						})
					case vacc && !vmodel:
						local = append(local, func() {
							r.holes = append(r.holes, fmt.Sprintf("%s:\n%s", v.Kind, render(vf, 1)))
						})
					case vterr || vacc != acc:
						local = append(local, func() {
							r.byKind[v.Kind]++
							if hasRef(ss) && !vterr {
								r.known++
								if len(r.examples[v.Kind]) < 3 {
									r.examples[v.Kind] = append(r.examples[v.Kind], fmt.Sprintf("original aceito: %v\n%s--- %s:\n%s", acc, render(full, 1), v.Kind, render(vf, 1)))
								}
							} else {
								r.unexpected = append(r.unexpected, fmt.Sprintf("%s (original aceito: %v, erro de tipo: %v):\n%s---\n%s", v.Kind, acc, vterr, render(full, 1), render(vf, 1)))
							}
						})
					}
				}
				mu.Lock()
				r.programs++
				r.variants += len(vs)
				for _, f := range local {
					f()
				}
				mu.Unlock()
			}
		}()
	}
	for n := 1; n <= max; n++ {
		Enumerate(n, func(ss []*Stmt) { work <- ss })
	}
	close(work)
	wg.Wait()
	return r
}

// A cada go test: os programas até o tamanho 3 e as versões transformadas.
// O modelo precisa dar o mesmo veredito para as duas (a transformação não
// muda a segurança), o verificador não pode aceitar uma versão insegura, e
// ele só pode mudar de veredito em programas com empréstimo (o caso
// conhecido do empréstimo que vale até o último uso no texto).
func TestMetamorphic(t *testing.T) {
	max := 3
	if s := os.Getenv("MLC_META"); s != "" {
		max, _ = strconv.Atoi(s)
	}
	r := metamorphic(max)
	for _, d := range r.modelDiff {
		t.Errorf("o modelo mudou de veredito com a transformação %s", d)
	}
	for _, h := range r.holes {
		t.Errorf("o verificador aceitou uma versão insegura (%s)", h)
	}
	for _, u := range r.unexpected {
		t.Errorf("o verificador mudou de veredito sem empréstimo no programa: %s", u)
	}
	var kinds []string
	for k := range r.byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	t.Logf("até o tamanho %d: %d programas, %d versões, %d divergências conhecidas (empréstimo), por transformação: %v", max, r.programs, r.variants, r.known, r.byKind)
	if os.Getenv("MLC_META") != "" {
		for _, k := range kinds {
			for _, ex := range r.examples[k] {
				t.Logf("exemplo (%s):\n%s", k, ex)
			}
		}
	}
}
