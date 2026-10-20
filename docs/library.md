# Standard library

The standard library is written in MLC, lives in [`std/`](../std) and is built into the `mlc` binary. Each module is imported with the `std/` prefix and used by the last name of the path:

```
import "std/io"
import "std/math"
import "std/sort"

fn main() {
    var a [4]int = [4]int{3, 1, 4, 1}
    sort.sort(&a)
    io.print(a[0], a[3], math.sqrt(2.0), math.max(3, 7))
}
```

Everything that comes with the language is here, including input and output (`std/io`) and text (`std/str`), detailed in [text.md](text.md). Even printing is MLC: `io.print` and `io.printf` are rewritten by the compiler into calls to each value's `write` method (the `io.Display` interface), implemented in [`std/io.mlc`](../std/io.mlc). See [interfaces.md](interfaces.md).

## Library rules

- **Nothing allocates behind your back.** Functions write into buffers borrowed from the caller (`dst &[]byte`) and return how many bytes they used, or keep a fixed buffer inside their own struct (`http.Message`, `url.URL`, `config.Config`, `csv.Reader`). Only the types that exist for that allocate, and always visibly: they're created by a `new` function (or `writer`, `open`) that does the `alloc`, they grow with `realloc` written in MLC in the module itself, and they need a `free` (or `close`), enforced by the checker. They are `vec`, `map`, `deque`, `heap`, `bytes.Buf`, `arena`, `json.Writer`, and the system handles (`fs.File`, `net.Conn`, `tls.Conn`, `sqlite.DB`...). See [`std/vec.mlc`](../std/vec.mlc) as an example.
- **Errors are values.** Whatever can fail returns a `result.Result[T, E]`, the enum from `std/result`; `fs` and `os` return `Result[int, int]` (bytes used or an error code). The `match` forces you to handle both cases:

  ```
  match fs.read_file(&path[:n], &buf) {
      Ok(got) => io.write(&buf[:got])
      Err(code) => io.print("error", code)
  }
  ```

- **A single error-code space.** Below 1000 are the system's (`errno`: `fs.NOT_FOUND` and `os.NOT_FOUND` are 2, `fs.DENIED` is 13); from 1000 up they're MLC's and the same on every system:

  | Range | Codes |
  |---|---|
  | 1000–1004 | general: `TOO_BIG` 1000 (doesn't fit), `fs.NOT_EMPTY` 1001, `TOO_LONG` 1002 (`os`, `process`, `bufio`, `csv`), `io.EOF` 1003, `fs.TOO_DEEP` 1004 |
  | 2001–2005 | `net`: `REFUSED`, `NO_HOST`, `IN_USE`, `TIMEOUT`, `RESET` |
  | 3001–3003 | `http`: `BAD_MESSAGE`, `HEAD_TOO_BIG`, `UNSUPPORTED_SCHEME` |
  | 3101, 3201, 3301 | `url.BAD_URL`, `config.BAD_LINE`, `csv.BAD_QUOTE` |
  | 4001–4899, 4900 | `sqlite`: 4000 + SQLite's code (`ERROR` 4001, `BUSY` 4005, `CONSTRAINT` 4019...), `UNAVAILABLE` 4900 |
  | 5001–5005 | `tls`: `HANDSHAKE`, `BAD_CERT`, `UNAVAILABLE`, `BROKEN`, `SERVER_FILES` |

  `os.error_text(code, &msg)` writes the text of any of them:

  ```
  Err(code) => {
      var msg [160]byte                  // always enough
      k := os.error_text(code, &msg)     // "doesn't exist", "end of the data", "connection refused: ..."
      io.eprintf("error: {:s}\n", &msg[:k])
  }
  ```

- **Names in English**, like the whole runtime.
- **Tested in MLC.** Every module has tests in `std/<module>_test.mlc`, which run with `mlc test std` (or `make test-std`) and under `go test`. See [testing.md](testing.md).
- **What needs the system** (files, clock, `libm`) is declared as `extern fn` and implemented in the C runtime (`mlc_std_<module>_<name>`). `extern fn` is only accepted inside the standard library; the rest is ordinary MLC, with the same ownership and type checks as any program.

## Modules

| Area | Modules | What for |
|---|---|---|
| Text and numbers | [`io`](#stdio-and-stdstr), [`str`](#stdio-and-stdstr), [`text`](#stdtext), [`utf8`](#stdutf8), [`bytes`](#stdbytes), [`math`](#stdmath), [`binary`](#stdbinary), [`encoding`](#stdencoding) | printing and formatting, searching and cutting text, UTF-8, growing text, math, numbers as bytes, hex and base64 |
| Collections and order | [`vec`](#stdvec), [`map`](#stdmap), [`deque`](#stddeque), [`heap`](#stdheap), [`slices`](#stdslices), [`sort`](#stdsort), [`cmp`](#stdcmp) | growing vector, hash table, double-ended queue, priority queue, operations on slices, sorting |
| Memory | [`arena`](#stdarena) | scratch memory per task, with a single alloc |
| Errors | [`result`](#stdresult) | the value or the error, with match |
| Files and system | [`fs`](#stdfs), [`path`](#stdpath), [`bufio`](#stdbufio), [`os`](#stdos), [`args`](#stdargs), [`process`](#stdprocess), [`signal`](#stdsignal), [`time`](#stdtime) | files, directories and trees, paths, buffered reading, environment, arguments, other programs, signals, clock and dates |
| Data and configuration | [`json`](#stdjson), [`csv`](#stdcsv), [`config`](#stdconfig), [`sqlite`](#stdsqlite) | JSON (reading and writing), spreadsheets, dotenv and INI files, database |
| Network | [`net`](#stdnet), [`http`](#stdhttp), [`url`](#stdurl), [`tls`](#stdtls) | TCP, HTTP/1.1 (server, client, router), URLs, HTTPS |
| Security | [`rand`](#stdrand), [`hash`](#stdhash), [`hmac`](#stdhmac), [`password`](#stdpassword), [`uuid`](#stduuid) | randomness (and secure randomness), hashes, signatures, passwords, identifiers |
| Concurrency | [`thread`](#stdthread), [`chan`](#stdchan), [`mutex`](#stdmutex-and-stdatomic), [`atomic`](#stdmutex-and-stdatomic), [`sync`](#stdsync) | threads, channels, mutexes, atomics, groups (details in [concurrency.md](concurrency.md)) |
| Observability and tests | [`log`](#stdlog), [`testing`](#stdtesting) | logging with level and time, tests and benchmarks |

The details of each function are also in the code itself, and `mlc doc` shows them in the terminal (`mlc doc http`, `mlc doc http.Router`).

### `std/io` and `std/str`

Input and output (`io.print`, `io.printf`, `io.eprint`, `io.eprintf`, `io.write`, `io.write_line`, `io.read_line`), the `io.Display` interface and `io.Writer` (writing with a fixed buffer on the stack), see [interfaces.md](interfaces.md); and text in buffers (`str.copy`, `str.put_int`, `str.parse_int`, `str.format`, `str.format_len`, ...). See [text.md](text.md). The `str` functions that may panic for lack of room are `extern fn`, so the panic points at the caller's line; the others (`int_len`, `eq`, `eq_bytes`, `parse_int`) are MLC, in [`std/str.mlc`](../std/str.mlc).

**Generic reading:** the `io.Reader` interface is any source of bytes, and `std/bufio` reads lines and blocks from any of them.

```
pub interface Reader {
    fn read(self &Self, dst &[]byte) result.Result[int, int]   // Ok(0): end of the data
}
```

| Declaration | What it is |
|---|---|
| `io.Reader` | implemented by `fs.File`, `net.Conn`, `tls.Conn`, `io.Stdin` and by the program's own types (`impl io.Reader for MySource`) |
| `io.stdin() Stdin` | standard input as a `Reader` (a value on the stack, nothing to free); `read` stops after a `\n`. It uses the same buffer as `io.read_line`, so the two can be mixed |
| `io.EOF` (1003) | the buffered readers' error at the end of the data |

### `std/result`

| Declaration | What it is |
|---|---|
| `enum Result[T owned, E owned] { Ok(value T); Err(err E) }` | the result of an operation that can fail. `E` can be a code (`int`) or an enum with content, including owners (a message allocated by whoever creates the error): the `match` that consumes the `Result` hands over ownership, and whoever handles it frees it. There's no propagation shortcut (`try`): each exit is a written `return`. See [types.md](types.md#enums-with-owners) |

### `std/math`

| Declaration | What it does |
|---|---|
| `PI`, `E` | untyped constants (they work as `float32` or `float64`) |
| `abs[T number](x T) T` | absolute value |
| `min`, `max[T ordered](a T, b T) T` | smaller and larger |
| `clamp[T ordered](x T, lo T, hi T) T` | `x` limited to `[lo, hi]` |
| `sqrt`, `floor`, `ceil`, `round`, `sin`, `cos`, `tan`, `log`, `exp` | `float64` → `float64`, from `libm` |
| `pow(x, y)`, `atan2(y, x)` | `float64` |
| `is_nan(x)`, `is_inf(x)` | `bool` |
| `float64_bits(x) uint64`, `float64_from_bits(u) float64` | a real's IEEE 754 bits, and back (`float32_bits`, `float32_from_bits` for `float32`) |

### `std/sort`

They sort a `&[]T` in place (a borrowed slice, or an array with `&a`). The order is `cmp.Ord`'s: numbers, `bool`, `string`, slices (`[][]byte`, lines of text) and structs with `impl cmp.Ord` (or `impl auto cmp.Ord`). Elements swap places by ownership, without copies, so owners are sorted too.

| Function | What it does |
|---|---|
| `sort[T owned + cmp.Ord](s &[]T)` | sorts in ascending order (quicksort with insertion sort on small parts; doesn't allocate) |
| `is_sorted[T owned + cmp.Ord](s &[]T) bool` | is it already in order? |
| `reverse[T owned](s &[]T)` | reverses the order |
| `swap[T owned](s &[]T, i int, j int)` | swaps two elements (by ownership) |
| `sort_by[T any](s &[]T, less fn(&T, &T) bool)` | sorts by the `less` function (for structs or another order) |
| `sort_by_with(s, &ctx, less)` | sorts by `less(&ctx, &a, &b)`, which looks at the context (ids by the priority kept in a `map`, for example) |
| `is_sorted_by[T any](s &[]T, less fn(&T, &T) bool) bool` | is it already in `less` order? |

### `std/cmp`

The `Eq` (`eq(self &Self, other &Self) bool`) and `Ord` (`cmp(self &Self, other &Self) int`: negative, zero or positive) interfaces, already implemented for numbers, `bool`, `string` and slices (lexicographic order). For structs and enums, `impl auto cmp.Eq` and `impl auto cmp.Ord`. See [interfaces.md](interfaces.md#equality-order-and-hash-cmpeq-cmpord-and-hashhash).

### `std/slices`

Operations on `&[]T` that compare by borrow (they work for owners and for structs with `cmp.Eq`/`cmp.Ord`). They don't allocate.

| Function | What it does |
|---|---|
| `index(s, &x) int` | position of the first one equal to `x`, or `-1` |
| `contains(s, &x) bool`, `count(s, &x) int` | |
| `equal(a, b) bool` | same length and same elements |
| `search(s, &x) int` | in a sorted slice: where `x` is or would go |
| `binary_search(s, &x) int` | in a sorted slice: the position of `x`, or `-1` |
| `min_index(s) int`, `max_index(s) int` | the smallest and the largest (the first one, on a tie); `-1` if empty |
| `dedup(s) int` | in a sorted slice, gathers the unique ones in `s[:n]` (the repeats go after, nothing is freed) |
| `index_by(s, &ctx, pred) int`, `count_by(s, &ctx, pred) int` | by the result of `pred(&ctx, &x)`: the context carries what to look for (a limit, a name) |
| `fill(s, x)` | puts `x` in every position (only for copyable types) |

### `std/text`

Functions on `&[]byte` that don't allocate. The ones that search return an index, or `-1` if they don't find it.

| Function | What it does |
|---|---|
| `is_space(b)`, `is_digit(b)`, `is_letter(b)` | classify a byte (ASCII) |
| `index_byte(s, b) int` | first position of `b` |
| `index_of(s, sub string) int` | first position of `sub` |
| `contains(s, sub)`, `starts_with(s, prefix)`, `ends_with(s, suffix)` | `bool` |
| `count_byte(s, b) int` | how many times `b` appears |
| `trim_start(s) int`, `trim_end(s) int` | where the text starts and ends without spaces: `&s[trim_start(s):trim_end(s)]` |
| `field_end(s, sep, start) int` | where the field that starts at `start` ends (the position of the next `sep`, or `len(s)`) |
| `split_next(s, sep, &pos) Span` | the next field separated by `sep` (`&s[f.start:f.end]`) and advances `pos`; it's over when `pos > len(s)`. Empty fields count |
| `word_next(s, &pos) Span` | the next word (separated by spaces); an empty `Span` at the end |
| `to_upper(s)`, `to_lower(s)` | change the case in place (ASCII) |

```
pos := 0
for pos <= len(line) {
    f := text.split_next(&line, ',', &pos)
    ... &line[f.start:f.end] ...
}
```

### `std/utf8`

| Function | What it does |
|---|---|
| `decode(s, i) Rune` | the character that starts at `s[i]`: `r.code` and `r.size` (1 to 4 bytes). Invalid bytes give `INVALID` (U+FFFD) with size 1 |
| `count(s) int` | how many characters (each invalid byte counts as one) |
| `valid(s) bool` | is `s` valid UTF-8? (overlong forms and surrogates aren't) |
| `encode(dst, code) int` | writes the character at the start of `dst` and returns how many bytes it used |

### `std/bytes`

Growing text, with the growth visible in [`std/bytes.mlc`](../std/bytes.mlc), like `vec`: `new` allocates, `reserve` doubles the capacity with `realloc`.

```
b := bytes.new(64)
b.push_str("total: ")
b.push_int(42)
b.reserve(str.format_len(" ({:.1}%)", p))          // format directly into the Buf
b.len += str.format(&b.data[b.len:], " ({:.1}%)", p)
io.write_line(&b.data[:b.len])                     // the text is &b.data[:b.len]
b.free()
```

| Method | What it does |
|---|---|
| `bytes.new(cap) *Buf` | allocates an empty `Buf` |
| `reserve(n)` | makes room for `n` more bytes |
| `push_byte`, `push_str`, `push_bytes`, `push_int` | append at the end |
| `truncate(n)`, `clear()` | shorten the text (the capacity stays) |
| `eq(s string) bool` | compares with a text |
| `free()` | frees it |

The text is `&b.data[:b.len]` (the fields are public): a method can't return that sub-slice, because the view is created on the spot.

### `std/arena`

Scratch memory for a task (an HTTP request, a file, a batch): one `alloc` at the start, the pieces come out of it in sequence without an `alloc` each, `reset` gives everything back for the next task, and `free` at the end. The cost of allocation stays constant, and forgetting to free a piece doesn't exist: `reset` takes them all.

```mlc
a := arena.new(64 * 1024)               // the only alloc
for ... each request ... {
    name := a.copy(&field)              // a piece (Span)
    if a.eq(name, "admin") { ... }
    a.reserve(64)
    k := str.format(&a.buf[a.used:a.used + 64], "id={}", id)
    key := a.commit(k)                  // written directly into the arena
    ...
    a.reset()                           // everything back; the buffer stays
}
a.free()
```

A piece is a `Span`: the position, the size and the arena's generation. It isn't a borrow, so the arena can grow (`realloc`, doubling) without invalidating anything. Each `reset` changes the generation, and using a `Span` from before the `reset` panics: it's a use after free, caught at run time, instead of reading another task's data. To read the buffer directly, check first with `a.check(s)` and use `&a.buf[s.at:s.at + s.n]`.

| Declaration | What it does |
|---|---|
| `new(cap) *Arena`, `new_fixed(cap) *Arena`, `a.free()` | creates (the fixed one never grows: going past the limit panics) and frees |
| `a.alloc(n) Span`, `a.copy(&src) Span`, `a.copy_str(s) Span` | a zeroed or copied piece |
| `a.reserve(n)`, `a.commit(n) Span` | write directly into `&a.buf[a.used:]` and turn it into a piece |
| `a.check(s)`, `a.len(s)`, `a.text(s, &dst)`, `a.eq(s, text)`, `a.get(s, i)`, `a.set(s, i, b)` | use a piece (they all check the generation) |
| `a.reset()` | gives everything back; the earlier pieces stop being valid |
| `a.in_use()`, `a.capacity()`, `a.high_water()` | the usage, the size of the buffer and the most ever used (the right size for the next `new`) |

### `std/vec`

A growing vector, for when the size isn't known in advance. It's the part of the library that allocates, and every allocation is visible:

```
v := vec.new[int](8)      // allocates the Vec and 8 slots
v.push(1)                 // if it doesn't fit, grow doubles the capacity with realloc
io.print(v.get(0), v.len(), v.cap())
v.free()                  // frees the items and the Vec; the checker requires this free
```

The elements can be owners (`Vec[T owned]`: `[]byte`, `?*Node`...). Ownership goes in and out explicitly:

```
names := vec.new[[]byte](8)
names.push(copy)           // copy was moved into the vector
name := names.take(0)      // ownership comes back (the zero value stays in place)
names.set(0, name)         // and goes into the vector again
names.free()               // frees each name (drop) and the vector
```

| Declaration | `T` | What it does |
|---|---|---|
| `new[T owned](cap int) *Vec[T]` | any | allocates an empty vector with capacity `cap` |
| `push(x T)` | any | moves `x` to the end; calls `grow` when it's full |
| `grow()` | any | the capacity becomes `2 * cap + 1` (`realloc`) |
| `pop() T` | any | takes the last one out and returns ownership; empty panics |
| `take(i) T` | any | takes element `i` out (the zero value stays) and returns ownership |
| `put(i, x T) T` | any | puts `x` at `i` and returns the old element |
| `set(i, x T)` | any | puts `x` at `i` and frees the old one (`drop`) |
| `swap(i, j)` | any | swaps two elements |
| `len() int`, `cap() int` | any | length and capacity |
| `clear()` | any | frees the elements (`drop`) and leaves the vector empty |
| `free()` | any | frees each element (`drop`) and the vector |
| `get(i) T` | copyable only (`any`) | returns a copy |
| `at(i) &T`, `last() &T` | any | borrows the element without taking it out; the vector stays frozen while the borrow is used |
| `each_with(&ctx, f fn(&C, &T))` | any | calls `f` with each element, in order, and the context |

Indices are checked: outside `0..len-1`, a panic.

### `std/deque`

A queue with both ends fast (a ring buffer): `push` and `pop` at the front and the back in O(1). It's the queue of a single thread (breadth-first search, pending tasks, bounded history, sliding window); between threads, use `std/chan`. It allocates like `vec`: `new` allocates, the `push`es call `grow` when it runs out of room, and `free` releases it. The elements can be owners: `push` moves inside, `pop` returns ownership.

```
q := deque.new[int](16)
q.push_back(start)
for q.len() > 0 {
    p := q.pop_front()
    ... q.push_back(neighbor) ...
}
q.free()
```

| Method | What it does |
|---|---|
| `deque.new[T owned](cap) *Deque[T]` | creates (allocates the buffer) |
| `push_back(x)`, `push_front(x)` | puts at the end or at the front (moves `x`) |
| `pop_front() T`, `pop_back() T` | takes out and returns ownership; panics if empty |
| `front() &T`, `back() &T`, `at(i) &T` | borrows, without taking out (the queue stays frozen while the borrow is used) |
| `get(i) T` | a copy of element `i` (copyable `T` only) |
| `len()`, `cap()`, `grow()` | length, capacity, double the capacity |
| `clear()`, `free()` | free the elements; `free` also frees the queue |

To keep only the last N, take out the oldest before putting a new one (`if q.len() == N { old := q.pop_front(); drop(old) }`): with `new[T](N)`, the queue never grows.

### `std/heap`

A priority queue: `pop` always returns the smallest element (or the largest, with `new_max`), in O(log n). The order is `cmp.Ord`'s: numbers, `string`, slices and structs with `impl cmp.Ord` (by hand, to sort by a field) or `impl auto cmp.Ord`. It allocates like `vec`, and the elements can be owners.

```
struct Timer {
    due int
    id int
}
fn (t &Timer) cmp(o &Timer) int { return t.due.cmp(&o.due) }
impl cmp.Ord for Timer

timers := heap.new[Timer](16)
timers.push(Timer{due: 30, id: 1})
timers.push(Timer{due: 10, id: 2})
next := timers.pop()                    // the one with due 10
timers.free()
```

| Method | What it does |
|---|---|
| `heap.new[T owned + cmp.Ord](cap) *Heap[T]`, `heap.new_max[...]` | creates: the smallest comes out first, or the largest |
| `push(x)` | puts (moves `x`) |
| `pop() T` | takes the first out and returns ownership; panics if empty |
| `peek() &T` | borrows the first, without taking it out |
| `replace_top(x) T` | replaces the first with `x` and returns the old one, in a single pass (the step of a top-K: with `new` and K elements, each value larger than the top takes its place) |
| `len()`, `cap()`, `grow()`, `clear()`, `free()` | as in `vec` |

Equivalent elements (`cmp` equal to 0) come out in any order among themselves; to break ties, compare one more field. A `?*Job` pointer doesn't implement `cmp.Ord` in this version: for items with owners, the heap keeps the key and the index (the `Timer` above, with `id` being the position in a `vec.Vec[?*Job]`).

### `std/rand`

Written in MLC (splitmix64 with `+%`, `*%`, `^` and `>>`).

A pseudorandom generator (splitmix64) **with explicit state**: there's no global generator, and the same seed always gives the same sequence.

| Declaration | What it does |
|---|---|
| `new(seed uint64) Rand` | creates a generator |
| `r.next() uint64` | the next 64-bit number |
| `r.int_n(n int) int` | an integer in `[0, n)` |
| `r.range(lo int, hi int) int` | an integer in `[lo, hi)` |
| `r.float() float64` | a real in `[0, 1)` |

For a different seed on each run, use `rand.seeded()` (or `uint64(time.now_ms())`).

`Rand` is predictable: whoever sees a few numbers can guess the next ones. For security (tokens, keys, temporary passwords, identifiers that must not be guessable), use the operating system's bytes:

| Declaration | What it does |
|---|---|
| `secure(dst &[]byte)` | fills `dst` with bytes from the kernel's cryptographic source (`getentropy`); panics if the system doesn't deliver |
| `secure_u64() uint64` | a secure `uint64` |
| `secure_int_n(n int) int` | a secure integer in `[0, n)`, without bias (values that would favor some remainders are drawn again) |
| `seeded() Rand` | a `Rand` with a secure seed |

```mlc
var token [32]byte
rand.secure(&token)
var txt [64]byte
n := encoding.base64_url(&txt, &token)   // for a URL or a cookie
```

### `std/hash`

Hashes in MLC: FNV for tables, CRC32 and SHA-256 for integrity and identifiers.

| Function | What it does |
|---|---|
| `sha256() Sha256` | starts a SHA-256; `h.write(&data)` as many times as needed, `h.sum(&dst)` writes the 32 bytes (and doesn't change the state) |
| `sha256_sum(data, dst)` | the SHA-256 in one go |
| `crc32(s) uint32`, `crc32_update(crc, s) uint32` | CRC-32 (IEEE, the one in zip, gzip and PNG), also in pieces |
| `fnv32(s &[]byte) uint32`, `fnv64(s &[]byte) uint64` | 32- and 64-bit FNV-1a |
| `fnv64_str(s string) uint64` | 64-bit FNV-1a of a fixed string |
| `mix64(x uint64) uint64` | spreads the bits of an integer (splitmix64's finalizer), to use as a key |
| `interface Hash { fn hash(self &Self) uint64; fn eq(self &Self, other &Self) bool }` | what a `map.Map` key needs; two equal values have the same hash |

`Hash` is already implemented for integers, `bool`, `string` and slices (`[]byte`, `[]int`...). For a struct or an enum, `impl auto hash.Hash for Point` generates `hash` and `eq` from the fields; or write both methods and declare `impl hash.Hash for Point`.

```
h := hash.sha256()
for { ... h.write(&buf[:n]) ... }       // a file read in pieces
var digest [32]byte
h.sum(&digest)
var txt [64]byte
n := encoding.hex(&txt, &digest)        // "ba7816bf..."
```

SHA-256 serves integrity and identifiers, but it isn't complete cryptography: encryption and public-key signatures call for a C library through the FFI (or `std/tls` for connections).

### `std/hmac`

HMAC-SHA256 (RFC 2104) in MLC, on top of `hash.sha256`: the signature of a message with a secret key. It's what checks a webhook (Stripe, GitHub), signs a token (JWT with HS256) or a session cookie. Nothing allocates.

| Declaration | What it does |
|---|---|
| `sha256(key, data, dst &[]byte)` | the HMAC in one go, into `dst` (`SIZE` = 32 bytes) |
| `verify(key, data, mac &[]byte) bool` | checks a signature, in constant time |
| `equal(a, b &[]byte) bool` | compares in constant time: the time doesn't depend on where the first difference is |
| `new(key) Hmac`, `h.write(data)`, `h.sum(dst)` | the same in pieces |
| `pbkdf2(&password, &salt, iterations, &dst)` | PBKDF2-HMAC-SHA256 (RFC 8018): derives `len(dst)` bytes of key; for user passwords, use `std/password` |

```mlc
if !hmac.verify(&secret, &body, &signature) {
    http.respond_str(&c, 401, "text/plain", "invalid signature\n")
    return
}
```

To check a signature or a token, use `verify` or `equal`, never `str.eq_bytes`: an ordinary comparison stops at the first different byte, and the time it takes tells an attacker how many bytes they already got right. The key can be any size; one longer than 64 bytes is replaced by its SHA-256, as the RFC says. Tested with the RFC 4231 vectors.

### `std/password`

Storing and checking user passwords without ever storing the password: PBKDF2-HMAC-SHA256 with 600 thousand iterations (OWASP's recommendation) and a 16-byte salt from `rand.secure` per password, in the PHC format, which carries the algorithm, the iterations and the salt along:

```
$pbkdf2-sha256$i=600000$0nG8n3nC3B7oTq7wR3a3Ow$Kj...
```

```mlc
var stored [password.MAX_LEN]byte
n := password.hash(&pw, &stored)             // save &stored[:n] in the database
...
if password.verify(&attempt, &saved) {       // in constant time
    ...
}
```

| Declaration | What it does |
|---|---|
| `hash(&pw, &dst) int` | the hash (dst with `MAX_LEN` bytes) |
| `hash_with(&pw, iterations, &dst) int` | with another number of iterations (fewer only in tests) |
| `verify(&pw, &stored) bool` | checks; a malformed hash gives `false` |
| `needs_rehash(&stored, iterations) bool` | the hash uses fewer iterations than asked: save a new one on the next correct login |

Each hash takes about 0.6 s on purpose: that's what makes testing millions of guesses expensive if the database leaks. The format is the same as Python's `hashlib` and other PHC libraries (the test checks a hash made by Python).

### `std/uuid`

UUIDs (RFC 9562), 128-bit identifiers that don't repeat, without a central server. A `UUID` is a 16-byte value; the text (36 characters) goes into a buffer of the caller's. The random bits come from `rand.secure`.

| Declaration | What it does |
|---|---|
| `v4() UUID` | random (122 bits) |
| `v7() UUID`, `v7_at(ms) UUID` | ordered by time: the top 48 bits are the milliseconds since 1970 |
| `u.text(dst) int` | writes `0192a3c4-5d6e-7f80-9a1b-2c3d4e5f6a7b` (`TEXT_LEN` = 36) |
| `parse(s, &u) bool` | reads the text (upper- or lowercase hex) |
| `u.version() int`, `u.ms() int`, `u.equal(&o) bool` | the version, a v7's time, equality |

For a database key, prefer `v7`: UUIDs created later sort later, so the index grows at the end instead of scattered, and inserts get much faster.

### `std/binary`

Numbers as bytes, for file formats and binary protocols: fixed-size integers and reals in big-endian (`_be`, network order) or little-endian (`_le`, x86's and ARM's), and variable-size integers (varint, the protobuf and Go format). Readers take a borrowed slice and read its start; writers write at the start of `dst`. The position stays with the caller, as a sub-slice. A slice that's too short is a panic.

```
var hdr [14]byte
binary.put_u32_be(&hdr, MAGIC)
binary.put_u16_le(&hdr[4:], version)
binary.put_f64_le(&hdr[6:], ratio)

size := binary.u32_be(&buf[off:])
```

| Declaration | What it does |
|---|---|
| `u16_be(src) uint16`, `u32_be`, `u64_be` and the `_le` ones | read an unsigned integer |
| `put_u16_be(dst, v uint16)`, `put_u32_be`, `put_u64_be` and the `_le` ones | write |
| `i16_be(src) int16`, `i32_be(src) int32`, `i64_be(src) int`, `put_i16_be`... and the `_le` ones | signed, in two's complement |
| `f32_be(src) float32`, `f64_be(src) float64`, `put_f32_be`, `put_f64_be` and the `_le` ones | IEEE 754 reals |
| `put_uvarint(dst, v uint64) int`, `put_varint(dst, v int) int` | varint (7 bits per byte; signed in zigzag: small numbers take few bytes); returns how many bytes it used |
| `uvarint(src, &v) int`, `varint(src, &v) int` | reads a varint; returns the bytes used, `0` if `src` ended in the middle (read more and try again) or `-1` if it doesn't fit in 64 bits |
| `uvarint_len(v) int`, `varint_len(v) int`, `MAX_VARINT` (10) | the size in advance; a `[binary.MAX_VARINT]byte` is always enough |

### `std/encoding`

| Function | What it does |
|---|---|
| `hex(dst, src) int` | lowercase hexadecimal (2 bytes per byte) |
| `unhex(dst, src) int` | reads hex (upper- or lowercase); `-1` if invalid |
| `base64(dst, src) int` | standard base64, with `+`, `/` and `=` |
| `base64_url(dst, src) int` | base64 for URLs and file names (`-` and `_`, without `=`) |
| `base64_len(n) int` | the size of the base64 of `n` bytes, to allocate |
| `unbase64(dst, src) int` | reads both forms, with or without `=`; `-1` if invalid. A `dst` of `len(src) * 3 / 4` bytes is always enough |

The functions that write panic if `dst` is too small.

### `std/map`

A hash table written in MLC: open addressing with linear probing, growing (doubling, with a visible `alloc` in `grow`) when it passes 3/4 occupancy. Keys and values can be owners (`Map[K owned + hash.Hash, V owned]`).

```
count := map.new[[]byte, int](16)
n := count.get_or(&word, 0)               // word: &[]byte, borrowed
if !count.set(&word, n + 1) {             // set only replaces the value of an existing key
    key := alloc([]byte, len(word))       // the new key is allocated by the caller
    str.copy_bytes(&key, &word)
    count.put(key, 1)                     // and moved into the map
}
count.each(show)                          // fn show(k &[]byte, v &int)
count.free()                              // frees each key and the map
```

| Method | Types | What it does |
|---|---|---|
| `new[K, V](cap int) *Map[K, V]` | any | allocates with room for about `cap` entries |
| `put(k K, v V)` | any | moves `k` and `v` into the map; if the key already exists, replaces the value (the new key and the old value are freed) |
| `set(k &K, v V) bool` | any | replaces the value of an existing key; if it doesn't exist, frees `v` and returns `false` |
| `has(k &K) bool` | any | does the key exist? |
| `get(k &K) &V` | any | borrows the value without taking it out (panics if the key doesn't exist) |
| `index(k &K) int`, `key_ref(i) &K`, `value_ref(i) &V` | any | position of a key, and borrows by position (also for walking with `next`) |
| `get_or(k &K, def V) V` | copyable `V` | a copy of the value, or `def` |
| `take_or(k &K, def V) V` | any | removes the entry and returns ownership of the value (the key is freed), or `def` |
| `remove(k &K) bool` | any | deletes the entry (frees key and value) |
| `len() int`, `clear()`, `free()` | any | size; `clear` and `free` free the entries |
| `each(f fn(&K, &V))` | any | calls `f` with each entry |
| `each_with(&ctx, f fn(&C, &K, &V))` | any | the same, with a context where `f` accumulates (sums, counts, a `vec` of results) |
| `next(i) int`, `key_at(i) K`, `value_at(i) V` | `key_at`: copyable `K`; `value_at`: copyable `V` | walking: `for i := m.next(0); i >= 0; i = m.next(i + 1)` |

### `std/os`

| Declaration | What it does |
|---|---|
| `arg_count() int` | how many arguments the program received (0 is its name) |
| `arg(i int, dst &[]byte) Result[int, int]` | copies argument `i` into `dst` |
| `getenv(name string, dst &[]byte) Result[int, int]` | copies the value of the environment variable |
| `setenv(name string, value &[]byte) Result[int, int]` | sets the environment variable (also applies to programs run with `std/process`) |
| `cwd(dst) Result[int, int]` | copies the current directory |
| `chdir(path) Result[int, int]` | changes the current directory |
| `exit(code int)` | ends the program |
| `error_text(code int, dst) int` | writes the text of any library error code; a `[160]byte` is always enough |
| `NOT_FOUND` (2), `TOO_LONG` (1002) | error codes: doesn't exist; doesn't fit in `dst` |

### `std/args`

Command-line options without allocation: the current argument stays in a buffer inside `Args` (on the stack), and the values go into the caller's buffers.

```
a := args.new()
for a.next() {
    if a.is("-v", "--verbose") {
        verbose = true
    } else if a.value("-o", "--out", &out, &out_n) {   // -o file, --out file, --out=file
        if out_n < 0 { ... the value is missing ... }
    } else if a.int_value("-n", "", &n, &ok) {
        if !ok { ... not a number ... }
    } else if a.is_option() {
        ... unknown option ...
    } else {
        k := a.text(&file)                              // positional
    }
}
```

| Method | What it does |
|---|---|
| `args.new() Args` | starts reading the arguments (without the program name) |
| `next() bool` | advances; after `--`, everything is positional |
| `is(short, long string) bool` | is the argument this option? (`""` to omit one of the forms) |
| `value(short, long, dst, &n) bool` | an option with a value; `n` gets the size, or `-1` if the value is missing |
| `int_value(short, long, &v, &ok) bool` | an option with an integer value |
| `is_option() bool` | looks like an option not yet recognized |
| `text(dst) int` | copies the current argument |

### `std/process`

Runs other programs, without a shell (the arguments go straight through, without `*` or `$VAR` and without the risk of injection). The arguments stay in a fixed buffer inside `Cmd`, and the captured output goes into a buffer of the caller's.

```
c := process.cmd("git")
c.arg("log")
c.arg_bytes(&branch[:n])
var out [65536]byte
status := 0
match c.run(&out, &status) {
    Ok(k) => io.write(&out[:k])          // status has the exit code
    Err(code) => ...                     // NOT_FOUND, TOO_BIG
}
```

| Declaration | What it does |
|---|---|
| `cmd(name string) Cmd` | starts the command line (the program is looked up in `PATH`) |
| `arg(s string)`, `arg_bytes(b)` | add arguments (up to 8 KB and 255 arguments) |
| `run(out, &status) Result[int, int]` | runs and waits; standard output goes into `out`, standard error stays on the terminal |
| `run_inherit(&status) Result[int, int]` | runs with the output going straight to the terminal |
| `NOT_FOUND`, `TOO_BIG`, `TOO_LONG` | the program doesn't exist; the output didn't fit (the rest was discarded and the program ran to the end); the arguments don't fit |

### `std/fs`

Paths are `&[]byte` (for example, coming from `os.arg`; for a literal, copy it first with `str.copy`). Content is read and written in the caller's buffers: to read a whole file, ask for its size and allocate the buffer.

```
match fs.file_size(path) {
    Ok(size) => {
        buf := alloc([]byte, size)
        match fs.read_file(path, &buf) {
            Ok(n) => io.write(&buf[:n])
            Err(code) => io.print("error", code)
        }
        free(buf)
    }
    Err(code) => io.print("error", code)
}
```

| Declaration | What it does |
|---|---|
| `file_size(path) Result[int, int]` | size in bytes |
| `read_file(path, dst) Result[int, int]` | reads the whole file at the start of `dst`; if it doesn't fit, nothing is read and the error is `TOO_BIG` |
| `write_file(path, data) Result[int, int]` | creates or replaces the file |
| `append_file(path, data) Result[int, int]` | appends at the end (creates it if needed) |
| `remove_file(path) Result[int, int]` | deletes the file |
| `exists(path) bool` | does the path exist? |
| `stat(path, &info) Result[int, int]` | fills an `fs.Info` (`size`, `is_dir`, `modified_ms`) |
| `mkdir(path)`, `remove_dir(path)`, `rename(from, to)` | directories and names (`remove_dir` requires the directory to be empty) |
| `EXISTS`, `NOT_DIR`, `IS_DIR`, `NOT_EMPTY` | more error codes, the same on every system |
| `NOT_FOUND` (2), `DENIED` (13), `TOO_BIG` (1000) | error codes; the others are the system's `errno` |

**Open files**, for reading and writing in pieces (large files, logs):

```
err := 0
f := fs.open(&path, fs.READ, &err)          // nil, with the code in err, on failure
if f != nil {
    var buf [4096]byte
    for {
        match f.read(&buf) {
            Ok(n) => {
                if n == 0 { break }          // end of the file
                ... &buf[:n] ...
            }
            Err(_) => break
        }
    }
    f.close()                                // the checker requires this close
}
```

| Declaration | What it does |
|---|---|
| `open(path, mode, &err) ?*File` | modes `READ`, `WRITE` (creates or truncates), `APPEND`, `READ_WRITE` (creates if needed, without truncating) |
| `f.read(dst) Result[int, int]` | reads up to `len(dst)` bytes; `Ok(0)` is the end |
| `f.write(data) Result[int, int]` | writes everything |
| `f.seek(pos) Result[int, int]` | goes to `pos` (from the start); with a negative `pos`, to the end |
| `f.close()` | closes |

An open file is an `io.Reader`: `std/bufio` reads lines from it.

**Directories:**

```
d := fs.open_dir(&path, &err)
if d != nil {
    var name [256]byte                       // a [256]byte is always enough
    for {
        n := d.next(&name)                   // -1 when they run out; without . and ..
        if n < 0 { break }
        ... &name[:n] ...
    }
    d.close()
}
```

#### Directory trees

| Declaration | What it does |
|---|---|
| `walk(&root, &ctx, visit) Result` | visits `root` and everything inside, depth first; `visit(&ctx, &path, &info)` returns `CONTINUE`, `SKIP` (doesn't enter the directory) or `STOP` |
| `glob(&root, pattern, &ctx, visit) Result` | the same, only with what matches the pattern (the path from `root`) |
| `glob_match(pattern, &path) bool` | `*` (without `/`), `?`, `[abc]`, `[a-z]`, `[!x]`, `**` (anything, including `/`; `**/` also matches nothing) |
| `mkdir_all(&path)` | creates the directory and its parents (like `mkdir -p`); what already exists isn't an error |
| `remove_all(&path)` | deletes everything (like `rm -r`); what doesn't exist isn't an error |
| `is_link(&path) bool` | a symbolic link (the `Info` from `walk` and `glob` also has `is_link`) |
| `temp_dir(&dst)`, `temp_path(prefix, &dst)` | the temporary folder (`TMPDIR` or `/tmp`) and a new path in it, with 16 random digits from `rand.secure` |

```mlc
struct Total {
    bytes int
}

fn add(t &Total, path &[]byte, info &fs.Info) int {
    if !info.is_dir {
        t.bytes += info.size
    }
    return fs.CONTINUE
}

var t Total
fs.glob(&dir, "**/*.log", &t, add)
```

Symbolic links aren't followed: `walk` visits the link but doesn't enter a directory it points to (no cycles), and `remove_all` deletes the link, not what it points to. The maximum depth is `MAX_DEPTH` (64), and going past it gives `TOO_DEEP`.

### `std/bufio`

Buffered reading from any `io.Reader`. A `bufio.Reader` keeps a fixed 4096-byte buffer inside itself (on the stack, if it's a local variable): nothing allocates. The source **isn't stored**: each call borrows it, so whoever opened the file stays its owner and closes it when they want (the checker requires the `close` as always).

```
f := fs.open(&path, fs.READ, &err)
if f != nil {
    br := bufio.new[fs.File]()
    var line [1024]byte
    for {
        match br.read_line(&f, &line) {
            Ok(n) => io.printf("{:s}\n", &line[:n])
            Err(code) => {
                if code == bufio.TOO_LONG {
                    continue                     // line is full; the rest of the line comes next
                }
                if code != io.EOF {
                    ... error from the source ...
                }
                break
            }
        }
    }
    f.close()
}
```

| Declaration | What it does |
|---|---|
| `new[R ref + io.Reader]() Reader[R]` | an empty reader for sources of type `R` (`bufio.new[fs.File]()`, `bufio.new[net.Conn]()`, `bufio.new[io.Stdin]()`) |
| `br.read_line(src, dst) Result[int, int]` | one line, without the `\n` (nor the `\r` of a `\r\n`); the last one may have no `\n` |
| `br.read_until(src, delim, dst) Result[int, int]` | up to the byte `delim`, inclusive; without it, whatever there is until the end |
| `br.read(src, dst) Result[int, int]` | what's in the buffer, or one read from the source; may read less than `len(dst)` |
| `br.read_exact(src, dst) Result[int, int]` | fills all of `dst`, or `Err(io.EOF)` if the data ends first |
| `br.read_byte(src) Result[byte, int]` | one byte |
| `br.buffered() int` | how many bytes are already in the buffer |
| `TOO_LONG` (1002) | the line (or the piece up to `delim`) didn't fit: `dst` is full and the rest comes on the next call |

The end of the data is `Err(io.EOF)`, and it keeps holding on the following calls. Errors from the source arrive as they came, after the data that was already in the buffer. Always use the same source with the same `Reader`: the bytes in the buffer belong to it.

### `std/config`

Configuration in a file: dotenv (`.env`) and INI. The text stays in a 16 KB buffer inside the `Config`, with up to 256 entries; values are copied into the caller's buffers.

```mlc
var cfg config.Config
match config.load_str(".env", &cfg) {
    Ok(_) => {}
    Err(code) => io.eprintf("config: invalid line {}\n", cfg.bad_line)
}
port := 8080
cfg.int("PORT", &port)               // the default stays if it doesn't exist
var url [256]byte
n := cfg.env("DATABASE_URL", &url)   // the environment variable, if set, wins over the file
```

| Declaration | What it does |
|---|---|
| `load(&path, &c)`, `load_str(path, &c)`, `c.parse(&text)` | reads the file (or the text); `Err(BAD_LINE)` with `c.bad_line` |
| `c.get(key, &dst) int`, `c.has(key)`, `c.is(key, s)` | the value (-1 if it doesn't exist); in a section, the key is `"section.key"` |
| `c.int(key, &out) bool`, `c.bool(key, &out) bool` | convert; without the key, `out` keeps the default you put there |
| `c.env(key, &dst) int` | the environment variable `key`, or the file's value |
| `c.len()`, `c.key(i, &dst)`, `c.value_at(i, &dst)` | to list everything |

The format: `key=value` per line; `#` and `;` at the start of a line are comments; `export ` before the key is accepted; `[section]` starts a section. Between double quotes, the value accepts `\n \t \\ \"` and can contain `#`; between single quotes, it stays as is; without quotes, ` #` or ` ;` starts a comment. A repeated key counts by its last occurrence. `c.bool` accepts `true/false`, `1/0`, `yes/no` and `on/off`.

### `std/csv`

CSV (RFC 4180). The reader streams from any `io.Reader`, one record at a time, with the fields in a 16 KB buffer: a gigabyte file goes through with fixed memory. The writer builds lines in a 16 KB buffer, with quotes where needed.

```mlc
r := csv.new[fs.File]()
r.sep = ';'                          // spreadsheets in some locales use ;
for {
    match r.next(&f) {
        Ok(n) => {
            var name [128]byte
            k := r.field(0, &name)
            price := 0
            r.int(2, &price)
        }
        Err(code) => break           // io.EOF at the end; BAD_QUOTE or TOO_LONG
    }
}
```

| Declaration | What it does |
|---|---|
| `new[R]()`, `r.sep` | the reader (separator `,`, or another) |
| `r.next(&src) Result` | the next record: how many fields; `Err(io.EOF)` at the end |
| `r.field(i, &dst) int`, `r.is(i, s)`, `r.int(i, &out)`, `r.float(i, &out)`, `r.len()`, `r.line` | the fields (the first is 0) and the record's line |
| `Writer` with `w.sep`, `w.crlf`, `w.add(&s)`, `add_str`, `add_int`, `add_float`, `w.end()`, `w.reset()` | writes into `&w.buf[:w.n]` |

Quoted fields can contain the separator, line breaks and quotes (`""`); empty lines are skipped.

### `std/path`

Slash-separated (`/`) paths, by text only (nothing looks at the disk). Each function writes the result into a buffer of the caller's and returns how many bytes it used; the result is never larger than the input (in `join`, than the sum of both plus the slash), and not fitting is a panic.

```
var out [4096]byte
n := path.join_str(&out, &dir[:k], "config.json")   // "docs/" + "config.json" → "docs/config.json"
var e [16]byte
m := path.ext(&e, &out[:n])                          // ".json"
```

| Declaration | What it does |
|---|---|
| `clean(dst, p) int` | the shortest equivalent form, with the rules of Go's `path.Clean`: merges repeated slashes, removes `.`, resolves `..` (in a relative path, the leftover ones at the start stay; in an absolute one, they vanish) and removes the trailing slash; empty becomes `.` (`"a//b/./c/../d/"` → `"a/b/d"`) |
| `join(dst, a, b) int`, `join_str(dst, a, b string) int` | `a/b`, cleaned; if `b` is absolute, the result is `b` |
| `dir(dst, p) int` | everything but the last element, cleaned (`"a/b/c.txt"` → `"a/b"`, `"c.txt"` → `"."`) |
| `base(dst, p) int` | the last element (`"a/b/"` → `"b"`, `"/"` → `"/"`, `""` → `"."`) |
| `ext(dst, p) int` | the extension of the last element, with the dot (`"x.tar.gz"` → `".gz"`; without an extension, 0) |
| `is_abs(p) bool` | starts with `/` |

### `std/testing`

Checks for the tests run with `mlc test` (`check`, `fail`, `eq_int`, `eq_uint`, `eq_bool`, `eq_float`, `near`, `eq_str`, `eq_bytes`, `expect_panic`). They're `extern fn`, so the failure points at the test's line. See [testing.md](testing.md).

### `std/thread`

System threads: `thread.spawn(f, ctx, stack)` and `thread.join(t)` (built into the compiler), `thread.cpu_count()` and the constants `STACK_16KB` to `STACK_8MB`. The context goes to the thread by ownership, and the result comes back through `join`. See [concurrency.md](concurrency.md).

### `std/chan`

Fixed-capacity channels: `chan.new[T](n)`, the ends `chan.Sender[T]` (`send`, `clone`, `close`) and `chan.Receiver[T]` (`recv`, `sender`, `clone`, `close`), and the result `chan.Recv[T] { Got(value T); Closed }`. Values pass by ownership. See [concurrency.md](concurrency.md#channels-import-stdchan).

### `std/mutex` and `std/atomic`

`mutex.new(v)`, with `lock`/`get`/`unlock`/`wait`/`notify` and handles that need a `close`; `atomic.new_int` and `atomic.new_bool`, with `load`, `store`, `add`, `swap` and `compare_swap`. See [concurrency.md](concurrency.md#mutex-import-stdmutex).

### `std/time`

| Function | What it does |
|---|---|
| `now_ms() int` | milliseconds since 1970 (wall clock) |
| `mono_ns() int` | nanoseconds of a monotonic clock, for measuring intervals |
| `sleep_ms(ms int)` | waits |
| `utc(ms) Date`, `local(ms) Date` | break an instant down in UTC or in the system's time zone |
| `to_ms(&d) int` | back to milliseconds (in the date's time zone) |
| `iso(dst, &d) int` | writes ISO 8601: `2026-10-17T14:05:09Z`, with `.123` and `-03:00` when present |
| `parse_iso(s, &ms) bool` | reads ISO 8601 (with `T` or a space, optional milliseconds and zone, or just the date) |
| `days_in_month(year, month) int` | with leap years |

`Date` has `year`, `month` (1 to 12), `day`, `hour`, `minute`, `second`, `ms`, `weekday` (0 is Sunday) and `offset_min` (the time zone, in minutes east of UTC). The calculations are the Gregorian calendar's for any year, in MLC, without allocating.

```
d := time.local(time.now_ms())
var buf [32]byte
n := time.iso(&buf, &d)                     // 2026-10-17T11:05:09-03:00
```

### `std/net`

TCP with handles the checker forces you to close. Data passes through the caller's buffers.

```
err := 0
l := net.listen(&host, 8080, &err)          // empty host: all interfaces; port 0: the system picks
for {
    c := l.accept(&err)
    if c == nil { break }
    t := thread.spawn(handle, c, thread.STACK_64KB)   // one thread per connection
    thread.join(t)
}
l.close()
```

| Declaration | What it does |
|---|---|
| `listen(host, port, &err) ?*Listener` | listens on `host:port` |
| `l.accept(&err) ?*Conn` | waits for the next connection |
| `l.port() int` | the port (useful with port 0) |
| `l.set_timeout(ms)` | how long `accept` waits (-1: no limit); after that, `accept` returns `nil` with `err` `TIMEOUT`, and the loop can check a `signal.received` before waiting again |
| `connect(host, port, &err) ?*Conn` | connects (names are resolved, IPv4 and IPv6) |
| `c.read(dst) Result[int, int]` | reads what arrives; `Ok(0)`: the other side closed |
| `c.write(data) Result[int, int]` | sends everything |
| `c.set_timeout(ms) Result[int, int]` | how long `read` and `write` wait (error `TIMEOUT`) |
| `c.shutdown() Result[int, int]` | announces it won't send anything else (the other side reads `Ok(0)`) |
| `close()` | closes the listener or the connection |
| `REFUSED`, `NO_HOST`, `IN_USE`, `TIMEOUT`, `RESET` | error codes, the same on every system |

A connection is an `io.Reader`: `bufio.new[net.Conn]()` reads lines from it (text protocols, like HTTP and SMTP).

Writing to a connection the other side closed returns `RESET`, instead of ending the program with `SIGPIPE`. `tests/run/servidor.mlc` has a complete service: one thread per connection, request and response in JSON.

### `std/signal`

System signals: Ctrl+C (`INT`), the stop request from Docker, systemd and Kubernetes (`TERM`), `HUP`, `QUIT`, `USR1` and `USR2`. Without `listen`, a signal does what it always did (`INT` and `TERM` end the program on the spot). With `listen`, it's only **recorded**: the program checks (`received`) or waits (`wait`) and decides, at the point it chooses, how to shut down (closing connections, saving what's left). The real handler, in C, only marks the signal and wakes whoever is in `wait`.

```
signal.listen_shutdown()                     // INT and TERM
l.set_timeout(200)                           // accept wakes up to check
for !signal.received(signal.TERM) && !signal.received(signal.INT) {
    c := l.accept(&err)
    if c == nil { continue }                 // TIMEOUT
    ...
}
... shut down calmly ...
```

| Declaration | What it does |
|---|---|
| `listen(sig)`, `listen_shutdown()` | start recording the signal (`listen_shutdown`: `INT` and `TERM`) |
| `received(sig) bool`, `clear(sig)` | did the signal arrive? (applies to every thread and stays until `clear`) |
| `wait(ms) int` | waits for a recorded signal (or `ms` milliseconds; `-1`: no limit) and returns its code, or `0` if time ran out; returns at once if one already arrived |
| `ignore(sig)`, `restore(sig)` | not see the signal; go back to the normal behavior |
| `raise(sig)` | sends the signal to the program itself (useful in tests) |
| `HUP` (1), `INT` (2), `QUIT` (3), `USR1` (10), `USR2` (12), `TERM` (15) | the codes, the same on every system (the runtime translates: `SIGUSR1` is 30 on macOS) |

### `std/log`

Logging with level, time and whole lines even with several threads. The logger is an **object**, not global state: whoever creates it passes it along (borrowed, or a `clone` for another thread) and closes it.

```
lg := log.new(log.STDERR, log.INFO)
lg.info("server listening on port {}", port)
lg.warn("slow connection: {} ms", ms)
lg.debug("not printed: below INFO")
t := thread.spawn(worker, lg.clone(), thread.STACK_64KB)
...
lg.close()
```

```
2026-10-19T14:03:22.123-03:00 INFO  server listening on port 8080
2026-10-19T14:03:22.124-03:00 WARN  slow connection: 812 ms
```

| Declaration | What it does |
|---|---|
| `new(dest, level) *Logger` | a logger on `STDOUT` or `STDERR`, from level `level` up |
| `open(path, level, &err) ?*Logger` | a logger in a file (appends at the end; creates it if needed) |
| `lg.debug(...)`, `lg.info(...)`, `lg.warn(...)`, `lg.error(...)` | one line, with `io.printf`'s format (checked at compile time) |
| `lg.write(level, msg)` | a ready-made line |
| `lg.enabled(level) bool`, `lg.level()`, `lg.set_level(level)` | the minimum level (applies to all handles) |
| `lg.set_time(on)` | turns the time on or off (off, the output is the same on every run) |
| `lg.clone() *Logger`, `lg.close()` | another handle for another thread; each one needs a `close`, and the last one closes the file |
| `DEBUG`, `INFO`, `WARN`, `ERROR` | the levels |

The formatted methods are written by the compiler like `w.printf`: the values are always computed (left to right), but only formatted if the level is enabled. The line is assembled in a per-thread buffer in the runtime (2048 bytes; whatever goes past is left out) and written at once, under the logger's lock. Nothing allocates per line.

### `std/sync`

A group of threads waited for at once. See [concurrency.md](concurrency.md#thread-groups-import-stdsync).

| Declaration | What it does |
|---|---|
| `group(cap) *Group` | creates the group, with room for `cap` threads |
| `g.add(t *thread.Thread)` | the group takes the thread (only threads without a result); when full, it collects the finished ones or waits for the oldest |
| `g.reap() int` | waits for the ones already finished (without blocking on the others) and returns how many |
| `g.len() int` | how many threads are in the group |
| `g.wait()` | waits for all of them, in the order they joined, and frees the group (the checker requires it) |

### `std/sqlite`

A database in a file, with SQL and transactions. The system library is loaded on the first call (`dlopen`), so whoever doesn't use the module doesn't need it, and whoever does needs no link flags. `MLC_SQLITE_LIB` picks another file; `available()` says whether it was found.

The database (`DB`) and the prepared statement (`Stmt`) are handles: the checker requires the `close` of each one. Text goes in borrowed (SQLite keeps a copy) and comes out copied into the caller's buffers, so nothing allocates on the MLC side.

```mlc
err := 0
db := sqlite.open_str("shop.db", &err)          // ":memory:" for an in-memory database
if db == nil {
    return
}
db.exec_str("CREATE TABLE IF NOT EXISTS item (id INTEGER PRIMARY KEY, name TEXT, price INTEGER)")

st := db.prepare_str("INSERT INTO item (name, price) VALUES (?, ?)", &err)
if st != nil {
    db.begin()
    st.bind_str(1, "coffee")
    st.bind_int(2, 1250)                          // money in cents
    st.run()                                      // 0, or sqlite.CONSTRAINT...
    db.commit()
    st.close()
}

q := db.prepare_str("SELECT id, name FROM item WHERE price > ?", &err)
if q != nil {
    q.bind_int(1, 1000)
    var name [256]byte
    for q.next() {
        n := q.text(1, &name)
        io.printf("{} {:s}\n", q.int(0), &name[:n])
    }
    q.close()
}
db.close()
```

| Declaration | What it does |
|---|---|
| `open(&path, &err)`, `open_str(path, &err)`, `open_read_only(&path, &err)` | opens (or creates) the database; waits up to 5 s for another process's lock |
| `db.exec(&sql)`, `db.exec_str(sql)` | statements without a result; returns 0 or the error code |
| `db.prepare(&sql, &err)`, `db.prepare_str(sql, &err)` | compiles a statement with `?` |
| `db.begin()`, `commit()`, `rollback()` | transaction |
| `db.changes()`, `db.last_insert_id()`, `db.error(&dst)` | rows changed, the last id, the message of the last error |
| `st.bind_int`, `bind_float`, `bind_text`, `bind_str`, `bind_blob`, `bind_null` | the parameters (the first `?` is 1); panics with an index that doesn't exist |
| `st.next() bool` | the next row of the result |
| `st.run() int` | runs an INSERT, UPDATE or DELETE and goes back to the start |
| `st.reset()` | goes back to the start and clears the parameters |
| `st.int(i)`, `float(i)`, `text(i, &dst)`, `blob(i, &dst)`, `text_len(i)`, `kind(i)`, `is_null(i)` | the columns of the current row (the first is 0); `text` panics if it doesn't fit |
| `st.columns()`, `st.column_name(i, &dst)` | the result's columns |

Errors are 4000 plus SQLite's code: `ERROR` (4001), `BUSY` (4005), `CANTOPEN` (4014), `CONSTRAINT` (4019), `MISUSE` (4021), and `UNAVAILABLE` (4900) when the library doesn't exist. `os.error_text` gives the text, and `db.error(&buf)` SQLite's message. Always use parameters (`?`) for values, never SQL built from outside text: that's what prevents SQL injection.

### `std/tls`

Encrypted connections (HTTPS), client and server, with the system's OpenSSL, loaded on the first call (no link flags). On macOS, `brew install openssl@3`; on Linux, it usually comes with the system. `MLC_TLS_LIB` points to another `libssl`, and `available()` says whether it was found.

**Client.** `connect` opens the TCP connection and does the handshake checking the server's certificate: the chain up to a system authority, the validity and the host name (with SNI). A certificate that isn't valid is refused with `BAD_CERT`, and there's no way to turn the check off. For your own authority (an internal server, tests), `MLC_TLS_CA` points to its file. TLS 1.2 at least.

```mlc
err := 0
c := tls.connect_str("api.example.com", 443, &err)
if c == nil {
    var why [256]byte
    n := tls.last_error(&why)     // "certificate refused: certificate has expired"
    return
}
c.write(&request)
c.close()
```

With `http`, the URL is enough: `http.get("https://...", &m, &body)` uses TLS by itself, and `http.dial_tls(&u, &err)` opens the connection for custom headers.

**Server.** `new_server` loads the certificate (the chain, in PEM) and the key; `srv.accept(c, &err)` does the handshake on a connection accepted by a `net.Listener` (the `net.Conn` now belongs to TLS). With `Router[tls.Conn, App]`, the same routes serve HTTPS:

```mlc
srv := tls.new_server_str("cert.pem", "key.pem", &err)
...
c := l.accept(&err)
if c != nil {
    t := srv.accept(c, &err)
    if t != nil {
        t.set_timeout(5000)
        http.serve_conn(&t, &router, &app)
        t.close()
    }
}
srv.close()
```

| Declaration | What it does |
|---|---|
| `connect(&host, port, &err)`, `connect_str(host, port, &err)` | client |
| `new_server(&cert, &key, &err)`, `new_server_str(...)`, `srv.accept(c, &err)`, `srv.close()` | server |
| `c.read(&dst)`, `c.write(&data)`, `c.set_timeout(ms)`, `c.close()` | like `net.Conn`; `tls.Conn` implements `io.Reader` and `http.Stream` |
| `last_error(&dst)` | why this thread's last error happened |

The errors: `HANDSHAKE` (5001), `BAD_CERT` (5002), `UNAVAILABLE` (5003, no `libssl`), `BROKEN` (5004) and `SERVER_FILES` (5005), besides those of `net.connect`. The test (`std/tls_test.mlc`) generates a certificate for `localhost` with `openssl` and runs server and client, including the cases that must be refused (unknown authority and wrong name); against real servers, an expired certificate and one for another name are refused too.

### `std/http`

HTTP/1.1 over any `http.Stream` (the `read` + `write` interface, implemented by `net.Conn` and `tls.Conn`; in the tests, by a fake connection). The server reads requests and writes responses, the client writes requests and reads responses. **Nothing allocates:** the received message (`Message`) keeps a fixed 8 KB buffer inside itself, holding the header and the start of the body; the outgoing one (`Builder`) assembles the header in a 4 KB buffer; the body comes and goes through the caller's buffers. Texts (method, path, fields) are copied into your buffers or compared without copying.

```
// server: one connection (accept and threads are up to you)
var m http.Message
for {
    match m.read_request(&c) {
        Ok(_) => {}
        Err(_) => break                          // io.EOF: the client closed
    }
    if m.method_is("GET") && m.path_is("/health") {
        http.respond_str(&c, 200, "text/plain", "ok\n")
    } else {
        http.respond_str(&c, 404, "text/plain", "not found\n")
    }
    if !m.keep_alive() { break }
}

// client
var b http.Builder
b.request("POST", "/api/items")
b.header("Host", "example.com")
b.header("Content-Type", "application/json")
b.send(&c, &json[:n])                            // automatic Content-Length
var r http.Message
r.read_response(&c)
match r.read_all(&c, &body) { ... }              // r.status() is the code
```

**Received message (`Message`):**

| Method | What it does |
|---|---|
| `read_request(&c)`, `read_response(&c)` | reads the header; on the same connection (keep-alive), reads the next one, discarding the leftover body |
| `method_is(s)`, `method(dst)` | the method |
| `path_is(s)`, `path_has_prefix(s)`, `path(dst)` | the path, without the query (not decoded) |
| `target(dst)`, `query(dst)` | the whole target; the query |
| `query_param(name, dst) int` | a parameter's value, decoded (`%41`, `+`); `-1` if absent |
| `header(name, dst) int`, `has_header(name)`, `header_is(name, value)` | a field (case-insensitive name); `-1` if absent |
| `header_count()`, `header_name(i, dst)`, `header_value(i, dst)` | all the fields |
| `status()`, `keep_alive()`, `content_length()` | the response code; whether the connection continues; the declared size (`-1`) |
| `read_body(&c, dst)` | up to `len(dst)` bytes of the body, already de-chunked; `Ok(0)` is the end |
| `read_all(&c, dst)` | the whole body; `Err(TOO_BIG)` if it doesn't fit |
| `skip_body(&c)` | discards the rest of the body |

**Outgoing message (`Builder`):**

| Method | What it does |
|---|---|
| `response(status)` | starts a response (`HTTP/1.1 200 OK`) |
| `request(method, target)`, `request_bytes(method, target)` | starts a request (remember the `Host`) |
| `header(name, value)`, `header_bytes`, `header_int` | a field |
| `send(&c, body)`, `send_str(&c, body)` | ends the header (with the `Content-Length`) and sends everything; a small body goes in the same write |
| `start(&c, length)` | sends only the header; the body goes afterwards, in pieces, with `c.write` |

| Function | What it does |
|---|---|
| `respond(&c, status, content_type, body)`, `respond_str(...)` | a complete response in one line |
| `status_text(code) string` | `"Not Found"` |
| `escape(dst, src)`, `unescape(dst, src)` | URL encoding (`%XX`; `unescape` also turns `+` into a space and returns `-1` if it's invalid) |
| `BAD_MESSAGE` (3001), `HEAD_TOO_BIG` (3002), `TOO_BIG` (1000) | errors: not HTTP/1.x; the header doesn't fit in the 8 KB (or has more than 64 fields); the body doesn't fit in `dst` |

The body can come with `Content-Length`, `Transfer-Encoding: chunked` or, in a response without a size, until the connection closes. `1xx`, `204` and `304` responses have no body. Left out of this version: `Expect: 100-continue`, HTTP/2 and responses to `HEAD` in the client. HTTPS comes from [`std/tls`](#stdtls), or from putting the service behind a proxy (nginx, Caddy or the cloud load balancer).

`tests/run/http_servico.mlc` has a complete service: routes, JSON, one thread per connection (at most 64 at a time, through `sync.group`), logging and a clean shutdown on `TERM`.

`tests/run/http_rotas.mlc` builds the equivalent of an `http.ListenAndServe` by hand: handlers `fn(&net.Conn, &http.Message, &Ctx)` in a fixed route table (`Router`, with a prefix for `/items/{id}`, 404 and 405), the keep-alive loop with a timeout, 431 for a large header, 413 for a large body and `skip_body` after each handler.

#### Router

`Router[S, C]` picks the function for each request by method and path, from a fixed table (up to 64 routes), without allocation. `S` is the connection (`net.Conn`, or `tls.Conn` for HTTPS) and `C` is the state each function receives (a logger, a database connection, counters). In the pattern, `{name}` matches one piece between slashes and `{name...}`, at the end, everything else; the values arrive decoded in `Params`.

```mlc
fn get_item(c &net.Conn, m &http.Message, p &http.Params, app &App) {
    id := 0
    if !p.int("id", &id) {
        http.respond_str(c, 400, "text/plain", "invalid id\n")
        return
    }
    ...
}

var r http.Router[net.Conn, App]     // tls.Conn instead of net.Conn: the same routes over HTTPS
r.handle("GET", "/items/{id}", get_item)
r.handle("POST", "/items", create_item)
r.handle("GET", "/files/{path...}", serve_file)
...
// in one thread per connection (accept, the connection limit and the timeout are up to you):
c.set_timeout(5000)
http.serve_conn(&c, &r, &app)
c.close()
```

| Declaration | What it does |
|---|---|
| `r.handle(method, pattern, h)` | adds a route (empty `method`: any); tried in order |
| `r.dispatch(&c, &m, &ctx)` | calls the route's function; 404 with no route for the path, 405 with a route for the path but not for the method |
| `serve_conn(&c, &r, &ctx)` | serves a connection until it closes: reads each request, calls `dispatch`, discards the unread body; 431 and 400 for bad requests |
| `p.get(name, &dst) int`, `p.int(name, &out) bool`, `p.is(name, s) bool` | the route's parameters |
| `match_path(pattern, &path, &p) bool` | the matching alone, to test it or build another router |

#### Client

| Declaration | What it does |
|---|---|
| `get(url, &m, &body)` | GET; `m` keeps the code and the headers, the body goes into `body`; returns the size |
| `post(url, content_type, &data, &m, &body)` | POST |
| `request(method, &url, content_type, &data, &m, &body)` | any method, with the URL built at run time |
| `dial(&u, &err) ?*net.Conn`, `dial_tls(&u, &err) ?*tls.Conn` and `b.request_to(method, &u)` | for custom headers: opens the connection (http or https) and starts the request (line and `Host`) |

```mlc
var m http.Message
var body [4096]byte
match http.get("http://127.0.0.1:8080/items/7", &m, &body) {
    Ok(n) => io.printf("{} {:s}\n", m.status(), &body[:n])
    Err(code) => io.printf("error {}\n", code)   // url.BAD_URL, UNSUPPORTED_SCHEME, net.REFUSED, TOO_BIG...
}
```

URLs can be `http://` or `https://` (through `std/tls`, with the certificate checked). The errors are `url.BAD_URL`, `UNSUPPORTED_SCHEME` (another scheme), those of `net.connect`, those of `tls` (`BAD_CERT`...) and `TOO_BIG` (the body doesn't fit in `body`). Each call opens and closes the connection (`Connection: close`).

### `std/json`

**Reading.** `json.Reader` walks the text and returns one token at a time (an enum: `ObjectStart`, `Key(a, b)`, `String(a, b)`, `Number(a, b)`, `True`, `Null`, `End`, `Error(at)`...), without allocating: the `(a, b)` pairs are positions in the text, and `json.unescape` decodes a string when needed. `r.skip(&doc)` skips the whole value that comes next.

```mlc
r := json.reader()
for {
    match r.next(&doc) {
        Key(a, b) => { ... &doc[a:b] is the name ... }
        Number(a, b) => { ... str.parse_int(&doc[a:b], &n) ... }
        End => break
        Error(at) => { ... invalid text at position at ... }
        _ => {}
    }
}
```

**Writing.** `json.Writer` builds the document with the right commas, quotes and escapes. The text is in `&w.buf[:w.n]`, and the buffer is an owner that grows (`realloc`): create it once, use `reset` between documents, `free` at the end.

```mlc
w := json.writer(4096)
w.object()
w.field_int("id", 7)
w.field_str("name", "café")
w.key("tags")
w.array()
w.str("hot")
w.str("strong")
w.end()
w.end()
http.respond(&c, 200, "application/json", &w.buf[:w.n])   // {"id":7,"name":"café","tags":["hot","strong"]}
w.reset()
```

| Declaration | What it does |
|---|---|
| `writer(cap) *Writer`, `w.free()`, `w.reset()`, `w.done()` | create, free, reuse, and check the document is complete |
| `w.object()`, `w.array()`, `w.end()` | open and close (up to 32 levels) |
| `w.key(name)`, `w.key_bytes(&name)` | the key of the next value, in an object |
| `w.str(s)`, `w.text(&s)`, `w.int(v)`, `w.float(v)`, `w.bool(v)`, `w.null()`, `w.raw(&json)` | the values (`raw` writes a piece that's already JSON) |
| `w.field_str`, `field_text`, `field_int`, `field_float`, `field_bool`, `field_null` | key and value at once |

Wrong use (a value without a key in an object, a key in an array, `end` with nothing open, two values at the root) is a program error and panics. NaN and infinity, which JSON doesn't have, become `null`.

### `std/url`

URLs (RFC 3986). A `URL` keeps a copy of the text in a fixed 2 KB buffer, and the parts are copied into the caller's buffers (or compared without copying).

| Declaration | What it does |
|---|---|
| `parse(&s, &u) bool`, `parse_str(s, &u) bool` | reads `scheme://[user@]host[:port]/path?query#fragment`, or just `/path?query` |
| `u.scheme(&dst)`, `u.scheme_is(s)`, `u.host(&dst)`, `u.host_is(s)` | scheme and host (the `_is` ones case-insensitive) |
| `u.port() int`, `u.has_port() bool` | the written port, or the scheme's (80, 443, 21); -1 if there's no way to know |
| `u.path(&dst)`, `u.query(&dst)`, `u.fragment(&dst)`, `u.user(&dst)` | the parts, not decoded (-1 when they don't exist) |
| `u.query_param(name, &dst) int` | a query parameter, decoded |
| `u.target(&dst)` | the target of an HTTP request: path (`/` if empty) and query |
| `u.dial_host(&dst)` | the host as `net.connect` expects it (IPv6 without brackets) |
| `Query` with `q.add(name, &value)`, `add_str`, `add_int` | builds an already-encoded query in `&q.buf[:q.n]` |
| `query_value(&query, name, &dst)`, `escape`, `unescape` | the loose pieces (`http.escape` and `http.unescape` are these) |
