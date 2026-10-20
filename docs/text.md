# Text in MLC

Text is built in a **`[]byte` buffer you allocate**. The text and input/output functions **never allocate**: they write into the borrowed buffer, return how many bytes they used, and panic if it doesn't fit. That way every text allocation shows up in the code as `alloc` or `realloc`, and the size of every buffer is chosen by the programmer.

```
import "std/io"
import "std/str"

fn main() {
    name := alloc([]byte, 32)
    n := io.read_line(&name)                  // reads into the buffer, without allocating

    need := len("Hello, ") + n                // how much space the sentence takes
    buf := alloc([]byte, need)                // the only allocation, with a known size
    k := 0
    k += str.copy(&buf[k:], "Hello, ")
    k += str.copy_bytes(&buf[k:], &name[:n])
    io.write_line(&buf[:k])

    free(name)
    free(buf)
}
```

## Types

| Type | What it is |
|---|---|
| `string` | fixed program text (literals). Doesn't allocate and doesn't need `free`. |
| `byte` | one byte, from 0 to 255. Literal: `'a'`, `'\n'`, `'\''`. |
| `[]byte` | text buffer: an owner, created with `alloc([]byte, n)`, grown with `realloc`, released with `free`. |

- `s[i]` on a `string` or a `[]byte` gives a `byte`.
- `len(s)` gives the size in **bytes**: `len("olá")` is 4, because `á` takes two bytes in UTF-8.
- `byte` is an 8-bit unsigned integer: it does arithmetic with other `byte`s (overflow panics), and converts with `int(b)` and `byte(n)`, which panics if `n` is outside 0 to 255. See [types.md](types.md).
- Bytes can be compared (`==`, `<`, ...) and used in `match` with literals: `'a', 'e' => ...`.

For small buffers, a `[N]byte` array avoids the `alloc` and the `free`: it lives on the stack, and `&buf` borrows it as `&[]byte`.

```
var buf [32]byte
k := str.copy(&buf, "hello, ")
k += str.put_int(&buf[k:], 42)
io.write_line(&buf[:k])
```

## Functions (`import "std/str"`)

| Function | What it does |
|---|---|
| `str.copy(dst &[]byte, s string) int` | copies `s` to the start of `dst`; returns how many bytes it copied |
| `str.copy_bytes(dst &[]byte, src &[]byte) int` | copies `src` to the start of `dst` (they may overlap) |
| `str.put_int(dst &[]byte, n int) int` | writes `n` in decimal at the start of `dst` |
| `str.int_len(n int) int` | how many bytes `n` takes in decimal (to know how much to allocate) |
| `str.eq(a &[]byte, s string) bool` | does `a` have the same text as `s`? |
| `str.eq_bytes(a &[]byte, b &[]byte) bool` | do `a` and `b` have the same text? |
| `str.parse_int(s &[]byte, out &int) bool` | reads a decimal integer (with an optional sign) from all of `s`; on success it stores it in `out` and returns `true` |
| `str.parse_uint(s &[]byte, out &uint64) bool` | reads an unsigned decimal integer (for values above the largest `int`) |
| `str.parse_int_base(s &[]byte, base int, out &int) bool` | reads an integer in the given base (2 to 36), with an optional sign; with 16, 2 or 8 it accepts the `0x`, `0b` or `0o` prefix; with base 0, the prefix picks the base and, without a prefix, it's decimal |
| `str.parse_bool(s &[]byte, out &bool) bool` | reads exactly `true` or `false` |
| `str.put_float(dst &[]byte, x float64) int` | writes `x` with the fewest digits that represent it exactly (like `io.print`) |
| `str.parse_float(s &[]byte, out &float64) bool` | reads a decimal (`2.5`, `1e9`) from all of `s` |

The `parse_*` functions read the whole text (with no surrounding spaces: trim first with `text.trim_start` and `text.trim_end`) and return `false`, without a panic, for invalid text or for a number that doesn't fit the type.

The writing functions panic if the text doesn't fit in `dst`, with a message like `str.copy: o texto (3 bytes) não cabe no destino (2 bytes)` ("the text (3 bytes) doesn't fit in the destination (2 bytes)").

## Formatting into a buffer: `str.format`

```
n := str.format_len("{} items, $ {:.2}", qty, total)   // how many bytes the text takes
line := alloc([]byte, n)                              // the allocation, with the exact size
str.format(&line, "{} items, $ {:.2}", qty, total)
```

| Function | What it does |
|---|---|
| `str.format(dst &[]byte, "...", a, ...) int` | writes the format into `dst`, like `io.printf`, and returns how many bytes it used; panics if it doesn't fit, pointing at the line of the `str.format` |
| `str.format_len("...", a, ...) int` | how many bytes the text takes, without writing anything |

- The format and the values follow the `io.printf` rules, including the specifications (below) and types with `io.Display`: a `write` written for `io.printf` also works for `str.format`.
- To keep writing after what's already in the buffer: `k += str.format(&buf[k:], "...", x)`.
- To write a lot to the output (a loop with thousands of lines), reuse an `io.Writer` with `w.printf(...)` instead of calling `io.printf` for every line. See [interfaces.md](interfaces.md#iowriter).
- `dst` can't also be one of the values (`str.format(&b, "{}", b)` is an error), because the text would be read while it's being overwritten.
- Nothing allocates. The text goes through an `io.Writer` with a buffer on the stack, which hands it to `dst`. The `Writer` doesn't keep `dst`: it keeps a destination index the runtime checks, and that destination only exists during the `str.format`. That's why there's no `Writer` over a `[]byte` that can be kept in a variable: it would need borrows stored in structs, item 8 of the roadmap.

## Format specifications

They apply to `io.printf`, `io.eprintf`, `str.format` and `str.format_len`: `{:[[fill]align][#][0][width][.places][kind]}`.

| Example | Result | Applies to |
|---|---|---|
| `{:x}`, `{:X}`, `{:b}`, `{:o}` | `ff`, `FF`, `101`, `10` | integers (negatives with `-`: `-ff`) |
| `{:#x}`, `{:#b}`, `{:#o}` | `0xff`, `0b101`, `0o10` | integers |
| `{:.2}` | `3.14` | `float64`, `float32` |
| `{:8}`, `{:<8}`, `{:>8}`, `{:^8}` | minimum width of 8 characters, aligned left, right or center | any value; without an alignment, numbers go right and everything else left |
| `{:*^8}` | fills with `*` instead of spaces | any value |
| `{:08}`, `{:08.2}`, `{:#010x}` | zeros after the sign and the prefix: `-0000042` | numbers |
| `{:s}`, `{:>10s}` | the bytes of a `[]byte` (or `[N]byte`) as text, instead of `[104 105]` | `[]byte`, `&[]byte`, `[N]byte` |

- **The width counts characters, not bytes.** `"café"` takes 4. A value longer than the width isn't cut.
- A specification that doesn't fit the type is a compile error (`{:x}` on a `float64`, `{:.2}` on an `int`).
- A value can be a borrow, written without copying: `&b[:n]` or a `&T` variable.
- On a type with its own `write`, width and alignment apply to the whole text the `write` writes. Base, places and zeros apply to the numbers written by `w.put_int`, `w.put_uint` and `w.put_float`.

## Input and output (`import "std/io"`)

| Function | What it does |
|---|---|
| `io.print(a, b, ...)` | writes values separated by spaces, with a line break at the end |
| `io.printf("x = {}\n", x)` | writes the format, replacing each `{}` with a value (the format is checked at compile time; specifications like `{:>8.2}`, see above) |
| `io.eprint(...)`, `io.eprintf(...)` | the same, to standard error |
| `io.write(s &[]byte)` | writes the bytes of `s`, with no separator or line break |
| `io.write_line(s &[]byte)` | writes `s` and a line break |
| `io.read_line(dst &[]byte) int` | reads a line (without the `\n`) into `dst` and returns how many bytes it read; `-1` at the end of the input |

The values of `io.print` and `io.printf` must implement `io.Display`: the built-in types, slices and arrays already do, and structs and enums get the implementation with `impl auto io.Display for T`. See [interfaces.md](interfaces.md).

If the line doesn't fit in `dst`, `read_line` fills the buffer and the rest of the line is left for the next call. To read lines of any length, grow the buffer with `realloc` and keep reading into the new space:

```
used := 0
for {
    got := io.read_line(&line[used:])
    if got < 0 { break }
    used += got
    if used == len(line) {
        line = realloc(line, len(line) * 2)   // the line didn't fit: double it
        continue
    }
    // full line in line[:used]
    used = 0
}
```

## Why there's no concatenation

A `concat(a, b)` would have to decide on its own how much to allocate, and a text that grows on its own is a built-in `append`. Both hide allocation, which goes against MLC's central rule. Building text in a buffer makes it visible **where** the memory is (the `alloc`) and **how much** it takes (the size passed to `alloc`, which `str.format_len`, `str.int_len` and `len` help compute).
