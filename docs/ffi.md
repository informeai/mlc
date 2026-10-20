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

- **Variadic functions** (`printf`, `open` with a mode) **are not accepted**: the way arguments are passed changes with the platform.
- `link "name"` asks for the `libname` library when linking (`-lname`). `mlc build -L dir` and `mlc run -L dir` say where to look for it. For the rest of linking (a macOS `-framework`, a `-Wl,-rpath,dir`, a static library by path), use `-ldflags "..."` or the `LDFLAGS` variable; see [The C compiler](../README.md#the-c-compiler).

## What crosses the boundary

| Type | How | Rule |
|---|---|---|
| numbers, `bool` | by value | |
| `string` | `const char *` | MLC literals are zero-terminated; C only reads |
| `&[]T` (`T` a number or `bool`) | the pointer to the data | the length goes in a separate parameter; C can't keep the pointer |
| `&T` (`T` a number, `bool`, a struct of scalar fields only, or a handle) | pointer | C can't keep the pointer |
| `*X` (`X` an `extern "C" type`) | the handle | the C function **takes ownership** (it closes or frees the handle) |
| `?*X` result | the handle (or `NULL`) | a new owner, which the checker requires you to close |
| `&?*X` | the address of a `?*X` variable (`X **`) | the C function writes a new handle into it; the variable can't hold a handle before, and must be closed after |
| `?&T`, `?&[]T`, `?&?*X` | `NULL` (with `nil`) or the same as `&T`, `&[]T`, `&?*X` | the same rules as the borrow or the handle, when it isn't `nil` |

**Nothing that owns MLC memory crosses:** an owning slice (`[]byte`), an owning pointer (`*P`) or a struct with owners are refused. C wouldn't know how to free that memory and might keep it. To hand data over, borrow it with `&`.

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

## Link errors

If a library from `link "..."` or a C function doesn't exist, `mlc build` says the program couldn't be linked and shows the linker's message. That's an error in the program, not in the compiler.

## Left out of this version

- Pointers returned by C to memory MLC would free (`char *` from `strdup`): use an opaque handle and the C function that frees it. (The standard library's `std/sqlite` and `std/tls` solve this with a small adapter in the runtime that copies the data into the caller's buffers.)
- Callbacks from C into MLC (function pointers across the boundary). A callback parameter that always receives `NULL` can be declared as `?&byte`.
- Structs by value across the boundary, and variadic functions.
