# ESP32

`mlc build -target esp32` turns an MLC program into an **ESP-IDF component**: the generated C, the runtime and a small entry that runs `main` in a FreeRTOS task. ESP-IDF compiles it with its own toolchain and flashes it, as with any C project. MLC doesn't call the chip's compiler.

**Status:** built with ESP-IDF 5.5.6 (GCC 14.2 for Xtensa) without warnings and run on an ESP32 (ESP32-D0WD-V3, rev 3.1): the `tests/run` and `tests/panic` programs that fit the chip give the same output there as on the desktop (see [How it's tested](#how-its-tested)). The other chips (S3, C3, C6) haven't been tried yet.

## Use

```
idf.py create-project blink         # an ESP-IDF project (ESP-IDF 5.x)
cd blink
rm main/blink.c main/CMakeLists.txt # the main component becomes MLC's
mlc build -target esp32 -o main led.mlc
idf.py set-target esp32             # or esp32s3, esp32c3, esp32c6...
idf.py build flash monitor
```

`-o` is the component's folder (by default `<name>-esp32`). Use it as the project's `main` component: ESP-IDF gives `main` access to every component (the GPIO driver, Wi-Fi...), so the FFI declarations link without `REQUIRES`. mlc writes five files, and generating again overwrites them; a folder with a `CMakeLists.txt` that mlc didn't write is refused. Generate the component before the project's first `idf.py set-target` or `build`: ESP-IDF finds the components when it configures, and a `main` folder without a `CMakeLists.txt` at that moment stays out of the build (the link then fails with `app_main` missing). If that happened, `idf.py fullclean` and build again.

| File | What it is |
|---|---|
| `mlc_app.c` | the generated C of the program (its `main` is compiled as `mlc_main`) |
| `mlc_runtime.c`, `mlc_runtime.h` | the runtime, the same as on the desktop, compiled with ESP-IDF's `ESP_PLATFORM` |
| `mlc_main.c` | `app_main`: creates the task where `main` runs |
| `CMakeLists.txt` | the component's registration |

## The chip's functions: FFI

The hardware is reached with `extern "C" fn` (see [ffi.md](ffi.md)). The C enums of ESP-IDF (`gpio_num_t`, `gpio_mode_t`) are `int`-sized, so they're `int32`, and `esp_err_t` is `int32`:

```
import "std/io"
import "std/time"

extern "C" fn gpio_reset_pin(pin int32) int32
extern "C" fn gpio_set_direction(pin int32, mode int32) int32
extern "C" fn gpio_set_level(pin int32, level uint32) int32

const LED = 2         // the on-board LED of many ESP32 DevKit boards
const OUTPUT = 2      // GPIO_MODE_OUTPUT

fn main() {
    gpio_reset_pin(LED)
    gpio_set_direction(LED, OUTPUT)
    for i := 0; i < 10; i++ {
        gpio_set_level(LED, uint32(i % 2))
        io.print("led", i % 2)
        time.sleep_ms(500)
    }
}
```

The values of the C enums and macros (`GPIO_MODE_OUTPUT` is 2) come from ESP-IDF's headers: the compiler doesn't read headers, so the numbers go in MLC constants. `link "name"` isn't used with this target: a C library comes in as an ESP-IDF component.

## What changes on the chip

- **`main` runs in a FreeRTOS task with 32 KB of stack** (ESP-IDF's own main task has 3.5 KB). The checker still estimates `main`'s stack against 8 MB, but the run-time check of the large frames uses the task's real stack: a function whose frame doesn't fit panics on entry instead of writing over other memory. To change the size, add `-DMLC_MAIN_STACK=65536` to the component's compile options.
- **A panic restarts the chip.** The message goes to the console (`idf.py monitor`) and, as `exit` does in ESP-IDF, the chip aborts and boots again. The leak check at the end of `main` works the same way.
- **When `main` returns, the program ends:** the task is deleted, and the chip stays on, doing nothing, until the next reset.
- **Threads are FreeRTOS tasks** (ESP-IDF's pthread), on both cores. `thread.spawn` asks for at least 16 KB, and the ESP32 has about 290 KB of free heap (the largest block, about 180 KB): use `thread.STACK_16KB`, and few threads at a time. A thread that doesn't fit doesn't corrupt anything: `thread.spawn` panics with "the system didn't create the thread (thread or memory limit)". Eight threads of 64 KB at once, as some desktop tests do, don't fit.
- **The runtime's per-thread variables are a few bytes.** ESP-IDF reserves the `_Thread_local` variables in the stack of every FreeRTOS task, the system's too, so the buffers of `str.format` and `log` come from the heap at a thread's first use (and go back when it ends).
- **`thread.cpu_count()`** is the number of cores (2 on the ESP32 and the S3, 1 on the C3 and the C6).
- **`rand.secure`** comes from the hardware generator (`esp_fill_random`), which is truly random with Wi-Fi or Bluetooth on.
- **`time.now_ms`** is the chip's clock, which starts at 1970 until something sets it (SNTP). `time.mono_ns` and `time.sleep_ms` work as usual.
- **`int` is 64 bits** on a 32-bit core: it works, but in hot loops `int32` is faster.
- **`io`** writes to the console (the UART or the USB). `io.read_line` reads from it when ESP-IDF's console driver is installed.

## What isn't available

The modules that need a desktop system are refused at compile time, including through another module:

```
app.mlc:1:1: import "std/config": not available with -target esp32 (it uses std/fs): std/fs needs files and directories, which the ESP32 runtime doesn't have
```

| Module | Why |
|---|---|
| `std/fs` (and `std/config`, which reads files) | files and directories |
| `std/process` | processes |
| `std/signal` | Unix signals |
| `std/sqlite`, `std/tls` | `dlopen` (and OpenSSL) |

`std/tls` is refused only in the program's own imports: `std/http` imports it for HTTPS, and on the ESP32 its functions answer `tls.UNAVAILABLE` (as on a desktop without `libssl`), so `http.dial_tls` returns `nil` with that error and the rest of `std/http` works.

`os.cwd` and `os.chdir` return an error (there's no current directory). Everything else in the standard library works: `io`, `str`, `text`, `utf8`, `vec`, `map`, `deque`, `heap`, `sort`, `slices`, `arena`, `json`, `csv` (from text in memory), `time`, `rand`, `hash`, `hmac`, `thread`, `chan`, `mutex`, `atomic`, `sync`, `log` to the console, `net` and `http` (see [The network](#the-network)).

ESP-IDF has a file system (SPIFFS, FAT), so `std/fs` may come later.

## The network

`std/net` and `std/http` work over ESP-IDF's TCP/IP stack (lwIP), with the same API and error codes as on the desktop: lwIP has the BSD socket calls. The runtime starts lwIP (`esp_netif_init`) at the first `listen` or `connect`.

**Without Wi-Fi only the loopback answers** (`127.0.0.1` and `localhost`): a server and a client in the same program work, as in `tests/run/http_loopback.mlc`. To reach the network, the Wi-Fi has to be connected first; there's no `std/wifi` yet, so for now it's done through the FFI, with ESP-IDF's functions (`esp_wifi_init`, `esp_wifi_connect`...), or in C in another component of the project.

What changes from the desktop:

- **Memory:** an `http.Message` has 8 KB, so the thread that reads one needs a stack of at least 32 KB (`32 * 1024`), and a server with a thread per connection serves few at a time. The Wi-Fi, when on, takes another 50 to 70 KB of the heap.
- **HTTPS isn't available** (see above).
- **A refused connection** is `net.REFUSED`, as on the desktop: lwIP reports the refusal as `ECONNRESET`, and `connect` translates it.

## How it's tested

There's no ESP32 in the test suite, so `internal/driver/esp32_test.go` simulates ESP-IDF on the host. `testdata/esp32sim` has the few FreeRTOS and ESP-IDF declarations the runtime uses, with FreeRTOS tasks as pthreads of the asked stack and a `main` that calls `app_main`, like ESP-IDF's startup.

- **TestESP32Sim**: every program in `tests/run` and `tests/panic` that doesn't import a missing module and doesn't take arguments becomes a component, is compiled with `ESP_PLATFORM` and `-Werror`, runs in the simulation and must give the same output and exit code as on the desktop. The only difference is expected: `stack_recursion.mlc` panics at its first recursion, because 100 calls of 4 KB don't fit in the task's 32 KB.
- **TestESP32Symbols**: the runtime compiled for the ESP32 only calls what ESP-IDF has (newlib, pthread, lwIP's sockets, `esp_fill_random`, `pxTaskGetStackStart`, `esp_netif_init`). A new call to something else (`dlopen`, `posix_spawn`, `nanosleep`) fails the test. The list was checked against ESP-IDF 5.5.6's libraries.
- **TestESP32ThreadLocal**: the runtime's `_Thread_local` variables stay under 128 bytes. With 6 KB there, the chip didn't start: ESP-IDF couldn't create its own main task.
- **TestESP32Externs**: every `extern fn` of a module the ESP32 has is defined in the embedded runtime.
- **TestESP32TLS**: `http.dial_tls` answers `tls.UNAVAILABLE` and the program goes on.
- **TestESP32Target**: the component's files, the refused imports (and the allowed ones, `std/net` and `std/http`) and the folder that isn't mlc's.

With ESP-IDF's GCC in the `PATH` (after `export.sh`), TestESP32Sim also compiles each program's generated C with it, with ESP-IDF's `-Werror=all`: GCC gives warnings that clang doesn't (it wants inner braces in the `{0}` of an array inside a struct, which the component turns off), and `-Wno-error` doesn't undo `-Werror=all`, so one of them stops `idf.py build`.

The simulation also reproduces what ESP-IDF's build differs in: `_GNU_SOURCE` already defined, `-Wtype-limits`, and a `struct tm` without `tm_gmtoff`, as in newlib.

**On the chip** (`tools/esp32_chip.py`, by hand, not in `make check`): each program of `tests/run` and `tests/panic` was built with ESP-IDF, flashed and run on an ESP32, and the console compared with the expected output (stdout, stderr and the panic message come through the same UART). 67 programs gave the same output; 10 weren't run (5 use the network or desktop C libraries, 5 read stdin or arguments). The other 3, `threads`, `channels` and `mutex`, start 64 KB threads (and one of 256 KB) at once: they panic in `thread.spawn` for lack of memory, and with the stacks changed to 16 KB they give the same output as on the desktop. Since `std/net` and `std/http` came to the ESP32, `http_loopback.mlc` and `server.mlc` (with 16 KB stacks) also give the desktop's output on the chip; the whole run hasn't been repeated since then. To run it again, with the board on USB:

```
. ~/esp/esp-idf/export.sh
python tools/esp32_chip.py --port /dev/cu.usbserial-0001                      # about 30 minutes
python tools/esp32_chip.py --port /dev/cu.usbserial-0001 --only threads,division
```

It builds `bin/mlc`, creates a temporary ESP-IDF project (or uses `--project`, which saves the first full build of ESP-IDF on later runs), and runs the thread and server tests with 16 KB stacks. It exits with 1 if a program fails.

What isn't checked: the other chips (S3 is Xtensa like the ESP32; C3 and C6 are RISC-V), and runs in QEMU, which would put the chip in the test suite.
