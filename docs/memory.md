# MLC memory model

> **Status:** implemented (milestones 5a, 5b and 6): owning pointers and slices in variables, parameters and struct fields.

## Principle

MLC uses C's model: **every allocation and every release is written by the programmer**. There's no garbage collector, no hidden reference counting and no automatic destructor. What changes from C is that the compiler **rejects the program when it can't prove** the use of memory is correct.

The checker is conservative by design. It may refuse a correct program it doesn't know how to analyze, but it never accepts a program with the bugs in the table below. When it refuses correct code, the way out is to rewrite the code more explicitly, never to turn the check off.

## Syntax

```
p := alloc(Point)              // *Point: owning pointer, zeroed memory
q := alloc(Point{x: 1, y: 2})  // alloc with an initial value
n := alloc(42)                 // *int
p.x = 10                       // field through the pointer
*n += 1                        // the pointer's contents
free(p)                        // required on every path

fn sum(p &Point) int           // &T: borrow; reads and writes, doesn't free
fn destroy(p *Point)           // *T as a parameter: the function takes ownership

sum(&p)                        // borrow an owner
sum(&local)                    // borrow a local struct

struct Node {                  // owning field: the list owns the next node
    value int
    next ?*Node
}
```

`alloc` ends the program with a panic if memory runs out, so that `*T` is never null. `free` of a `?*T` that is `nil` does nothing, as in C.

## Memory without the heap: arrays

An array `[N]T` has a fixed size, known at compile time, and it's a value: it lives on the stack (or inside a struct) and goes away when the scope ends, with no `alloc` or `free`. It's the most explicit form of memory: the type says exactly how many bytes it takes. So as not to overflow the stack, an array is at most 1 MiB; for more, use a slice. The total of each thread's variables is also checked against its stack (see [Stack](#stack)).

## Slices

```
s := alloc([]int, n)       // []int: owner of n zeroed elements
s[i] = 1                   // index checked at run time
for v in s { }             // v is a copy of each element
free(s)                    // required, as for *T

fn sum(s &[]int) int       // borrow: reads and writes elements, doesn't free
var empty []int            // zero value: empty slice, no obligation to free
```

### Slices of owners

```
nodes := alloc([]?*Node, n)        // elements start nil (that's why ?*T, and not *T)
old := replace(&nodes[i], fresh)   // puts fresh in place and returns the old one
n := take(&nodes[i])               // takes the element out and leaves nil in its place
for n in drain(nodes) {            // each element leaves the slice into n...
    free(n)                        // ...and must be freed or moved in that iteration
}
free(nodes)                        // accepted at compile time: drain took them all out

m := alloc([][]int, 3)             // matrix: rows start as empty slices
m[1][2] = 5                        // indexing moves nothing
```

The checker doesn't track element by element (the index only exists at run time). For each slice of owners, it knows whether the elements are **all empty** or **may have owners**:

- right after `alloc([]?*T, n)`, they're all empty;
- after a `replace` with an owner, they may have owners;
- after a complete `for x in drain(s)` (no `break`), they're all empty;
- a slice received as a parameter or returned by a function may have owners.

`free(s)` is only accepted with every element empty, so leaking an element is a **compile error**. To keep the accounting right:

- an owning element isn't copied or overwritten directly (`x := s[i]`, `s[i] = x`): use `take` and `replace`;
- the contents of `s[i]` aren't used directly, because it may be `nil` and the index is dynamic: take it out with `take`, use it, put it back with `replace`;
- inside a `for x in drain(s)`, `s` doesn't receive elements and isn't borrowed.

`realloc` on a slice of owners can only grow: shrinking would discard elements that may hold memory, and that panics at run time. It's a precondition, like bounds checking; a leak is still always a compile error.

### Growing and borrowing a part

```
s = realloc(s, len(s) * 2)   // consumes the old slice and returns a new one
sum(&s[1:3])                 // borrows elements 1 and 2
sum(&s[2:])                  // from 2 to the end
```

`realloc` behaves like a function that takes ownership of the old slice and returns ownership of a new one: the existing elements are copied, and the new ones come zeroed. So there's no way to keep a name pointing at the old block, the classic `realloc` bug in C. Shrinking discards the elements at the end; `realloc(s, 0)` frees and returns the empty slice. There's no automatic growth (`append`): every reallocation shows up in the code.

A sub-slice `&s[a:b]` owns nothing: it's a borrow of the elements from `a` to `b-1` and follows the borrow rules (only as a call argument). The bounds are checked at run time: `0 <= a <= b <= len(s)`. `a` and `b` can be left out (`&s[:b]`, `&s[a:]`, `&s[:]`).

A slice is an owner, like `*T`, and follows the same ownership rules: it moves when assigned or passed to a `[]T` parameter, and it needs a `free` on every path. Freeing a slice inside the `for in` that walks it is an error. `s[i]` on an empty slice needs no `nil` check: bounds checking panics. The elements can be owners; see the previous section.

## Ownership

Every owning pointer has **exactly one owner**: a variable or a field. The checker tracks each owning **path**, like `p`, `p.next`, `p.next.next` or `l.head`, and knows, at every point in the code, its set of possible states:

| State | Meaning |
|---|---|
| owning | it has memory and must free it or pass it on |
| nil | it's `nil` (only `?*T`), with no obligation |
| freed | `free` was called, or ownership passed on (moved) |

Where two execution paths join (end of an `if`, `match` arms, loop exit), the sets are merged. If one path frees and another doesn't, the owner becomes "owning or freed", and the rules below treat that as an error.

Rules:

1. **Using** an owner that may be freed is an error: use after `free` or after a move.
2. **`free`** on an owner that may be freed is an error: double `free`.
3. Leaving the scope of an owning variable (end of the block, `return`, `break`, `continue`) while it may hold memory is a **leak** error. The message points at the line of the allocation and where the exit happened.
4. **Overwriting** a variable or field that may hold memory is an error, because the old memory would leak.
5. **Moving** ownership: `q := p`, `q = p`, `x := p.next`, passing to a `*T` parameter, `return p`. After that, the source counts as freed.
6. **A discarded owned value** is a leak: calling `make()` without keeping the result, or using `make().x`.
7. **Loops:** the body is analyzed until the state at the start of an iteration stabilizes (a fixed point). So the second iteration onward is checked too: freeing inside the loop an owner that came from outside becomes "double `free` on some path", and allocating without freeing becomes "would be overwritten".

## `nil` and the flow

Using the contents of a `?*T` (`q.x`, `*q`, `&q`) or passing it where `*T` is expected is only allowed where it **can't be `nil` at that point**. The checker learns this from conditions:

```
if q != nil { q.x = 1 }                  // inside the if, q isn't nil
for cur != nil { ...; cur = next }       // inside the for, cur isn't nil
if p != nil && p.x > 0 { }               // the right side of && already knows
if n.next != nil { n.next.value }        // it works for fields too
```

What the checker learned stops applying when the value changes: after `q = nil`, or after passing `&p` to a function that may have changed `p.next`, you need to check again.

A value that comes out of `alloc` is never `nil`, even when kept in a `?*T`, and the checker knows it.

## Pointers inside structs

- **Invariant:** every owning field of an object is `nil` or owns memory.
- **Fields of a new object:** the checker knows the state of the fields of an object that just came out of `alloc`. In `alloc(Node)`, the pointer fields are `nil`. In `alloc(Node{...})`, they are what the literal says.
- **Fields of an object that came from outside** (a parameter, a function result): each field "may be owning or `nil`".
- **`free(p)`** is only accepted when no owning field of `*p` may still hold memory. The fields must be freed or moved first, so freeing an object never leaves memory dangling.
- **Whole object:** an object with a field moved out can only be used as a whole (moved, borrowed, returned, freed) after that field gets a new value.
- **Borrow:** a field of a borrowed object (`&T`) can be moved out, as long as it gets a value before the function ends. Whoever lent it gets the whole object back.

A struct with an owning field **contains ownership** and only exists behind a pointer. It can't be a local variable by value, a parameter or a result by value, or be copied (`*p`), compared or printed. Its literal only appears inside `alloc(...)`. Without this rule, copying the struct would duplicate the owners.

Example, freeing a whole list:

```
for cur != nil {
    next := cur.next   // moves the field out: cur.next becomes freed
    free(cur)          // accepted: the only owning field was already moved
    cur = next         // cur was freed, so it can receive another owner
}
```

And inserting at the front of a borrowed list:

```
fn push(l &List, v int) {
    n := alloc(Node)
    n.value = v
    n.next = l.head        // moves the head out of the borrow...
    l.head = n             // ...and gives a value back before leaving
}
```

## Borrows (`&T`)

To keep the analysis simple and correct, borrows are restricted:

- `&x` appears as a **call argument**, as the value of a variable (`x := &a.field`) or in a `return`. `&T` can't be stored in fields, slices or arrays.
- A received borrow can be passed on directly to another `&T` parameter, without `&`.
- In the same call, you can't borrow and move parts of the same object: `f(&p, p)` and `f(&p, p.next)` are errors.
- Whoever receives a borrow can't free it or reassign it.

### Returning and storing borrows

```
fn (v &Vec[T]) at(i int) &T {
    return &v.items[i]          // the result comes from the receiver
}

x := v.at(0)                    // x: &T, stored in a variable
x.field = 3
io.print(*v.at(1), v.len())     // read right away: the value is copied there
```

- **Where the returned borrow comes from:** the `&` receiver, in a method, or the only `&` parameter, in a function. The `return` is checked: `&v.items[i]` is fine; `&local` (it would vanish at the end of the function), a sub-slice (the view is created on the spot) or an ambiguous source (two `&` parameters) are errors.
- **Freezing:** while a borrowed variable is in use (from the declaration to its last use; in a loop, to the end of the loop), the source can't be mentioned except through it. You can't grow, free, move or pass the source again while a piece of it is borrowed. Two `&` parameters may point at the same object, so borrowing from one freezes both.
- **In the same statement:** a call that returns a borrow of `m` can't share the statement with another mention of `m`, unless the borrow is only read right away (`*m.get(&k)`, `m.at(0).field`).
- **Owners inside a stored borrow** can leave and must come back before the end of the variable's scope, as in a `&T` parameter.

With these rules, a borrow never outlives the owner or sees memory that moved, and so there are no dangling pointers.

## Growing collections

The language has no `append` and no collection that grows on its own. For that there's `std/vec`, written in MLC: `vec.new[T](cap)` allocates, `push` calls `grow` (which `realloc`s, doubling the capacity) when it runs out of room, and `free()` releases it. The allocation is visible in the library's code, and the `*Vec[T]` returned by `new` is an ordinary owner, enforced by the checker. See [library.md](library.md).

## Owners in generics

A generic with `[T owned]` can hold owners (`Vec[[]byte]`, `Vec[?*Node]`), including nested ones (`Vec[?*vec.Vec[int]]`, `Vec[[][]byte]`, trees), when each struct with owners has a `fn (x *X) free()` method that frees the fields and the object itself: the generic `drop` calls it, and the checker checks the `free` like any other function. Inside it, a `T` value is treated like a `?*X`: assigning moves, using after a move is an error, and every value needs a destination. `drop(x)` is the generic way to free: it becomes `free` when `T` is an owner and does nothing when it isn't. As in every generic, each concrete instance goes through the checker; on top of that, each generic with `owned` is checked with `T = []byte`, so errors show up in the definition. Collections like `Vec` free the elements in `free()`, written in MLC and visible in the library.

## C functions and handles

A C function (`extern "C" fn`) only receives what doesn't own MLC memory: numbers, `string`, borrows (`&[]T`, `&T`) and C handles. A handle (`?*X`, with `X` an `extern "C" type`) is an owner like the others: it comes from a C function and must be passed to a C function that takes it by ownership (`*X`); `free` doesn't apply to it. That way the checker also guarantees that files, connections and other C resources are closed. A handle returned through a pointer (`sqlite3_open(name, &db)`, with a `&?*X` parameter) can only go into a variable with no open handle, and it leaves the call as an ordinary `?*X`, which must be closed. A parameter C accepts as `NULL` (`?&T`, `?&?*X`) receives `nil` or a borrow, with the same rules. See [ffi.md](ffi.md).

## Functions as values

A `fn(T) R` value is just the address of a function: since it doesn't capture variables, it doesn't allocate and has no owner, and it can be copied freely. A call through it follows the parameter types (`&T` borrows, `*T` moves), and the checker checks it as a direct call. The zero value is nil; calling it panics, never undefined behavior.

## Enums with owners (errors with content)

An enum variant can hold owners (`BadDigit(at int, msg []byte)`). The whole enum becomes an owner by value: building it moves the owning arguments inside, and assigning, passing or returning moves the enum. It goes away in three ways, all explicit:

- **a consuming `match`** (over an owning variable or a temporary, like `match parse(s)`): each owning field gets an owning name in the arm, which must be freed or moved there; `_` on an owning field and a `_` arm that would cover a variant with owners are errors, because the memory would leak without showing up in the code.
- **`free(e)`**, which frees the current variant's data.
- **moving** the enum on (`return e`, a by-value parameter).

A `match` over `*e`, a field or a borrow **only reads**: the owning fields come as borrows (`&[]byte`), with the same rules as [Returning and storing borrows](#returning-and-storing-borrows). There's no `try`: propagating an error is a written `return`, and ownership travels with the value. See [types.md](types.md#enums-with-owners).

## Callbacks with a context

Function values don't capture variables. For a callback to keep state, the caller passes the context along (`m.each_with(&ctx, f)`, with `f fn(&C, &K, &V)`). The context is a borrow during the call: the function can change it but not keep it, and its memory belongs to the caller, with the usual rules. It's what a closure would do, with the state in plain sight. See [types.md](types.md#generic-methods-and-callbacks-with-a-context).

## Threads

A thread receives its context **by ownership** (`thread.spawn(f, ctx, stack)` moves `ctx`) and returns its result by ownership at `thread.join`. The thread function doesn't receive borrows and there are no mutable global variables, so two threads never reach the same memory. The `*thread.Thread[R]` handle is an owner like a C handle: it must go to `thread.join`, and the checker requires it. The thread's record is allocated at the `spawn` and freed at the `join`, and the stack has the size written in the code.

A channel (`chan.new[T](n)`) allocates its `n` slots at the start and never grows. Each end (`Sender`, `Receiver`) is an owner that needs `close()`. `send` moves the value into the channel and `recv` hands ownership to the receiver. The last `close` frees the channel and `drop`s what's left in it.

A mutex (`mutex.new(v)`) owns the value. The guard from `lock()` is an owner that needs `unlock()`, and the value is only reached through `g.get()`, a borrow from the guard that doesn't survive the `unlock`. The last `close` (of handles and guards) frees the value. See [concurrency.md](concurrency.md).

## What's forbidden

- Pointer arithmetic and conversion between pointer types.
- Pointers to pointers.
- Comparing pointers with each other (only with `nil`) and printing pointers.
- Taking an address to keep it: `&` only exists as a borrow.
- Uninitialized variables: every variable starts with its zero value, and `var p *T` requires an initial value.
- A zeroed `alloc(T)` when `T` has a `*T` field, which can't be `nil`. In that case, use `alloc(T{...})`.

## Run-time safety net

The runtime counts live allocations. At the end of `main`, if any are left, the program reports an **internal error** and exits with code 3. By the rules above this should never happen: the counter exists to catch bugs in the checker itself. Every test that runs programs goes through this check.

## C bugs MLC eliminates

| C bug | How MLC handles it | When |
|---|---|---|
| use-after-free | state of each owning path | compile time |
| double free | state of each owning path | compile time |
| memory leak | owner alive when leaving the scope; `free` requires freed fields | compile time |
| null pointer | `*T` is never null; `?*T` requires a check, tracked through the flow | compile time |
| dangling pointer to the stack | borrows can't be stored | compile time |
| stack overflow (a frame that skips the guard page) | stack estimated against `thread.spawn` and `main`; large frames checked on entry | compile time and run time (panic) |
| uninitialized variable | required zero value | compile time |
| signed integer overflow | `__builtin_*_overflow` | run time (panic) |
| division by zero | check before dividing | run time (panic) |
| undefined evaluation order | left to right, written in the generated C | compile time |
| format string (`printf`) | the `io.printf` format checked against the values; each one written by its `io.Display` | compile time |
| buffer overflow | bounds checking on slices and strings | run time (panic) |

## Stack

Arrays and structs with arrays live on the stack, and the stack is also memory with a known size: 8 MB on the main thread and the value written in `thread.spawn` on the others. In C, a function whose frame is larger than what's left of the stack doesn't fail on entry: the frame skips the guard page, and the writes land in something else's memory (another thread's stack, the heap). MLC closes that door in two layers:

- **At compile time**, the checker estimates each function's stack: the frame (parameters, local variables and the large values C keeps in temporaries) plus the largest stack among the functions it calls directly. A `thread.spawn` with a constant size, `main` and the tests are checked against the stack they'll have, minus a reserve (1/8 of the stack, between 4 KB and 32 KB) for what C uses besides the variables and for the panic:

  ```text
  the thread of work needs about 489 KB of stack, and thread.spawn asks for 64 KB
  (the heaviest is the variable body [500000]byte (489 KB) in fill): put the buffer
  on the heap with alloc([]byte, 500000), or ask for a larger stack (thread.STACK_1MB)
  ```

- **At run time**, every function with an estimated frame of 2 KB or more gets an entry that checks, before the frame exists, whether it fits in what's left of the thread's stack. If it doesn't, it panics pointing at the function. This covers what compile time can't reach: recursion (the depth depends on the data), calls through `fn` values and a computed stack size. Functions with small frames pay nothing: an overflow from them always lands on the guard page.

The estimate is the sum of the variables, as if they all lived at the same time, so it tends to overshoot. The run-time check uses the same estimate and the thread's real stack (`pthread_get_stackaddr_np` on macOS, `pthread_getattr_np` on Linux). A thread created by C code (a callback) has no check.

## What MLC doesn't guarantee

- **Logical leaks:** keeping memory longer than needed is the programmer's decision, and still possible.
- **Out of memory:** `alloc` ends the program; it doesn't recover.
- **Deadlocks:** the thread model rules out data races (see [concurrency.md](concurrency.md)), but two threads waiting for each other still hang the program.
- **External C code:** the standard library's `extern fn` are implemented in the C runtime and don't go through the checker; their code is reviewed by hand. Ordinary programs can't declare `extern fn`.
- **Moves in a call** take effect when the call happens, after all arguments: `f(s, len(s))` is accepted, but moving twice (`f(s, s)`) or moving and borrowing (`f(s, &s)`) in the same call is an error.
- **Shared structures:** with a single owner per object, graphs with cycles or nodes with two parents (a doubly linked list) can't be expressed with owning pointers. That would need another mechanism, like indices into a slice or explicit reference counting.
