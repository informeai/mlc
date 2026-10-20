# MLC types

## Primitives

| Type | Size | Range | In C |
|---|---|---|---|
| `int8` | 1 byte | -128 to 127 | `int8_t` |
| `int16` | 2 bytes | -32768 to 32767 | `int16_t` |
| `int32` | 4 bytes | -2147483648 to 2147483647 | `int32_t` |
| `int` | 8 bytes | -9223372036854775808 to 9223372036854775807 | `int64_t` |
| `byte` | 1 byte | 0 to 255 | `uint8_t` |
| `uint16` | 2 bytes | 0 to 65535 | `uint16_t` |
| `uint32` | 4 bytes | 0 to 4294967295 | `uint32_t` |
| `uint64` | 8 bytes | 0 to 18446744073709551615 | `uint64_t` |
| `float32` | 4 bytes | single precision (IEEE 754) | `float` |
| `float64` | 8 bytes | double precision (IEEE 754) | `double` |
| `bool` | 1 byte | `true`, `false` | `bool` |
| `string` | 16 bytes | fixed program text (literals) | `{ptr, len}` |

The size of every value is explicit: the programmer chooses how much a field or an element takes (`[]int16` with a million elements uses 2 MB; `[]int`, 8 MB).

## Rules

- **Arithmetic only between values of the same type.** `a + b` with `a int32` and `b int` is an error: convert one side (`int(a) + b`). There's no implicit conversion between numbers.
- **Integer overflow panics**, at every size, signed and unsigned (`estouro de int8 na soma`, "int8 overflow in addition"). So do division and remainder by zero.
- **Floats follow IEEE 754:** overflow becomes infinity, `1.0 / 0.0` is `+Inf`, `0.0 / 0.0` is `NaN`. None of this is undefined behavior. `%` doesn't exist for floats, and floats can't be `match` patterns.
- **Unsigned types have no unary `-`.**

## Bits and wrapping arithmetic

| Operator | What it does |
|---|---|
| `a & b`, `a \| b`, `a ^ b` | bitwise and, or, exclusive or |
| `^a` | flips every bit (`^0` is -1; on a `byte`, `^0xF0` is `0x0F`) |
| `a << n`, `a >> n` | shifts `n` bits; a signed `>>` repeats the sign bit |
| `a +% b`, `a -% b`, `a *% b` | addition, subtraction and multiplication **modulo 2^bits**: they wrap around instead of panicking |
| `&= \|= ^= <<= >>= +%= -%= *%=` | the compound assignments |

- They only apply to integers, and both sides of `& | ^ +% -% *%` must be the same type.
- In a shift, `n` can be any integer type; outside `0..bits-1` it panics (a compile error if `n` is a constant). The bits that go out on the left in a `<<` are dropped.
- Precedence is Go's: `<< >> &` together with `* / %`, and `| ^` together with `+ -`. So `a & b == c` is `(a & b) == c`, and `1 + 2 << 1` is 5.
- Ordinary arithmetic stays checked: whoever wants it to wrap (hashes, cryptography, random generators) writes `+%`, and that's visible in the code.

```
var h uint64 = 0xCBF2_9CE4_8422_2325       // FNV-1a
for i := 0; i < len(s); i++ {
    h ^= uint64(s[i])
    h *%= 0x0000_0100_0000_01B3
}
```

## Integer literals

`255`, `0xFF`, `0b1111_1111`, `0o377` and `1_000_000`: the `_` can only go between digits. A leading zero doesn't change the base (`010` is ten).

## Functions as values

`fn(int, int) bool` is the type of a function value: the address of a function, with no captured variables (there are no closures), so it doesn't allocate and has no owner.

```
fn double(x int) int { return x * 2 }

fn apply(f fn(int) int, x int) int {
    return f(x)
}

f := double                     // the function's name is the value
io.print(apply(f, 21))
var g fn(int, int) int = larger[int]   // generic: with the types
sort.sort_by(&people, by_age)          // fn(&Person, &Person) bool
```

- The parameter types say what the call does with each argument (copies, borrows `&T`, moves `*T`), and the ownership checker checks it as in a direct call.
- The zero value of a fn (a variable with no value, a field not filled in) is nil, and calling it panics with the line of the call.
- Methods and `extern fn` don't become values; a generic function needs its types (`larger[int]`). Fn values can't be compared.

## Conversions

`int8(x)`, `int(x)`, `byte(x)`, `uint64(x)`, `float32(x)`, `float64(x)` and the others convert between any numbers.

| Conversion | Behavior |
|---|---|
| to an integer that holds every source value | always works |
| to a smaller integer, or between signed and unsigned | panics if the value doesn't fit: `valor 3000000000 não cabe em int32` ("value 3000000000 doesn't fit in int32") |
| from float to integer | truncates toward zero (`int(-2.9)` is -2); panics if it doesn't fit or if it's `NaN` |
| from integer to float | always works (it may round) |
| from `float64` to `float32` | rounds; out of range becomes infinity |

In C, converting a value that doesn't fit is undefined behavior; in MLC, it's a panic with the `.mlc` line.

## Untyped constants

Numeric literals don't have a fixed type: they take the type of the context.

```
var x int32 = 5        // the 5 is int32
var f float64 = 3      // the 3 is float64
n := 5                 // no context: int
g := 2.5               // no context: float64
var b int8 = 300       // compile error: constant 300 doesn't fit in int8 (-128 to 127)
```

- In an arithmetic expression, the constant takes the type of the other side: with `n int32`, in `n + 1` the `1` is `int32`.
- Expressions with only constants are computed at compile time: `const MASK = 255 * 2 - 255` is 255. Overflow and division by zero in a constant are compile errors.
- `const N = 100` has no type and works for any number; `const N int32 = 100` has one.
- A float doesn't become an integer without a conversion: `var n int = 2.5` is an error. With `int(2.5)`, the conversion is explicit and truncates.
- Literals accept scientific notation: `1e9`, `2.5e-3`.

## Composites

| Type | What it is |
|---|---|
| `[N]T` | fixed-size array: a value, on the stack or inside a struct; no `alloc` |
| `[]T` | slice: owner of heap memory (`alloc`/`realloc`/`free`) |
| `struct` | named fields, by value |
| `*T`, `?*T` | owning pointer (never nil / may be nil) |
| `&T` | borrow, only as a parameter |

### Arrays `[N]T`

```
const N = 4
a := [N]int{1, 2, 3, 4}      // every element, or none: [N]int{} is the zero value
a[2] = 30                    // checked index; a constant out of range is a compile error
b := a                       // copies
io.print(a == b, len(a))     // element-by-element equality; len is a constant

var buf [64]byte             // text buffer on the stack
k := str.copy(&buf, "hi")    // &buf borrows the array as &[]byte
sum(&a[1:3])                 // sub-slice of an array
```

- `N` is an integer constant greater than zero.
- An array goes on the stack or inside a struct, so its size is limited to 1 MiB, so as not to overflow the stack. For more, use `alloc([]T, n)`. The total of a thread's arrays is also checked against its stack (see [memory.md](memory.md#stack)).
- The elements can't be owners (`[4]?*Node`, `[2][]int`).
- There's no pointer to an array: use the array directly, or a slice.

## Generics

```
fn larger[T ordered](a T, b T) T {
    if a > b { return a }
    return b
}

struct Pair[A any, B any] {
    first A
    second B
}

fn (p Pair[A, B]) swap() Pair[B, A] {
    return Pair[B, A]{first: p.second, second: p.first}
}

larger(3, 7)              // T inferred: int
larger[int8](-3, -9)      // explicit T
p := Pair[int, string]{first: 1, second: "one"}
```

- **Constraints:** each type parameter declares what the code can do with it.

  | Constraint | Accepted types | Allowed operations |
  |---|---|---|
  | `any` | any type without ownership | assign, pass, return, print |
  | `comparable` | any type without ownership | those of `any`, and `==`, `!=` |
  | `ordered` | numbers | those of `comparable`, and `<`, `<=`, `>`, `>=` |
  | `number` | numbers | those of `ordered`, and `+`, `-`, `*`, `/` |
  | `integer` | integers | those of `number`, and `%` |
  | `owned` | types without ownership **and** simple owners (`?*X`, `[]X`, with `X` without owners) | move, pass, return, `take`/`replace`/`drain` on `[]T`, `drop(x)`; **no** copying or `==` |
  | `ref` | any type, including structs with owners (except borrows) | only borrowed: the parameter only appears as `&T` (a callback's context) |

- **The definition is checked once,** with `T` allowed only what the constraint permits: `a < b` with `T any` is an error in the definition itself, before any use.
- **Each use with concrete types generates an instance** (`larger[int]`, `Pair[int, string]`): a copy checked with the real types and compiled into C with its own name. There's no run-time cost. An error that only shows up with a concrete type (like `x + 300` with `T = int8`) points at the line of the definition and says which instance failed.
- **Inference:** the types are inferred from the arguments; when that's not possible (no argument uses `T`), write them: `empty[int]()`.
- **`T(x)`** converts to the type parameter: `total := T(0)`.
- **Methods of a generic struct** name the parameters in the receiver: `fn (v &Vector[T]) add(x T)`.
- **Constraint in the receiver:** `fn (v &Vec[T any]) get(i int) T` only exists in the instances where `T` meets `any`. Calling `get` on a `Vec[[]byte]` is an error, with a suggestion of `take`/`put`.
- With the ordinary constraints, type arguments can't be owners or borrows (`*Node`, `[]int`, `&T`). To accept owners, use `owned`.
- **Methods with their own type parameters**, besides the struct's (see below).

### Generic methods and callbacks with a context

Function values don't capture variables (there are no closures, which would keep hidden state). For a callback to accumulate something, the caller passes the **context** along, and the function receives it borrowed:

```
pub fn (m &Map[K, V]) each_with[C ref](ctx &C, f fn(&C, &K, &V))

struct Totals {
    sum int
    count int
}

fn add(t &Totals, k &int, v &int) {
    t.sum += *v
    t.count++
}

var t Totals
m.each_with(&t, add)          // C inferred: Totals
m.each_with[Totals](&t, add)  // or written
```

- A method can have its own type parameters (`each_with[C ref]`), besides the struct's. They're inferred from the call's arguments (without the receiver) or written (`m.each_with[Totals](...)`), and each combination generates an instance, like a generic function. It works on ordinary structs too (`fn (c &Clock) run[C ref](...)`). The `ref` constraint also accepts opaque C types (`extern "C" type`), which only exist behind a pointer; combined with an interface (`[R ref + io.Reader]`), it's the one used by `bufio.Reader`.
- **`ref`** is the context's constraint: it accepts any type (including structs with owners, like a `vec` where the callback stores results), because the parameter only appears borrowed. Using `C` by value in the body is an error.
- The context is borrowed during the call: the function can change it, but not keep it. The memory belongs to the caller (on the stack, with `var`, or with `alloc`), and nothing is allocated behind your back.
- In the library: `map.each_with`, `vec.each_with`, `sort.sort_by_with`, `slices.index_by` and `slices.count_by`; also `fs.walk` and `fs.glob`.

### Owners in generics: `owned`

```
struct Vec[T owned] {
    items []T
    count int
}

fn (v &Vec[T]) push(x T) {           // x is moved inside
    if v.count == len(v.items) {
        v.grow()
    }
    old := replace(&v.items[v.count], x)
    drop(old)                        // the slot after the end was empty
    v.count++
}
```

- Inside the body, a value of type `T` is treated as an owner: assigning, passing or returning **moves**, using after a move is an error, and every value needs a destination (move, store, return or `drop(x)`). Copying and comparing with `==` aren't allowed.
- **`drop(x)`** only exists in generic code: it frees `x` if it's an owner (`free`) and does nothing if it isn't (with `T = int`). Outside a generic, use `free`.
- `take(&s[i])`, `replace(&s[i], v)` and `for x in drain(s)` work on `[]T`, as on slices of owners.
- `*X` isn't accepted (it has no zero value, and a collection needs empty slots): use `?*X`.
- **Nested owners:** `?*X` with `X` containing owners, and slices of owners (`[][]byte`, `[]?*Node`), are allowed, as long as each struct with owners knows how to free itself with a **`fn (x *X) free()`** method, written by the programmer (the library's `Vec` and `Map` already have one). `drop(x)` uses that method: in `Vec[?*vec.Vec[int]]`, freeing the outer vector frees each inner vector. Without the `free`, the error points at where the type was used. That's how trees and nested data fit:

  ```
  struct Value {
      kind int
      text []byte
      items ?*vec.Vec[?*Value]   // children
  }

  fn (v *Value) free() {
      free(v.text)
      if v.items != nil {
          v.items.free()         // frees each child (Value.free) and the vector
      }
      v.items = nil
      free(v)
  }
  ```

- With `T = ?*X`, `&T` is `&X`: `v.at(i)` borrows the pointed-to object (`grid.at(1).get(2)`), and a `nil` slot panics.
- **Combined constraints:** a built-in constraint and an interface, with `+`: `[K owned + hash.Hash]` accepts simple owners that implement `Hash` (the keys of `map.Map`). In the receiver's constraint, the missing part comes from the struct: in `Map[K owned + hash.Hash, V owned]`, `fn (m &Map[K any, V]) key_at` asks for `K any + hash.Hash`.
- **Ownership errors show up in the definition:** each generic with `owned` is also checked with `T = []byte`. Such an error says "verificado com T dono" ("checked with an owning T").

## Enums with data

```
enum Shape {
    Circle(radius float64)
    Rectangle(width float64, height float64)
    Empty
}

enum Outcome[T any] {
    Ok(value T)
    Failure(code int)
}

f := Shape.Circle(2.0)
v := Shape.Empty

match f {
    Circle(r) => io.print(3.14 * r * r)     // r gets a copy of the field
    Rectangle(w, _) => io.print(w)          // _ ignores a field
    Empty => io.print("nothing")
}
```

- An enum value is one of the variants, with its data. It's built with the enum's name in front: `Shape.Circle(2.0)`, `Shape.Empty`.
- The `match` must cover every variant (or have `_`); forgetting one is a compile error, and the message says which ones are missing. In a variant with data, the pattern gives a name to each field (or `_`).
- **Generic enums:** the types can be written (`Outcome[int].Failure(2)`) or inferred from the value (`Outcome.Ok(5)`) or from the context (`return Outcome.Failure(1)` in a function that returns `Outcome[int]`).
- `==` compares the variant and the data; `io.print` shows `Circle(2)`.
- The zero value (`var f Shape`) is the first variant, with zeroed fields.
- An enum without owners is an ordinary, copyable value.

### Enums with owners

A variant can hold owners (`[]T`, `?*X`, or structs and generics that hold them): that's how an error carries a message allocated by whoever created it.

```
enum ParseError {
    Empty
    BadDigit(at int, msg []byte)
}

fn describe(e &ParseError) int {
    match *e {                              // only reads: msg comes borrowed (&[]byte)
        Empty => return 0
        BadDigit(at, msg) => return at + len(msg)
    }
}

fn handle(e ParseError) {
    match e {                               // consumes: msg becomes an owner
        Empty => {}
        BadDigit(_, msg) => free(msg)       // the checker requires this free
    }
}
```

- Such an enum is an **owner by value**: assigning, passing or returning moves, like a `[]byte`. When building it (`ParseError.BadDigit(i, msg)`), the owning arguments are moved inside.
- A pointer field is always `?*X` (the enum's zero value is the first variant with zeroed fields, that is, `nil`); `*X` is an error.
- **`match` over an owning variable or a temporary consumes:** each owning field gets a name (`_` is an error, because the data would leak) and must be freed or moved in the arm. A `_` arm that would cover a variant with owners is also an error. After the `match`, the variable no longer exists.
- **`match` over `*e`, a field or a borrow only reads:** the owning fields come borrowed (`&[]byte`, `&X`) and the source stays frozen while the arm uses them.
- **`free(e)`** frees the current variant's data, without a `match`.
- `==` doesn't apply (use `match`); `io.print` works with `impl auto io.Display`.
- The standard library's `Result` accepts owners on both sides: `result.Result[int, ParseError]`, `result.Result[[]byte, int]`. See [library.md](library.md).
