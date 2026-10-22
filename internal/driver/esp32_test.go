package driver

import (
	"bytes"
	"debug/elf"
	"debug/macho"
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
// mlc_main.c, mlc_wifi.c and the simulation (in that order: esp32Common()[0]
// is the runtime, [2] std/wifi).
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
		for _, f := range []string{"mlc_runtime", "mlc_main", "mlc_wifi"} {
			obj := filepath.Join(dir, f+".o")
			if out, err := simCompile(obj, filepath.Join(comp, f+".c")); err != nil {
				esp32Objs.err = out
				return
			}
			esp32Objs.objs = append(esp32Objs.objs, obj)
		}
		for _, f := range []string{"sim", "sim_wifi"} {
			obj := filepath.Join(dir, f+".o")
			if out, err := simCompile(obj, filepath.Join(esp32Sim, f+".c")); err != nil {
				esp32Objs.err = out
				return
			}
			esp32Objs.objs = append(esp32Objs.objs, obj)
		}
	})
	if esp32Objs.err != "" {
		t.Fatal(esp32Objs.err)
	}
	return esp32Objs.objs
}

// simCompile compiles a C file of the simulated component into obj, as ESP-IDF
// would (ESP_PLATFORM, and the _GNU_SOURCE and -Wtype-limits of its build), with
// -Werror like the other tests.
func simCompile(obj, src string, extra ...string) (string, error) {
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	args := append([]string{"-std=c11", "-O2", "-Wall", "-Werror", "-Wno-unused-function", "-Wtype-limits", "-DESP_PLATFORM", "-D_GNU_SOURCE", "-I", esp32Sim, "-I", filepath.Dir(src)}, extra...)
	args = append(args, "-c", "-o", obj, src)
	out, err := exec.Command(cc, args...).CombinedOutput()
	return src + ": " + string(out), err
}

// simBuild compiles the component's program (mlc_app.c) for the simulation
// and links it with the runtime and sim.c; returns the executable.
func simBuild(t *testing.T, comp string, common []string) string {
	t.Helper()
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
	return exe
}

// TestESP32TLS: std/http reaches std/tls, which on the ESP32 has no
// OpenSSL: HTTPS answers tls.UNAVAILABLE (5003), with the explanation, and
// the program goes on.
func TestESP32TLS(t *testing.T) {
	requireCC(t)
	common := esp32Common(t)
	src := `import "std/http"
import "std/io"
import "std/url"

fn main() {
    var u url.URL
    if !url.parse_str("https://example.com/", &u) {
        return
    }
    err := 0
    c := http.dial_tls(&u, &err)
    if c != nil {
        io.print("connected")
        c.close()
        return
    }
    io.print("dial_tls:", err)
}
`
	comp := filepath.Join(t.TempDir(), "comp")
	if err := BuildESP32("app.mlc", []byte(src), comp, func(string) ([]byte, error) { return nil, os.ErrNotExist }, Options{}); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(simBuild(t, comp, common)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if string(out) != "dial_tls: 5003\n" {
		t.Errorf("output %q, expected tls.UNAVAILABLE (5003)", out)
	}
}

// TestESP32WiFi: std/wifi against the networks of sim_wifi.c (a refused
// password, a name nobody answers, a silent network, two that connect),
// connecting again over a connection, the name too long, and the driver
// stopped and started again. The simulation writes "sim:" on stderr when
// mlc_wifi.c uses ESP-IDF out of its order.
func TestESP32WiFi(t *testing.T) {
	requireCC(t)
	common := esp32Common(t)
	src := `import "std/io"
import "std/wifi"

fn main() {
    io.print("available:", wifi.available())
    err := 0
    ok := wifi.connect_str("mlc-sim", "wrong", 5000, &err)
    io.print("wrong password:", ok, err == wifi.AUTH)
    ok = wifi.connect_str("nobody-here", "x", 200, &err)
    io.print("unknown network:", ok, err == wifi.NOT_FOUND)
    ok = wifi.connect_str("mlc-silent", "", 200, &err)
    io.print("silent network:", ok, err == wifi.TIMEOUT)
    var ip [16]byte
    io.print("not connected:", wifi.connected(), wifi.ip(&ip), wifi.rssi())

    ok = wifi.connect_str("mlc-sim", "secret123", 5000, &err)
    n := wifi.ip(&ip)
    io.printf("mlc-sim: {} {} {:s} {}\n", ok, err, &ip[:n], wifi.rssi())
    ok = wifi.connect_str("mlc-open", "", 5000, &err)
    n = wifi.ip(&ip)
    io.printf("mlc-open: {} {:s} {}\n", ok, &ip[:n], wifi.rssi())

    var long [40]byte
    ok = wifi.connect(&long, &long[:0], 1000, &err)
    io.print("long name:", ok, err == wifi.BAD_NAME, wifi.connected())
    ok = wifi.connect(&long[:0], &long[:0], 1000, &err)
    io.print("empty name:", ok, err == wifi.BAD_NAME)

    wifi.disconnect()
    io.print("disconnected:", wifi.connected(), wifi.ip(&ip), wifi.rssi())
    wifi.disconnect()
    ok = wifi.connect_str("mlc-sim", "secret123", 5000, &err)
    io.print("again:", ok, wifi.connected())
    wifi.disconnect()
}
`
	want := `available: true
wrong password: false true
unknown network: false true
silent network: false true
not connected: false 0 0
mlc-sim: true 0 192.168.4.2 -48
mlc-open: true 192.168.4.3 -71
long name: false true true
empty name: false true
disconnected: false 0 0
again: true true
`
	comp := filepath.Join(t.TempDir(), "comp")
	if err := BuildESP32("app.mlc", []byte(src), comp, func(string) ([]byte, error) { return nil, os.ErrNotExist }, Options{}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command(simBuild(t, comp, common))
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	if out.String() != want {
		t.Errorf("stdout:\n%s\nexpected:\n%s", out.String(), want)
	}
	if errOut.Len() > 0 {
		t.Errorf("stderr:\n%s", errOut.String())
	}
}

// esp32GCCFlags compile the generated C as ESP-IDF does (-Og, -Wall with
// -Werror=all, gnu17), with the component's options (rt/c/esp32/CMakeLists.txt).
var esp32GCCFlags = []string{"-std=gnu17", "-Og", "-Wall", "-Werror=all", "-Wno-unused-function", "-Wno-missing-braces", "-Dmain=mlc_main"}

// TestESP32Sim: each tests/run and tests/panic program that doesn't need what
// the ESP32 lacks becomes a component, runs in the simulation and must give
// the same output and exit code as on this machine. When ESP-IDF's GCC is in
// the PATH (after export.sh), the generated C also compiles with it, without
// warnings: ESP-IDF stops at any -Wall warning, and GCC gives some that
// clang doesn't.
func TestESP32Sim(t *testing.T) {
	requireCC(t)
	common := esp32Common(t)
	espGCC, _ := exec.LookPath("xtensa-esp32-elf-gcc")
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
				exe := simBuild(t, comp, common)
				if espGCC != "" {
					args := append(append([]string{}, esp32GCCFlags...), "-I", comp, "-c", "-o", filepath.Join(t.TempDir(), "app_xtensa.o"), filepath.Join(comp, "mlc_app.c"))
					if out, err := exec.Command(espGCC, args...).CombinedOutput(); err != nil {
						t.Fatalf("ESP-IDF's GCC: %v\n%s", err, out)
					}
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
// library), its pthread (over FreeRTOS), lwIP's sockets, a few ESP-IDF
// functions and, in mlc_wifi.c, the Wi-Fi driver. A call to something else (dlopen, posix_spawn, nanosleep...)
// wouldn't link on the chip.
var esp32Allowed = map[string]bool{
	"atan2": true, "atoi": true, "atoll": true, "calloc": true, "ceil": true, "clock_gettime": true, "close": true,
	"cos": true, "difftime": true, "exit": true, "exp": true, "ferror": true, "fflush": true, "floor": true,
	"fprintf": true, "fputc": true, "fputs": true, "free": true, "fwrite": true, "getchar": true, "getenv": true,
	"gmtime_r": true, "localtime_r": true, "log": true, "malloc": true, "memcmp": true, "memcpy": true, "memmove": true,
	"memset": true, "mktime": true, "open": true, "pow": true, "putchar": true, "read": true,
	"realloc": true, "round": true, "setenv": true, "sin": true, "snprintf": true, "sqrt": true, "strchr": true,
	"strcmp": true, "strerror": true, "strftime": true, "strlen": true, "strtod": true, "tan": true,
	"usleep": true, "vfprintf": true, "write": true,
	"esp_fill_random": true, "pxTaskGetStackStart": true,
	// std/net, over lwIP (its socket calls have the BSD names), and the
	// start of lwIP's thread
	"accept": true, "bind": true, "connect": true, "freeaddrinfo": true, "getaddrinfo": true,
	"getsockname": true, "listen": true, "poll": true, "recv": true, "send": true,
	"setsockopt": true, "shutdown": true, "socket": true, "esp_netif_init": true,
	// std/wifi (mlc_wifi.c): NVS, the default event loop and the driver
	"nvs_flash_init": true, "nvs_flash_erase": true, "esp_event_loop_create_default": true,
	"esp_event_handler_instance_register": true, "esp_event_handler_instance_unregister": true,
	"esp_netif_create_default_wifi_sta": true, "esp_netif_destroy_default_wifi": true,
	"esp_wifi_init": true, "esp_wifi_deinit": true, "esp_wifi_set_mode": true, "esp_wifi_start": true,
	"esp_wifi_stop": true, "esp_wifi_set_config": true, "esp_wifi_connect": true, "esp_wifi_disconnect": true,
	"esp_wifi_sta_get_ap_info": true, "WIFI_EVENT": true, "IP_EVENT": true,
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

// TestESP32Symbols: the runtime and mlc_wifi.c compiled for the ESP32 only
// call what ESP-IDF has (mlc_wifi.c may also call the runtime).
func TestESP32Symbols(t *testing.T) {
	requireCC(t)
	if _, err := exec.LookPath("nm"); err != nil {
		t.Skip("nm not found")
	}
	common := esp32Common(t)
	inRuntime := definedSymbols(t, common[0])
	var bad []string
	for _, obj := range []string{common[0], common[2]} {
		out, err := exec.Command("nm", "-u", obj).Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range strings.Fields(string(out)) {
			if f == "U" {
				continue
			}
			if runtime.GOOS == "darwin" {
				f = strings.TrimPrefix(f, "_")
			}
			if esp32Host(f) || esp32Allowed[f] || strings.HasPrefix(f, "pthread_") || inRuntime[f] {
				continue
			}
			bad = append(bad, filepath.Base(obj)+": "+f)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("the ESP32 runtime calls what ESP-IDF doesn't have (or what isn't in esp32Allowed yet): %s", strings.Join(bad, " "))
	}
}

// TestESP32ThreadLocal: the runtime's _Thread_local variables stay small.
// ESP-IDF reserves them in the stack of every FreeRTOS task, the system's
// ones too (its main task has 3.5 KB): with the 6 KB of str.format and log
// buffers there, the chip failed to start before app_main. Big per-thread
// buffers come from the heap.
func TestESP32ThreadLocal(t *testing.T) {
	requireCC(t)
	common := esp32Common(t)
	size, err := threadLocalSize(common[0])
	if err != nil {
		t.Skip(err)
	}
	wifi, err := threadLocalSize(common[2])
	if err != nil {
		t.Fatal(err)
	}
	size += wifi
	if size > 128 {
		t.Errorf("the ESP32 runtime has %d bytes of _Thread_local variables; every FreeRTOS task pays them in its stack (keep them under 128, big buffers on the heap)", size)
	}
}

// threadLocalSize is the size of the thread-local data of an object file
// (ELF .tdata/.tbss, Mach-O __thread_data/__thread_bss).
func threadLocalSize(obj string) (uint64, error) {
	if f, err := elf.Open(obj); err == nil {
		defer f.Close()
		var n uint64
		for _, s := range f.Sections {
			if s.Flags&elf.SHF_TLS != 0 {
				n += s.Size
			}
		}
		return n, nil
	}
	f, err := macho.Open(obj)
	if err != nil {
		return 0, errors.New("object format not ELF nor Mach-O")
	}
	defer f.Close()
	var n uint64
	for _, s := range f.Sections {
		if s.Name == "__thread_data" || s.Name == "__thread_bss" {
			n += s.Size
		}
	}
	return n, nil
}

// TestESP32Externs: every extern fn of a module the ESP32 has is defined in
// the embedded runtime; one left in a desktop part would only fail when ESP-IDF links.
func TestESP32Externs(t *testing.T) {
	requireCC(t)
	if _, err := exec.LookPath("nm"); err != nil {
		t.Skip("nm not found")
	}
	common := esp32Common(t)
	defined := definedSymbols(t, common[0])
	for f := range definedSymbols(t, common[2]) {
		defined[f] = true
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

// definedSymbols is the functions an object file defines (nm's T), without
// the underscore that macOS puts in front.
func definedSymbols(t *testing.T, obj string) map[string]bool {
	t.Helper()
	out, err := exec.Command("nm", "-g", obj).Output()
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
	return defined
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
	for _, f := range []string{"mlc_app.c", "mlc_runtime.c", "mlc_runtime.h", "mlc_main.c", "mlc_wifi.c", "CMakeLists.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
	// std/net (lwIP) and std/http (without HTTPS: std/tls, which it imports,
	// answers UNAVAILABLE) are available
	for _, src := range []string{"import \"std/net\"\n\nfn main() {\n}\n", "import \"std/http\"\n\nfn main() {\n}\n"} {
		if err := BuildESP32("app.mlc", []byte(src), filepath.Join(t.TempDir(), "n"), noFiles, Options{}); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
	// generating again over its own folder works
	if err := BuildESP32("app.mlc", []byte("fn main() {\n}\n"), dir, noFiles, Options{}); err != nil {
		t.Errorf("generating again: %v", err)
	}

	for _, c := range []struct{ src, want string }{
		{"import \"std/fs\"\n\nfn main() {\n}\n", `app.mlc:1:1: import "std/fs": not available with -target esp32: std/fs needs files and directories`},
		{"import \"std/tls\"\n\nfn main() {\n}\n", `import "std/tls": not available with -target esp32: std/tls needs dlopen and OpenSSL`},
		{"import \"std/sqlite\"\n\nfn main() {\n}\n", `std/sqlite needs dlopen`},
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
