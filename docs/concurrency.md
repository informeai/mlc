# Concurrency

MLC uses **operating system threads, created explicitly**, and ownership guarantees at compile time that two threads never touch the same memory. There's no custom scheduler, no green threads and no `async`: those allocate behind your back (stacks that grow on their own, state machines on the heap), which goes against the language's central rule.

The model came in phases:

| Phase | Content | Status |
|---|---|---|
| A | `thread.spawn` and `thread.join`, with data crossing by move | done |
| B | fixed-capacity channels (`std/chan`) | done |
| C | `Mutex[T]` with a guard, and atomics | done |
| D | scoped threads that receive borrows | off the roadmap (see [What was left out](#what-was-left-out)) |

## Threads (`import "std/thread"`)

```
import "std/io"
import "std/thread"

fn sum(part []int) int {
    t := 0
    for x in part {
        t += x
    }
    free(part)
    return t
}

fn main() {
    a := alloc([]int, 1000)
    // ... fill a ...
    t := thread.spawn(sum, a, thread.STACK_64KB)   // a is MOVED into the thread
    r := thread.join(t)                            // the result comes back by ownership
    io.print(r)
}
```

| Function | What it does |
|---|---|
| `thread.spawn(f, ctx, stack) *thread.Thread[R]` | creates a thread that runs `f(ctx)`. `f` is `fn(C) R` or `fn(C)`; with `fn(C)`, the handle is `*thread.Thread` |
| `thread.join(t) R` | waits for the thread, returns the result and frees the thread's record |
| `thread.cpu_count() int` | how many processors the system has |
| `thread.STACK_16KB` … `thread.STACK_8MB` | stack sizes |

### The rules

- **The context is moved.** After the `spawn`, the caller no longer has `ctx`. To carry several things, put them together in a struct (`alloc(Job{...})`), and the thread function owns it.
- **The thread function doesn't receive borrows** (`fn(&T)` is refused) and isn't a closure. Since MLC has no mutable global variables, there's no other door for shared state.
- **Every thread has an owner.** The handle must go to `thread.join`, and forgetting is a leak detected at compile time, like a forgotten `free`. It doesn't accept `free` or `alloc`, and doesn't go into a generic `Vec` (`drop` wouldn't know how to wait for the thread). For several threads, use a `[]?*thread.Thread[R]` slice with `replace` and `drain`.
- **The result comes back by ownership.** If `R` is an owner (`[]byte`, `*T`), whoever calls `join` becomes its owner and must free it.
- **The stack is chosen by whoever creates the thread**, in bytes (at least 16 KB). It's memory, and the programmer should know how much is used.
- **The stack is checked.** At compile time, the checker estimates the thread function's stack (the local variables, the parameters and the large values of each function, plus the largest stack among the functions it calls) and refuses a `spawn` that doesn't fit in the requested size, saying which variable weighs the most and which constant to use. Recursion, calls through `fn` values and computed sizes are left for the run-time check: before entering a function with a large frame, the generated C checks that it fits in what's left of the stack and, if it doesn't, panics. A large buffer never skips the guard page to write into another thread's memory. See [memory.md](memory.md#stack).
- **A panic in any thread ends the whole program**, with the same message and exit code as an ordinary panic.

### Several threads

```
ts := alloc([]?*thread.Thread[int], 4)
for p := 0; p < 4; p++ {
    job := alloc(Job{data: part(p), from: 0, to: 250})
    old := replace(&ts[p], thread.spawn(partial_sum, job, thread.STACK_64KB))
    if old != nil {             // the slot was empty; the checker asks for the handling
        thread.join(old)
    }
}
total := 0
for t in drain(ts) {
    if t != nil {
        total += thread.join(t)
    }
}
free(ts)
```

### Thread groups (`import "std/sync"`)

When the threads don't return a result (the result goes through a channel, a mutex or an atomic), a `sync.Group` waits for all of them at once:

```
g := sync.group(8)                      // room for 8 threads (allocates)
for i := 0; i < 8; i++ {
    g.add(thread.spawn(worker, ctx(i), thread.STACK_64KB))   // the group keeps the handle
}
g.wait()                                // waits for all of them and frees the group
```

The capacity is the limit of live threads in the group: when it's full, `add` collects the ones that already finished and, if none did, waits for the oldest one. In a server, `sync.group(64)` means "at most 64 connections at the same time", and the `accept` loop waits for a slot (see `tests/run/http_service.mlc`). `g.reap()` collects the ones that finished without waiting for the others.

The checker requires the `wait`, as it requires each thread's `join`, and `add` only accepts `*thread.Thread` (no result): an owned result can't disappear without anyone receiving it.

## Channels (`import "std/chan"`)

A channel passes values from one thread to another **by ownership**. It's used by several threads at the same time, but in MLC each thing has only one owner. That's why the channel has **ends**, and each end is an owner:

```
rx := chan.new[[]byte](16)    // allocates the channel (16 slots) and returns the receiving end
tx := rx.sender()             // a sending end, connected to the same channel
tx2 := tx.clone()             // one more producer
```

```
fn produce(tx *chan.Sender[[]byte]) {
    for i := 0; i < 3; i++ {
        b := alloc([]byte, str.format_len("request {}", i))
        str.format(&b, "request {}", i)
        if !tx.send(b) {          // b was moved; false: nobody receives anymore
            break
        }
    }
    tx.close()                    // required: without it, the consumer would wait forever
}

fn main() {
    rx := chan.new[[]byte](4)
    t := thread.spawn(produce, rx.sender(), thread.STACK_64KB)
    for {
        match rx.recv() {
            Got(b) => {
                io.write_line(&b)
                free(b)                // the message is now on this side
            }
            Closed => break
        }
    }
    rx.close()
    thread.join(t)
}
```

| Operation | What it does | Memory | Blocks |
|---|---|---|---|
| `chan.new[T](n) *chan.Receiver[T]` | creates the channel, with `n >= 1` slots | allocates the `n` slots and the header | no |
| `rx.sender() *chan.Sender[T]` | a sending end | nothing (it only counts the end) | no |
| `tx.clone()`, `rx.clone()` | another end on the same side (several producers or consumers) | nothing | no |
| `tx.send(v) bool` | moves `v` into the channel | `v` passes to the channel | if it's full |
| `tx.try_send(v) chan.Try[T]` | moves `v` into the channel if there's room: `Sent`, or `Full(v)` and `Closed(v)` with `v` back | `v` passes to the channel, or comes back in the result | never |
| `rx.recv() chan.Recv[T]` | `Got(v)`, with ownership of `v`, or `Closed` | `v` passes to the receiver | if it's empty and a `Sender` is open |
| `x.close()` | closes the end (consumes it) | the last `close` frees the channel and `drop`s what's left in it | no |

- **Every end needs `close()`**, and the checker requires it like a `free`. It doesn't accept `free` or `alloc`. In a struct with ends, the struct's `free` closes the ends (the nested-owners rule). In a `Vec`, each end's `drop` closes it.
- **`recv` returns an enum with owned data**, `chan.Recv[T] { Got(value T); Closed }`. The `match` consumes the result, and the checker forces you to handle the received message.
- **`Closed`** arrives when every `Sender` was closed and the channel is empty, including when no `Sender` was created yet. Create the sending ends before you start receiving.
- **`send` with no open `Receiver`** frees the value (`drop`) and returns `false`, so the producer stops. Nothing leaks.
- **There's no unbounded channel.** A full channel makes `send` wait, which gives flow control. A channel that grew on its own would be hidden allocation.
- **`try_send` never waits**, for code that must not block: a C callback, which runs in a task of the C library (see [ffi.md](ffi.md#channels-in-callbacks-try_send)). When the value comes back (`Full(v)`, `Closed(v)`), it's the caller's again, and an owner must be freed.

### Several workers

`spawn` takes one context. To carry several ends, they go together in a struct:

```
struct Worker {
    jobs *chan.Receiver[int]
    results *chan.Sender[int]
}

fn (w *Worker) free() {
    w.jobs.close()
    w.results.close()
    free(w)
}

fn work(w *Worker) {
    for {
        match w.jobs.recv() {
            Got(n) => w.results.send(n * n)
            Closed => break
        }
    }
    w.free()
}
```

```
w := alloc(Worker{jobs: jobs.clone(), results: results.sender()})
t := thread.spawn(work, w, thread.STACK_64KB)
```

**Watch out for deadlocks.** If `main` sends every job before reading the results, and the results channel fills up, the workers stop at `send` and `main` stops at sending the jobs. Size the channels for the usage pattern, or consume the results in another thread. The checker doesn't detect this.

## Mutex (`import "std/mutex"`)

A mutex **owns the value** it protects: the value can only be reached with the lock.

```
struct Account {
    balance int
}

fn deposit(m *mutex.Mutex[*Account]) {
    g := m.lock()                // waits for the lock; g is a *mutex.Guard[*Account]
    a := g.get()                 // &Account, borrowed from the guard
    a.balance += 10
    g.unlock()                   // required; using a after this is an error
    m.close()
}

fn main() {
    m := mutex.new(alloc(Account{balance: 0}))   // the account goes inside the mutex
    t := thread.spawn(deposit, m.clone(), thread.STACK_64KB)
    thread.join(t)
    m.close()                                     // the last close frees the account
}
```

| Operation | What it does |
|---|---|
| `mutex.new(v) *mutex.Mutex[T]` | allocates the mutex with `v` inside (`v` is moved); `mutex.new[T](v)` picks the type |
| `m.lock() *mutex.Guard[T]` | waits for the lock and returns the guard |
| `m.clone() *mutex.Mutex[T]` | another handle to the same mutex, to take to another thread |
| `m.close()` | closes the handle; the last one out (handle or guard) frees the value |
| `g.get() &T` | borrows the value |
| `g.unlock()` | unlocks (consumes the guard) |
| `g.wait()` | releases the lock, waits for a `notify` and locks again |
| `notify()`, `notify_all()` | wake whoever is waiting in `wait` (on the handle or the guard) |

- **The lock is a resource.** The guard is an owner: forgetting the `unlock` is a compile error, like a leak.
- **The value only exists inside the lock.** `get()` borrows from the guard, and the guard stays frozen while the borrow is used. That's why using the value after the `unlock` is a compile error. The borrow lasts from the `get` to its last use in the text, so unlock after the last use, including on the paths with `break`.
- **The guard belongs to the thread that locked.** Unlocking in another thread is undefined behavior in pthread, so the guard doesn't go into a `spawn` context, channels, fields, slices or collections.
- **The guard keeps the mutex alive.** Closing the last handle with an open guard is safe: the value is only freed at the `unlock`.
- **Locking twice in the same thread** without unlocking panics with a clear message, instead of locking the program forever.
- **Several handles:** each `clone()` needs a `close()`. In a `Vec`, each handle's `drop` closes it.

### Waiting for a condition

```
fn ready(q &Queue) bool {
    return q.count > 0 || q.done
}

g := m.lock()
for !ready(g.get()) {     // the get() borrow only lasts for the call
    g.wait()
}
```

Whoever changes the condition calls `g.notify()` (or `notify_all()`) before the `unlock`.

## Atomics (`import "std/atomic"`)

For simple counters and flags, without a mutex:

```
n := atomic.new_int(0)
t := thread.spawn(count, n.clone(), thread.STACK_64KB)   // another handle, for the thread
n.add(1)
thread.join(t)
io.print(n.load())
n.close()
```

| Type | Methods |
|---|---|
| `atomic.Int` (from `atomic.new_int(v)`) | `load`, `store`, `add` (returns the new value; overflow panics), `swap` (returns the old one), `compare_swap(expected, new) bool`, `clone`, `close` |
| `atomic.Bool` (from `atomic.new_bool(v)`) | `load`, `store`, `swap`, `compare_swap`, `clone`, `close` |

Every operation is sequentially consistent. Each handle needs `close()`, and the last one frees the value. The module is ordinary MLC, in [`std/atomic.mlc`](../std/atomic.mlc), on top of runtime functions.

## What the model guarantees

- **No data races:** data only reaches another thread by move (the context, the result, a channel's values) or inside a mutex, with the lock. Atomics are the other way to share.
- **No forgotten thread, channel, mutex or lock:** every `spawn` has its `join`, every channel end and every mutex or atomic handle has its `close`, and every guard has its `unlock`.
- **No use of the protected value outside the lock:** the `get()` borrow doesn't survive the `unlock`.
- **All memory in plain sight:** the thread's record (the context and the result) is allocated at the `spawn` and freed at the `join`, and the stack size is in the code.

## What it doesn't guarantee

- **Deadlock.** Two threads waiting for each other (two full channels, a `recv` with its own `Sender` open in the same thread, two mutexes locked in different orders) hang the program, and the checker doesn't prevent it. The only exception is locking the same mutex twice in the same thread, which panics.
- **What goes through the FFI.** A C library that creates threads or shares memory on its own is outside the guarantee.

## What was left out

**Scoped threads that receive borrows**, like several threads processing disjoint parts of the same buffer in place (`&s[0:n/2]` for one, `&s[n/2:]` for another). Today the same work is done in three ways:

- with the data already split from the start (one slice per worker, moved into the thread and returned at the `join`), which is the idiomatic way and costs nothing extra;
- copying the parts and joining them afterward, with the copy visible in the code;
- with everything inside a `Mutex`, safely but without real parallelism.

Getting the "in place, without copying" case would require proving at compile time that the parts don't overlap and that no borrow outlives the threads. That's a lot of complexity in the checker for a niche case (heavy numeric processing over a single buffer). The current model fits in one sentence: **data crosses by move, by channel or inside a mutex**.

If a real program needs more, the first step would be a **read-only borrow** (today every `&T` allows changes), which would let several threads read the same data and would serve the whole language, not just threads.

## How it works

`thread.spawn` and `thread.join` are rewritten by the compiler, like `io.print`. For each kind of thread function, the generated C gets a record (the runtime's `mlc_thread`, the result, the function and the context) and an entry function that calls `f(ctx)` and stores the result. `spawn` allocates the record with `mlc_alloc` and creates the thread with `pthread_create`, with the requested stack size. `join` calls `pthread_join`, takes the result out and frees the record.

A channel is a ring buffer in the runtime, with a lock and two condition variables (pthread). Each end is a pointer to it, and the runtime counts the ends. `send` copies the value's bytes into the ring and `recv` copies them out: ownership passes from one side to the other with no hidden copy on the heap. `chan.new` hands the runtime the function that frees a value of the type, used at the last `close` and at a `send` with no receiver.

A mutex is an error-checking `pthread_mutex_t`, plus a condition variable and the value, right after the header. Handles and guards are counted with an atomic. `get()` returns a pointer to the value inside the mutex, and the checker turns it into a borrow from the guard.

The runtime is thread-safe: the allocation counter is atomic, and the `str.format` destinations are per thread.
