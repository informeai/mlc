package fuzz

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mlc/internal/driver"
)

func noImports(string) ([]byte, error) { return nil, os.ErrNotExist }

// checkGen passes the program through the checker.
func checkGen(p *Program) error {
	_, _, err := driver.Check("generated.mlc", p.Source(), noImports, driver.Options{})
	return err
}

// typeError says whether the error isn't about ownership: the generator wrote a malformed
// program (the checker didn't even get to judge ownership).
var ownershipError = regexp.MustCompile(`freed|moved|borrowed|own(s)? (the )?memory|leak|nil here|without free`)

// On every go test: the generator writes well-formed programs, the checker
// refuses all the ones with an injected bug and accepts almost all the valid ones.
func TestGenerate(t *testing.T) {
	accepted, valid := 0, 0
	for seed := int64(1); seed <= 300; seed++ {
		p := Generate(seed, false)
		valid++
		if err := checkGen(p); err == nil {
			accepted++
		} else if !ownershipError.MatchString(err.Error()) {
			t.Errorf("seed %d: malformed valid program: %v", seed, firstLine(err.Error()))
		}
		b := Generate(seed, true)
		err := checkGen(b)
		switch {
		case err == nil:
			t.Errorf("seed %d: the checker accepted the injected bug (%s):\n%s", seed, b.Bug, b.Source())
		case !ownershipError.MatchString(err.Error()):
			t.Errorf("seed %d: malformed program with a bug: %v", seed, firstLine(err.Error()))
		}
	}
	if accepted*100 < valid*97 {
		t.Errorf("the checker accepted only %d of %d valid programs", accepted, valid)
	}
}

// TestReduce: the reducer gets to a small program that still has the bug.
func TestReduce(t *testing.T) {
	for seed := int64(1); seed <= 50; seed++ {
		p := Generate(seed, true)
		if p.Lines = count(p.Body); p.Lines < 15 {
			continue
		}
		r := Reduce(p, func(q *Program) bool { return checkGen(q) != nil && hasBugNode(q.Body) })
		if !hasBugNode(r.Body) || count(r.Body) >= count(p.Body) {
			t.Errorf("seed %d: reduction from %d to %d statements", seed, count(p.Body), count(r.Body))
		}
		return
	}
	t.Skip("no large seed")
}

func count(ns []*Node) int {
	n := 0
	for _, x := range ns {
		n += 1 + count(x.Body) + count(x.Else)
	}
	return n
}

func hasBugNode(ns []*Node) bool {
	for _, x := range ns {
		if x.Bug || hasBugNode(x.Body) || hasBugNode(x.Else) {
			return true
		}
	}
	return false
}

// TestFuzz is make fuzz: it generates programs for FUZZTIME (default 1m), half
// valid and half with a bug, and checks each one. It only runs with MLC_FUZZ=1.
//
//   - with a bug and accepted: a hole (the oracle confirms, and the program is reduced);
//   - valid, accepted and with a run-time error: a hole (in the C generator or in the
//     runtime, or in the checker);
//   - valid and refused: a false-positive candidate, grouped by message.
//
// MLC_FUZZ_SEED picks the first seed (to reproduce: MLC_FUZZ_SEED=n
// MLC_FUZZ_COUNT=1); MLC_FUZZ_OUT, the findings folder (default /tmp/mlc-fuzz).
func TestFuzz(t *testing.T) {
	if os.Getenv("MLC_FUZZ") == "" {
		t.Skip("run with MLC_FUZZ=1 (make fuzz)")
	}
	dur := time.Minute
	if s := os.Getenv("FUZZTIME"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		dur = d
	}
	start := time.Now().UnixNano() % 1_000_000_000
	if s := os.Getenv("MLC_FUZZ_SEED"); s != "" {
		start, _ = strconv.ParseInt(s, 10, 64)
	}
	limit := int64(-1)
	if s := os.Getenv("MLC_FUZZ_COUNT"); s != "" {
		limit, _ = strconv.ParseInt(s, 10, 64)
	}
	out := os.Getenv("MLC_FUZZ_OUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "mlc-fuzz")
	}
	os.RemoveAll(out)
	os.MkdirAll(out, 0o755)
	work := t.TempDir()

	var next atomic.Int64
	next.Store(start)
	deadline := time.Now().Add(dur)
	var mu sync.Mutex
	st := &fuzzStats{fp: map[string]int{}, fpSeed: map[string]int64{}}
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			exe := filepath.Join(work, "w"+strconv.Itoa(w))
			for time.Now().Before(deadline) {
				seed := next.Add(1) - 1
				if limit >= 0 && seed >= start+limit {
					return
				}
				for _, withBug := range []bool{false, true} {
					f := fuzzOne(seed, withBug, exe)
					mu.Lock()
					st.add(f, out)
					mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()
	rep := st.report(start, next.Load()-1, out)
	t.Log(rep)
	os.WriteFile(filepath.Join(out, "report.txt"), []byte(rep), 0o644)
	if st.holes > 0 {
		t.Errorf("%d hole(s): see %s", st.holes, out)
	}
}

// finding is the result of one program.
type finding struct {
	p       *Program
	checker error
	oracle  string // the run-time error ("" if it ran clean, or didn't run)
	ran     bool   // ran under the oracle
	hole    bool
	reduced *Program
}

// fuzzOne generates, checks and (on a hole) reduces a program.
func fuzzOne(seed int64, withBug bool, exe string) finding {
	p := Generate(seed, withBug)
	f := finding{p: p, checker: checkGen(p)}
	if f.checker != nil {
		return f
	}
	// accepted. A program with a bug always runs under the oracle; a valid one, only
	// 1 in 10 (looks for bugs in the C generator and in the runtime; the checker's
	// holes show up in the programs with a bug, which only compile when
	// they're accepted)
	if !withBug && seed%10 != 0 {
		return f
	}
	f.ran = true
	f.oracle = runGen(p, exe)
	if f.oracle == "" && !withBug {
		return f
	}
	f.hole = true
	keep := func(q *Program) bool {
		if checkGen(q) != nil {
			return false
		}
		if f.oracle == "" {
			return hasBugNode(q.Body) // the oracle didn't see it: keeps the statement with the bug
		}
		return runGen(q, exe) != ""
	}
	f.reduced = Reduce(p, keep)
	return f
}

// runGen compiles the program with AddressSanitizer and runs it, returning what the
// oracle found ("" if nothing).
func runGen(p *Program, exe string) string {
	opts := driver.Options{Sanitize: "address"}
	if err := driver.BuildOpts("generated.mlc", p.Source(), exe, noImports, opts); err != nil {
		return "accepted, but the C doesn't compile: " + firstLine(err.Error())
	}
	t := &Target{}
	return t.oracle(exe, nil)
}

type fuzzStats struct {
	programs, valid, validOK, validRan, bugs, bugsRejected, holes int
	fp                                                            map[string]int // normalized message → how many
	fpSeed                                                        map[string]int64
	holeLines                                                     []string
}

var normalize = regexp.MustCompile(`\b[a-z]+[0-9]+\b|[0-9]+`)

func (s *fuzzStats) add(f finding, out string) {
	s.programs++
	if f.p.Bug == "" {
		s.valid++
		if f.checker == nil && !f.hole {
			s.validOK++
		}
		if f.ran {
			s.validRan++
		}
		if f.checker != nil {
			m := normalize.ReplaceAllString(strings.TrimPrefix(firstLine(f.checker.Error()), "generated.mlc:"), "N")
			if s.fp[m] == 0 {
				s.fpSeed[m] = f.p.Seed
			}
			s.fp[m]++
		}
	} else {
		s.bugs++
		if f.checker != nil {
			s.bugsRejected++
		}
	}
	if !f.hole {
		return
	}
	s.holes++
	what := "valid"
	if f.p.Bug != "" {
		what = "bug " + f.p.Bug
	}
	detail := f.oracle
	if detail == "" {
		detail = "the checker accepted, and execution didn't show the bug"
	}
	file := filepath.Join(out, fmt.Sprintf("finding_%d.mlc", f.p.Seed))
	if f.p.Bug != "" {
		file = filepath.Join(out, fmt.Sprintf("finding_%d_bug.mlc", f.p.Seed))
	}
	src := f.reduced.Source()
	src = append([]byte(fmt.Sprintf("// finding: %s\n", detail)), src...)
	os.WriteFile(file, src, 0o644)
	s.holeLines = append(s.holeLines, fmt.Sprintf("HOLE seed %d (%s): %s\n    reduced from %d to %d statements: %s", f.p.Seed, what, detail, count(f.p.Body), count(f.reduced.Body), file))
}

func (s *fuzzStats) report(first, last int64, out string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nseeds %d to %d: %d programs\n", first, last, s.programs)
	fmt.Fprintf(&b, "  valid:         %d, accepted %d (%d ran under the oracle), refused %d\n", s.valid, s.validOK, s.validRan, s.valid-s.validOK-countValidHoles(s))
	fmt.Fprintf(&b, "  with a bug:    %d, refused %d\n", s.bugs, s.bugsRejected)
	fmt.Fprintf(&b, "  holes:         %d\n", s.holes)
	for _, l := range s.holeLines {
		fmt.Fprintf(&b, "\n%s\n", l)
	}
	if len(s.fp) > 0 {
		fmt.Fprintf(&b, "\nRefused valid ones (false-positive candidates; MLC_FUZZ_SEED=<seed> MLC_FUZZ_COUNT=1 reproduces):\n")
		var ks []string
		for k := range s.fp {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool { return s.fp[ks[i]] > s.fp[ks[j]] })
		for _, k := range ks {
			fmt.Fprintf(&b, "  %5d  (seed %d) %s\n", s.fp[k], s.fpSeed[k], k)
		}
	}
	fmt.Fprintf(&b, "\nfindings in %s\n", out)
	return b.String()
}

func countValidHoles(s *fuzzStats) int {
	n := 0
	for _, l := range s.holeLines {
		if strings.Contains(l, "(valid)") {
			n++
		}
	}
	return n
}

// TestDump writes the program of a seed (MLC_FUZZ_DUMP=n, and
// MLC_FUZZ_BUG=1 for the version with the bug), to look at or reproduce.
func TestDump(t *testing.T) {
	s := os.Getenv("MLC_FUZZ_DUMP")
	if s == "" {
		t.Skip("MLC_FUZZ_DUMP=<seed>")
	}
	seed, _ := strconv.ParseInt(s, 10, 64)
	os.Stdout.Write(Generate(seed, os.Getenv("MLC_FUZZ_BUG") != "").Source())
}
