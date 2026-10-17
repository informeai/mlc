package driver

import (
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

// TestStatus é o resultado de um teste, tirado do código de saída do
// processo que o rodou (veja mlc_test_finish no runtime).
type TestStatus int

const (
	Passed   TestStatus = iota // terminou sem falhas
	Failed                     // uma verificação falhou
	Panicked                   // panic que o teste não esperava
	Leaked                     // sobrou alocação (erro interno do mlc)
	Crashed                    // outro código de saída ou sinal
)

func (s TestStatus) String() string {
	switch s {
	case Passed:
		return "ok"
	case Failed:
		return "falhou"
	case Panicked:
		return "panic"
	case Leaked:
		return "vazamento"
	}
	return "terminou de forma anormal"
}

// TestResult é o resultado de rodar um teste.
type TestResult struct {
	Name    string
	Status  TestStatus
	Output  []byte // saída padrão e de erro, juntas, na ordem em que saíram
	Elapsed time.Duration
}

// RunTest roda o teste de índice i do executável exe (feito por BuildTest)
// num processo só dele.
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
