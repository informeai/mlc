package driver

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	if err := BuildWith(filepath.Base(path), src, exe, dirLoader(path)); err != nil {
		t.Fatalf("build falhou:\n%v", err)
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command(exe)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// x.in, se existir, vira a entrada padrão do programa
	if in, err := os.ReadFile(strings.TrimSuffix(path, ".mlc") + ".in"); err == nil {
		cmd.Stdin = bytes.NewReader(in)
	}
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
	files := programs("run")
	for _, path := range files {
		t.Run(testName(path), func(t *testing.T) {
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
	files := programs("panic")
	for _, path := range files {
		t.Run(testName(path), func(t *testing.T) {
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
	files := programs("errors")
	for _, path := range files {
		t.Run(testName(path), func(t *testing.T) {
			src := readExpected(t, path)
			first, _, _ := strings.Cut(src, "\n")
			want, ok := strings.CutPrefix(first, "// erro: ")
			if !ok {
				t.Fatal("a primeira linha precisa ser \"// erro: <trecho esperado>\"")
			}
			_, err := CompileWith(filepath.Base(path), []byte(src), dirLoader(path))
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
	src := `import "std/io"
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
	// as temporárias são numeradas no programa todo (a biblioteca também
	// usa): renumera as da main na ordem em que aparecem
	start := strings.Index(csrc, "mlc_fn_main(void) {")
	body := csrc[start:]
	names := map[string]string{}
	body = regexp.MustCompile(`mlc_t_[0-9]+`).ReplaceAllStringFunc(body, func(n string) string {
		if _, ok := names[n]; !ok {
			names[n] = fmt.Sprintf("t%d", len(names)+1)
		}
		return names[n]
	})
	for _, want := range []string{
		// t1 é o io.Writer; os argumentos vão para t2 e t4, da esquerda para a direita
		"t2 = (t3 = mlc_fn_a(), mlc_fn_soma(t3, mlc_fn_b()))",
		"t4 = (t5 = mlc_fn_a(), mlc_sub_i64(t5, mlc_fn_b(), __FILE__, __LINE__))",
		// io.print calcula todos os argumentos antes de escrever
		"t4 = (t5 = mlc_fn_a(), mlc_sub_i64(t5, mlc_fn_b(), __FILE__, __LINE__)), mlc_fn_m0_io__impl_Display__int__write(&t2, &t1)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("C gerado não contém %q:\n%s", want, body)
		}
	}
}

// programs lista os programas de teste de tests/<kind>: cada x.mlc da pasta e,
// para programas de vários arquivos, cada <pasta>/main.mlc (os outros
// arquivos da pasta são os módulos que ele importa).
func programs(kind string) []string {
	files, _ := filepath.Glob(filepath.Join(testsDir, kind, "*.mlc"))
	multi, _ := filepath.Glob(filepath.Join(testsDir, kind, "*", "main.mlc"))
	return append(files, multi...)
}

// dirLoader lê os imports da pasta do programa de teste. O programa é
// compilado pelo nome sem pasta, para as mensagens não dependerem dela.
func dirLoader(path string) Loader {
	dir := filepath.Dir(path)
	return func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, name))
	}
}

// testName nomeia o teste pelo arquivo, ou pela pasta num programa de vários
// arquivos (projeto/main.mlc vira projeto).
func testName(path string) string {
	if filepath.Base(path) == "main.mlc" {
		return filepath.Base(filepath.Dir(path))
	}
	return filepath.Base(path)
}

// TestLinkErrors: uma biblioteca de C ou um símbolo que não existe é erro do
// programa (LinkError), não do mlc.
func TestLinkErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "prog")
	for _, src := range []string{
		"import \"std/io\"\nlink \"naoexiste_mlc_123\"\nextern \"C\" fn abs(x int32) int32\nfn main() {\n    io.print(abs(1))\n}\n",
		"import \"std/io\"\nextern \"C\" fn funcao_que_nao_existe_mlc(x int32) int32\nfn main() {\n    io.print(funcao_que_nao_existe_mlc(1))\n}\n",
	} {
		err := Build("link.mlc", []byte(src), out)
		var le *LinkError
		if !errors.As(err, &le) || !strings.Contains(err.Error(), "não foi possível ligar o programa") {
			t.Errorf("esperado LinkError, veio %v", err)
		}
	}
}

// TestImportSearch: um import é procurado na pasta do arquivo principal, em
// lib/ ao lado dele, nas pastas de -I e nas de MLC_PATH, nessa ordem; os
// imports de dentro de uma biblioteca seguem a mesma busca.
func TestImportSearch(t *testing.T) {
	root := t.TempDir()
	write := func(path, src string) {
		full := filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proj/main.mlc", `import "std/io"
import "util"
import "texto"
import "json/parse"
import "dup"

fn main() {
    io.print(util.um(), texto.dois(), parse.tres(), dup.qual())
}
`)
	write("proj/lib/util.mlc", "pub fn um() int {\n    return 1\n}\n")
	write("proj/dup.mlc", "pub fn qual() string {\n    return \"projeto\"\n}\n")
	write("extra/texto.mlc", "pub fn dois() int {\n    return 2\n}\n")
	write("shared/json/parse.mlc", "import \"json/lexer\"\n\npub fn tres() int {\n    return lexer.um() + 2\n}\n")
	write("shared/json/lexer.mlc", "pub fn um() int {\n    return 1\n}\n")
	write("shared/dup.mlc", "pub fn qual() string {\n    return \"MLC_PATH\"\n}\n")
	t.Setenv("MLC_PATH", filepath.Join(root, "nada")+string(os.PathListSeparator)+filepath.Join(root, "shared"))

	mainPath := filepath.Join(root, "proj", "main.mlc")
	src, _ := os.ReadFile(mainPath)
	exe := filepath.Join(root, "prog")
	opts := Options{ImportDirs: []string{filepath.Join(root, "extra")}}
	if err := BuildOpts(mainPath, src, exe, os.ReadFile, opts); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(exe).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "1 2 3 projeto" {
		t.Errorf("saída %q, esperado \"1 2 3 projeto\"", got)
	}

	// sem o -I, o módulo texto não é achado, e a mensagem diz onde procurou
	err = BuildOpts(mainPath, src, exe, os.ReadFile, Options{})
	if err == nil || !strings.Contains(err.Error(), `arquivo texto.mlc não encontrado (procurado em`) || !strings.Contains(err.Error(), "shared") {
		t.Errorf("esperado erro de módulo desconhecido com as pastas, veio %v", err)
	}

	// o diagnóstico do editor acha lib/ e MLC_PATH (o -I é só da linha de comando)
	write("proj/main2.mlc", "import \"std/io\"\nimport \"util\"\nimport \"json/parse\"\n\nfn main() {\n    io.print(util.um(), parse.tres())\n}\n")
	if errs := Diagnose(filepath.Join(root, "proj", "main2.mlc"), Overlay(nil)); len(errs) != 0 {
		t.Errorf("o diagnóstico não achou os módulos: %v", errs)
	}
}
