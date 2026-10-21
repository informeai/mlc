// Command mlc: compiler for the MLC language (My Lang C).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mlc/internal/doc"
	"mlc/internal/driver"
	"mlc/internal/format"
	"mlc/internal/lsp"
	"mlc/internal/token"
)

const usage = `mlc — compiler for the MLC language (My Lang C)

Usage:
  mlc build [-o out] [-emit-c] [-I dir] [-L dir] [-cflags "..."] [-ldflags "..."] [-sanitize address|thread] file.mlc
                                               compiles to an executable
  mlc build -target esp32 [-o folder] [-I dir] file.mlc
                                               writes an ESP-IDF component (docs/esp32.md)
  mlc run [-I dir] [-L dir] [-cflags "..."] [-ldflags "..."] file.mlc [arguments]
                                               compiles and runs
      -I: folder where to look for imports (besides the file's, lib/ and MLC_PATH)
      -L: folder where to look for the C libraries of link "name"
      -cflags "...": extra flags for compiling the C (-O3, -march=native, -fsanitize=address)
      -ldflags "...": extra flags for linking (-framework Cocoa, -Wl,-rpath,/opt/lib)
      -sanitize address|thread: runs under AddressSanitizer + UndefinedBehaviorSanitizer,
          or under ThreadSanitizer (use after free, double free, data race...)
      (the variables CC, CFLAGS and LDFLAGS also apply; the options come after and win)
  mlc test [-v] [-run text] [-bench text] [-I dir] [-L dir] [-cflags "..."] [-ldflags "..."] [files or folders]
                                               runs the tests of the *_test.mlc files
                                               (default: the current folder and the ones inside)
      -v: shows each test and its output, even when it passes
      -run: only the tests whose name contains the text
      -bench text: also measures the benchmarks (fn bench_*(b &testing.B)); . for all
      -benchtime ms: how long to measure each benchmark (default 500)
  mlc fmt [-l] [files or folders]              formats in place (default: the current folder)
  mlc fmt -                                    formats standard input to standard output
  mlc doc [-I dir] [-all] [module[.symbol]] [symbol]
                                               shows the documentation in the terminal
      no arguments: the standard library's modules
      mlc doc http · mlc doc http.Message · mlc doc http.Message.read_body
      mlc doc util/text (a library of yours, looked up like the import) · mlc doc alloc
      -all: also shows what isn't pub
  mlc lsp                                      language server (LSP) for editors
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		os.Exit(build(os.Args[2:]))
	case "run":
		os.Exit(run(os.Args[2:]))
	case "test":
		os.Exit(testCmd(os.Args[2:]))
	case "fmt":
		os.Exit(fmtCmd(os.Args[2:]))
	case "doc":
		os.Exit(docCmd(os.Args[2:]))
	case "lsp":
		os.Exit(lsp.Run(os.Stdin, os.Stdout))
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "mlc: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func readSource(path string) ([]byte, error) {
	if filepath.Ext(path) != ".mlc" {
		return nil, fmt.Errorf("%s: the file must have the .mlc extension", path)
	}
	return os.ReadFile(path)
}

// readProgram is readSource for build and run: a _test.mlc is run with mlc test.
func readProgram(path string) ([]byte, error) {
	if driver.IsTestFile(path) {
		return nil, fmt.Errorf("%s: test file; run it with mlc test %s", path, path)
	}
	return readSource(path)
}

func build(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "path of the executable (default: the file's name without .mlc)")
	emitC := fs.Bool("emit-c", false, "prints the generated C instead of compiling")
	target := fs.String("target", "", "esp32: writes an ESP-IDF component into the -o folder instead of an executable")
	var libDirs, importDirs dirList
	fs.Var(&libDirs, "L", "folder where to look for C libraries (can repeat)")
	fs.Var(&importDirs, "I", "folder where to look for imports (can repeat)")
	cflags, ldflags, sanitize := ccFlags(fs)
	fs.Parse(args)
	opts := driver.Options{LibDirs: libDirs, ImportDirs: importDirs, CFlags: driver.SplitFlags(*cflags), LDFlags: driver.SplitFlags(*ldflags), Sanitize: *sanitize, Target: *target}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := fs.Arg(0)
	src, err := readProgram(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if *target != "" && !*emitC {
		if len(libDirs) > 0 || *cflags != "" || *ldflags != "" || *sanitize != "" {
			fmt.Fprintln(os.Stderr, "mlc build -target: -L, -cflags, -ldflags and -sanitize don't apply; ESP-IDF compiles the component")
			return 2
		}
		if *target != "esp32" {
			fmt.Fprintf(os.Stderr, "-target %q: the only target is esp32 (without -target, this machine)\n", *target)
			return 2
		}
		if *out == "" {
			*out = strings.TrimSuffix(filepath.Base(path), ".mlc") + "-esp32"
		}
		if err := driver.BuildESP32(path, src, *out, os.ReadFile, opts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	if *emitC {
		csrc, err := driver.CompileOpts(path, src, os.ReadFile, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(csrc)
		return 0
	}

	if *out == "" {
		*out = strings.TrimSuffix(filepath.Base(path), ".mlc")
	}
	if err := driver.BuildOpts(path, src, *out, os.ReadFile, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var libDirs, importDirs dirList
	fs.Var(&libDirs, "L", "folder where to look for C libraries (can repeat)")
	fs.Var(&importDirs, "I", "folder where to look for imports (can repeat)")
	cflags, ldflags, sanitize := ccFlags(fs)
	fs.Parse(args) // stops at the first argument that isn't an option: the file
	args = fs.Args()
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := args[0]
	src, err := readProgram(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir, err := os.MkdirTemp("", "mlc-run-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	exe := filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".mlc"))
	opts := driver.Options{LibDirs: libDirs, ImportDirs: importDirs, CFlags: driver.SplitFlags(*cflags), LDFlags: driver.SplitFlags(*ldflags), Sanitize: *sanitize}
	if err := driver.BuildOpts(path, src, exe, os.ReadFile, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cmd := exec.Command(exe, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// testCmd compiles each _test.mlc file into a test program and runs each
// test in a separate process. Returns 1 if any fails.
func testCmd(args []string) int {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	verbose := fs.Bool("v", false, "shows each test and its output")
	only := fs.String("run", "", "only the tests whose name contains the text")
	bench := fs.String("bench", "", "also runs the benchmarks whose name contains the text (. for all)")
	benchTime := fs.Int("benchtime", 500, "how long (ms) each benchmark is measured, at least")
	var libDirs, importDirs dirList
	fs.Var(&libDirs, "L", "folder where to look for C libraries (can repeat)")
	fs.Var(&importDirs, "I", "folder where to look for imports (can repeat)")
	cflags, ldflags, sanitize := ccFlags(fs)
	fs.Parse(args)
	opts := driver.Options{LibDirs: libDirs, ImportDirs: importDirs, CFlags: driver.SplitFlags(*cflags), LDFlags: driver.SplitFlags(*ldflags), Sanitize: *sanitize}
	bc := benchConfig{filter: *bench, target: time.Duration(*benchTime) * time.Millisecond}
	paths := fs.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d iofs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && driver.IsTestFile(path) {
				files = append(files, path)
			}
			return nil
		})
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "mlc test: no *_test.mlc file found")
		return 1
	}
	dir, err := os.MkdirTemp("", "mlc-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	status := 0
	for n, file := range files {
		if !testFile(file, filepath.Join(dir, fmt.Sprintf("t%d", n)), *only, *verbose, bc, opts) {
			status = 1
		}
	}
	return status
}

// benchConfig says which benchmarks to run (empty filter: none; ".": all)
// and for how long to measure each one.
type benchConfig struct {
	filter string
	target time.Duration
}

// testFile runs a file's tests (and the benchmarks, with -bench) and says
// whether they all passed.
func testFile(file, exe, only string, verbose bool, bc benchConfig, opts driver.Options) bool {
	src, err := readSource(file)
	if err == nil {
		var prog driver.TestProgram
		prog, err = driver.BuildTest(file, src, exe, os.ReadFile, opts)
		if err == nil {
			ok := len(prog.Tests) == 0 || runTests(file, exe, prog.Tests, only, verbose)
			if ok && bc.filter != "" {
				ok = runBenches(file, exe, prog.Benches, bc)
			}
			return ok
		}
	}
	fmt.Fprintln(os.Stderr, err)
	fmt.Printf("FAIL\t%s\t(didn't compile)\n", file)
	return false
}

func runTests(file, exe string, names []string, only string, verbose bool) bool {
	start := time.Now()
	ran, failed := 0, 0
	for i, name := range names {
		if only != "" && !strings.Contains(name, only) {
			continue
		}
		ran++
		if verbose {
			fmt.Printf("=== %s\n", name)
		}
		r := driver.RunTest(exe, i, name)
		if r.Status != driver.Passed {
			failed++
		}
		if r.Status == driver.Passed && !verbose {
			continue
		}
		label := "ok"
		if r.Status != driver.Passed {
			label = strings.ToUpper(r.Status.String())
		}
		fmt.Printf("--- %s: %s (%.2fs)\n", label, name, r.Elapsed.Seconds())
		for _, line := range strings.SplitAfter(string(r.Output), "\n") {
			if line != "" {
				fmt.Print("    ", line)
			}
		}
		if len(r.Output) > 0 && r.Output[len(r.Output)-1] != '\n' {
			fmt.Println()
		}
	}
	elapsed := time.Since(start).Seconds()
	switch {
	case failed > 0:
		fmt.Printf("FAIL\t%s\t%d of %s failed\t%.2fs\n", file, failed, plural(ran, "test", "tests"), elapsed)
		return false
	case ran == 0:
		fmt.Printf("ok\t%s\tno test matching %q\n", file, only)
	default:
		fmt.Printf("ok\t%s\t%s\t%.2fs\n", file, plural(ran, "test", "tests"), elapsed)
	}
	return true
}

// runBenches measures the benchmarks: each one runs with b.n = 1, 2, ... (one
// process per round, with the time measured inside the program) until the measurement
// passes bc.target, like go test -bench.
func runBenches(file, exe string, names []string, bc benchConfig) bool {
	ran := 0
	for i, name := range names {
		if bc.filter != "." && !strings.Contains(name, bc.filter) {
			continue
		}
		ran++
		n := 1
		for {
			r := driver.RunBench(exe, i, n)
			if r.Status != driver.Passed {
				fmt.Printf("--- %s: %s (b.n = %d)\n", strings.ToUpper(r.Status.String()), name, n)
				for _, line := range strings.SplitAfter(string(r.Output), "\n") {
					if strings.TrimSpace(line) != "" {
						fmt.Print("    ", line)
					}
				}
				fmt.Printf("FAIL\t%s\n", file)
				return false
			}
			if time.Duration(r.Ns) >= bc.target || n >= 1_000_000_000 {
				fmt.Printf("%s\t%12d times\t%14.1f ns/op\n", name, n, float64(r.Ns)/float64(n))
				break
			}
			next := n * 100
			if r.Ns > 0 {
				// predicts the n that reaches the time, with a margin, without jumping more than 100x
				next = int(float64(n) * float64(bc.target) / float64(r.Ns) * 1.2)
				next = min(max(next, n+1), n*100)
			}
			n = min(next, 1_000_000_000)
		}
	}
	if ran == 0 {
		fmt.Printf("ok\t%s\tno benchmark matching %q\n", file, bc.filter)
	}
	return true
}

// docCmd shows the documentation of a module, of one of its symbols or of a
// built-in, taken from the // comments above each declaration.
func docCmd(args []string) int {
	fs := flag.NewFlagSet("doc", flag.ExitOnError)
	all := fs.Bool("all", false, "also shows what isn't pub")
	var importDirs dirList
	fs.Var(&importDirs, "I", "folder where to look for imports (can repeat)")
	fs.Parse(args)
	switch {
	case fs.NArg() == 0:
		doc.WriteIndex(os.Stdout)
		return 0
	case fs.NArg() == 1 && (fs.Arg(0) == "builtin" || fs.Arg(0) == "builtins"):
		doc.WriteBuiltins(os.Stdout)
		return 0
	}
	q, err := doc.Lookup(fs.Args(), driver.SearchDirs(".", importDirs), *all)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mlc doc:", err)
		return 1
	}
	q.Write(os.Stdout)
	return 0
}

// fmtCmd formats .mlc files in place. With -l, it only lists the ones that would change.
func fmtCmd(args []string) int {
	fs := flag.NewFlagSet("fmt", flag.ExitOnError)
	list := fs.Bool("l", false, "only lists the files whose formatting differs, without changing them")
	fs.Parse(args)
	paths := fs.Args()
	if len(paths) == 1 && paths[0] == "-" {
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		out, errs := format.Source("<stdin>", src)
		if len(errs) > 0 {
			printErrors(errs)
			return 1
		}
		os.Stdout.Write(out)
		return 0
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d iofs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Ext(path) == ".mlc" {
				files = append(files, path)
			}
			return nil
		})
	}
	status := 0
	for _, file := range files {
		src, err := readSource(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			status = 1
			continue
		}
		out, errs := format.Source(file, src)
		if len(errs) > 0 {
			printErrors(errs)
			status = 1
			continue
		}
		if bytes.Equal(src, out) {
			continue
		}
		if *list {
			fmt.Println(file)
			continue
		}
		if err := os.WriteFile(file, out, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			status = 1
		}
	}
	return status
}

func printErrors(errs []token.Error) {
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, e)
	}
}

// ccFlags registers -cflags and -ldflags: extra flags for the C compiler,
// after mlc's and the variables CFLAGS and LDFLAGS.
func ccFlags(fs *flag.FlagSet) (cflags, ldflags, sanitize *string) {
	cflags = fs.String("cflags", "", "extra flags for compiling the C (separated by spaces), after mlc's and CFLAGS")
	ldflags = fs.String("ldflags", "", "extra flags for linking the program (separated by spaces), after LDFLAGS")
	sanitize = fs.String("sanitize", "", "compiles with sanitizers: address (AddressSanitizer and UndefinedBehaviorSanitizer) or thread (ThreadSanitizer)")
	return
}

// dirList gathers the repeated -L.
type dirList []string

func (d *dirList) String() string { return strings.Join(*d, ",") }

func (d *dirList) Set(v string) error {
	*d = append(*d, v)
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
