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

// Outcome é o veredito sobre um mutante.
type Outcome int

const (
	Rejected Outcome = iota // o verificador recusou: certo
	Benign                  // aceito, e roda limpo: a mutação não criou bug ali
	Hole                    // aceito, e roda com erro: buraco no verificador
)

func (o Outcome) String() string {
	switch o {
	case Rejected:
		return "recusado"
	case Benign:
		return "aceito sem bug"
	}
	return "BURACO"
}

// Result é o veredito e o porquê (o erro do verificador, ou o que o
// oráculo achou).
type Result struct {
	Mutant
	Outcome Outcome
	Detail  string
}

// Target é um programa aceito pelo verificador, base das mutações.
type Target struct {
	Path  string // o arquivo, para ler e para o relatório
	Name  string // o nome com que é compilado (as mensagens não dependem da pasta)
	Read  driver.Loader
	Opts  driver.Options
	Test  bool     // arquivo _test.mlc: roda cada teste
	Args  []string // os argumentos do programa (x.args)
	Stdin []byte   // a entrada padrão (x.in)
}

// Timeout limita cada execução: um mutante que trava (um unlock apagado, um
// canal que nunca fecha) é um buraco.
var Timeout = 20 * time.Second

// Mutate gera os mutantes do alvo e dá o veredito de cada um. work é uma
// pasta temporária para os executáveis.
func (t *Target) Mutate(work string) ([]Result, error) {
	src, err := os.ReadFile(t.Path)
	if err != nil {
		return nil, err
	}
	f, info, err := driver.Check(t.Name, src, t.Read, t.Opts)
	if err != nil {
		return nil, fmt.Errorf("%s: o original não passa no verificador: %v", t.Path, err)
	}
	var out []Result
	for i, m := range Mutants(src, f, info) {
		out = append(out, t.judge(m, filepath.Join(work, "m"+strconv.Itoa(i))))
	}
	return out, nil
}

// judge decide um mutante: o verificador primeiro, o oráculo se ele aceitar.
func (t *Target) judge(m Mutant, exe string) Result {
	r := Result{Mutant: m}
	if _, _, err := driver.Check(t.Name, m.Src, t.Read, t.Opts); err != nil {
		r.Outcome, r.Detail = Rejected, firstLine(err.Error())
		return r
	}
	opts := t.Opts
	opts.Sanitize = "address"
	var runs [][]string // os argumentos de cada execução
	if t.Test {
		prog, err := driver.BuildTest(t.Name, m.Src, exe, t.Read, opts)
		if err != nil {
			r.Outcome, r.Detail = Hole, "aceito, mas o C não compila: "+firstLine(err.Error())
			return r
		}
		for i := range prog.Tests {
			runs = append(runs, []string{strconv.Itoa(i)})
		}
	} else {
		if err := driver.BuildOpts(t.Name, m.Src, exe, t.Read, opts); err != nil {
			r.Outcome, r.Detail = Hole, "aceito, mas o C não compila: "+firstLine(err.Error())
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

// oracle roda o executável e diz o que deu errado ("" se nada). Um panic
// (código 2) ou um teste que falha (1) é comportamento seguro: a mutação
// mudou o resultado, mas sem corromper memória.
func (t *Target) oracle(exe string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if t.Stdin != nil {
		cmd.Stdin = bytes.NewReader(t.Stdin)
	}
	cmd.Env = append(os.Environ(), "ASAN_OPTIONS=detect_leaks=0") // vazamento: o contador do runtime
	err := cmd.Run()
	text := out.String()
	for _, sig := range []string{"AddressSanitizer", "UndefinedBehaviorSanitizer", "runtime error:", "erro interno"} {
		if i := strings.Index(text, sig); i >= 0 {
			return strings.TrimSpace(firstLine(text[i:]))
		}
	}
	if ctx.Err() != nil {
		return fmt.Sprintf("travou (mais de %v)", Timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch code := exitErr.ExitCode(); code {
		case 1, 2:
			return ""
		case 3:
			return "sobrou alocação no fim"
		default:
			return fmt.Sprintf("terminou com %v", exitErr)
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
