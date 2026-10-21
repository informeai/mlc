// Package driver chains the compiler's phases and calls the C compiler.
package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"mlc/internal/ast"
	"mlc/internal/checker"
	"mlc/internal/codegen"
	"mlc/internal/token"
	"mlc/rt"
)

// Errors is the list of compile errors of a file.
type Errors []token.Error

func (e Errors) Error() string {
	lines := make([]string, len(e))
	for i, err := range e {
		lines[i] = err.Error()
	}
	return strings.Join(lines, "\n")
}

// Compile translates the program whose main file is file into C. The imports
// are read from the disk, relative to file's folder.
func Compile(file string, src []byte) (string, error) {
	return CompileWith(file, src, os.ReadFile)
}

// CompileWith is Compile with another way of reading the imported files.
func CompileWith(file string, src []byte, read Loader) (string, error) {
	csrc, _, err := compile(file, src, read, Options{})
	return csrc, err
}

// CompileOpts is CompileWith with options (the -I folders).
func CompileOpts(file string, src []byte, read Loader, opts Options) (string, error) {
	csrc, _, err := compile(file, src, read, opts)
	return csrc, err
}

// compile translates the program into C and also returns the C libraries
// asked for with link "name".
func compile(file string, src []byte, read Loader, opts Options) (string, []string, error) {
	csrc, info, err := compileInfo(file, src, read, opts)
	if err != nil {
		return "", nil, err
	}
	return csrc, info.Links, nil
}

// compileInfo is compile returning the check's result. A
// _test.mlc file becomes a test program (see BuildTest).
func compileInfo(file string, src []byte, read Loader, opts Options) (string, *checker.Info, error) {
	srcs, errs := load(file, src, read, opts.ImportDirs...)
	if len(errs) > 0 {
		return "", nil, Errors(errs)
	}
	if opts.Target != "" && opts.Target != "esp32" {
		return "", nil, fmt.Errorf("-target %q: the only target is esp32 (without -target, this machine)", opts.Target)
	}
	if errs := checkTarget(srcs, opts.Target); len(errs) > 0 {
		return "", nil, Errors(errs)
	}
	srcs[len(srcs)-1].Test = IsTestFile(file)
	info, errs := checker.CheckProgram(srcs)
	if len(errs) > 0 {
		return "", nil, Errors(errs)
	}
	return codegen.Generate(file, srcs[len(srcs)-1].AST, info), info, nil
}

// Check checks the program (syntax, types and ownership) without generating C, and returns the
// main file's AST and the checker's result.
func Check(file string, src []byte, read Loader, opts Options) (*ast.File, *checker.Info, error) {
	srcs, errs := load(file, src, read, opts.ImportDirs...)
	if len(errs) > 0 {
		return nil, nil, Errors(errs)
	}
	srcs[len(srcs)-1].Test = IsTestFile(file)
	info, errs := checker.CheckProgram(srcs)
	if len(errs) > 0 {
		return nil, nil, Errors(errs)
	}
	return srcs[len(srcs)-1].AST, info, nil
}

// IsTestFile says whether file is a test file (name_test.mlc): instead of
// main, it declares fn test_*() functions, which mlc test runs.
func IsTestFile(file string) bool {
	return strings.HasSuffix(filepath.Base(file), "_test.mlc")
}

// extraCFlags is added to the C compiler's line. The tests use
// -Werror: a warning in the generated C is an mlc bug.
var extraCFlags []string

// Options adjusts the building of the executable.
type Options struct {
	LibDirs    []string // -L: folders where to look for the C libraries of link "name"
	ImportDirs []string // -I: folders where to look for imports (besides lib/ and MLC_PATH)
	CFlags     []string // -cflags: extra flags for compiling the C (after the environment's, CFLAGS)
	LDFlags    []string // -ldflags: extra flags for linking (after the environment's, LDFLAGS)
	Sanitize   string   // -sanitize: "address" (ASan + UBSan) or "thread" (TSan); "" without sanitizers
	Target     string   // -target: "esp32" (an ESP-IDF component, see BuildESP32); "" for this machine
}

// SanitizeFlags returns a sanitizer's compile and link flags:
// address turns on AddressSanitizer (use after free, double free, out-of-bounds
// access) and UndefinedBehaviorSanitizer (C undefined behavior,
// which ends the program instead of just warning); thread turns on
// ThreadSanitizer (data races). The two can't go together.
func SanitizeFlags(kind string) (cflags, ldflags []string, err error) {
	switch kind {
	case "":
		return nil, nil, nil
	case "address":
		f := []string{"-fsanitize=address,undefined", "-fno-sanitize-recover=all", "-fno-omit-frame-pointer", "-g"}
		return f, []string{"-fsanitize=address,undefined"}, nil
	case "thread":
		return []string{"-fsanitize=thread", "-g"}, []string{"-fsanitize=thread"}, nil
	}
	return nil, nil, fmt.Errorf("-sanitize %q: use address (AddressSanitizer and UndefinedBehaviorSanitizer) or thread (ThreadSanitizer)", kind)
}

// SplitFlags splits a list of flags written as text ("-O3
// -march=native") by spaces. There are no quotes: a flag with a space inside
// doesn't fit here.
func SplitFlags(s string) []string {
	return strings.Fields(s)
}

// Build compiles src into an executable at out. The C compiler is the one in the
// CC variable, or cc. The C flags are mlc's, then those of the variables
// CFLAGS and LDFLAGS, then those of Options (which, since they come last, win
// in a contradiction, like -O3 after mlc's -O2).
func Build(file string, src []byte, out string) error {
	return BuildWith(file, src, out, os.ReadFile)
}

// BuildWith is Build with another way of reading the imported files.
func BuildWith(file string, src []byte, out string, read Loader) error {
	return BuildOpts(file, src, out, read, Options{})
}

// LinkError is a failure linking the program: a C library (link
// "name") or a symbol (extern "C" fn) that wasn't found. It's an error of the
// program, not of mlc.
type LinkError struct {
	Output string
	Flags  string // the link flags of whoever compiles (LDFLAGS and -ldflags), if any
}

func (e *LinkError) Error() string {
	msg := "couldn't link the program: a C library (link \"...\") or a C function (extern \"C\" fn) wasn't found"
	if e.Flags != "" {
		msg += ", or one of the link flags (" + e.Flags + ") isn't valid for this compiler"
	}
	return msg + "\n" + e.Output
}

// BuildOpts is BuildWith with options. The C is compiled in two steps:
// compilation (a failure there is an mlc bug, because the checker accepted the
// program) and linking with the libraries (a failure there is the program's).
func BuildOpts(file string, src []byte, out string, read Loader, opts Options) error {
	csrc, info, err := compileInfo(file, src, read, opts)
	if err != nil {
		return err
	}
	return buildC(csrc, info.Links, out, opts)
}

// TestProgram is the names of the tests and benchmarks of a test
// program, in code order.
type TestProgram struct {
	Tests   []string
	Benches []string
}

// BuildTest compiles the test file file into an executable at out. The
// executable runs the test of index i with the argument i (see RunTest) and the
// benchmark i with "bench i n" (see RunBench).
func BuildTest(file string, src []byte, out string, read Loader, opts Options) (TestProgram, error) {
	var p TestProgram
	if !IsTestFile(file) {
		return p, fmt.Errorf("%s: a test file ends in _test.mlc", file)
	}
	csrc, info, err := compileInfo(file, src, read, opts)
	if err != nil {
		return p, err
	}
	if err := buildC(csrc, info.Links, out, opts); err != nil {
		return p, err
	}
	for _, fn := range info.Tests {
		p.Tests = append(p.Tests, fn.Name)
	}
	for _, fn := range info.Benches {
		p.Benches = append(p.Benches, fn.Name)
	}
	return p, nil
}

// buildC compiles and links the generated C with the runtime.
func buildC(csrc string, links []string, out string, opts Options) error {

	dir, err := os.MkdirTemp("", "mlc-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	files := map[string]string{
		"program.c":     csrc,
		"mlc_runtime.h": rt.Header,
		"mlc_runtime.c": rt.Source,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}

	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	// the flags of whoever compiles: from the environment, then from the command line
	userC := append(SplitFlags(os.Getenv("CFLAGS")), opts.CFlags...)
	userLD := append(SplitFlags(os.Getenv("LDFLAGS")), opts.LDFlags...)
	sanC, sanLD, err := SanitizeFlags(opts.Sanitize)
	if err != nil {
		return err
	}
	userC = append(sanC, userC...)
	userLD = append(sanLD, userLD...)
	var objs []string
	for _, name := range []string{"program", "mlc_runtime"} {
		obj := filepath.Join(dir, name+".o")
		args := append([]string{"-std=c11", "-O2", "-Wall", "-Wno-unused-function", "-pthread"}, extraCFlags...)
		args = append(args, userC...)
		args = append(args, "-I", dir, "-c", "-o", obj, filepath.Join(dir, name+".c"))
		output, err := exec.Command(cc, args...).CombinedOutput()
		if err != nil {
			if len(userC) > 0 {
				// with the compiler user's flags, the failure may be theirs
				return fmt.Errorf("the C compiler (%s) refused the compilation with the flags %q (from CFLAGS and -cflags): check that they exist in this compiler; without them, it would be an internal mlc error\n%s", cc, strings.Join(userC, " "), output)
			}
			// If the checker accepted the program, the generated C should compile:
			// a failure here is an mlc bug, not a bug in the user's program.
			return fmt.Errorf("internal error: %s rejected the generated code: %v\n%s", cc, err, output)
		}
		if len(output) > 0 {
			if len(userC) > 0 {
				fmt.Fprintf(os.Stderr, "mlc: C compiler warning (with the flags %q):\n%s", strings.Join(userC, " "), output)
			} else {
				fmt.Fprintf(os.Stderr, "mlc: C compiler warning (probably an mlc bug):\n%s", output)
			}
		}
		objs = append(objs, obj)
	}
	args := append([]string{"-pthread", "-o", out}, objs...)
	for _, d := range opts.LibDirs {
		args = append(args, "-L", d)
	}
	args = append(args, userLD...)
	for _, l := range links {
		args = append(args, "-l"+l)
	}
	args = append(args, "-lm") // libm, for std/math
	if runtime.GOOS == "linux" {
		args = append(args, "-ldl") // dlopen, for std/sqlite (old glibc)
	}
	if output, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
		return &LinkError{Output: string(output), Flags: strings.Join(userLD, " ")}
	}
	return nil
}
