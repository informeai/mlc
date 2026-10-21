package driver

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"mlc/internal/checker"
	"mlc/std"
)

// The ESP32 target is tested on this machine with a simulation of ESP-IDF
// (testdata/esp32sim): FreeRTOS tasks become pthreads with the asked stack,
// and the runtime is compiled as ESP-IDF would compile it (ESP_PLATFORM
// defined). That checks that the embedded runtime compiles without the
// desktop parts, that it only calls what ESP-IDF has (TestESP32Symbols) and
// that the programs behave the same (TestESP32Sim). It doesn't replace a
// build with ESP-IDF's GCC on the chip.

const esp32Sim = "testdata/esp32sim"

// esp32Stdout is the output that changes on the chip: the main task has
// 32 KB of stack, not 8 MB, so the first deep(100) (4 KB per call) already
// stops with the panic.
var esp32Stdout = map[string]string{
	"stack_recursion.mlc": "",
}

// esp32Objs compiles, once, what is the same in every program: the runtime,
// mlc_main.c and the simulation.
var esp32Objs struct {
	once sync.Once
	dir  string
	objs []string
	err  string
}

func esp32Common(t *testing.T) []string {
	t.Helper()
	esp32Objs.once.Do(func() {
		dir, err := os.MkdirTemp("", "mlc-esp32-")
		if err != nil {
			esp32Objs.err = err.Error()
			return
		}
		esp32Objs.dir = dir
		comp := filepath.Join(dir, "comp")
		src := []byte("fn main() {\n}\n")
		if err := BuildESP32("empty.mlc", src, comp, func(string) ([]byte, error) { return nil, os.ErrNotExist }, Options{}); err != nil {
			esp32Objs.err = err.Error()
			return
		}
		for _, f := range []string{"mlc_runtime", "mlc_main"} {
			obj := filepath.Join(dir, f+".o")
			if out, err := simCompile(obj, filepath.Join(comp, f+".c")); err != nil {
				esp32Objs.err = out
				return
			}
			esp32Objs.objs = append(esp32Objs.objs, obj)
		}
		obj := filepath.Join(dir, "sim.o")
		if out, err := simCompile(obj, filepath.Join(esp32Sim, "sim.c")); err != nil {
			esp32Objs.err = out
			return
		}
		esp32Objs.objs = append(esp32Objs.objs, obj)
	})
	if esp32Objs.err != "" {
		t.Fatal(esp32Objs.err)
	}
	return esp32Objs.objs
}

// simCompile compiles a C file of the simulated component into obj, as ESP-IDF
// would (ESP_PLATFORM), with -Werror like the other tests.
func simCompile(obj, src string, extra ...string) (string, error) {
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	args := append([]string{"-std=c11", "-O2", "-Wall", "-Werror", "-Wno-unused-function", "-DESP_PLATFORM", "-I", esp32Sim, "-I", filepath.Dir(src)}, extra...)
	args = append(args, "-c", "-o", obj, src)
	out, err := exec.Command(cc, args...).CombinedOutput()
	return src + ": " + string(out), err
}

// TestESP32Sim: each tests/run and tests/panic program that doesn't need what
// the ESP32 lacks becomes a component, runs in the simulation and must give
// the same output and exit code as on this machine.
func TestESP32Sim(t *testing.T) {
	requireCC(t)
	common := esp32Common(t)
	ran := 0
	for _, kind := range []string{"run", "panic"} {
		for _, path := range programs(kind) {
			base := strings.TrimSuffix(path, ".mlc")
			if _, err := os.Stat(base + ".args"); err == nil {
				continue // on the chip, argv is just the program's name
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			comp := filepath.Join(t.TempDir(), "comp")
			err = BuildESP32(filepath.Base(path), src, comp, dirLoader(path), Options{})
			if err != nil {
				if strings.Contains(err.Error(), "not available with -target esp32") || strings.HasPrefix(err.Error(), "link ") {
					continue
				}
				t.Fatalf("%s: %v", path, err)
			}
			ran++
			t.Run(kind+"/"+testName(path), func(t *testing.T) {
				dir := t.TempDir()
				app := filepath.Join(dir, "app.o")
				if out, err := simCompile(app, filepath.Join(comp, "mlc_app.c"), "-Dmain=mlc_main"); err != nil {
					t.Fatal(out)
				}
				exe := filepath.Join(dir, "prog")
				cc := os.Getenv("CC")
				if cc == "" {
					cc = "cc"
				}
				args := append([]string{"-pthread", "-o", exe, app}, common...)
				if out, err := exec.Command(cc, append(args, "-lm")...).CombinedOutput(); err != nil {
					t.Fatalf("link: %v\n%s", err, out)
				}
				var out, errOut bytes.Buffer
				cmd := exec.Command(exe)
				cmd.Stdout, cmd.Stderr = &out, &errOut
				if in, err := os.ReadFile(base + ".in"); err == nil {
					cmd.Stdin = bytes.NewReader(in)
				}
				code := 0
				if err := cmd.Run(); err != nil {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) {
						t.Fatal(err)
					}
					code = exitErr.ExitCode()
				}
				want := 0
				if kind == "panic" {
					want = 2
					if w := readExpected(t, base+".err"); errOut.String() != w {
						t.Errorf("stderr:\n%q\nexpected:\n%q", errOut.String(), w)
					}
				}
				if code != want {
					t.Fatalf("exit code %d, expected %d; stderr:\n%s", code, want, errOut.String())
				}
				w, ok := esp32Stdout[filepath.Base(path)]
				if !ok {
					w = readExpected(t, base+".out")
				}
				if out.String() != w {
					t.Errorf("stdout:\n%q\nexpected:\n%q", out.String(), w)
				}
			})
		}
	}
	if ran < 75 {
		t.Errorf("only %d programs ran in the simulation", ran)
	}
}

// esp32Allowed is what the ESP32 runtime may call: newlib (ESP-IDF's C
// library), its pthread (over FreeRTOS) and two ESP-IDF functions. A call
// to something else (dlopen, socket, posix_spawn...) wouldn't link on the chip.
var esp32Allowed = map[string]bool{
	"atan2": true, "atoi": true, "atoll": true, "calloc": true, "ceil": true, "clock_gettime": true, "close": true,
	"cos": true, "difftime": true, "exit": true, "exp": true, "ferror": true, "fflush": true, "floor": true,
	"fprintf": true, "fputc": true, "fputs": true, "free": true, "fwrite": true, "getchar": true, "getenv": true,
	"gmtime_r": true, "localtime_r": true, "log": true, "memcmp": true, "memcpy": true, "memmove": true,
	"memset": true, "mktime": true, "nanosleep": true, "open": true, "pow": true, "putchar": true, "read": true,
	"realloc": true, "round": true, "setenv": true, "sin": true, "snprintf": true, "sqrt": true, "strchr": true,
	"strcmp": true, "strerror": true, "strftime": true, "strlen": true, "strtod": true, "tan": true,
	"vfprintf": true, "write": true,
	"esp_fill_random": true, "pxTaskGetStackStart": true,
}

// esp32Host is what this machine's compiler adds on its own (stack and
// buffer checks, the names of stdin/stdout/stderr, errno), which isn't a call of the runtime.
func esp32Host(name string) bool {
	switch name {
	case "bzero", "stdin", "stdout", "stderr":
		return true
	}
	return strings.HasPrefix(name, "_")
}

// TestESP32Symbols: the runtime compiled for the ESP32 only calls what
// ESP-IDF has.
func TestESP32Symbols(t *testing.T) {
	requireCC(t)
	if _, err := exec.LookPath("nm"); err != nil {
		t.Skip("nm not found")
	}
	common := esp32Common(t)
	out, err := exec.Command("nm", "-u", common[0]).Output()
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, f := range strings.Fields(string(out)) {
		if f == "U" {
			continue
		}
		if runtime.GOOS == "darwin" {
			f = strings.TrimPrefix(f, "_")
		}
		if esp32Host(f) || esp32Allowed[f] || strings.HasPrefix(f, "pthread_") {
			continue
		}
		bad = append(bad, f)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("the ESP32 runtime calls what ESP-IDF doesn't have (or what isn't in esp32Allowed yet): %s", strings.Join(bad, " "))
	}
}

// TestESP32Externs: every extern fn of a module the ESP32 has is defined in
// the embedded runtime; one left in a desktop part would only fail when ESP-IDF links.
func TestESP32Externs(t *testing.T) {
	requireCC(t)
	if _, err := exec.LookPath("nm"); err != nil {
		t.Skip("nm not found")
	}
	common := esp32Common(t)
	out, err := exec.Command("nm", "-g", common[0]).Output()
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[1] == "T" {
			defined[strings.TrimPrefix(f[2], "_")] = true
		}
	}
	files, _ := fs.Glob(std.FS, "*.mlc")
	ext := regexp.MustCompile(`(?m)^(?:pub )?extern fn ([a-z_0-9]+)`)
	checked := 0
	for _, file := range files {
		mod := strings.TrimSuffix(file, ".mlc")
		if strings.HasSuffix(mod, "_test") {
			continue
		}
		src, _ := fs.ReadFile(std.FS, file)
		if checkTarget(esp32Probe(mod), "esp32") != nil {
			continue // a module the ESP32 doesn't have
		}
		for _, m := range ext.FindAllStringSubmatch(string(src), -1) {
			checked++
			if sym := "mlc_std_" + mod + "_" + m[1]; !defined[sym] {
				t.Errorf("std/%s: %s isn't in the ESP32 runtime", mod, sym)
			}
		}
	}
	if checked < 100 {
		t.Errorf("only %d extern fn checked", checked)
	}
}

// esp32Probe is the sources of a program that imports std/mod, for checkTarget.
func esp32Probe(mod string) []checker.Source {
	srcs, errs := load("probe.mlc", []byte("import \"std/"+mod+"\"\n\nfn main() {\n}\n"), func(string) ([]byte, error) { return nil, os.ErrNotExist })
	if len(errs) > 0 {
		return nil
	}
	return srcs
}

// TestESP32Target: the component's files, the refused imports and the
// folder that isn't mlc's.
func TestESP32Target(t *testing.T) {
	noFiles := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	dir := filepath.Join(t.TempDir(), "main")
	if err := BuildESP32("app.mlc", []byte("import \"std/io\"\n\nfn main() {\n    io.print(1)\n}\n"), dir, noFiles, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"mlc_app.c", "mlc_runtime.c", "mlc_runtime.h", "mlc_main.c", "CMakeLists.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
	// generating again over its own folder works
	if err := BuildESP32("app.mlc", []byte("fn main() {\n}\n"), dir, noFiles, Options{}); err != nil {
		t.Errorf("generating again: %v", err)
	}

	for _, c := range []struct{ src, want string }{
		{"import \"std/fs\"\n\nfn main() {\n}\n", `app.mlc:1:1: import "std/fs": not available with -target esp32: std/fs needs files and directories`},
		{"import \"std/http\"\n\nfn main() {\n}\n", `import "std/http": not available with -target esp32 (it uses std/net): std/net needs BSD sockets`},
		{"import \"std/config\"\n\nfn main() {\n}\n", `(it uses std/fs)`},
	} {
		err := BuildESP32("app.mlc", []byte(c.src), filepath.Join(t.TempDir(), "x"), noFiles, Options{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %v, expected %q", c.src, err, c.want)
		}
	}

	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "CMakeLists.txt"), []byte("idf_component_register(SRCS \"main.c\")\n"), 0o644)
	if err := BuildESP32("app.mlc", []byte("fn main() {\n}\n"), other, noFiles, Options{}); err == nil || !strings.Contains(err.Error(), "wasn't written by mlc") {
		t.Errorf("overwrote another CMakeLists.txt: %v", err)
	}

	if _, err := CompileOpts("app.mlc", []byte("fn main() {\n}\n"), noFiles, Options{Target: "avr"}); err == nil || !strings.Contains(err.Error(), "the only target is esp32") {
		t.Errorf("unknown target: %v", err)
	}
}
