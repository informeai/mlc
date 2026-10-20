package driver

import (
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// TestStatus is a test's result, taken from the exit code of the
// process that ran it (see mlc_test_finish in the runtime).
type TestStatus int

const (
	Passed   TestStatus = iota // finished without failures
	Failed                     // a check failed
	Panicked                   // a panic the test didn't expect
	Leaked                     // allocation left over (an internal mlc error)
	Crashed                    // another exit code or a signal
)

func (s TestStatus) String() string {
	switch s {
	case Passed:
		return "ok"
	case Failed:
		return "failed"
	case Panicked:
		return "panic"
	case Leaked:
		return "leak"
	}
	return "ended abnormally"
}

// TestResult is the result of running a test.
type TestResult struct {
	Name    string
	Status  TestStatus
	Output  []byte // standard output and error, together, in the order they came out
	Elapsed time.Duration
}

// RunTest runs the test of index i of the executable exe (made by BuildTest)
// in a process of its own.
func RunTest(exe string, i int, name string) TestResult {
	var out bytes.Buffer
	cmd := exec.Command(exe, strconv.Itoa(i))
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	r := TestResult{Name: name, Output: out.Bytes(), Elapsed: time.Since(start)}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		r.Status = Passed
	case errors.As(err, &exitErr):
		switch exitErr.ExitCode() {
		case 1:
			r.Status = Failed
		case 2:
			r.Status = Panicked
		case 3:
			r.Status = Leaked
		default:
			r.Status = Crashed
		}
	default:
		r.Status = Crashed
		r.Output = append(r.Output, []byte(err.Error()+"\n")...)
	}
	return r
}

// BenchResult is one round of a benchmark: b.n repetitions in Ns
// nanoseconds (measured inside the program, without the time to start the
// process).
type BenchResult struct {
	Status TestStatus
	Ns     int64
	Output []byte // the benchmark's output, without the time line
}

// RunBench runs the benchmark of index i of the executable exe with b.n = n.
func RunBench(exe string, i, n int) BenchResult {
	var out bytes.Buffer
	cmd := exec.Command(exe, "bench", strconv.Itoa(i), strconv.Itoa(n))
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	r := BenchResult{Status: Passed}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		switch exitErr.ExitCode() {
		case 1:
			r.Status = Failed
		case 2:
			r.Status = Panicked
		case 3:
			r.Status = Leaked
		default:
			r.Status = Crashed
		}
	default:
		r.Status = Crashed
	}
	text := out.String()
	if k := strings.LastIndex(text, "\nmlc-bench-ns "); k >= 0 {
		line := text[k+len("\nmlc-bench-ns "):]
		line, rest, _ := strings.Cut(line, "\n")
		r.Ns, _ = strconv.ParseInt(line, 10, 64)
		text = text[:k] + rest
	} else if r.Status == Passed {
		r.Status = Crashed
	}
	r.Output = []byte(text)
	return r
}
