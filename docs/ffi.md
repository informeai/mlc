# C functions (FFI)

Any file can call functions from C libraries. The boundary is checked by the compiler: what crosses from one side to the other has clear ownership rules.

```
import "std/io"

link "m"                                            // -lm when linking

extern "C" type FILE                                // opaque C handle

extern "C" fn strlen(s string) uint64
extern "C" fn cbrt(x float64) float64
extern "C" fn memcmp(a &[]byte, b &[]byte, n uint64) int32
extern "C" fn fopen(path string, mode string) ?*FILE
extern "C" fn fputs(s string, f &FILE) int32
extern "C" fn fclose(f *FILE) int32                 // takes ownership of the handle

fn main() {
    io.print(strlen("hello"), cbrt(27.0))
    f := fopen("out.txt", "w")
    if f != nil {
        fputs("hi\n", &f)
        fclose(f)                                   // the checker requires this close
    }
}
```

## Declaring

- `extern "C" fn name(...)` calls the C symbol `name`. It has no body, and it isn't generic or a method.
- **The signature must match the C one.** The compiler doesn't read C headers, and a wrong type is undefined behavior on the C side.

  | C | MLC |
  |---|---|
  | `int` | `int32` |
  | `long`, `int64_t` (Linux and macOS) | `int` |
  | `size_t`, `uint64_t` | `uint64` |
  | `unsigned char`, `uint8_t` | `byte` |
  | `double`, `float` | `float64`, `float32` |
  | `_Bool` | `bool` |
  | `const char *` (text C only reads) | `string` |
  | `T *` for data C reads or writes | `&[]T` (the pointer to the data) or `&T` |
  | `X **` where C writes a new handle | `&?*X` (output parameter) |
  | any of the above, when C accepts `NULL` | `?&T`, `?&[]T`, `?&?*X` |
  | a function pointer (`int (*)(void *, int)`) | `fn(&C, int32) int32`, given the name of an MLC function ([callbacks](#callbacks-from-c)) |
  | the `void *` context of a callback C keeps | `kept *C` |

- **Variadic functions** (`printf`, `open` with a mode) **are not accepted**: the way arguments are passed changes with the platform.
- `link "name"` asks for the `libname` library when linking (`-lname`). `mlc build -L dir` and `mlc run -L dir` say where to look for it. For the rest of linking (a macOS `-framework`, a `-Wl,-rpath,dir`, a static library by path), use `-ldflags "..."` or the `LDFLAGS` variable; see [The C compiler](../README.md#the-c-compiler).

## What crosses the boundary

| Type | How | Rule |
|---|---|---|
| numbers, `bool` | by value | |
| `string` | `const char *` | MLC literals are zero-terminated; C only reads |
| `&[]T` (`T` a number, `bool` or a struct of scalar fields) | the pointer to the data | the length goes in a separate parameter; C can't keep the pointer |
| `fn(...)` | the address of a C wrapper of the MLC function named in the call | see [Callbacks from C](#callbacks-from-c) |
| `kept *T` | the pointer, moved to C | a callback's context that C keeps until the program ends |
| `&T` (`T` a number, `bool`, a struct of scalar fields only, or a handle) | pointer | C can't keep the pointer |
| `*X` (`X` an `extern "C" type`) | the handle | the C function **takes ownership** (it closes or frees the handle) |
| `?*X` result | the handle (or `NULL`) | a new owner, which the checker requires you to close |
| `&?*X` | the address of a `?*X` variable (`X **`) | the C function writes a new handle into it; the variable can't hold a handle before, and must be closed after |
| `?&T`, `?&[]T`, `?&?*X` | `NULL` (with `nil`) or the same as `&T`, `&[]T`, `&?*X` | the same rules as the borrow or the handle, when it isn't `nil` |

**Nothing that owns MLC memory crosses:** an owning slice (`[]byte`), an owning pointer (`*P`) or a struct with owners are refused. C wouldn't know how to free that memory and might keep it. To hand data over, borrow it with `&`. The one exception is a callback's context that C keeps (`kept *T`): it's moved to C for good, and C only passes it back to the callback.

## C handles

`extern "C" type Db` declares an opaque type. It only exists behind a pointer (`?*Db`, `*Db`, `&Db`), and it's an owner like the others:

- it comes from a C function (`?*Db`, which may be `NULL`);
- it's borrowed with `&b` by the functions that only use it;
- it must be **passed to a C function that takes it by ownership** (`fn close_db(b *Db)`); forgetting is a leak, detected at compile time;
- you can't `free` it, look inside it, create it with `alloc` or keep it in a generic `Vec` (`drop` wouldn't know which C function closes it).

The opaque type can implement interfaces (`impl io.Reader for Db`, with `&Db` receiver methods) and be the argument of a generic whose constraint is `ref`, which only uses the borrowed type (`&T`): that's how `bufio.new[fs.File]()` and `bufio.new[net.Conn]()` work. With another constraint (`[T any]`, `[T io.Reader]`), the generic could use `T` by value, and the opaque type is refused with a suggestion of `[T ref + ...]`.

## Handles returned through a pointer (`&?*X`)

Many libraries return the handle through a parameter and use the result for the error code:

```
link "sqlite3"

extern "C" type sqlite3
extern "C" fn sqlite3_open(name string, db &?*sqlite3) int32     // int sqlite3_open(const char *, sqlite3 **)
extern "C" fn sqlite3_close(db *sqlite3) int32

fn main() {
    var db ?*sqlite3 = nil
    rc := sqlite3_open("data.db", &db)
    if db != nil {
        // ... use &db ...
        sqlite3_close(db)                   // the checker requires this close
    }
    io.print("rc", rc)
}
```

- `&?*X` only exists in `extern "C" fn` parameters, and only for handles (`X` an `extern "C" type`).
- The argument is `&h`, with `h` a `?*X` variable or field. **`h` can't hold an open handle** before the call (it must be `nil` or already closed), because the C function would write another one over it and the old one would leak.
- After the call, `h` may hold a handle or be `nil` (the function may fail). Like any `?*X`, it needs `if h != nil` before being closed, and forgetting to close is a leak detected at compile time.
- The same variable can't receive two handles in the same call (`f(&h, &h)`).

## Pointers that may be `NULL` (`?&T`)

In C, `NULL` in a parameter usually means "I don't care" or "use the default". Passing something else instead (a zeroed struct, a pointer to a variable nobody reads) sometimes gives the same result and sometimes doesn't, depending on the system. With `?&T`, the declaration says the parameter accepts `NULL`, and the call passes `nil` or a borrow:

```
extern "C" type AddrInfo
extern "C" type Msg

extern "C" fn getaddrinfo(node string, service string, hints ?&Hints, res &?*AddrInfo) int32
extern "C" fn time(t ?&int) int
extern "C" fn sqlite3_exec(db &sqlite3, sql string, callback ?&byte, arg ?&byte, errmsg ?&?*Msg) int32
extern "C" fn sqlite3_free(m *Msg)

getaddrinfo("localhost", "80", nil, &res)      // hints NULL: the system default
now := time(nil)

var msg ?*Msg = nil
rc := sqlite3_exec(&db, sql, nil, nil, &msg)   // with &msg, SQLite allocates the error message
if msg != nil {
    sqlite3_free(msg)                           // and the checker requires this free
}
```

- `?&T` only exists in `extern "C" fn` parameters. In an MLC function, use `?*T` or two functions.
- `?&T` and `?&[]T` accept `nil` or the same borrow as `&T` and `&[]T`, with the same rules: C only uses the pointer during the call.
- `?&?*X` is the optional handle returned through a pointer. With `nil`, C returns nothing. With `&h`, the `&?*X` rules apply: `h` can't hold a handle before and must be closed after.
- For a parameter of a type MLC doesn't represent (a function pointer, for example) that you always pass as `NULL`, any `?&T` works (`?&byte`), since only the `NULL` crosses.

## Callbacks from C

Many C APIs take a function to call back: `qsort` calls the comparison, `sqlite3_exec` calls a function per row, ESP-IDF calls a handler for each event. MLC functions are already plain C functions (no hidden parameters, no captured variables), so most of the work is in the rules: what the callback may receive, how long C may keep it, and in which thread it runs.

### Declaring and passing

A parameter of an `extern "C" fn` may have a function type. The argument must be **the name of an MLC function** with exactly that signature (`by_age`, `geo.by_age`, `larger[int]`), not a variable of `fn` type:

```
struct Person {
    id int
    age int
}

extern "C" fn qsort(base &[]Person, n uint64, size uint64, cmp fn(&Person, &Person) int32)

fn by_age(a &Person, b &Person) int32 {
    if a.age < b.age {
        return -1
    }
    if a.age > b.age {
        return 1
    }
    return 0
}

qsort(&people, uint64(len(people)), 16, by_age)   // 16: the size of Person in C (two int64)
```

- What C passes the callback follows the table of `extern` parameters, borrowed: numbers, `bool`, `&T` of a number, a struct or a C handle. A callback can't receive or return an owner (C can't give ownership of MLC memory) or `?&T` (MLC functions don't take it).
- **A buffer** comes as in C, the pointer and then its length: `fn(&RecvInfo, &[]byte, int32)` for `void (*)(const esp_now_recv_info_t *info, const uint8_t *data, int len)`. The wrapper joins the two into the slice (`len(data)` is the count; `NULL` with 0, or a negative count, is an empty slice), and the callback also receives the count. The elements are numbers, `bool` or structs of scalar fields, and the slice is a borrow of C's data during that call: read it (or copy it), and write into it only if the C API says the buffer is writable. For a handle C may pass as `NULL`, receive `&X`: MLC never looks inside a handle, it only passes it back to C.
- The compiler generates, for each function passed this way, a C wrapper with the same signature (`mlc_fn_by_age__cb`), and passes the wrapper. That's why it needs the name: a `fn` variable could hold any function, and the wrapper is written at compile time. The wrapper is what makes it safe to call from a thread MLC didn't create (see [Threads](#in-which-thread-the-callback-runs)). A C function can't be passed as a callback; call it from an MLC function.
- A `&T` that C passes to the callback (the elements in `qsort`, the event's data) is a borrow during that call, with the usual rules: the callback can't keep it.
- `&[]T` with `T` a struct of scalar fields goes to C as the array (C's layout), which is what `qsort` sorts.

### How long C keeps it: `kept`

The type alone doesn't say whether C calls the function only during the call (`qsort`, `bsearch`, `sqlite3_exec`) or keeps it to call later (a timer, an event handler, a task). The declaration says it, with `kept` on the parameters C stores:

```
// synchronous: C calls f only while cb_each runs, with ctx
extern "C" fn cb_each(n int32, f fn(&Sum, int32), ctx &Sum) int32

// kept: ESP-IDF calls h later, in its event task, with arg
extern "C" fn esp_event_handler_instance_register(base ?&byte, id int32, h kept fn(&Watch, &EventBase, int32, &int32), arg kept *Watch, inst ?&?*Instance) int32
```

- **Without `kept`** (the default), C uses the function and the context only during the call. The context is a borrow (`ctx &Sum`), checked like any other: the caller keeps ownership, and after the call nothing in C points to it. It can be any struct of the program: C only hands it back.
- **With `kept`**, C stores them after returning. A borrow would be left dangling the moment its owner is freed, which the checker can't see across the boundary, so a `kept` context is **moved** (`arg kept *Watch`): the `alloc` that created it is in the caller's code, and after the call the caller no longer owns it. The memory stays with the registration until the program ends (an embedded program usually registers its handlers once and runs forever). It's an allocation that is never freed, written in plain sight; with a `kept` context, the leak check at the end of the program is skipped, since what C keeps (and what it owns) can't be counted.
- The checker pairs each callback with its context. A callback's `&T`, with `T` a struct of the program, is MLC memory C only has because the call gave it: a borrow (`&T`, or `&[]T` for its elements) for a synchronous callback, the `kept *T` for a kept one. A kept callback with a lent context, a kept context with a callback that isn't kept, and a callback whose `&T` comes from nowhere are refused. A struct of scalar fields only may also be C's own data (it has C's layout), so it may go unpaired.
- Getting the context back when C unregisters (and freeing it) is left out of this version: the C API would have to return the pointer, and the checker would have to tie the unregistering call to the registration.

### The context and the callback's first parameter

C passes the context back as the callback's `void *`. In the declaration, both are the same MLC type (`arg kept *Watch` and the handler's `&Watch`): the wrapper receives the pointer and hands the callback a borrow of it, valid during each call. As with every `extern` declaration, the compiler doesn't read the C header: the position of the context among the callback's parameters is the C library's, and writing it wrong is undefined behavior.

Since there are no mutable global variables, the context is the only state a callback has. To get a result out of a `kept` callback, the context holds an end of a channel (a `*chan.Sender[T]`) or a `mutex.Mutex`, as between threads:

```
struct Watch {
    tx *chan.Sender[int]
}

fn on_event(w &Watch, base &EventBase, id int32, value &int32) {
    match w.tx.try_send(int(*value)) {   // never blocks ESP-IDF's event task
        Sent => {}
        Full(_) => {}
        Closed(_) => {}
    }
}

rx := chan.new[int](8)
w := alloc(Watch{tx: rx.sender()})
esp_event_handler_instance_register(nil, ANY_ID, on_event, w, nil)   // w belongs to ESP-IDF now
```

### In which thread the callback runs

A synchronous callback runs in the caller's thread. A `kept` one runs wherever C decides: ESP-IDF's event task, the `esp_timer` task, a thread of the library. The generated wrapper prepares the runtime there:

- **the stack limit:** if the thread wasn't started by MLC, the wrapper finds its stack (as `thread.spawn` does), so a frame too large is a panic instead of memory corruption, with a hint that the thread is C's. This matters on the ESP32, whose system tasks have small stacks (the event task has 2.3 KB, `esp_timer`'s 3.5 KB): a callback there should do little, and hand the rest to a channel;
- **the per-thread buffers** of `str.format` and `log`, allocated at first use, are freed when the outermost callback in that thread returns, so a library that calls from a new thread each time doesn't leak. A callback that calls C that calls back is counted, and only the outermost one undoes;
- **a panic** in the callback ends the program, with the message, as in any thread. It never unwinds into C.

A `kept` callback may be called by C while the code that registered it runs in another thread. The context is only reached through the callback, so MLC's data doesn't race; what C guarantees about calling the same registration from two threads at once is the C library's to say (ESP-IDF's event task and `esp_timer` call one handler at a time).

### Interrupts: not with callbacks

An interrupt handler (ESP-IDF's `gpio_isr_handler_add`, a timer's ISR) can't allocate, block or print, must be in IRAM, and has no thread of its own. The runtime does all of that (the stack check, the panic message, the per-thread variables), so an MLC function must not run there. On the ESP32 the wrapper checks it (`xPortInIsrContext()`) and stops the chip with a message written through the ROM's `printf`, which works in an interrupt:

```
panic: app.mlc:5: an MLC function was called from an interrupt (ISR); handle the interrupt in C and pass the event on (see docs/ffi.md)
```

For interrupts, the way is a handler written in C that only puts the event in a queue an MLC thread reads, as ESP-IDF itself recommends. That's what `std/gpio` does for the pins: `gpio.watch` registers the handler in the runtime, and `w.next()` reads the edges in the program's thread (see [esp32.md](esp32.md#pins-stdgpio)).

### Channels in callbacks: `try_send`

`Sender.send` waits when the channel is full. In a `kept` callback that would stop the C task that called it (all of ESP-IDF's timers, all its events). `try_send(v T) chan.Try[T]` never waits: `Sent`, or `Full(v)` and `Closed(v)`, giving the value back so the callback decides what to do with it (drop it, count it). An owner that comes back must be freed, as the checker requires.

### What it doesn't do

- fn variables, methods and `extern fn` as callbacks (only names of MLC functions);
- callbacks inside C structs (`esp_timer_create_args_t` has the function as a field): structs with function fields come later; ESP-IDF's timers can come through a standard module instead;
- variadic callbacks, and callbacks that receive or return C structs by value;
- unregistering and getting the `kept` context back.

### How it's tested

- `tests/run/ffi_qsort.mlc`: libc's `qsort` sorts MLC structs, calling the comparison back (on the desktop and in the ESP32 simulation);
- `TestFFICallbacks`, with `tests/ffi/callbacks.c`: a synchronous callback with a borrowed context; a buffer passed as pointer and length (also `NULL` with 0 and a negative length); a kept one called later from threads C creates, five of them, that formats text (the per-thread buffers) and passes the values on with `try_send`; and a frame larger than C's thread's stack, which panics with the hint. It also runs under AddressSanitizer and ThreadSanitizer (`make sanitize`);
- the checker's refusals in `tests/errors/ffi_callback_*.mlc` and `ffi_kept_*.mlc`: a `fn` variable or a C function as the argument, a signature that doesn't match, an owner in a callback's parameters, a slice without its length or with elements C can't lay out, a borrow as a `kept` context, a kept callback with a lent context or without one, a kept context without a kept callback, a context that comes from nowhere, `kept` outside an `extern "C" fn`, and the context used after it was moved;
- on the ESP32 (`TestESP32Callbacks`, in the simulation, and `tools/esp32_chip.py` on the chip): `tests/esp32/events.mlc`, a handler registered with `esp_event_handler_instance_register` that receives the events posted with `esp_event_post` in ESP-IDF's event task and passes them on with `try_send`; and an MLC function called from an interrupt, which stops with the message.

## C structs from headers

A struct of the program crosses the boundary only when MLC can write its exact layout: scalar fields, in C's order (`&T` with `T` a struct of numbers). The configuration structs of real drivers don't fit. ESP-IDF's `i2c_master_bus_config_t` has a member inside an anonymous `union`, bitfields inside a nested anonymous `struct`, a field that only exists on some chips (`#if SOC_LP_I2C_SUPPORTED`) and a `size_t`, whose size changes between the chip and a desktop. `esp_timer_create_args_t` has the callback, its `void *` context and a `const char *`. Copying those layouts into MLC by hand would break from one chip (or ESP-IDF version) to the next, silently.

### The header's struct, with the fields you use

MLC doesn't copy the layout: MLC compiles to C, so the generated C **includes the header and uses the real struct**. The MLC declaration only names the C type, the header it comes from, and the fields the program uses, with MLC types for the checker:

```
extern "C" struct i2c_master_bus_config_t from "driver/i2c_master.h" {
    i2c_port int32
    sda_io_num int32
    scl_io_num int32
    clk_source int32                  // a member of the anonymous union, written as a field
    glitch_ignore_cnt byte
    flags {                           // the nested anonymous struct
        enable_internal_pullup uint32 // a bitfield
    }
}

var cfg i2c_master_bus_config_t      // every field zero, the ones MLC doesn't see too
cfg.i2c_port = -1
cfg.sda_io_num = 21
cfg.scl_io_num = 22
cfg.glitch_ignore_cnt = 7
cfg.flags.enable_internal_pullup = 1
i2c_new_master_bus(&cfg, &bus)
```

- `cfg.sda_io_num` in MLC is `cfg.sda_io_num` in C. **The C compiler checks the names**: a misspelled field, or one that doesn't exist on this chip, is a compile error (mlc says the C compiler refused the program because of a header, and C's message points at the MLC line).
- **Where C finds the header:** in the system's folders (`stdio.h`), in the main `.mlc` file's folder, and in the `-I` of `-cflags`. On the ESP32 the component sees every ESP-IDF component's headers (`driver/ledc.h`, `esp_timer.h`). The layout is the header's, on every chip, with its unions, bitfields and conditional fields.
- MLC declares a **subset** of the fields; the rest are zero. `var cfg T` and `T{}` zero the whole struct, and a literal may leave fields out (`T{sda_io_num: 21}`): unlike MLC's own structs, the program never sees all of a C struct's fields.
- An anonymous inner struct is a block in the declaration (`flags { ... }`); a member of an anonymous union is a field like the others (the program writes one member, as in C). A named struct inside another is a field of that type (also declared with `extern "C" struct`).
- The field types are numbers, `bool`, `[N]T` of numbers, other C structs (by value), `string`, callbacks and their contexts (below). The values are converted by C as in an assignment, so the MLC type should be C's (`int32` for an `int` or a C enum, `uint32` for a `size_t` on the ESP32): the checker uses it, C converts what doesn't match.
- A C struct is a value, copied like an MLC struct of scalars, with no owner. It crosses the boundary **lent** (`&T`, the pointer, as the configuration functions take it) and also **by value** (`T`): with the real type, the C compiler passes it by the platform's rules, which is what "structs by value" needed.
- Its fields are read and written, but not borrowed (`&cfg.flags` is refused): a bitfield has no address, and lending the inside of a C struct gains little. Borrow the whole struct.
- A callback can receive one (`fn(&esp_now_recv_info_t, &[]byte, int32)`): an event's struct, read in the callback.

### Strings, callbacks and contexts in fields

```
struct Blink {
    on bool
}

extern "C" type esp_timer                        // esp_timer_handle_t is struct esp_timer *
extern "C" struct esp_timer_create_args_t from "esp_timer.h" {
    callback fn(&Blink)
    arg kept *Blink
    name string
}

extern "C" fn esp_timer_create(args &esp_timer_create_args_t, out &?*esp_timer) int32
extern "C" fn esp_timer_start_periodic(t &esp_timer, period_us uint64) int32

fn tick(b &Blink) {
    b.on = !b.on
    gpio.write(LED, b.on)
}

args := esp_timer_create_args_t{callback: tick, arg: keep(alloc(Blink{on: false})), name: "blink"}
var t ?*esp_timer = nil
esp_timer_create(&args, &t)
```

- **`string`** is a `const char *` field (written as `cfg.name = "bus0"` or in the literal). MLC's strings are the program's literals, which live as long as the program, so C may keep the pointer (ESP-IDF keeps the timer's name). In this version string fields are only written by MLC: reading a `const char *` that C filled would need its length and its lifetime.
- **A callback field** (`callback fn(&Blink)`) takes the name of an MLC function, as a callback parameter does, and gets the C wrapper (converted to the field's own type: C's `void (*)(void *)` and the wrapper's `void (*)(Blink *)` are the same pointer in the ABI). Since C may keep a struct it receives, a callback in a field follows the `kept` rules: its context must be a `kept` field of the same struct, and a `kept` field must be received by some callback of the struct.
- **A context field** has type `kept *T`, and its value comes from **`keep(x)`**: it moves `x` (an owner from `alloc`) to C for good and gives the pointer C keeps. It's the `kept *T` parameter's move, written where it happens, so the struct stays a plain value: copying it doesn't copy an owner. The kept memory lives until the program ends, and with it the leak check at the end is skipped, as with a `kept` parameter.

### Constants from headers

The numbers of C's enums and macros (`GPIO_MODE_OUTPUT`, `LEDC_TIMER_13_BIT`) don't need to be copied by hand: with the header included, MLC names them and C gives the value:

```
extern "C" const GPIO_MODE_OUTPUT int32 from "driver/gpio.h"
extern "C" const LEDC_LOW_SPEED_MODE int32 from "driver/ledc.h"
```

The value is only known when C compiles, so such a constant works where a value goes (arguments, fields, comparisons), and not where MLC needs the number itself (an array's size, a `match` pattern, a `const` computed from it), which the checker refuses. It's a number or `bool`, and it can't be assigned or borrowed.

### What it doesn't do

- reading a `string` or a pointer field that C filled (`char *topic; int topic_len` in an MQTT event): a pointer and length field pair as a slice is a later step;
- handles (`?*X`) and borrows as fields;
- C's `union` as a type of its own (only the members of an anonymous union, as fields), and `struct tag` without a `typedef`;
- getting a `keep`'s memory back.

### How it's tested

- `TestFFICStructs`, with `tests/ffi/cstructs.h` and `cstructs.c`: an anonymous union, bitfields in a nested struct, a field MLC doesn't declare (C checks it stayed zero), a `size_t`, a `const char *`, a callback with its kept context called from a thread of C's, a struct by value, one a callback receives, and constants from a `#define` and an `enum`; and a field the header doesn't have, which is C's error at the MLC line. It also runs under the sanitizers;
- the refusals, in `tests/errors/cstruct_*.mlc` and `cconst_*.mlc`: `&` of a field, reading a string or a callback field, a fn variable in a callback field, an anonymous struct as a value or in a literal, a callback field without its `kept` context, a `kept` field no callback receives, a pointer field that isn't `kept`, `keep` of something that isn't an owner, the owner used after `keep`, and a C constant in a `const`, an array's size, an assignment and a pattern;
- on the ESP32 (`tests/esp32`, in the simulation and on the chip): `timer.mlc`, with `esp_timer` calling an MLC function every 20 ms through `esp_timer_create_args_t`, and `pwm.mlc`, PWM on the LED with `ledc_timer_config_t`, `ledc_channel_config_t` and the LEDC enums' values from the header.

## More of C's headers

A Bluetooth LE peripheral in NimBLE, ESP-IDF's Bluetooth LE stack, needs five things more: structs named by their `struct` tag, the header's macros and inline functions, a global variable of the library, a table of services that the library keeps for good, and the pointer fields of the structs it hands to the callbacks.

### 1. Structs without a typedef: `tag`

NimBLE names its structs by tag (`struct ble_gap_event`, `struct ble_gatt_access_ctxt`), without a `typedef`. `tag` after `struct` says the C type is `struct name`:

```
extern "C" struct tag ble_gap_adv_params from "host/ble_gap.h" {
    conn_mode byte
    disc_mode byte
}
```

In MLC the type is still `ble_gap_adv_params`; only the generated C changes.

### 2. Functions and macros from the header: `fn ... from`

`extern "C" fn` binds to a symbol, so a function-like macro (`OS_MBUF_PKTLEN(om)`) or a `static inline` function of a header can't be called: there's nothing to link. With `from`, the generated C calls the header's name as written, and **the C compiler checks the call against the header's prototype**:

```
extern "C" fn OS_MBUF_PKTLEN(om &os_mbuf) uint16 from "os/os_mbuf.h"
extern "C" fn ble_gatts_count_cfg(defs &[]ble_gatt_svc_def) int32 from "host/ble_gatt.h"
```

An argument with effects (a call) is computed once, into a temporary, in order, so a macro that uses an argument twice doesn't evaluate it twice. The types follow the rules of `extern "C" fn`, plus C structs by value in the result; a pointer goes as `void *`, which C converts to the header's own type. Without `from`, nothing changes.

### 3. A library's global variable: `extern "C" var`

NimBLE is configured through a global struct (`extern struct ble_hs_cfg ble_hs_cfg`):

```
extern "C" struct tag ble_hs_cfg from "host/ble_hs.h" {
    sync_cb fn()
    reset_cb fn(int32)
}
extern "C" var ble_hs_cfg ble_hs_cfg from "host/ble_hs.h"

ble_hs_cfg.sync_cb = on_sync
```

Its fields are read and written as those of any C struct (and the type can also be a number or `bool`). Its name may be its struct's tag, as in C (`struct ble_hs_cfg ble_hs_cfg`): the type and the value are in different places. MLC has no mutable global variables, so that two threads never reach the same memory; a C global is the library's memory, which MLC can't protect: it's written before starting the library and the program's threads, and the checker can't tell. It can't be borrowed as a whole or assigned as a whole (only its fields).

### 4. Data the library keeps: `keep` of a C struct or array

NimBLE's service table is an array of `struct ble_gatt_svc_def`, ended by a zeroed entry, each pointing to an array of characteristics, each pointing to its UUID; the library keeps it until the end. MLC has no global data, so the table is built on the heap and **moved to C with `keep`**, which already moves a callback's context:

```
chars := alloc([]ble_gatt_chr_def, 2)          // the last one stays zero: the end of the array
chars[0].uuid = keep(alloc(ble_uuid16_t{u: ble_uuid_t{type: BLE_UUID_TYPE_16}, value: 0x2A19}))
chars[0].access_cb = on_battery
chars[0].arg = keep(alloc(Battery{level: 100}))
chars[0].flags = BLE_GATT_CHR_F_READ
svcs := alloc([]ble_gatt_svc_def, 2)
svcs[0].type = BLE_GATT_SVC_TYPE_PRIMARY
svcs[0].uuid = keep(alloc(ble_uuid16_t{u: ble_uuid_t{type: BLE_UUID_TYPE_16}, value: 0x180F}))
svcs[0].characteristics = keep(chars)
ble_gatts_add_svcs(keep(svcs))
```

- `keep` also takes a C struct (`keep(alloc(ble_uuid16_t{...}))`, a `kept *T`) and a slice of C structs (`keep(chars)`, a `kept []T`). After it, the program no longer reaches that memory: it's filled first, then kept. A function can't return a `kept` value: it returns the owner (`*ble_uuid16_t`), and the `keep` happens where the value is given.
- A parameter `kept *T` or `kept []T` of a C struct takes what `keep` gave (`ble_gatts_add_svcs(table)`); a `kept *T` of the program's struct is still a context, moved by the call.
- A C struct's pointer field to C's data is declared `kept *T` or `kept []T`, and only takes what `keep` gave. The C field's own type wins in C (`const ble_uuid_t *` receiving a `ble_uuid16_t *`, whose first member is the `ble_uuid_t`), converted as with callbacks.
- A `kept` value can be copied: several characteristics may share one context.
- The zeroed entry that ends C's arrays is the program's to leave: `alloc` gives zeroed memory, so allocating one more than the entries is enough.
- What C writes back into the table (NimBLE's `val_handle`) isn't read by MLC, since the memory is C's after `keep`: the handle comes from a function, `ble_gatts_find_chr(svc, chr, nil, &handle)`.

### 5. Pointer fields of what C hands over

A callback receives a C struct whose fields point to more of C's data: `ctxt.om` is the `struct os_mbuf *` with the bytes read or written. A field of type `&X`, with `X` an `extern "C" type`, is read as a borrow of that C memory, valid while the struct is (during the callback), and passed to C's functions:

```
extern "C" struct tag ble_gatt_access_ctxt from "host/ble_gatt.h" {
    op byte
    om &os_mbuf
}

fn on_battery(conn uint16, attr uint16, ctxt &ble_gatt_access_ctxt, b &Battery) int32 {
    level := byte(b.level)
    return os_mbuf_append(ctxt.om, &level, 1)
}
```

Only opaque types (`X`, which MLC never looks inside) in this version: a pointer field to a C struct, which MLC would read through, could be `NULL`. These fields are read, not written.

### And the smaller ones

- A string field whose C type is `const uint8_t *` (a device name in the advertising data) takes a literal too: the pointer is converted to the field's type.
- The Bluetooth stack is turned on in ESP-IDF's configuration (`CONFIG_BT_ENABLED`, `CONFIG_BT_NIMBLE_ENABLED` in `sdkconfig.defaults` or `idf.py menuconfig`): MLC's component doesn't change the project's configuration.

### What it doesn't do

- reading or writing through a `kept` value after the `keep` (the memory is C's);
- pointer fields to C structs that MLC reads through;
- Classic Bluetooth (Bluedroid), which this proposal doesn't look at.

### How it's tested

- `TestFFIRegistry`, with `tests/ffi/registry.h` and `registry.c`, a library shaped like NimBLE: structs by tag, a function-like macro that uses its argument twice (and gets it computed once), a `static inline` function, a function without a result, a global configuration struct named like its tag, a table of services kept with `keep`, with pointers between its structs and a context shared by two characteristics, that the library walks later from a thread of its own, and an opaque pointer field read in the callbacks. It also runs under the sanitizers;
- the refusals: a `kept` field given an owner instead of `keep(x)`, the memory reached after its `keep`, a C global struct read, borrowed or assigned as a whole, a C global of another type, a pointer field to a C struct, and a pointer field written;
- on the ESP32: `tests/esp32ble/battery.mlc`, a BLE peripheral in MLC over NimBLE (the battery service, read and notified); see [esp32.md](esp32.md#bluetooth-le).

## Link errors

If a library from `link "..."` or a C function doesn't exist, `mlc build` says the program couldn't be linked and shows the linker's message. That's an error in the program, not in the compiler.

## Left out of this version

- Pointers returned by C to memory MLC would free (`char *` from `strdup`): use an opaque handle and the C function that frees it. (The standard library's `std/sqlite` and `std/tls` solve this with a small adapter in the runtime that copies the data into the caller's buffers.)
- A callback parameter that always receives `NULL` can still be declared as `?&byte` (only the `NULL` crosses); for real callbacks, see [Callbacks from C](#callbacks-from-c).
- A program's own struct by value across the boundary (a C struct from a header crosses by value: see [C structs](#c-structs-from-headers)), and variadic functions.
