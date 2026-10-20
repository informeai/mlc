package fuzz

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"mlc/internal/driver"
)

const small = `import "std/io"
import "std/vec"

struct No {
    v int
}

fn soma(n &No) int {
    return n.v
}

fn guarda(v &vec.Vec[[]byte], n []byte) {
    v.push(n)
}

fn main() {
    nos := vec.new[[]byte](4)
    a := alloc(No{v: 1})
    io.print(soma(&a))
    free(a)
    b := alloc([]byte, 2)
    guarda(&nos, b)
    buf := alloc([]byte, 8)
    buf[0] = 1
    io.print(len(buf))
    free(buf)
    nos.free()
}
`

// Each mutation of the small program creates a bug the checker refuses.
func TestMutantsSmall(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	f, info, err := driver.Check("small.mlc", []byte(small), read, driver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ms := Mutants([]byte(small), f, info)
	kinds := map[string]int{}
	for _, m := range ms {
		kinds[m.Kind]++
		if _, _, err := driver.Check("small.mlc", m.Src, read, driver.Options{}); err == nil {
			t.Errorf("o verificador aceitou o mutante %q:\n%s", m.Desc, m.Src)
		}
	}
	for _, k := range []string{DropRelease, DupRelease, EarlyRelease, UseAfter, CondRelease, DupMove, BorrowToMove} {
		if kinds[k] == 0 {
			t.Errorf("nenhum mutante %q; gerados: %v", k, kinds)
		}
	}
}

// TestMutate is make mutate: it mutates the programs of tests/run, tests/panic,
// tests/ffi and the std's tests, and fails if some accepted mutant errors under
// the oracle. It only runs with MLC_MUTATE=1 (it takes minutes).
func TestMutate(t *testing.T) {
	if os.Getenv("MLC_MUTATE") == "" {
		t.Skip("run with MLC_MUTATE=1 (make mutate)")
	}
	targets := allTargets(t)
	if only := os.Getenv("MLC_MUTATE_ONLY"); only != "" {
		var keep []*Target
		for _, tg := range targets {
			if strings.Contains(tg.Path, only) {
				keep = append(keep, tg)
			}
		}
		targets = keep
	}
	var mu sync.Mutex
	var all []Result
	byTarget := map[string][]Result{}
	t.Run("alvos", func(t *testing.T) {
		for _, tg := range targets {
			t.Run(filepath.Base(tg.Path), func(t *testing.T) {
				t.Parallel()
				rs, err := tg.Mutate(t.TempDir())
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				all = append(all, rs...)
				byTarget[tg.Path] = rs
				mu.Unlock()
			})
		}
	})
	report(t, targets, byTarget, all)
}

// report writes the summary per mutation, the accepted ones without a bug (for review) and
// the holes, each one in a file to reproduce.
func report(t *testing.T, targets []*Target, byTarget map[string][]Result, all []Result) {
	count := map[string]map[Outcome]int{}
	for _, r := range all {
		if count[r.Kind] == nil {
			count[r.Kind] = map[Outcome]int{}
		}
		count[r.Kind][r.Outcome]++
	}
	var kinds []string
	for k := range count {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var b strings.Builder
	fmt.Fprintf(&b, "\n%d programs, %d mutants\n\n", len(targets), len(all))
	fmt.Fprintf(&b, "%-24s %9s %15s %8s\n", "mutation", "refused", "accepted w/o bug", "holes")
	total := map[Outcome]int{}
	for _, k := range kinds {
		c := count[k]
		fmt.Fprintf(&b, "%-24s %9d %15d %8d\n", k, c[Rejected], c[Benign], c[Hole])
		for o, n := range c {
			total[o] += n
		}
	}
	fmt.Fprintf(&b, "%-24s %9d %15d %8d\n", "total", total[Rejected], total[Benign], total[Hole])

	out := os.Getenv("MLC_MUTATE_OUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "mlc-mutate")
	}
	os.RemoveAll(out)
	var paths []string
	for p := range byTarget {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	holes, accepted := 0, 0
	var benign strings.Builder
	for _, p := range paths {
		for _, r := range byTarget[p] {
			switch r.Outcome {
			case Benign:
				accepted++
				file := filepath.Join(out, fmt.Sprintf("accepted%d_%s", accepted, filepath.Base(p)))
				os.MkdirAll(out, 0o755)
				os.WriteFile(file, r.Src, 0o644)
				fmt.Fprintf(&benign, "  %s:%d: %s\n      %s\n", p, r.Line, r.Desc, file)
			case Hole:
				holes++
				file := filepath.Join(out, fmt.Sprintf("hole%d_%s", holes, filepath.Base(p)))
				os.MkdirAll(out, 0o755)
				os.WriteFile(file, r.Src, 0o644)
				fmt.Fprintf(&b, "\nBURACO %s:%d: %s\n    %s\n    mutante: %s\n", p, r.Line, r.Desc, r.Detail, file)
			}
		}
	}
	if benign.Len() > 0 {
		fmt.Fprintf(&b, "\nAccepted without a bug (the mutation didn't create a bug there; review now and then):\n%s", benign.String())
	}
	if os.Getenv("MLC_MUTATE_REJECTED") != "" {
		fmt.Fprintf(&b, "\nRecusados:\n")
		for _, p := range paths {
			for _, r := range byTarget[p] {
				if r.Outcome == Rejected {
					fmt.Fprintf(&b, "  %s:%d: %s\n      %s\n", p, r.Line, r.Desc, r.Detail)
				}
			}
		}
	}
	t.Log(b.String())
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "report.txt"), []byte(b.String()), 0o644)
	if holes > 0 {
		t.Errorf("%d hole(s) in the checker: see the mutants in %s", holes, out)
	}
}

const testsDir = "../../tests"

// allTargets lists the programs to mutate.
func allTargets(t *testing.T) []*Target {
	var out []*Target
	for _, kind := range []string{"run", "panic"} {
		files, _ := filepath.Glob(filepath.Join(testsDir, kind, "*.mlc"))
		multi, _ := filepath.Glob(filepath.Join(testsDir, kind, "*", "main.mlc"))
		for _, path := range append(files, multi...) {
			tg := &Target{Path: path, Name: filepath.Base(path), Read: dirLoader(path)}
			base := strings.TrimSuffix(path, ".mlc")
			if b, err := os.ReadFile(base + ".args"); err == nil {
				tg.Args = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			}
			if b, err := os.ReadFile(base + ".in"); err == nil {
				tg.Stdin = b
			}
			out = append(out, tg)
		}
	}
	tests, _ := filepath.Glob("../../std/*_test.mlc")
	for _, path := range tests {
		out = append(out, &Target{Path: path, Name: "std/" + filepath.Base(path), Read: os.ReadFile, Test: true})
	}
	ffi := filepath.Join(testsDir, "ffi", "out_handle.mlc")
	out = append(out, &Target{Path: ffi, Name: filepath.Base(ffi), Read: dirLoader(ffi), Opts: driver.Options{LibDirs: []string{counterLib(t)}}})
	return out
}

func dirLoader(path string) driver.Loader {
	dir := filepath.Dir(path)
	return func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, name))
	}
}

// counterLib compiles tests/ffi/counter.c into a libcounter.a (with
// AddressSanitizer, like the mutants) and returns the folder.
func counterLib(t *testing.T) string {
	t.Helper()
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	dir := t.TempDir()
	obj := filepath.Join(dir, "counter.o")
	if out, err := exec.Command(cc, "-fsanitize=address,undefined", "-c", "-o", obj, filepath.Join(testsDir, "ffi", "counter.c")).CombinedOutput(); err != nil {
		t.Fatalf("compilando counter.c: %v\n%s", err, out)
	}
	if out, err := exec.Command("ar", "rcs", filepath.Join(dir, "libcounter.a"), obj).CombinedOutput(); err != nil {
		t.Fatalf("ar: %v\n%s", err, out)
	}
	return dir
}
