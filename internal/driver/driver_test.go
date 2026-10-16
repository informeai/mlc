package driver

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testsDir = "../../tests"

func init() { extraCFlags = []string{"-Werror"} }

// buildAndRun compila tests/<dir>/<name> e executa o binário.
func buildAndRun(t *testing.T, path string) (stdout, stderr string, code int) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "prog")
	if err := Build(filepath.Base(path), src, exe); err != nil {
		t.Fatalf("build falhou:\n%v", err)
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command(exe)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatal(err)
		}
		code = exitErr.ExitCode()
	}
	return out.String(), errOut.String(), code
}

func readExpected(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func requireCC(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cc"); err != nil && os.Getenv("CC") == "" {
		t.Skip("compilador C não encontrado")
	}
}

// TestRun: cada tests/run/x.mlc precisa compilar, sair com 0 e imprimir x.out.
func TestRun(t *testing.T) {
	requireCC(t)
	files, _ := filepath.Glob(filepath.Join(testsDir, "run", "*.mlc"))
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out, errOut, code := buildAndRun(t, path)
			if code != 0 {
				t.Fatalf("código de saída %d, stderr:\n%s", code, errOut)
			}
			if want := readExpected(t, strings.TrimSuffix(path, ".mlc")+".out"); out != want {
				t.Errorf("saída:\n%q\nesperado:\n%q", out, want)
			}
		})
	}
}

// TestPanic: cada tests/panic/x.mlc precisa sair com 2, imprimir x.out e
// reportar o panic de x.err apontando para a linha do .mlc.
func TestPanic(t *testing.T) {
	requireCC(t)
	files, _ := filepath.Glob(filepath.Join(testsDir, "panic", "*.mlc"))
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out, errOut, code := buildAndRun(t, path)
			base := strings.TrimSuffix(path, ".mlc")
			if code != 2 {
				t.Errorf("código de saída %d, esperado 2", code)
			}
			if want := readExpected(t, base+".out"); out != want {
				t.Errorf("stdout:\n%q\nesperado:\n%q", out, want)
			}
			if want := readExpected(t, base+".err"); errOut != want {
				t.Errorf("stderr:\n%q\nesperado:\n%q", errOut, want)
			}
		})
	}
}

// TestErrors: cada tests/errors/x.mlc começa com "// erro: <trecho>" e
// precisa ser rejeitado com uma mensagem que contenha o trecho.
func TestErrors(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testsDir, "errors", "*.mlc"))
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := readExpected(t, path)
			first, _, _ := strings.Cut(src, "\n")
			want, ok := strings.CutPrefix(first, "// erro: ")
			if !ok {
				t.Fatal("a primeira linha precisa ser \"// erro: <trecho esperado>\"")
			}
			_, err := Compile(filepath.Base(path), []byte(src))
			if err == nil {
				t.Fatalf("compilou, mas deveria falhar com %q", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("erro:\n%v\nnão contém %q", err, want)
			}
		})
	}
}

// TestEvaluationOrderIsExplicit confere que a ordem esquerda-para-direita está
// escrita no C gerado. Rodar o programa não basta: o clang já avalia da
// esquerda para a direita por conta própria, mas o padrão do C não garante.
func TestEvaluationOrderIsExplicit(t *testing.T) {
	src := `import "io"
fn a() int { return 1 }
fn b() int { return 2 }
fn soma(x int, y int) int { return x + y }
fn main() {
    io.print(soma(a(), b()), a() - b())
}`
	csrc, err := Compile("ordem.mlc", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mlc_t_1 = mlc_fn_a(), mlc_fn_soma(mlc_t_1, mlc_fn_b())",
		"mlc_t_3 = mlc_fn_a(), MLC_SUB(mlc_t_3, mlc_fn_b())",
		// io.print calcula todos os argumentos antes de imprimir
		"(mlc_t_2 = (mlc_t_1", "mlc_t_4 = (mlc_t_3",
		"mlc_t_4 = (mlc_t_3 = mlc_fn_a(), MLC_SUB(mlc_t_3, mlc_fn_b())), mlc_io_print_int(mlc_t_2)",
	} {
		if !strings.Contains(csrc, want) {
			t.Errorf("C gerado não contém %q:\n%s", want, csrc)
		}
	}
}
