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

// enumResult is the comparison of one size.
type enumResult struct {
	programs, skipped, agreeSafe, agreeUnsafe int
	holes                                     []string            // the checker accepts, the model says unsafe
	fp                                        map[string][]string // checker message → safe programs refused
}

// compareSize judges every program of size n.
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
					r.skipped++ // type error (r out of scope, r declared twice...)
				case err == nil && model == "":
					r.agreeSafe++
				case err != nil && model != "":
					r.agreeUnsafe++
				case err == nil:
					r.holes = append(r.holes, fmt.Sprintf("the model says: %s\n%s", model, render(ss, 1)))
				default:
					m := normalize.ReplaceAllString(strings.TrimPrefix(firstLine(err.Error()), "enum.mlc:"), "N")
					r.fp[m] = append(r.fp[m], firstLine(err.Error())+"\n"+render(ss, 1))
				}
				mu.Unlock()
			}
		}()
	}
	Enumerate(n, func(ss []*Stmt) { work <- ss }) // one at a time: size 6 has millions
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

// On every go test: every program up to size 3 (a few thousand). The
// checker can't accept any that the model says is unsafe.
func TestEnumerateSmall(t *testing.T) {
	for n := 1; n <= 3; n++ {
		r := compareSize(n)
		for _, h := range r.holes {
			t.Errorf("size %d: the checker accepted an unsafe program; %s", n, h)
		}
		t.Logf("size %d: %d programs, %d with a type error, %d safe accepted, %d unsafe refused, %d holes, %d false positives",
			n, r.programs, r.skipped, r.agreeSafe, r.agreeUnsafe, len(r.holes), r.fpCount())
	}
}

// TestEnumerate is make enum: every size up to MLC_ENUM (default 5),
// with the report of the holes and the catalog of false positives in
// MLC_ENUM_OUT (default /tmp/mlc-enum).
func TestEnumerate(t *testing.T) {
	s := os.Getenv("MLC_ENUM")
	if s == "" {
		t.Skip("run with MLC_ENUM=<size> (make enum)")
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
	fmt.Fprintf(&b, "\n%-8s %10s %10s %14s %16s %8s %16s\n", "size", "programs", "type error", "safe/accepted", "unsafe/refused", "holes", "false positives")
	fps := map[string][]string{}
	holes := 0
	var holeText strings.Builder
	for n := 1; n <= max; n++ {
		r := compareSize(n)
		fmt.Fprintf(&b, "%-8d %10d %10d %14d %16d %8d %16d\n", n, r.programs, r.skipped, r.agreeSafe, r.agreeUnsafe, len(r.holes), r.fpCount())
		for _, h := range r.holes {
			holes++
			fmt.Fprintf(&holeText, "\nHOLE (size %d): %s", n, h)
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
		fmt.Fprintf(&b, "\nFalse positives (safe programs the checker refuses), by message:\n")
		for i, k := range ks {
			fmt.Fprintf(&b, "\n%5d  %s\n", len(fps[k]), k)
			// the smallest example of each one
			ex := fps[k]
			sort.Slice(ex, func(a, c int) bool { return len(ex[a]) < len(ex[c]) })
			fmt.Fprintf(&b, "       example: %s\n", strings.ReplaceAll(strings.TrimSpace(ex[0]), "\n", "\n       "))
			file := filepath.Join(out, fmt.Sprintf("false_positive_%02d.txt", i+1))
			os.WriteFile(file, []byte(k+"\n\n"+strings.Join(ex, "\n---\n")), 0o644)
		}
	}
	fmt.Fprintf(&b, "\nexamples in %s\n", out)
	t.Log(b.String())
	os.WriteFile(filepath.Join(out, "report.txt"), []byte(b.String()), 0o644)
	if holes > 0 {
		t.Errorf("%d hole(s) in the checker", holes)
	}
}

// The model treats each r := &v as a borrow of its own: the r inside
// a block hides the outer one only until the end of the block (found in the enumeration
// of size 6, when the model had a single r and reported false holes).
func TestModelShadowedRef(t *testing.T) {
	a := func(s string) *Stmt { return &Stmt{Atom: s} }
	prog := []*Stmt{
		a("r := &x"),
		{If: true, Body: []*Stmt{a("r := &y"), a("acc += r.v")}},
		a("free(y)"),    // the outer r borrows x, not y
		a("acc += r.v"), // the outer r
		a("free(x)"),
		a("free(q)"),
	}
	if v := Verdict(prog); v != "" {
		t.Errorf("safe, but the model says: %s", v)
	}
	prog[2] = a("free(x)") // now yes: x is touched before the outer r's last use
	prog[4] = a("free(y)")
	if v := Verdict(prog); v == "" {
		t.Error("unsafe, but the model accepted")
	}
}
