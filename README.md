# MLC — My Lang C

A compiled language with a simple syntax, inspired by Go, that generates C and uses the system's C compiler to produce the binary. Memory is managed by hand, as in C (explicit `alloc`/`free`, no garbage collector), but the compiler refuses programs with C's classic bugs before generating the code. See [docs/memory.md](docs/memory.md).

```
file.mlc  →  mlc (Go)  →  program.c  →  cc + C runtime  →  binary
```

## Example

```
import "std/io"

fn is_prime(n int) bool {
    if n < 2 {
        return false
    }
    for d := 2; d * d <= n; d++ {
        if n % d == 0 {
            return false
        }
    }
    return true
}

fn main() {
    total := 0
    for n := 0; n <= 50; n++ {
        if is_prime(n) {
            total++
        }
    }
    io.print("primes up to 50:", total)
}
```

```sh
make                                     # builds the compiler into bin/mlc
bin/mlc run example.mlc                  # compiles and runs
bin/mlc build -o example example.mlc     # produces the executable
bin/mlc build -emit-c example.mlc        # shows the generated C
bin/mlc build -L /opt/lib prog.mlc       # -L: where to look for the C libraries of link "name"
bin/mlc build -I ~/mlc-libs prog.mlc     # -I: where to look for imports, besides the project
bin/mlc build -cflags "-O3 -march=native" prog.mlc               # extra flags for the C compiler
bin/mlc build -ldflags "-framework Cocoa" prog.mlc               # and for linking
bin/mlc test -cflags "-fsanitize=address" -ldflags "-fsanitize=address" std   # tests with AddressSanitizer
bin/mlc fmt .                            # formats the folder's .mlc files (in place)
bin/mlc fmt -l .                         # only lists the ones that would change
bin/mlc test std                         # runs the tests in *_test.mlc (here, the std's)
bin/mlc test -bench . std                # and measures the benchmarks (fn bench_*)
bin/mlc doc http.Message                 # the documentation of a type, function or module
bin/mlc run -sanitize address prog.mlc   # runs under AddressSanitizer + UBSan (thread: ThreadSanitizer)
make sanitize                            # the suite under the sanitizers
make mutate                              # mutants with ownership bugs: the checker must refuse them
make fuzz FUZZTIME=10m                   # random programs, valid and buggy, against the checker
make enum ENUM=6                         # every small program, against a reference model
make robust ROBUSTTIME=5m                # fuzzing of the compiler and the formatter: they never panic
make cover                               # the checker's coverage and the rules without a test
make test                                # runs the tests
make check                               # gofmt, mlc fmt -l, go vet and tests
```

## The C compiler

`mlc build` generates C and calls the C compiler in two steps: it compiles the program and the runtime (`-std=c11 -O2 -Wall -pthread`) and links with the libraries (`link "name"`, `-L`) and `libm`. You can add flags, which come after `mlc`'s (and so win in a contradiction, like `-O3` after `-O2`):

| Where | Compiling | Linking |
|---|---|---|
| environment variable | `CFLAGS` | `LDFLAGS` |
| option of `build`, `run` and `test` | `-cflags "..."` | `-ldflags "..."` |

`CC` picks the compiler (`CC=gcc mlc build ...`, default: `cc`). Flags are separated by spaces, without quotes. The ones `mlc` needs (`-std=c11`, `-pthread`, `-lm`) are still there. If the compiler refuses a flag, the message says it may have been that flag, instead of reporting an internal `mlc` error.

## Formatting (`mlc fmt`)

`mlc fmt` rewrites files in the canonical form, like `gofmt`: 4-space indentation, one space around binary operators and after commas, none after unary operators (`-x`, `*p`, `&s`), end-of-line comments aligned. The author's line breaks, parentheses and comments are kept; consecutive blank lines become one. A file with a syntax error isn't formatted. `mlc fmt -` reads from standard input and writes to standard output (for editors).

## Documentation (`mlc doc`)

`mlc doc` shows in the terminal the documentation taken from the `//` comments right above each declaration and from the comment at the top of the file (the module's description), like `go doc`:

```sh
mlc doc                          # the standard library's modules, with each one's description
mlc doc http                     # the module: description, constants, types with their methods, functions
mlc doc http.Message             # a type: the declaration (with the fields), the description and the methods
mlc doc http.Message.read_body   # a method (mlc doc http Message.read_body works too)
mlc doc http.respond             # a function; constants, interfaces and enum variants too
mlc doc util/text.clean          # a library of yours, looked up like the import (-I and MLC_PATH apply)
mlc doc prog.mlc                 # a file
mlc doc alloc                    # a built-in (io.printf, thread.spawn, owned...); mlc doc builtin lists them all
```

Only what's `pub` shows up, except in a file with nothing `pub` (a program), which shows everything; `-all` always shows everything. Symbol names are case-insensitive. A declaration's comment is the block of `//` lines attached to it; a blank line separates it. Lines indented by 4 spaces inside the comment are code examples. The LSP hover uses the same comments.

## Tests (`mlc test`)

A `name_test.mlc` file declares `fn test_name()` functions and uses the checks from `std/testing` (`testing.eq_int(v.len(), 1)`, `testing.eq_str(&buf[:n], "hi")`, `testing.expect_panic()`). `mlc test` runs each test in a separate process, so a panic or a failure stays in the test that caused it. A failing check points at its line. The standard library has its tests in `std/*_test.mlc`. See [docs/testing.md](docs/testing.md).

## Libraries

An `import "json/parse"` looks for `json/parse.mlc` in this order, and the first folder that has the file wins:

1. the main file's folder;
2. the `lib/` folder next to it (libraries copied into the project);
3. the `-I` folders (`mlc build -I dir`, `mlc run -I dir`);
4. the folders in the `MLC_PATH` variable, separated by `:` (libraries shared between projects).

Imports inside a library follow the same search, so a library in `~/mlc-libs/json/` imports its own parts as `import "json/lexer"`. The LSP uses `lib/` and `MLC_PATH`, so the editor finds the same libraries. `import "std/..."` is always the standard library, built into `mlc`.

## Editors

`mlc lsp` is a Language Server Protocol server (over standard input and output). It marks errors on the line as you type, without saving: syntax, types and memory ownership, the same as `mlc build`. It also provides semantic colors (keywords, types, functions, constants, literals, comments) and formats through `mlc fmt`. **Hover** shows the declaration of any name (variable, parameter, constant, function, method, field, type, variant, module, built-in function) with its type and the `//` comments right above it. **Go to definition** takes you to the declaration, including in other files and in the standard library (copied to the system's cache folder, for the editor to open). **Completions** look at the context: after `io.` or `vec.`, what the module exports (including built-ins, like `io.printf` and `thread.spawn`); after `x.`, the fields and methods of `x`'s type (including those from `impl` and from generic structs, and those of channels, mutexes and `io.Writer`); inside `import "...`, the modules; inside a `match` on an enum, the variants; and elsewhere, the variables visible at that point, the file's declarations, the imported modules, the built-in functions, the types and the keywords. They work with half-written text: if the current line doesn't compile yet, the analysis uses a placeholder name at the cursor, a blank line, or the last analysis that succeeded. A module without `fn main` is checked through the nearest `main.mlc` that imports it, so imports resolve as in a build.

**VS Code** (`editors/vscode`): an extension with a TextMate grammar (immediate colors) and an LSP client.

```sh
cd editors/vscode
npm install --omit=dev
npx @vscode/vsce package --allow-missing-repository --skip-license
code --install-extension mlc-0.1.0.vsix
```

`mlc` must be in the `PATH`, or configured in `mlc.path`.

**Neovim 0.11+** (`editors/nvim`): file type, syntax, 4-space indentation and the LSP through `vim.lsp.config`/`vim.lsp.enable`, without nvim-lspconfig. With lazy.nvim (LazyVim):

```lua
-- ~/.config/nvim/lua/plugins/mlc.lua
return {
  { dir = "~/Projects/mlc/editors/nvim", lazy = false },
}
```

For another path to the executable: `vim.lsp.config("mlc", { cmd = { "/path/to/mlc", "lsp" } })`. Errors show up through `vim.diagnostic` and formatting through `vim.lsp.buf.format()` (LazyVim already formats on save).

## Layout

| Folder | Role |
|---|---|
| `cmd/mlc` | command line (`build`, `run`, `test`, `fmt`, `doc`, `lsp`) |
| `internal/token` | tokens, positions and compile errors |
| `internal/lexer` | tokens, with automatic `;` at the end of the line |
| `internal/parser` | recursive descent parser → AST |
| `internal/ast` | syntax tree |
| `internal/checker` | names, types, ownership and stack |
| `internal/codegen` | AST → C11 |
| `internal/driver` | chains the phases and calls `cc` |
| `internal/format` | `mlc fmt`: a formatter over the tokens, with the comments |
| `internal/lsp` | `mlc lsp`: diagnostics, semantic colors and formatting for editors |
| `internal/fuzz` | mutation, the program generator, enumeration and the reference model that test the checker |
| `editors/` | VS Code extension and Neovim plugin |
| `rt/c` | the C runtime, built into the `mlc` binary |
| `std` | the standard library written in MLC (`import "std/io"`, `import "std/math"`), built into the binary, with its tests in `std/*_test.mlc`. See [docs/library.md](docs/library.md) |
| `tests/run` | programs and expected output (`.out`); optional input (`.in`) |
| `tests/panic` | programs that must abort (`.out` + `.err`) |
| `tests/errors` | programs that must be rejected (`// error: ...` on the 1st line) |
| `tests/testing` | a test file with known results, to check `mlc test` itself |

A multi-file program lives in its own folder, with the main file in `main.mlc` (for example `tests/run/project/main.mlc`) and the imported files next to it.

```sh
go test ./...
```

## Current guarantees

- `realloc` and `free` are the only ways to change heap memory: there's no automatic growth.
- A conversion between numbers that doesn't fit is a panic (in C it would be undefined behavior); a constant that doesn't fit is a compile error.
- Integer overflow, division by zero and out-of-bounds indices are panics with the `.mlc` file and line, never C undefined behavior.
- `#line` directives in the generated C: panics and errors point to the MLC code.
- Printing checked at compile time: `io.printf("x = {}\n", x)` checks the format and that each value implements `io.Display`, and becomes direct calls, written in MLC, with no C `printf` and no allocation.
- A variable declared and never read is a compile error, as in Go.
- `match` without fallthrough and with mandatory coverage: forgetting a case is a compile error.
- A struct literal lists every field (`Point{}` is the explicit zero value): a forgotten new field is a compile error.
- Overflow and division by zero in a `const` are compile errors.
- Explicit memory (`alloc`/`free`) with an ownership checker at compile time: use after `free`, double `free`, leaks, null pointers and dangling pointers are compile errors. See [docs/memory.md](docs/memory.md).
- The stack is checked: a thread or `main` whose stack doesn't fit is refused at compile time, and deep recursion panics instead of corrupting memory. See [docs/memory.md](docs/memory.md#stack).
- Defined evaluation order: left to right, always. The arguments of a call, including `io.print`'s, are all computed before the call.
- Every variable starts with the zero value (`0`, `false`, `""`): there's no reading of uninitialized memory.

## Principle: every allocation is explicit

The idea of MLC is that whoever programs understands and knows **how much and where** they're allocating memory. Every allocation appears in the code as an explicit call (`alloc`, `realloc`), and every release as `free`. No language construct allocates behind your back: there's no string concatenation that allocates, no built-in `append` and no implicit copies to the heap. The compiler checks that each allocation is freed exactly once.

## Conventions

- Library and runtime functions exposed in the language are always in English (`io.print`).
- Compiler messages and the tools' output are in English.

## Roadmap

- [x] **Milestone 1:** `import`, functions with parameters and return values, `int`/`bool`/`string` literals, checked arithmetic, `io.print`
- [x] **Milestone 2:** `var`, `:=`, assignment (`=`, `+=`...), `++`/`--`, `if`/`else if`/`else`, `for` (infinite, condition and three clauses), `break`/`continue`
- [x] **Milestone 3:** `match` with literal patterns and `_`, coverage checking; becomes a C `switch` on `int` and an `if` chain on `string`/`bool`; `==`/`!=` between strings
- [x] **Milestone 4:** `struct` (by value, in any declaration order), composite literals with every field explicit, reading and assigning fields, `==` and `io.print` of structs, `const` evaluated at compile time (also as a `match` pattern)
- [x] **Evaluation order:** arguments, operands and literal fields are evaluated left to right, with the order written in the generated C (temporaries + comma operator), not relying on the C compiler
- [x] **Milestone 5a:** `alloc`/`free`, `*T`, `?*T`, `&T` and the ownership checker for variables and parameters
- [x] **Milestone 5b:** pointers inside structs (lists, trees), with ownership tracked per field; `nil` checks by flow (`if`, `for`, `&&`, `||`); loops analyzed to a fixed point
- [x] **Milestone 6:** owning slices `[]T` (`alloc([]T, n)`, `free`), bounds checking, `len`, `for x in s`, `&[]T`; `len` and indexing on strings
- [x] **Slices, part 2:** `realloc(s, n)` (consumes the slice and returns another, new elements zeroed) and borrowed sub-slices `&s[a:b]`
- [x] **Slices of owners:** `[]?*T` and `[][]T`, with `take(&s[i])`, `replace(&s[i], v)` and `for x in drain(s)`; `free(s)` proven safe at compile time
- [x] **Milestone 7:** text in explicit buffers: the `byte` type and `'a'` literals, `int(x)`/`byte(x)` conversions, the `str` module (`copy`, `put_int`, `int_len`, `eq`, `parse_int`) and `io.write`/`io.read_line`, none of which allocate. See [docs/text.md](docs/text.md)
- [x] **Multiple files:** `import "geometry"` (path relative to the main file's folder), explicit visibility with `pub`, qualified names (`geometry.area`, `geometry.Point`), circular imports refused
- [x] **Methods:** `fn (p &Point) area() int`, called as `p.area()`; the receiver (`Point`, `&Point` or `*Point`) says whether the call copies, borrows or moves
- [x] **Generics:** `fn larger[T ordered](a T, b T) T`, `struct Pair[A any, B any]`, generic methods, inference; constraints `any`, `comparable`, `ordered`, `number`, `integer`; monomorphization in C
- [x] **Enums with data:** `enum Shape { Circle(radius float64); Empty }`, `match` with destructuring and mandatory coverage, generic enums (`Result[T]`) inferred from the value or from the context
- [x] **Numbers:** `int8`, `int16`, `int32`, `uint16`, `uint32`, `uint64`, `float32`, `float64`; untyped constants; checked conversions. See [docs/types.md](docs/types.md)
- [x] **Arrays `[N]T`:** fixed size, by value, on the stack or in structs, without `alloc`; `&a` borrows as `&[]T`
- [x] **Standard library** in MLC, imported with `std/`: `io`, `str`, `result`, `math`, `sort`, `text`, `vec`, `rand`, `os`, `fs`, `time`, `testing`, `thread`, `chan`, `mutex`, `atomic`, `args`, `process`, `bytes`, `utf8`, `net`, `json`, `encoding`, `cmp`, `slices`, `hash`, `map`, `bufio`, `path`, `binary`, `deque`, `heap`, `signal`, `log`, `sync`, `http`, `url`, `hmac`, `uuid`, `sqlite`, `config`, `csv`, `arena`, `tls`, `password`. Nothing allocates behind your back; errors as `Result[T, E]`. See [docs/library.md](docs/library.md)
- [x] **`mlc fmt`:** canonical formatter (4 spaces), keeping comments and line breaks; `-l` lists, `-` uses standard input
- [x] **Static interfaces:** `interface`, `impl` (one-line, with a block or generic) and `impl auto`; interfaces as generic constraints, with direct calls (no method tables). `io.print`/`io.printf`/`io.eprintf` over `io.Display`, with the formatting in MLC. See [docs/interfaces.md](docs/interfaces.md)
- [x] **Bits and functions as values:** `& | ^ << >>`, `^x`, wrapping arithmetic (`+% -% *%`), literals `0xFF`, `0b1010`, `0o17`, `1_000_000`; `fn(int) bool` types, functions as values and indirect calls (no closures, no allocation). `std/rand` became MLC; new `std/hash` and `sort.sort_by`. See [docs/types.md](docs/types.md)
- [x] **Owners in generics:** the `owned` constraint (`Vec[[]byte]`, `Vec[?*Node]`), `drop(x)` in generic code, constraints on the receiver (`fn (v &Vec[T any]) get`); `std/vec` with `push`/`pop`/`take`/`put`/`set` moving ownership and `free` freeing the elements; ownership errors reported at the generic's definition. See [docs/types.md](docs/types.md)
- [x] **Hash map in MLC:** `std/map` (`Map[K owned + hash.Hash, V owned]`, open addressing) and the `hash.Hash` interface for integers, `bool`, `string` and slices; constraints combined with `+`. See [docs/library.md](docs/library.md)
- [x] **Returned and stored borrows:** `fn (v &Vec[T]) at(i int) &T`, `x := m.get(&k)`, `x := &a[i]`; the source stays frozen while the borrow is used, checked at compile time. `vec.at`/`last` and `map.get`/`key_ref`/`value_ref`. See [docs/memory.md](docs/memory.md)
- [x] **Tests:** `mlc test` runs the `test_*` functions of `*_test.mlc` files, each in a process, and accepts `-run` and `-v`. The checks live in `std/testing` (`eq_int`, `eq_str`, `near`, `expect_panic`, ...) and point at the line that failed. Every `std` module has its tests. See [docs/testing.md](docs/testing.md)
- [x] **Documentation in the terminal:** `mlc doc` (module, type, function, method or built-in, from the standard library or your own libraries), with every `pub` declaration of the `std` commented (checked by a test). See [Documentation](#documentation-mlc-doc)
- [x] **LSP:** `mlc lsp` with diagnostics as you type, colors, formatting, hover, go to definition and completions; a VS Code extension and a Neovim configuration. See [Editors](#editors)

### Next steps (in order of priority)

- [x] **Nested owners:** `Vec[?*vec.Vec[int]]`, `Map[[]byte, ?*vec.Vec[[]byte]]`, `Vec[[][]byte]` and trees (`Vec[?*Value]` inside `Value`); structs with owners are freed with `fn (x *X) free()`, which the generic `drop` calls. Enums with owning data came with the errors-with-content item. See [docs/types.md](docs/types.md)
- [x] **FFI for user libraries:** `extern "C" fn` in any file, with the boundary checked (numbers, `string`, `&[]T`, `&T` and `extern "C" type` handles, which the checker forces you to close); `link "name"` and `-L`; link errors reported as program errors. See [docs/ffi.md](docs/ffi.md)
- [x] **Library search path:** imports looked up in the project folder, in `lib/`, in the `-I` folders and in `MLC_PATH`'s. See [Libraries](#libraries)
- [x] **Formatting into a buffer:** `str.format(&dst, "...", x)`, `str.format_len` and `w.printf` (on a reused Writer, without a flush on every call) over the same `io.Writer` as `io.printf`, with the destination checked at run time. Specs `{:x}`, `{:#x}`, `{:.2}`, `{:>8}`, `{:*^8}`, `{:08}` and `{:s}`, with the width counted in characters. See [docs/text.md](docs/text.md)
- [x] **Out parameters in the FFI:** `extern "C" fn sqlite3_open(name string, db &?*sqlite3) int32` and `sqlite3_open("x.db", &db)`. The checker requires `db` to hold no handle before the call and requires closing it afterwards. See [docs/ffi.md](docs/ffi.md#handles-returned-through-a-pointer-x)
- [x] **A library for command-line tools:** `fs` with open files (`open`, `read`, `write`, `seek`), directories, `stat`, `mkdir` and `rename`; `args` (options and positionals); `process` (running programs, without a shell); `bytes.Buf` (growing text, in plain sight); `utf8`; `text.split_next` and `word_next`; `os.setenv`, `cwd` and `chdir`. See [docs/library.md](docs/library.md)
- [x] **Network, JSON and dates:** `std/net` (TCP: `listen`, `accept`, `connect`, `read`, `write`, timeouts, portable errors, no `SIGPIPE`); `std/json` (an allocation-free reader that returns tokens as an enum, `skip`, `escape`/`unescape`); `std/time` with dates (`utc`, `local`, `iso`, `parse_iso`). See [docs/library.md](docs/library.md)
- [x] **Hash, encoding and benchmarks:** SHA-256 (incremental) and CRC32 in `std/hash`; `std/encoding` with hex and base64; benchmarks in `mlc test -bench` (`fn bench_*(b &testing.B)`). See [docs/testing.md](docs/testing.md#benchmarks)
- [x] **Optional `NULL` pointers in the FFI:** `?&T`, `?&[]T` and `?&?*X` in `extern "C" fn` parameters, which accept `nil` or a borrow (`getaddrinfo(host, port, nil, &res)`, `sqlite3_exec`'s `errmsg`). See [docs/ffi.md](docs/ffi.md#pointers-that-may-be-null-t)
- [x] **Generic reading, paths and error texts:** the `io.Reader` interface (implemented by `fs.File`, `net.Conn` and `io.stdin()`), `std/bufio` (`read_line`, `read_until`, `read_exact`, `read_byte`, with a fixed buffer inside the reader and the source borrowed on each call), `std/path` (`join`, `clean`, `dir`, `base`, `ext`), a single error-code space and `os.error_text`. Opaque C types as arguments of `[T ref]` generics. See [docs/library.md](docs/library.md#stdbufio)
- [x] **Numbers as bytes:** `std/binary` with 16-, 32- and 64-bit integers (signed and unsigned) and reals in big-endian and little-endian, and varints (zigzag for signed ones); `math.float64_bits`/`float64_from_bits`. See [docs/library.md](docs/library.md#stdbinary)
- [x] **Queues:** `std/deque` (a ring buffer, push and pop at both ends in O(1)) and `std/heap` (a priority queue over `cmp.Ord`, `new_max`, `replace_top` for top-K), with visible allocation and owners as elements. See [docs/library.md](docs/library.md#stddeque)
- [x] **Services:** `std/signal` (Ctrl+C and `TERM` recorded, `received`, `wait`), `std/log` (a logger as an object, no global state, with level, time, `lg.info("...", x)` checked at compile time and whole lines across threads), `std/sync` (a thread group whose `wait` the checker requires) and a timeout on `accept`. `tests/run/service.mlc` shuts down calmly on `TERM`. See [docs/library.md](docs/library.md#stdsignal)
- [x] **HTTP/1.1:** `std/http` with a server and a client over `net.Conn` (or any `http.Stream`), without allocation: `Message` with a fixed buffer, `Content-Length`, `chunked`, keep-alive, decoded query, `Builder` with automatic `Content-Length`; `sync.group` as the limit of simultaneous connections (`add` waits for a slot, `reap`). HTTPS through `std/tls`. See [docs/library.md](docs/library.md#stdhttp)
- [x] **Basic security:** `rand.secure` (bytes from the system's cryptographic source, unbiased `secure_int_n`, `seeded`), `std/hmac` (HMAC-SHA256 with `verify` and constant-time comparison, tested with RFC 4231) and `std/uuid` (v4 and time-ordered v7). See [docs/library.md](docs/library.md#stdhmac)
- [x] **URL, HTTP client and router:** `std/url` (parts, default port, IPv6, decoded query, `Query` for building), `http.get`/`post`/`request` and `dial` + `request_to` for custom headers, `http.Router[S, C]` (S: `net.Conn` or `tls.Conn`) with `{id}` and `{rest...}`, 404/405, and `serve_conn` with the keep-alive loop. See [docs/library.md](docs/library.md#router)
- [x] **SQLite:** `std/sqlite` with the database and the prepared statement as handles the checker requires closing, parameters, transactions, columns copied into buffers and errors in the single space (4000 + SQLite's code). The system library is loaded only when used, without flags. See [docs/library.md](docs/library.md#stdsqlite)
- [x] **Configuration, CSV and file trees:** `std/config` (dotenv and INI, sections, quotes, `env` that prefers the environment), `std/csv` (streaming reader and writer, RFC 4180, configurable separator) and `fs.walk`, `glob` (with `**`), `mkdir_all`, `remove_all` (without following links) and `temp_path`. See [docs/library.md](docs/library.md#stdconfig)
- [x] **Arena:** `std/arena`, scratch memory per task with a single `alloc`, pieces as a `Span` (position, size and generation), a `reset` that gives everything back and a panic when using a piece from before the `reset`. The leak message now suggests `x.free()` for types that have that method. See [docs/library.md](docs/library.md#stdarena)
- [x] **TLS, JSON and passwords:** `std/tls` (client and server with the system's OpenSSL loaded on demand; certificate, validity and name always checked; `http.get` with `https://` and `Router[tls.Conn, C]` to serve HTTPS), `json.Writer` (the right commas, quotes and escapes, a growing buffer) and `std/password` (PBKDF2-HMAC-SHA256 with 600 thousand iterations, random salt, PHC format, constant-time `verify`; `hmac.pbkdf2` tested with the RFC 7914 vectors). See [docs/library.md](docs/library.md#stdtls)
- [x] **Checked stack:** the checker estimates each function's stack (its frame plus the largest stack of the functions it calls) and refuses a `thread.spawn` or a `main` that doesn't fit, naming the heaviest variable and the suggested constant; functions with a large frame check on entry whether they fit in what's left of the stack and panic instead of jumping over the guard page (recursion, `fn` values, computed sizes). See [docs/memory.md](docs/memory.md#stack)
- [x] **Testing the checker on purpose:** a run-time oracle (AddressSanitizer, UndefinedBehaviorSanitizer, ThreadSanitizer and the allocation counter) over the suite, mutation of correct programs, an ownership-focused program generator, exhaustive enumeration of small programs against a reference model, and metamorphic and coverage tests. See [docs/verification.md](docs/verification.md)
  - [x] phase 1: an oracle over the current suite (`-sanitize address|thread`, `make sanitize`): the whole suite passes under AddressSanitizer + UBSan and ThreadSanitizer
  - [x] phase 2: mutation of correct programs (`make mutate`): 1,944 mutants of 116 programs, all refused or bug-free; it found and fixed one hole (a borrow of an enum with owners already freed)
  - [x] phase 3: an ownership-focused program generator (`make fuzz`): valid programs and programs with an injected bug that execution sees; an accepted bug is reduced to the smallest example, and the refused valid ones form the catalog of false positives
  - [x] phase 4: exhaustive enumeration with a reference model (`make enum`): every program up to size 6 (12.7 million) over an ownership alphabet, also judged by an abstract interpreter that follows each path; no holes, and the catalog of false positives (all from the borrow that lasts until its last textual use)
  - [x] phase 5: robustness (`make robust`: `go test -fuzz` over the compiler and the formatter; it found and fixed 4 panics and 4 formatter errors), coverage (`make cover`, with tests for the safety rules no test triggered), metamorphic tests and classic hard cases
- [ ] **C-to-MLC callbacks in the FFI:** function pointers at the boundary (`qsort`, `sqlite3_exec` with a function), with the C APIs' `void *ctx`
- [x] **Callbacks with a context:** methods with their own type parameters (`fn (m &Map[K, V]) each_with[C ref](ctx &C, f fn(&C, &K, &V))`), inferred at the call or written out; the `ref` constraint for the context, which is only borrowed; `map.each_with`, `vec.each_with`, `sort.sort_by_with`, `slices.index_by` and `count_by`. See [docs/types.md](docs/types.md#generic-methods-and-callbacks-with-a-context)
- [x] **Errors with content:** enums with owning data (messages explicitly allocated by whoever creates the error, freed by whoever handles it); a `match` that consumes or only reads, `free` of an enum; `Result[T, E]` with owners on both sides. No propagation shortcut (`try`): every exit from a function is still written as a `return`. See [docs/types.md](docs/types.md#enums-with-owners)
- [x] **`Ord` and `Eq` interfaces:** `std/cmp` with `Eq` and `Ord` (already implemented for numbers, `bool`, `string` and slices); `impl auto cmp.Eq`, `cmp.Ord` and `hash.Hash` for structs and enums; `sort.sort` over `cmp.Ord`, swapping by ownership (sorts structs and owners); `std/slices` (`index`, `contains`, `count`, `equal`, `search`, `binary_search`, `min_index`, `max_index`, `dedup`, `fill`). See [docs/interfaces.md](docs/interfaces.md)
- [ ] **Iterators as objects:** borrows stored in structs, with their lifetime checked
- [ ] **Dynamic interfaces:** `&I` and `*I`, to store values of different types together (no boxing: only from `&T` or `*T`)
- [ ] **Concurrency:** system threads created explicitly, with ownership guaranteeing the absence of data races. See [docs/concurrency.md](docs/concurrency.md)
  - [x] phase A: `thread.spawn(f, ctx, stack)` and `thread.join(t)`; the context and the result cross by move; the stack has an explicit size; every `spawn` needs a `join`
  - [x] phase B: fixed-capacity channels (`std/chan`): `chan.new[T](n)`, `Sender`/`Receiver` ends that need `close()`, `recv` returning `Got(v)`/`Closed`, a `send` without a receiver freeing the value, several producers and consumers
  - [x] phase C: `mutex.new(v)`, owner of the value, with `lock()` returning a guard that needs `unlock()` and `get()` borrowing the value only while the lock exists; `wait`/`notify`; locking twice on the same thread panics; `atomic.Int` and `atomic.Bool`
  - Off the roadmap: scoped threads that receive borrows (disjoint pieces of the same buffer). They only come in if a real program needs them; before that would come a read-only borrow, useful for the whole language. See [docs/concurrency.md](docs/concurrency.md#what-was-left-out)
