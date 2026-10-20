package fuzz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mlc/internal/driver"
)

// Outcome is the verdict on a mutant.
type Outcome int

const (
	Rejected Outcome = iota // the checker refused: correct
	Benign                  // accepted, and runs clean: the mutation didn't create a bug there
	Hole                    // accepted, and runs with an error: a hole in the checker
)

func (o Outcome) String() string {
	switch o {
	case Rejected:
		return "rejected"
	case Benign:
		return "accepted without a bug"
	}
	return "HOLE"
}

// Result is the verdict and the why (the checker's error, or what the
// oracle found).
type Result struct {
	Mutant
	Outcome Outcome
	Detail  string
}

// Target is a program accepted by the checker, the base of the mutations.
type Target struct {
	Path  string // the file, for reading and for the report
	Name  string // the name it's compiled with (the messages don't depend on the folder)
	Read  driver.Loader
	Opts  driver.Options
	Test  bool     // a _test.mlc file: runs each test
	Args  []string // the program's arguments (x.args)
	Stdin []byte   // standard input (x.in)
}

// Timeout limits each run: a mutant that hangs (a deleted unlock, a
// channel that never closes) is a hole.
var Timeout = 20 * time.Second

// Mutate generates the target's mutants and gives the verdict on each one. work is a
// temporary folder for the executables.
func (t *Target) Mutate(work string) ([]Result, error) {
	src, err := os.ReadFile(t.Path)
	if err != nil {
		return nil, err
	}
	f, info, err := driver.Check(t.Name, src, t.Read, t.Opts)
	if err != nil {
		return nil, fmt.Errorf("%s: the original doesn't pass the checker: %v", t.Path, err)
	}
	var out []Result
	for i, m := range Mutants(src, f, info) {
		out = append(out, t.judge(m, filepath.Join(work, "m"+strconv.Itoa(i))))
	}
	return out, nil
}

// judge decides a mutant: the checker first, the oracle if it accepts.
func (t *Target) judge(m Mutant, exe string) Result {
	r := Result{Mutant: m}
	if _, _, err := driver.Check(t.Name, m.Src, t.Read, t.Opts); err != nil {
		r.Outcome, r.Detail = Rejected, firstLine(err.Error())
		return r
	}
	opts := t.Opts
	opts.Sanitize = "address"
	var runs [][]string // the arguments of each run
	if t.Test {
		prog, err := driver.BuildTest(t.Name, m.Src, exe, t.Read, opts)
		if err != nil {
			r.Outcome, r.Detail = Hole, "accepted, but the C doesn't compile: "+firstLine(err.Error())
			return r
		}
		for i := range prog.Tests {
			runs = append(runs, []string{strconv.Itoa(i)})
		}
	} else {
		if err := driver.BuildOpts(t.Name, m.Src, exe, t.Read, opts); err != nil {
			r.Outcome, r.Detail = Hole, "accepted, but the C doesn't compile: "+firstLine(err.Error())
			return r
		}
		runs = [][]string{t.Args}
	}
	for _, args := range runs {
		if bad := t.oracle(exe, args); bad != "" {
			r.Outcome, r.Detail = Hole, bad
			return r
		}
	}
	r.Outcome = Benign
	return r
}

// oracle runs the executable and says what went wrong ("" if nothing). A panic
// (code 2) or a failing test (1) is safe behavior: the mutation
// changed the result, but without corrupting memory.
func (t *Target) oracle(exe string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if t.Stdin != nil {
		cmd.Stdin = bytes.NewReader(t.Stdin)
	}
	cmd.Env = append(os.Environ(), "ASAN_OPTIONS=detect_leaks=0") // leak: the runtime's counter
	err := cmd.Run()
	text := out.String()
	for _, sig := range []string{"AddressSanitizer", "UndefinedBehaviorSanitizer", "runtime error:", "internal error"} {
		if i := strings.Index(text, sig); i >= 0 {
			return strings.TrimSpace(firstLine(text[i:]))
		}
	}
	if ctx.Err() != nil {
		return fmt.Sprintf("hung (more than %v)", Timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch code := exitErr.ExitCode(); code {
		case 1, 2:
			return ""
		case 3:
			return "allocation left at the end"
		default:
			return fmt.Sprintf("ended with %v", exitErr)
		}
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
