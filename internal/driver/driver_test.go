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

// sanitize is the sanitizer of the run tests (MLC_SANITIZE=address or
// thread, as in make sanitize): every program the checker accepts must
// run clean under them (see docs/verification.md).
var sanitize = os.Getenv("MLC_SANITIZE")

// buildAndRun compiles tests/<dir>/<name> and runs the binary.
func buildAndRun(t *testing.T, path string) (stdout, stderr string, code int) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "prog")
	if err := BuildOpts(filepath.Base(path), src, exe, dirLoader(path), Options{Sanitize: sanitize}); err != nil {
		t.Fatalf("build failed:\n%v", err)
	}
	var out, errOut bytes.Buffer
	var args []string // x.args, if it exists: the arguments, one per line
	if b, err := os.ReadFile(strings.TrimSuffix(path, ".mlc") + ".args"); err == nil {
		args = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// x.in, if it exists, becomes the program's standard input
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
		t.Skip("C compiler not found")
	}
}

// TestRun: each tests/run/x.mlc must compile, exit with 0 and print x.out.
func TestRun(t *testing.T) {
	requireCC(t)
	files := programs("run")
	for _, path := range files {
		t.Run(testName(path), func(t *testing.T) {
			out, errOut, code := buildAndRun(t, path)
			if code != 0 {
				t.Fatalf("exit code %d, stderr:\n%s", code, errOut)
			}
			if want := readExpected(t, strings.TrimSuffix(path, ".mlc")+".out"); out != want {
				t.Errorf("output:\n%q\nexpected:\n%q", out, want)
			}
		})
	}
}

// TestPanic: each tests/panic/x.mlc must exit with 2, print x.out and
// report x.err's panic pointing at the .mlc line.
func TestPanic(t *testing.T) {
	requireCC(t)
	files := programs("panic")
	for _, path := range files {
		t.Run(testName(path), func(t *testing.T) {
			out, errOut, code := buildAndRun(t, path)
			base := strings.TrimSuffix(path, ".mlc")
			if code != 2 {
				t.Errorf("exit code %d, expected 2", code)
			}
			if want := readExpected(t, base+".out"); out != want {
				t.Errorf("stdout:\n%q\nexpected:\n%q", out, want)
			}
			if want := readExpected(t, base+".err"); errOut != want {
				t.Errorf("stderr:\n%q\nexpected:\n%q", errOut, want)
			}
		})
	}
}

// TestErrors: each tests/errors/x.mlc starts with "// error: <piece>" and
// must be rejected with a message that contains the piece.
func TestErrors(t *testing.T) {
	files := programs("errors")
	for _, path := range files {
		t.Run(testName(path), func(t *testing.T) {
			src := readExpected(t, path)
			first, _, _ := strings.Cut(src, "\n")
			want, ok := strings.CutPrefix(first, "// error: ")
			if !ok {
				t.Fatal("the first line must be \"// error: <expected piece>\"")
			}
			_, err := CompileWith(filepath.Base(path), []byte(src), dirLoader(path))
			if err == nil {
				t.Fatalf("compiled, but should fail with %q", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error:\n%v\ndoesn't contain %q", err, want)
			}
		})
	}
}

// TestEvaluationOrderIsExplicit checks that the left-to-right order is
// written in the generated C. Running the program isn't enough: clang already evaluates
// left to right on its own, but the C standard doesn't guarantee it.
func TestEvaluationOrderIsExplicit(t *testing.T) {
	src := `import "std/io"
fn a() int { return 1 }
fn b() int { return 2 }
fn sum(x int, y int) int { return x + y }
fn main() {
    io.print(sum(a(), b()), a() - b())
}`
	csrc, err := Compile("order.mlc", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	// the temporaries are numbered over the whole program (the library also
	// uses them): renumbers main's in the order they appear
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
		// t1 is the io.Writer; the arguments go into t2 and t4, left to right
		"t2 = (t3 = mlc_fn_a(), mlc_fn_sum(t3, mlc_fn_b()))",
		"t4 = (t5 = mlc_fn_a(), mlc_sub_i64(t5, mlc_fn_b(), __FILE__, __LINE__))",
		// io.print computes every argument before writing
		"t4 = (t5 = mlc_fn_a(), mlc_sub_i64(t5, mlc_fn_b(), __FILE__, __LINE__)), mlc_fn_m1_io__impl_Display__int__write(&t2, &t1)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("generated C doesn't contain %q:\n%s", want, body)
		}
	}
}

// programs lists the test programs of tests/<kind>: each x.mlc of the folder and,
// for multi-file programs, each <folder>/main.mlc (the other
// files of the folder are the modules it imports).
func programs(kind string) []string {
	files, _ := filepath.Glob(filepath.Join(testsDir, kind, "*.mlc"))
	multi, _ := filepath.Glob(filepath.Join(testsDir, kind, "*", "main.mlc"))
	return append(files, multi...)
}

// dirLoader reads the imports from the test program's folder. The program is
// compiled by its name without the folder, so the messages don't depend on it.
func dirLoader(path string) Loader {
	dir := filepath.Dir(path)
	return func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, name))
	}
}

// testName names the test by the file, or by the folder in a multi-file
// program (project/main.mlc becomes project).
func testName(path string) string {
	if filepath.Base(path) == "main.mlc" {
		return filepath.Base(filepath.Dir(path))
	}
	return filepath.Base(path)
}

// TestLinkErrors: a C library or a symbol that doesn't exist is an error of the
// program (LinkError), not of mlc.
func TestLinkErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "prog")
	for _, src := range []string{
		"import \"std/io\"\nlink \"naoexiste_mlc_123\"\nextern \"C\" fn abs(x int32) int32\nfn main() {\n    io.print(abs(1))\n}\n",
		"import \"std/io\"\nextern \"C\" fn function_that_does_not_exist_mlc(x int32) int32\nfn main() {\n    io.print(function_that_does_not_exist_mlc(1))\n}\n",
	} {
		err := Build("link.mlc", []byte(src), out)
		var le *LinkError
		if !errors.As(err, &le) || !strings.Contains(err.Error(), "couldn't link the program") {
			t.Errorf("expected a LinkError, got %v", err)
		}
	}
}

// TestImportSearch: an import is looked up in the main file's folder, in
// lib/ next to it, in the -I folders and in MLC_PATH's, in that order; the
// imports inside a library follow the same search.
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
import "text"
import "json/parse"
import "dup"

fn main() {
    io.print(util.one(), text.two(), parse.three(), dup.which())
}
`)
	write("proj/lib/util.mlc", "pub fn one() int {\n    return 1\n}\n")
	write("proj/dup.mlc", "pub fn which() string {\n    return \"project\"\n}\n")
	write("extra/text.mlc", "pub fn two() int {\n    return 2\n}\n")
	write("shared/json/parse.mlc", "import \"json/lexer\"\n\npub fn three() int {\n    return lexer.one() + 2\n}\n")
	write("shared/json/lexer.mlc", "pub fn one() int {\n    return 1\n}\n")
	write("shared/dup.mlc", "pub fn which() string {\n    return \"MLC_PATH\"\n}\n")
	t.Setenv("MLC_PATH", filepath.Join(root, "nothing")+string(os.PathListSeparator)+filepath.Join(root, "shared"))

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
	if got := strings.TrimSpace(string(out)); got != "1 2 3 project" {
		t.Errorf("output %q, expected \"1 2 3 project\"", got)
	}

	// without -I, the module text isn't found, and the message says where it looked
	err = BuildOpts(mainPath, src, exe, os.ReadFile, Options{})
	if err == nil || !strings.Contains(err.Error(), `file text.mlc not found (searched in`) || !strings.Contains(err.Error(), "shared") {
		t.Errorf("expected an unknown-module error with the folders, got %v", err)
	}

	// the editor's diagnostics find lib/ and MLC_PATH (-I is only for the command line)
	write("proj/main2.mlc", "import \"std/io\"\nimport \"util\"\nimport \"json/parse\"\n\nfn main() {\n    io.print(util.one(), parse.three())\n}\n")
	if errs := Diagnose(filepath.Join(root, "proj", "main2.mlc"), Overlay(nil)); len(errs) != 0 {
		t.Errorf("the diagnostics didn't find the modules: %v", errs)
	}
}

// TestTestRunner checks the result and the output of each test of
// tests/testing/runner_test.mlc (mlc test).
func TestTestRunner(t *testing.T) {
	requireCC(t)
	path := filepath.Join(testsDir, "testing", "runner_test.mlc")
	src := []byte(readExpected(t, path))
	exe := filepath.Join(t.TempDir(), "prog")
	prog, err := BuildTest(filepath.Base(path), src, exe, dirLoader(path), Options{})
	if err != nil {
		t.Fatalf("build failed:\n%v", err)
	}
	names := prog.Tests
	want := []struct {
		name   string
		status TestStatus
		output []string // pieces expected in the output
	}{
		{"test_passes", Passed, nil},
		{"test_fails", Failed, []string{
			"the test's output\n",
			"runner_test.mlc:17: different text\n    got:      \"hi\\n\"\n    expected: \"hello\"\n",
			"runner_test.mlc:18: got 3, expected 4\n",
			"runner_test.mlc:19: got 255 (0xff), expected 256 (0x100)\n",
			"runner_test.mlc:20: got 0.30000000000000004, expected 0.3\n",
			"runner_test.mlc:22: explicit\n",
		}},
		{"test_panics", Panicked, []string{"panic: runner_test.mlc:29: index 5 out of bounds (length 2)"}},
		{"test_expected_panic", Passed, []string{"expected panic: runner_test.mlc:37:"}},
		{"test_missing_panic", Failed, []string{"ended without the expected panic"}},
		{"test_fails_then_panics", Failed, []string{"runner_test.mlc:47: got 1, expected 2", "expected panic:"}},
	}
	if len(names) != len(want) {
		t.Fatalf("tests: %v", names)
	}
	for i, w := range want {
		if names[i] != w.name {
			t.Errorf("test %d: %s, expected %s", i, names[i], w.name)
			continue
		}
		r := RunTest(exe, i, names[i])
		if r.Status != w.status {
			t.Errorf("%s: %v, expected %v; output:\n%s", w.name, r.Status, w.status, r.Output)
		}
		for _, frag := range w.output {
			if !strings.Contains(string(r.Output), frag) {
				t.Errorf("%s: the output doesn't contain %q:\n%s", w.name, frag, r.Output)
			}
		}
		if w.name == "test_fails" && strings.Contains(string(r.Output), "never shows up") {
			t.Errorf("a true check shouldn't show up")
		}
	}
}

// TestStdLibrary runs the standard library's tests (std/*_test.mlc).
func TestStdLibrary(t *testing.T) {
	requireCC(t)
	files, _ := filepath.Glob("../../std/*_test.mlc")
	if len(files) == 0 {
		t.Fatal("no std/*_test.mlc")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := []byte(readExpected(t, path))
			exe := filepath.Join(t.TempDir(), "prog")
			prog, err := BuildTest("std/"+filepath.Base(path), src, exe, os.ReadFile, Options{Sanitize: sanitize})
			if err != nil {
				t.Fatalf("build failed:\n%v", err)
			}
			for i, name := range prog.Tests {
				if r := RunTest(exe, i, name); r.Status != Passed {
					t.Errorf("%s: %v\n%s", name, r.Status, r.Output)
				}
			}
		})
	}
}

// TestFFIOutHandle links tests/ffi/out_handle.mlc to a C library made
// on the spot (tests/ffi/counter.c), which returns handles through a pointer (&?*X).
func TestFFIOutHandle(t *testing.T) {
	requireCC(t)
	dir := ffiLib(t, "counter")
	path := filepath.Join(testsDir, "ffi", "out_handle.mlc")
	exe := filepath.Join(dir, "prog")
	if err := BuildOpts(filepath.Base(path), []byte(readExpected(t, path)), exe, dirLoader(path), Options{LibDirs: []string{dir}, Sanitize: sanitize}); err != nil {
		t.Fatalf("build failed:\n%v", err)
	}
	out, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := readExpected(t, filepath.Join(testsDir, "ffi", "out_handle.out")); string(out) != want {
		t.Errorf("output:\n%s\nexpected:\n%s", out, want)
	}
}

// TestBenchRunner checks the benchmarks of tests/testing/runner_test.mlc.
func TestBenchRunner(t *testing.T) {
	requireCC(t)
	path := filepath.Join(testsDir, "testing", "runner_test.mlc")
	exe := filepath.Join(t.TempDir(), "prog")
	prog, err := BuildTest(filepath.Base(path), []byte(readExpected(t, path)), exe, dirLoader(path), Options{})
	if err != nil {
		t.Fatalf("build failed:\n%v", err)
	}
	if fmt.Sprint(prog.Benches) != "[bench_sum bench_panics]" {
		t.Fatalf("benchmarks: %v", prog.Benches)
	}
	r := RunBench(exe, 0, 100000)
	if r.Status != Passed || r.Ns <= 0 {
		t.Errorf("bench_sum: %v, %d ns\n%s", r.Status, r.Ns, r.Output)
	}
	if strings.Contains(string(r.Output), "mlc-bench-ns") {
		t.Errorf("the time line should leave the output: %q", r.Output)
	}
	r = RunBench(exe, 1, 1)
	if r.Status != Panicked || !strings.Contains(string(r.Output), "out of bounds") {
		t.Errorf("bench_panics: %v\n%s", r.Status, r.Output)
	}
}

// ffiLib compiles tests/ffi/<name>.c into a lib<name>.a and returns the folder.
func ffiLib(t *testing.T, name string) string {
	t.Helper()
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	dir := t.TempDir()
	obj := filepath.Join(dir, name+".o")
	if out, err := exec.Command(cc, "-c", "-o", obj, filepath.Join(testsDir, "ffi", name+".c")).CombinedOutput(); err != nil {
		t.Fatalf("compiling %s.c: %v\n%s", name, err, out)
	}
	if out, err := exec.Command("ar", "rcs", filepath.Join(dir, "lib"+name+".a"), obj).CombinedOutput(); err != nil {
		t.Fatalf("ar: %v\n%s", err, out)
	}
	return dir
}

// TestFFICStructs links tests/ffi/cstructs.mlc to tests/ffi/cstructs.c:
// C's own structs, from tests/ffi/cstructs.h, with an anonymous union,
// bitfields, a field MLC doesn't declare, a string, a callback with its kept
// context, a struct by value and one a callback receives, and constants from
// the header. A field the header doesn't have is C's error, pointed at the MLC
// line.
func TestFFICStructs(t *testing.T) {
	requireCC(t)
	dir := ffiLib(t, "cstructs")
	ffi := filepath.Join(testsDir, "ffi")
	path := filepath.Join(ffi, "cstructs.mlc")
	exe := filepath.Join(t.TempDir(), "prog")
	opts := Options{LibDirs: []string{dir}, HeaderDirs: []string{ffi}, Sanitize: sanitize}
	if err := BuildOpts("cstructs.mlc", []byte(readExpected(t, path)), exe, dirLoader(path), opts); err != nil {
		t.Fatalf("build failed:\n%v", err)
	}
	out, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := readExpected(t, filepath.Join(ffi, "cstructs.out")); string(out) != want {
		t.Errorf("output:\n%s\nexpected:\n%s", out, want)
	}

	wrong := `import "std/io"

extern "C" struct cs_point_t from "cstructs.h" {
    x int32
    z int32
}

fn main() {
    var p cs_point_t
    p.z = 1
    io.print(p.x)
}
`
	err = BuildOpts("wrong.mlc", []byte(wrong), filepath.Join(t.TempDir(), "w"), dirLoader(path), opts)
	if err == nil || !strings.Contains(err.Error(), "the C compiler refused the program") || !strings.Contains(err.Error(), "wrong.mlc:10") {
		t.Errorf("a field the header doesn't have: %v", err)
	}
}

// TestFFICallbacks links tests/ffi/callbacks.mlc and callback_overflow.mlc
// to tests/ffi/callbacks.c, which calls back during a call and, later, from
// threads it creates: the kept callback gets its context, the per-thread
// buffers are freed in each of C's threads, and a frame too large for C's
// stack is a panic.
func TestFFICallbacks(t *testing.T) {
	requireCC(t)
	dir := ffiLib(t, "callbacks")
	build := func(name string) string {
		path := filepath.Join(testsDir, "ffi", name)
		exe := filepath.Join(t.TempDir(), "prog")
		if err := BuildOpts(name, []byte(readExpected(t, path)), exe, dirLoader(path), Options{LibDirs: []string{dir}, Sanitize: sanitize}); err != nil {
			t.Fatalf("%s: build failed:\n%v", name, err)
		}
		return exe
	}
	out, err := exec.Command(build("callbacks.mlc")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := readExpected(t, filepath.Join(testsDir, "ffi", "callbacks.out")); string(out) != want {
		t.Errorf("output:\n%s\nexpected:\n%s", out, want)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(build("callback_overflow.mlc"))
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("callback_overflow: %v, expected the panic's exit code 2\nstderr:\n%s", err, stderr.String())
	}
	if want := "callback_overflow.mlc:11: stack overflow"; !strings.Contains(stderr.String(), want) || !strings.Contains(stderr.String(), "the thread is C's") {
		t.Errorf("callback_overflow stderr:\n%s\nexpected %q and the hint for C's thread", stderr.String(), want)
	}
	if stdout.Len() > 0 {
		t.Errorf("callback_overflow stdout: %q", stdout.String())
	}
}

// TestCCFlags checks -cflags, -ldflags and the variables CFLAGS and LDFLAGS.
func TestCCFlags(t *testing.T) {
	requireCC(t)
	src := []byte("import \"std/io\"\n\nfn main() {\n    io.print(42)\n}\n")
	exe := filepath.Join(t.TempDir(), "prog")
	run := func() string {
		out, err := exec.Command(exe).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}

	if err := BuildOpts("p.mlc", src, exe, os.ReadFile, Options{CFlags: SplitFlags("-O0 -g -DMLC_FLAG=1")}); err != nil {
		t.Fatalf("valid -cflags: %v", err)
	}
	if got := run(); got != "42\n" {
		t.Errorf("output %q", got)
	}

	err := BuildOpts("p.mlc", src, exe, os.ReadFile, Options{CFlags: []string{"-fdoes-not-exist-mlc"}})
	if err == nil || !strings.Contains(err.Error(), "refused the compilation with the flags") || strings.HasPrefix(err.Error(), "internal error:") {
		t.Errorf("an invalid flag should point at the flag, not an internal error: %v", err)
	}

	err = BuildOpts("p.mlc", src, exe, os.ReadFile, Options{LDFlags: []string{"-ldoes-not-exist-mlc"}})
	var le *LinkError
	if !errors.As(err, &le) || !strings.Contains(err.Error(), "link flags (-ldoes-not-exist-mlc)") {
		t.Errorf("invalid ldflag: %v", err)
	}

	t.Setenv("CFLAGS", "-fdoes-not-exist-env")
	err = BuildOpts("p.mlc", src, exe, os.ReadFile, Options{})
	if err == nil || !strings.Contains(err.Error(), "-fdoes-not-exist-env") {
		t.Errorf("the environment's CFLAGS should apply: %v", err)
	}
	t.Setenv("CFLAGS", "")

	// the library's folder through -ldflags, instead of -L
	dir := ffiLib(t, "counter")
	path := filepath.Join(testsDir, "ffi", "out_handle.mlc")
	if err := BuildOpts(filepath.Base(path), []byte(readExpected(t, path)), exe, dirLoader(path), Options{LDFlags: []string{"-L" + dir}}); err != nil {
		t.Fatalf("-ldflags -L: %v", err)
	}
	if got, want := run(), readExpected(t, filepath.Join(testsDir, "ffi", "out_handle.out")); got != want {
		t.Errorf("output:\n%s\nexpected:\n%s", got, want)
	}

	// sanitizers through both options: the executable has AddressSanitizer
	asan := Options{CFlags: []string{"-fsanitize=address"}, LDFlags: []string{"-fsanitize=address"}}
	if err := BuildOpts("p.mlc", src, exe, os.ReadFile, asan); err != nil {
		t.Skipf("the C compiler doesn't have AddressSanitizer: %v", err)
	}
	if got := run(); got != "42\n" {
		t.Errorf("with AddressSanitizer: %q", got)
	}
	bin, _ := os.ReadFile(exe)
	if !bytes.Contains(bin, []byte("asan")) {
		t.Errorf("the executable should have AddressSanitizer")
	}
}
