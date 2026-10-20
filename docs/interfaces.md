# Interfaces and printing

## Static interfaces

An interface lists methods a type can implement:

```
interface Measure {
    fn size(self &Self) int
    fn scale(self &Self, k int) int
}
```

- `Self` is the implementing type. The first parameter is always `self &Self`: the method receives the value **borrowed**.
- **In this version, an interface only serves as a generic constraint**, never as a variable type:

```
fn larger[T Measure](a &T, b &T) int {   // ok
    if a.size() > b.size() {
        return a.size()
    }
    return b.size()
}

var m Measure                            // error: interfaces only serve as generic constraints
```

Each concrete use (`larger[Square]`, `larger[int]`) becomes its own instance, and the `a.size()` call inside it is a **direct call** to the type's method, with no method table, function pointer or allocation. It's the same monomorphization as the other generics.

## impl: the implementation is explicit

Having the methods isn't enough: an `impl` declares the intent, and the compiler checks the signatures.

**A struct with the methods already declared** (`&S` receiver):

```
struct Square {
    side int
}

fn (q &Square) size() int {
    return q.side * q.side
}

fn (q &Square) scale(k int) int {
    return q.size() * k
}

impl Measure for Square
```

**With a block**, for types that have no methods of their own (built-in types, slices) or when the methods only make sense through the interface:

```
impl Measure for int {
    fn (x &int) size() int {
        return *x
    }
    fn (x &int) scale(k int) int {
        return *x * k
    }
}
```

**Generic**, for a whole shape of type:

```
impl[T Measure] Measure for []T {
    fn (s &[]T) size() int {
        total := 0
        for i := 0; i < len(s); i++ {
            total += s[i].size()
        }
        return total
    }
    fn (s &[]T) scale(k int) int {
        return len(s) * k
    }
}
```

An array `[N]T` uses the `[]T` impl (through a view of the array, without copying).

Implementing an interface makes its methods public: a type from one module can be the key of a `map.Map` from another module even without `pub fn` on the methods.

An impl's methods can also be called directly: `n.size()` with `n int`, `s.size()` with `s []Square`.

### Rules

- The impl lives **in the type's file or in the interface's file**: two modules can't implement the same thing in different ways.
- A type implements each interface only once.
- The impl must have all of the interface's methods, with the right signatures, and no others.
- Pointers and borrows don't implement interfaces (implement it for the pointed-to type). Slices and structs with owners can: the methods only borrow. Opaque C types can too (`impl io.Reader for File`); in a generic, they only fit with the `ref` constraint (see [ffi.md](ffi.md#c-handles)).
- An owner (`*T`, `[]T`) can't be a **type argument** of a generic, as everywhere in the language in this version: `larger(&s, &s)` with `s []int` is refused, but `s.size()` works.

## Printing: io.Display

`io.print` and `io.printf` write any value that implements `io.Display`:

```
// std/io.mlc
pub interface Display {
    fn write(self &Self, w &Writer)
}
```

The built-in types (`int`, `float64`, `bool`, `string`, `byte`...), slices and arrays already implement it, in MLC, in the library itself ([std/io.mlc](../std/io.mlc)).

For structs and enums, `impl auto` generates the method in the usual format (it also exists for `cmp.Eq`, `cmp.Ord` and `hash.Hash`, see [Equality, order and hash](#equality-order-and-hash-cmpeq-cmpord-and-hashhash)):

```
impl auto io.Display for Point       // {1 2}
impl auto io.Display for Shape       // Circle(2), Rectangle(3, 4), Empty
impl auto io.Display for Pair        // generic: Pair[int, string] writes {1 one}
```

Every field must implement `io.Display`; otherwise the error points at the field. You can also write the method by hand:

```
fn (f &Fraction) write(w &io.Writer) {
    w.put_int(f.num)
    w.put_byte('/')
    w.put_int(f.den)
}

impl io.Display for Fraction
```

### io.printf

```
io.printf("point {} has area {}\n", p, area(p))
io.printf("braces: {{ and }}\n")
io.eprintf("error: {}\n", code)         // to standard error
io.print(a, b, c)                       // values separated by spaces, and \n
io.eprint(a, b, c)
```

- The format is a **string literal**, checked at compile time: the number of `{}` must match the number of values, and a lone `{` or `}` is an error (`{{` and `}}` write braces).
- Specifications like `{:x}`, `{:.2}`, `{:>8}`, `{:08}` and `{:s}` say how to write the value. See [text.md](text.md#format-specifications).
- `str.format(&buf, "...", x)` does the same writing into a `[]byte`, and `str.format_len` says how many bytes it takes. See [text.md](text.md#formatting-into-a-buffer-strformat).
- A value without `io.Display` is a compile error, with the `impl auto` suggestion.
- The values are computed before writing, left to right.
- A borrow (`x` with `x &T`) is written without copying; inside a generic `[T io.Display]`, `io.print(x)` works too.

### What the compiler does

Only the rewriting of the call. `io.printf("x = {}\n", x)` becomes, in essence:

```
w := io.stdout()              // Writer with a [256]byte buffer on the stack
w.put_str("x = ")
x.write(&w)                   // direct call to the write of x's type
w.put_str("\n")
w.flush()
```

All formatting is MLC, in `std/io.mlc`: the `Writer`, integer writing, the `write` of the built-in types. Only the formatting of real numbers (the fewest digits that represent the value) and the writing to the file stay in the C runtime. Nothing allocates on the heap.

### io.Writer

| Method | What it does |
|---|---|
| `io.stdout()`, `io.stderr()` | create a Writer (a value on the stack) |
| `w.put_str(s string)`, `w.put_bytes(b &[]byte)`, `w.put_byte(b byte)` | text |
| `w.put_int(n int)`, `w.put_uint(n uint64)` | decimal integers |
| `w.put_float(x float64)`, `w.put_f32(x float32)` | reals, in Go's format |
| `w.put_bool(b bool)` | `true` / `false` |
| `w.flush()` | writes what's in the buffer (also happens when it fills up) |
| `w.printf("...", a, ...)` | the `io.printf` format (including the specifications), written to `w`, without creating another Writer and without a `flush` |

`w.printf` covers two cases:

- **Loops that write a lot:** each `io.printf` creates a Writer and flushes it at the end, so the output goes out line by line (in a terminal, one system call per line). With a reused Writer, it goes out in 256-byte blocks:

  ```
  w := io.stdout()
  for i := 0; i < 1000000; i++ {
      w.printf("{:>7}\n", i)
  }
  w.flush()                       // what's left in the buffer
  ```

- **Custom `write` methods:** the `&io.Writer` parameter accepts `w.printf`, so the format stays readable: `fn (p &Price) write(w &io.Writer) { w.printf("$ {}.{:02}", p.cents / 100, p.cents % 100) }`.

The Writer must be in a variable or field (or be a `&io.Writer` or `*io.Writer`): a temporary Writer (`io.stdout().printf(...)`) would lose what was written, and is refused.

During the `write` of a value with a specification, the Writer's fields (`base`, `prec`, `zero`, `width`, `upper`, `alt`) make `put_int`, `put_uint` and `put_float` follow the specification. A hand-written `write` inherits this without doing anything. Alignment is done around the `write` by the `measure`, `pad_before` and `pad_after` methods, also in MLC.

## Equality, order and hash: cmp.Eq, cmp.Ord and hash.Hash

```
// std/cmp.mlc
pub interface Eq {
    fn eq(self &Self, other &Self) bool
}

pub interface Ord {
    fn cmp(self &Self, other &Self) int     // negative: self comes first; 0: equivalent; positive: after
}
```

Numbers, `bool`, `string` and slices (in lexicographic order) already implement both. For structs and enums, `impl auto` generates the methods:

```
struct Version {
    major int
    minor int
}

impl auto cmp.Eq for Version         // field by field
impl auto cmp.Ord for Version        // in field order: major, then minor
impl auto hash.Hash for Version      // hash and eq: Version becomes a map key

sort.sort(&versions)
i := slices.binary_search(&versions, &v)
m := map.new[Version, []byte](16)
```

- In a struct, fields are compared in declaration order. In an enum, first the variant (in declaration order), then its fields.
- Every field must implement the interface; otherwise the error points at the field.
- The `impl auto` of a generic struct (`impl auto cmp.Ord for Pair`) requires the interface for each type parameter.
- For another order, write the method and declare the one-line `impl`: `fn (p &Person) cmp(o &Person) int { return p.age.cmp(&o.age) }` and `impl cmp.Ord for Person`.
- Structs with owners implement them too (the methods receive `&Self`).

`sort.sort`, `sort.is_sorted` and the `slices` module use these interfaces. See [library.md](library.md).

## Left for later

- Dynamic interfaces (`&Measure`, `*Measure`), to keep values of different types together.
- Interface methods that receive `Self` (a copy) or `*Self` (ownership).
