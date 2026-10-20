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

// metaResult gathers the divergences of one round.
type metaResult struct {
	programs, variants int
	byKind             map[string]int      // divergences per transformation
	known              int                 // divergences in programs with a borrow (the known case)
	unexpected         []string            // divergences without a borrow: unexpected
	holes              []string            // the transformed version is accepted and the model says unsafe
	modelDiff          []string            // the model gave different verdicts: a bug in the model or in the transformation
	examples           map[string][]string // examples of the known divergences, per transformation
}

func checkVerdict(ss []*Stmt) (accepted, typeErr bool) {
	_, _, err := driver.Check("enum.mlc", EnumSource(ss), noImports, driver.Options{})
	if err == nil {
		return true, false
	}
	return false, !ownershipError.MatchString(err.Error())
}

// metamorphic compares each program up to size max with the
// transformed versions.
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
									r.examples[v.Kind] = append(r.examples[v.Kind], fmt.Sprintf("original accepted: %v\n%s--- %s:\n%s", acc, render(full, 1), v.Kind, render(vf, 1)))
								}
							} else {
								r.unexpected = append(r.unexpected, fmt.Sprintf("%s (original accepted: %v, type error: %v):\n%s---\n%s", v.Kind, acc, vterr, render(full, 1), render(vf, 1)))
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

// On every go test: the programs up to size 3 and the transformed versions.
// The model must give the same verdict for both (the transformation doesn't
// change safety), the checker can't accept an unsafe version, and
// it can only change its verdict in programs with a borrow (the
// known case of the borrow that lasts until its last textual use).
func TestMetamorphic(t *testing.T) {
	max := 3
	if s := os.Getenv("MLC_META"); s != "" {
		max, _ = strconv.Atoi(s)
	}
	r := metamorphic(max)
	for _, d := range r.modelDiff {
		t.Errorf("the model changed its verdict with the transformation %s", d)
	}
	for _, h := range r.holes {
		t.Errorf("the checker accepted an unsafe version (%s)", h)
	}
	for _, u := range r.unexpected {
		t.Errorf("the checker changed its verdict without a borrow in the program: %s", u)
	}
	var kinds []string
	for k := range r.byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	t.Logf("up to size %d: %d programs, %d versions, %d known divergences (borrow), per transformation: %v", max, r.programs, r.variants, r.known, r.byKind)
	if os.Getenv("MLC_META") != "" {
		for _, k := range kinds {
			for _, ex := range r.examples[k] {
				t.Logf("exemplo (%s):\n%s", k, ex)
			}
		}
	}
}
