# ESP32

`mlc build -target esp32` turns an MLC program into an **ESP-IDF component**: the generated C, the runtime and a small entry that runs `main` in a FreeRTOS task. ESP-IDF compiles it with its own toolchain and flashes it, as with any C project. MLC doesn't call the chip's compiler.

**Status:** the embedded runtime is tested on the host with a simulation of ESP-IDF (see [How it's tested](#how-its-tested)), but it hasn't been built with ESP-IDF's GCC or run on a chip yet. Treat it as a first version, and report what breaks.

## Use

```
idf.py create-project blink         # an ESP-IDF project (ESP-IDF 5.x)
cd blink
rm main/blink.c main/CMakeLists.txt # the main component becomes MLC's
mlc build -target esp32 -o main led.mlc
idf.py set-target esp32             # or esp32s3, esp32c3, esp32c6...
idf.py build flash monitor
```

`-o` is the component's folder (by default `<name>-esp32`). Use it as the project's `main` component: ESP-IDF gives `main` access to every component (the GPIO driver, Wi-Fi...), so the FFI declarations link without `REQUIRES`. mlc writes five files, and generating again overwrites them; a folder with a `CMakeLists.txt` that mlc didn't write is refused.

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
- **Threads are FreeRTOS tasks** (ESP-IDF's pthread). `thread.spawn` still asks for at least 16 KB, which is a lot in about 300 KB of RAM: few threads, and the stack size chosen with care.
- **`thread.cpu_count()`** is the number of cores (2 on the ESP32 and the S3, 1 on the C3 and the C6).
- **`rand.secure`** comes from the hardware generator (`esp_fill_random`), which is truly random with Wi-Fi or Bluetooth on.
- **`time.now_ms`** is the chip's clock, which starts at 1970 until something sets it (SNTP). `time.mono_ns` and `time.sleep_ms` work as usual.
- **`int` is 64 bits** on a 32-bit core: it works, but in hot loops `int32` is faster.
- **`io`** writes to the console (the UART or the USB). `io.read_line` reads from it when ESP-IDF's console driver is installed.

## What isn't available

The modules that need a desktop system are refused at compile time, including through another module:

```
app.mlc:1:1: import "std/http": not available with -target esp32 (it uses std/net): std/net needs BSD sockets, which the ESP32 runtime doesn't have
```

| Module | Why |
|---|---|
| `std/fs` (and `std/config`, which reads files) | files and directories |
| `std/process` | processes |
| `std/net` (and `std/http`) | BSD sockets |
| `std/signal` | Unix signals |
| `std/sqlite`, `std/tls` | `dlopen` (and OpenSSL) |

`os.cwd` and `os.chdir` return an error (there's no current directory). Everything else in the standard library works: `io`, `str`, `text`, `utf8`, `vec`, `map`, `deque`, `heap`, `sort`, `slices`, `arena`, `json`, `csv` (from text in memory), `time`, `rand`, `hash`, `hmac`, `thread`, `chan`, `mutex`, `atomic`, `sync`, `log` to the console.

ESP-IDF has a file system (SPIFFS, FAT) and sockets (lwIP), so `std/fs` and `std/net` may come later, once they're tested on a chip.

## How it's tested

There's no ESP32 in the test suite, so `internal/driver/esp32_test.go` simulates ESP-IDF on the host. `testdata/esp32sim` has the few FreeRTOS and ESP-IDF declarations the runtime uses, with FreeRTOS tasks as pthreads of the asked stack and a `main` that calls `app_main`, like ESP-IDF's startup.

- **TestESP32Sim**: every program in `tests/run` and `tests/panic` that doesn't import a missing module and doesn't take arguments becomes a component, is compiled with `ESP_PLATFORM` and `-Werror`, runs in the simulation and must give the same output and exit code as on the desktop. The only difference is expected: `stack_recursion.mlc` panics at its first recursion, because 100 calls of 4 KB don't fit in the task's 32 KB.
- **TestESP32Symbols**: the runtime compiled for the ESP32 only calls what ESP-IDF has (newlib, pthread, `esp_fill_random`, `pxTaskGetStackStart`). A new call to something else (`dlopen`, `socket`, `posix_spawn`) fails the test.
- **TestESP32Target**: the component's files, the refused imports and the folder that isn't mlc's.

What the simulation doesn't check: ESP-IDF's GCC and its warnings (the component compiles with `-Wno-error` for that reason), the 32-bit ABI of the chip and the real FreeRTOS. The next step is a build with ESP-IDF and a run in QEMU (which ESP-IDF supports) or on a board.
