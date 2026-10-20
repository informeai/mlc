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

// checkGen passa o programa pelo verificador.
func checkGen(p *Program) error {
	_, _, err := driver.Check("gerado.mlc", p.Source(), noImports, driver.Options{})
	return err
}

// typeError diz se o erro não é de posse: o gerador escreveu um programa
// mal formado (o verificador nem chegou a julgar a posse).
var ownershipError = regexp.MustCompile(`liberad|movid|emprestad|dona de memória|vazamento|nil aqui|sem free`)

// A cada go test: o gerador escreve programas bem formados, o verificador
// recusa todos os que têm bug injetado e aceita quase todos os válidos.
func TestGenerate(t *testing.T) {
	accepted, valid := 0, 0
	for seed := int64(1); seed <= 300; seed++ {
		p := Generate(seed, false)
		valid++
		if err := checkGen(p); err == nil {
			accepted++
		} else if !ownershipError.MatchString(err.Error()) {
			t.Errorf("semente %d: programa válido mal formado: %v", seed, firstLine(err.Error()))
		}
		b := Generate(seed, true)
		err := checkGen(b)
		switch {
		case err == nil:
			t.Errorf("semente %d: o verificador aceitou o bug injetado (%s):\n%s", seed, b.Bug, b.Source())
		case !ownershipError.MatchString(err.Error()):
			t.Errorf("semente %d: programa com bug mal formado: %v", seed, firstLine(err.Error()))
		}
	}
	if accepted*100 < valid*97 {
		t.Errorf("o verificador aceitou só %d de %d programas válidos", accepted, valid)
	}
}

// TestReduce: o redutor chega a um programa pequeno que ainda tem o bug.
func TestReduce(t *testing.T) {
	for seed := int64(1); seed <= 50; seed++ {
		p := Generate(seed, true)
		if p.Lines = count(p.Body); p.Lines < 15 {
			continue
		}
		r := Reduce(p, func(q *Program) bool { return checkGen(q) != nil && hasBugNode(q.Body) })
		if !hasBugNode(r.Body) || count(r.Body) >= count(p.Body) {
			t.Errorf("semente %d: redução de %d para %d comandos", seed, count(p.Body), count(r.Body))
		}
		return
	}
	t.Skip("nenhuma semente grande")
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

// TestFuzz é o make fuzz: gera programas por FUZZTIME (padrão 1m), metade
// válidos e metade com um bug, e confere cada um. Só roda com MLC_FUZZ=1.
//
//   - com bug e aceito: buraco (o oráculo confirma, e o programa é reduzido);
//   - válido, aceito e com erro na execução: buraco (no gerador de C ou no
//     runtime, ou no verificador);
//   - válido e recusado: candidato a falso positivo, agrupado pela mensagem.
//
// MLC_FUZZ_SEED escolhe a primeira semente (para reproduzir: MLC_FUZZ_SEED=n
// MLC_FUZZ_COUNT=1); MLC_FUZZ_OUT, a pasta dos achados (padrão /tmp/mlc-fuzz).
func TestFuzz(t *testing.T) {
	if os.Getenv("MLC_FUZZ") == "" {
		t.Skip("rode com MLC_FUZZ=1 (make fuzz)")
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
	os.WriteFile(filepath.Join(out, "relatorio.txt"), []byte(rep), 0o644)
	if st.holes > 0 {
		t.Errorf("%d buraco(s): veja %s", st.holes, out)
	}
}

// finding é o resultado de um programa.
type finding struct {
	p       *Program
	checker error
	oracle  string // o erro na execução ("" se rodou limpo, ou não rodou)
	ran     bool   // rodou sob o oráculo
	hole    bool
	reduced *Program
}

// fuzzOne gera, confere e (num buraco) reduz um programa.
func fuzzOne(seed int64, withBug bool, exe string) finding {
	p := Generate(seed, withBug)
	f := finding{p: p, checker: checkGen(p)}
	if f.checker != nil {
		return f
	}
	// aceito. Um programa com bug sempre roda sob o oráculo; um válido, só
	// 1 em cada 10 (procura bug no gerador de C e no runtime; os buracos do
	// verificador aparecem nos programas com bug, que só compilam quando
	// são aceitos)
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
			return hasBugNode(q.Body) // o oráculo não viu: guarda o comando com o bug
		}
		return runGen(q, exe) != ""
	}
	f.reduced = Reduce(p, keep)
	return f
}

// runGen compila o programa com AddressSanitizer e roda, devolvendo o que o
// oráculo achou ("" se nada).
func runGen(p *Program, exe string) string {
	opts := driver.Options{Sanitize: "address"}
	if err := driver.BuildOpts("gerado.mlc", p.Source(), exe, noImports, opts); err != nil {
		return "aceito, mas o C não compila: " + firstLine(err.Error())
	}
	t := &Target{}
	return t.oracle(exe, nil)
}

type fuzzStats struct {
	programs, valid, validOK, validRan, bugs, bugsRejected, holes int
	fp                                                            map[string]int // mensagem normalizada → quantos
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
			m := normalize.ReplaceAllString(strings.TrimPrefix(firstLine(f.checker.Error()), "gerado.mlc:"), "N")
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
	what := "válido"
	if f.p.Bug != "" {
		what = "bug " + f.p.Bug
	}
	detail := f.oracle
	if detail == "" {
		detail = "o verificador aceitou, e a execução não mostrou o bug"
	}
	file := filepath.Join(out, fmt.Sprintf("achado_%d.mlc", f.p.Seed))
	if f.p.Bug != "" {
		file = filepath.Join(out, fmt.Sprintf("achado_%d_bug.mlc", f.p.Seed))
	}
	src := f.reduced.Source()
	src = append([]byte(fmt.Sprintf("// achado: %s\n", detail)), src...)
	os.WriteFile(file, src, 0o644)
	s.holeLines = append(s.holeLines, fmt.Sprintf("BURACO semente %d (%s): %s\n    reduzido de %d para %d comandos: %s", f.p.Seed, what, detail, count(f.p.Body), count(f.reduced.Body), file))
}

func (s *fuzzStats) report(first, last int64, out string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nsementes %d a %d: %d programas\n", first, last, s.programs)
	fmt.Fprintf(&b, "  válidos:       %d, aceitos %d (%d rodaram sob o oráculo), recusados %d\n", s.valid, s.validOK, s.validRan, s.valid-s.validOK-countValidHoles(s))
	fmt.Fprintf(&b, "  com bug:       %d, recusados %d\n", s.bugs, s.bugsRejected)
	fmt.Fprintf(&b, "  buracos:       %d\n", s.holes)
	for _, l := range s.holeLines {
		fmt.Fprintf(&b, "\n%s\n", l)
	}
	if len(s.fp) > 0 {
		fmt.Fprintf(&b, "\nVálidos recusados (candidatos a falso positivo; MLC_FUZZ_SEED=<semente> MLC_FUZZ_COUNT=1 reproduz):\n")
		var ks []string
		for k := range s.fp {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool { return s.fp[ks[i]] > s.fp[ks[j]] })
		for _, k := range ks {
			fmt.Fprintf(&b, "  %5d  (semente %d) %s\n", s.fp[k], s.fpSeed[k], k)
		}
	}
	fmt.Fprintf(&b, "\nachados em %s\n", out)
	return b.String()
}

func countValidHoles(s *fuzzStats) int {
	n := 0
	for _, l := range s.holeLines {
		if strings.Contains(l, "(válido)") {
			n++
		}
	}
	return n
}

// TestDump escreve o programa de uma semente (MLC_FUZZ_DUMP=n, e
// MLC_FUZZ_BUG=1 para a versão com bug), para ver ou reproduzir.
func TestDump(t *testing.T) {
	s := os.Getenv("MLC_FUZZ_DUMP")
	if s == "" {
		t.Skip("MLC_FUZZ_DUMP=<semente>")
	}
	seed, _ := strconv.ParseInt(s, 10, 64)
	os.Stdout.Write(Generate(seed, os.Getenv("MLC_FUZZ_BUG") != "").Source())
}
