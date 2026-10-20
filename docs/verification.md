# Testing the checker

A plan to attack the ownership and borrow checker (`internal/checker/ownership.go`, `borrows.go` and what they use) on purpose, before calling the language beta. Each phase says what to deliver and when it's done. **All five phases are done** (`make sanitize`, `make mutate`, `make fuzz`, `make enum`, and in phase 5 `make robust`, `make cover` and the metamorphic and hard-case tests).

## What to look for

The checker can be wrong in two ways:

| Error | What happens | Severity |
|---|---|---|
| **Accepting an unsafe program** (false negative) | a leak, a use after `free`, a double `free`, a dangling borrow or a data race gets through compilation | serious: it breaks the language's promise |
| **Refusing a safe program** (false positive) | the programmer has to rewrite correct code | annoying: it hurts ergonomics |

The priority is the first one. The second is recorded to improve ergonomics, without blindly loosening the checker: `CLAUDE.md` asks for it to stay conservative.

## The oracle: the rule every phase uses

The checker decides at compile time. To know whether it got it right, the accepted program is **run** under tools that detect the error for real:

- **AddressSanitizer:** use after `free`, double `free`, out-of-bounds access;
- **the runtime's allocation counter** (`mlc_check_allocs`): memory left over at the end becomes an "internal error";
- **UndefinedBehaviorSanitizer:** overflow, alignment and the rest of C's undefined behavior;
- **ThreadSanitizer**, for programs with threads, channels or mutexes;
- **Valgrind**, on Linux, as a second opinion.

> **Every program the checker accepts must run clean under these tools.** If it doesn't, we found a hole in the checker (or in the C generator, which is also a bug).

The sanitizers require compiling the generated C with their own flags (`-fsanitize=address,undefined` or `-fsanitize=thread`, `-g`). That could already be done with `-cflags` and `-ldflags`:

```sh
mlc test -cflags "-fsanitize=address,undefined -g" -ldflags "-fsanitize=address,undefined" std
```

Phase 1 added the `-sanitize address|thread` shortcut on top of the same mechanism (`mlc test -sanitize address std`), and `make sanitize` runs the whole suite that way.

Every problem found in any phase becomes a permanent test: in `tests/errors` (if the program should be refused) or in `tests/run` (if it should be accepted and the generated C was wrong), with the smallest example that reproduces the case.

## Phase 1: the oracle over the current suite

**What:** run the whole execution suite (`tests/run`, `tests/panic`, the `std` tests and `tests/ffi`) under the sanitizers.

**Deliverables:**
- a sanitizer shortcut (`-sanitize address|thread` on `mlc build`, `mlc run` and `mlc test`), which builds the right `-cflags` and `-ldflags`;
- `make sanitize`: runs `go test` with the execution tests under AddressSanitizer + UndefinedBehaviorSanitizer, and the ones that use threads under ThreadSanitizer too;
- CI (when it exists) runs `make sanitize` besides `make check`.

**Done when:** the whole suite passes under the sanitizers, and `make sanitize` is documented in `CLAUDE.md` as part of "before considering a milestone done".

**Effort:** small. Part of the work was already done by hand in the concurrency tests.

**Done.** `-sanitize address` turns on `-fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer -g` (UBSan ends the program instead of just warning); `-sanitize thread` turns on `-fsanitize=thread -g`. In the driver tests, `MLC_SANITIZE=address` (or `thread`) compiles every program that way, and `make sanitize` runs `TestRun`, `TestPanic`, `TestStdLibrary` and `TestFFIOutHandle` under both. The whole suite passes under both. The only problem found was in a test itself: `tests/panic/pilha_recursao.mlc` had a buffer clang optimized down to almost nothing, and the recursion went past 70 thousand levels, more than ThreadSanitizer's shadow stack holds; the test now uses the whole buffer.

## Phase 2: mutating correct programs

**What:** take programs the checker accepts and apply mutations that **are known** to introduce an ownership bug. The checker must refuse the mutant.

| Mutation | Bug it creates |
|---|---|
| delete a `free` (or `close`, `unlock`, `thread.join`) | a leak, a held lock or a forgotten thread |
| duplicate a `free` | double `free` |
| use the variable after passing it to a `*T` parameter or moving it | use after move |
| move a `free` inside an `if` | a leak on one of the paths |
| change `&x` to `x` in an argument | a move where there was a borrow |
| use a stored borrow (`x := v.at(0)`) after freeing or growing the source | a dangling borrow |
| remove the `if h != nil` before closing a `?*X` | use of `nil` |
| send a value through a channel and use it afterward | use after move |

**How to decide:**
- the checker refuses → correct;
- the checker accepts → run the mutant under the oracle:
  - it errors → **a hole in the checker**: it becomes a test in `tests/errors`;
  - it doesn't error → the mutation didn't actually create a bug at that point (for example, the `free` was on a path that never runs); it's discarded.

**Deliverables:**
- a mutator in Go (`internal/fuzz/mutate.go`) that works on the AST (not on the text) and writes each mutant with the applied mutation in a comment;
- `make mutate`: applies the mutations to `tests/run`, the `std` tests and `tests/ffi`, and writes a report with how many mutants were refused, how many were accepted without a bug and how many were holes.

**Done when:** the report shows no holes, and every hole found along the way became a test.

**Effort:** small to medium.

**Done.** `internal/fuzz/mutate.go` finds, in the AST and with the checker's result, the releases (`free`, `drop`, `thread.join`, `close`, `unlock` and every method with a `*T` receiver), the moves (an owning argument to a `*T` or `[]T` parameter, `tx.send(x)`) and the borrows of owners (`&x`), and generates the mutants:

| Mutation | What it does |
|---|---|
| deletes the release | removes the line |
| duplicates the release | repeats the line |
| releases before the use | swaps the release with the line above, when it uses the same path |
| uses after releasing | repeats, after the release, the last use of the path in the block |
| releases on one path only | puts the release inside an `if` the checker can't decide |
| moves twice | repeats the call that moves |
| uses after moving | puts the next use of the variable right after the move |
| changes &x to x | the borrow becomes a move |

`internal/fuzz/run.go` decides each mutant: the checker refuses (correct), or the accepted mutant is compiled with `-sanitize address` and run (with the test's `.args` and `.in`, and each test of a `_test.mlc`) with a 20 s timeout. AddressSanitizer, UBSan, "erro interno" (memory left over), another signal or hanging is a **hole**; a panic and a failing test are safe behavior. `make mutate` (about 15 seconds) mutates `tests/run`, `tests/panic`, `tests/ffi` and the `std` tests, writes the report to `MUTATE_OUT/report.txt` (default `/tmp/mlc-mutate`) and keeps every accepted mutant and every hole there, to reproduce them. `TestMutantsSmall` runs on every `go test` and checks that the mutants of a small program are all refused.

**Result:** 116 programs, 1,944 mutants, no holes, and a single accepted one, which is correct (a `free` of a `?*T` that's always `nil`, put inside an `if`).

**A hole found along the way:** `&e` of an enum with owners didn't check whether `e` had already been freed or moved (`free(e)` followed by `describe(&e)` compiled). The oracle didn't catch it because `describe` only read the slice's length, which lives in the enum itself, and not the freed memory; it showed up in the list of accepted mutants. Fixed in `borrowArg` and kept in `tests/errors/enum_emprestado_depois_de_free.mlc` and `enum_emprestado_depois_de_mover.mlc`. The lesson: **the oracle only catches the bug that touches memory**, so the "accepted without a bug" mutants of mutations that always create a bug (the use-after-free and use-after-move ones) must be read, not discarded.

**Left for later:** only each program's main file is mutated (the `std` modules aren't, because they're built into the compiler); the mutations that remove the `if h != nil` and those over multi-line statements are missing.

## Phase 3: an ownership-focused program generator

**What:** generate small random programs, restricted to what matters for the checker:

- `alloc`, `free`, assignment (move), `&` borrows, `take` and `replace`;
- `if`, `for` with `break` and `continue`, `match` over enums with owners;
- functions that take `*T`, `&T` and `T` and that return owners and borrows;
- struct fields, slices of owners, `?*T`, generics with `owned` and `ref`;
- in a second stage: `thread.spawn`, channels and mutexes.

**For each program:**
- **accepted** → run under the oracle; any error is a hole;
- **refused** → no automatic conclusion. A sample goes to manual review, to find the most annoying false positives.

**Deliverables:**
- `internal/fuzz/gen.go`, with a seed to reproduce each program;
- `make fuzz` (with `FUZZTIME=10m`, for example), which keeps each problematic program, already reduced to the smallest that reproduces the case;
- an automatic reducer (removing statements while the problem persists) for the findings.

**Done when:** a long run (for example, an hour) finds no new holes.

**Effort:** medium.

**Done**, with a change of direction that made the oracle much stronger: the generator (`internal/fuzz/gen.go`) tracks the state of each variable while it writes (live, freed, `nil`, the fields of each struct), and so **it knows, by construction, whether the program has a bug**. Half of the programs are valid; the other half get a single bug, only in code that runs (the conditions are known, `one == 1` or `one == 2`, and loops run at least once), always in a way the execution sees:

| Injected bug | How the execution sees it |
|---|---|
| use after `free`, a `byte` read after `free`, an enum read after `free` | AddressSanitizer (reads the freed memory) |
| double `free` | AddressSanitizer |
| a borrow used after freeing the source | AddressSanitizer |
| use after moving and freeing | AddressSanitizer |
| a leak, a struct freed with a live field | the runtime's allocation counter |

The programs use `alloc` and `free`, moves (to variables, fields, `?*T` and `*T` parameters), borrows stored in variables (`r := &x`), functions that consume and return owners, `realloc`, `?*T` with `if x != nil`, a struct with a `?*T` and a `[]byte` (moving into and out of the fields, freeing field by field), an enum with a `[]byte` (a consuming `match`, `free`, a borrow for a function that reads the data), and nested `if`/`else` and `for` with `break`. The outer variables end both sides of an `if` (and each loop iteration) in the same state.

**Decision for each program:**
- **with a bug, refused:** correct;
- **with a bug, accepted:** a hole. It runs under the oracle to confirm, and the program is **reduced** (`internal/fuzz/reduce.go`: removes statements, sequences of up to 4 neighboring statements and blocks, while the checker accepts and the oracle complains) and kept in `FUZZ_OUT` (default `/tmp/mlc-fuzz`);
- **valid, refused:** a false-positive candidate; the report groups them by message, with an example seed;
- **valid, accepted:** 1 in 10 runs under the oracle, looking for bugs in the C generator and the runtime.

`make fuzz` runs for `FUZZTIME` (default `1m`) with one thread per core; `MLC_FUZZ_SEED=n MLC_FUZZ_COUNT=1` reproduces a seed, and `MLC_FUZZ_DUMP=n` (with `MLC_FUZZ_BUG=1` for the version with a bug) writes the program. `TestGenerate` runs on every `go test`: 300 seeds, every program with a bug must be refused, the valid ones can't have an error that isn't about ownership, and at least 97% of them must be accepted.

**The fuzzer finds holes:** with phase 2's enum fix undone on purpose, a 30-second run found the hole, confirmed by AddressSanitizer, and reduced it from 23 statements to the 4 that matter:

```mlc
s1 := alloc([]byte, 3)
e2 := Bag.Full(s1)
free(e2)
acc += bag_peek(&e2)
```

**Result** (a 1-minute run, 8 cores): 4,242 programs, no holes; all 2,121 with a bug were refused, and 2,117 of the 2,121 valid ones were accepted. The 4 refused are the same known false positive: a borrow lasts until its last use in the **text**, so using the source on one side of an `if` is refused when the borrow is still used on the other side, even though it's safe on each path. A 5-minute run (20,382 programs) gave the same picture: no holes, and 15 false positives of the same kind.

**Left for later:** `take` and `replace` on slices of owners, generics with `owned` and `ref`, generated functions (today the helper functions are fixed), `continue`, `match` with more variants, and the second stage (`thread.spawn`, channels and mutexes).

## Phase 4: exhaustive enumeration with a reference model

**What:** random fuzzing may never draw the right case. For **small** programs, you can generate **all** of them: for example, two or three variables and up to six statements among `alloc`, `free`, move, use, borrow, `if` and loop.

Instead of running each program, a **reference model** decides whether it's safe. It's an abstract interpreter, in Go, much smaller than the checker, which follows every path and tracks the state of each variable (no memory, owning, freed, moved, borrowed).

**Comparison:**
- the checker accepts and the model says unsafe → **a hole**;
- the checker refuses and the model says safe → **excess conservatism**, already with the minimal example, to guide ergonomic improvements (like the borrow that lasts until its last use in the text).

**Deliverables:**
- `internal/fuzz/model.go` (the model) and `internal/fuzz/enum.go` (the enumeration);
- a Go test that runs the enumeration up to a small size on every `go test` and up to a larger size with a flag;
- the list of false positives found, sorted by frequency, as input for improvements to the checker.

**Done when:** the enumeration up to the agreed size shows no holes, and the false positives are cataloged.

**Effort:** medium to large. It's the technique with the strongest guarantee, even if only for small programs, which is where logic errors tend to show up.

**Done.** The alphabet (`internal/fuzz/enum.go`) has 15 statements over the parameters `x` and `y` (`*Box`, start live) and `q` (`?*Box`, which may arrive nil or live): `free` of each one, `x = alloc(...)`, `q = alloc(...)`, using `x` and `y`, `if q != nil { use q }`, the moves `y = x`, `x = y` and `q = x`, `r := &x`, `r := &y`, using `r`, and `break`; plus `if flip()` with and without `else` and `for flip()`, with conditions the checker doesn't know. The size counts each statement and each block. The enumeration doesn't generate what wouldn't even type-check (`r` used without a declaration, declared twice in the same scope, or never used) and visits only once the programs that differ only by swapping `x` and `y`. At the end of each program, a variable that ends live on every path gets a `free` (without it, almost every small program would just leak).

The model (`internal/fuzz/model.go`, about 250 lines) is an abstract interpreter with the intended semantics: each path's state is that of the three variables (live, freed, nil) plus that of each borrow (of whom, and whether the source was touched after it), using `r` after touching the source is a violation, and at the end nothing can be live. Since the state is small, identical paths merge and the loop is computed to a fixed point (any number of iterations). The model judges each path on its own, without being conservative.

**Result:**

| Size | Programs | Safe and accepted | Unsafe and refused | Holes | False positives |
|---|---|---|---|---|---|
| 1 | 8 | 4 | 4 | 0 | 0 |
| 2 | 100 | 26 | 74 | 0 | 0 |
| 3 | 1,573 | 216 | 1,357 | 0 | 0 |
| 4 | 28,912 | 2,108 | 26,803 | 0 | 1 |
| 5 | 586,940 | 23,880 | 562,981 | 0 | 79 |
| 6 | 12,710,438 | 311,198 | 12,395,868 | 0 | 3,452 |

**No holes** up to size 6. Up to size 5 it takes about 30 seconds (`make enum`); size 6, about 20 minutes (`make enum ENUM=6`). `TestEnumerateSmall` runs sizes up to 3 on every `go test` (0.1 s).

**The false positives are all of the same kind:** the borrow lasts until its last use **in the text**, not on each path. The two minimal examples:

```mlc
r := &x
if flip() {
    acc += x.v      // refused: r is still used... on the other side of the if
} else {
    acc += r.v
}
```

```mlc
r := &x
free(x)             // refused: r is still used... after a break
for flip() {
    break
    acc += r.v
}
```

Both are safe on every path. Fixing it would require computing the borrow's lifetime per path (the last use on each path, like Rust's NLL), instead of the last line in the text. It's recorded as the ergonomic improvement with the most evidence.

**The test finds holes:** in a copy of the compiler with the `&x` check removed on purpose (borrowing an already freed variable), size 4 showed two holes. At size 3 (`free(x); r := &x; acc += r.v`), another rule of the checker still caught the error; the hole only showed up with the borrow inside an `if`, exactly the kind of combination phase 3's random draw may never produce:

```mlc
free(x)
if flip() {
    r := &x
    acc += r.v
}
```

**The model is wrong sometimes too, and the comparison shows it:** the first run of size 6 reported 8 holes, all the same program in different forms. The error was the model's: it had a single `r`, and an `r := &y` inside a block erased the outer `r := &x`, which applies again after the block. The checker was right. The model now has one borrow per declaration, tied to its uses by scope (`TestModelShadowedRef`), and the next run showed no holes.

**Left for later:** struct fields, enums with owners, `take`/`replace` and function calls in the alphabet (each one multiplies the size of the enumeration; the model must grow along with it).

## Phase 5: continuous

- **Metamorphic tests:** transformations that don't change safety can't change the verdict (renaming variables, wrapping in a block, swapping independent statements, extracting into a function, replacing an `if` with an equivalent `match`). A different verdict shows a case handled by the shape of the code, not its meaning.
- **Coverage:** `go test -coverprofile` over `ownership.go`, `borrows.go`, `owned.go` and `threads.go`; every branch no test reaches gets a targeted test.
- **Known hard cases:** the history of Rust's borrow checker has a list of hard cases (borrows in loops, aliasing through fields, moves inside `match`, generics instantiated with owners, borrows returned along different paths). Each one becomes a test.
- **Robustness:** `go test -fuzz` over the parser and the checker with random text, to make sure the compiler never panics (like the `match *(&e)` one, found in the errors-with-content item).

**Done**, on all four fronts:

**Robustness** (`internal/driver/robust_test.go`, `make robust`): `go test -fuzz` over the compiler (parser, checker and C generator, as a program and as a test file) and over the formatter, with the suite and the `std` as seeds. In a few minutes, it found:

| Input | What happened | Fix |
|---|---|---|
| `struct A { a A }` with `alloc(A)` | stack overflow in the checker (the recursive-struct error was reported, but the type stayed cyclic) | the field that closes the cycle loses its type; cycles through `[N]T` are found too; one error per cycle |
| `struct T { t T; d T }` with `[2]T` | the same, through the second field | every field of the cycle loses its type |
| `interface A { fn a() int }` with an `impl` | index out of range (a method without `self`) | only well-formed methods enter the interface |
| `replace(!s[0], v)` | a nil pointer (the operator wasn't checked) | only `&s[i]` is accepted |
| `struct A{ ;;}` | formatting twice gave different texts | a `;;` before `}` goes away entirely |
| `for 0;;{}` | the previous fix broke the `for` | the `;` only goes away when nothing follows it on the line |
| `(& &x)` | the formatter glued `& &` into `&&` (another operator) | a space between two unary `&` or two unary `-` |
| `match 0 {;` and a line break | the indentation changed on the second formatting | the `;` doesn't count when deciding whether the block opens lines |

Each input stays in `internal/driver/testdata/fuzz` and runs on every `go test`; the checker cases also became tests in `tests/errors`.

**Coverage** (`make cover`): the checker's coverage by the suite, mutation, fuzzing and enumeration (89%), with an HTML page and the list of checker errors no test triggers. There were 76; the ones about safety each got a test (`tests/errors/cobertura_*.mlc`: aliasing in the same call, `nil` going to `*T`, `free` of a temporary with owners, a borrow returned from a temporary, a struct with owners in a variant, a thread that returns a borrow, `free` of a mutex, a zeroed `alloc` with a `*T` field, a pointer to a pointer, a borrow of a borrow, `drain` without owners and others). 66 were left, almost all usage errors (number of arguments, repeated name, visibility); some seem unreachable, because a more general rule catches the case first.

**Metamorphic tests** (`internal/fuzz/metamorphic.go`, `TestMetamorphic`): each program of the enumeration gets versions that don't change safety (the body inside a block, `if`/`else` with the condition negated and the sides swapped, an empty `else`, two independent statements swapped, a neutral statement inserted), and the checker must give the same verdict. The reference model confirms that the transformation really didn't change safety. Up to size 4 (30 thousand programs, 340 thousand versions): no holes, and only two divergences, both the same one, which shows the borrow false positive as clearly as possible:

```mlc
r := &x
if flip() {
    acc += x.v       // refused
} else {
    acc += r.v
}
```

```mlc
r := &x
if !flip() {
    acc += r.v
} else {
    acc += x.v       // accepted: the same program, with the sides swapped
}
```

The verdict depends on which side comes first in the text. `TestMetamorphic` runs up to size 3 on every `go test` and fails on any divergence in a program without a borrow (`MLC_META=4` goes further).

**Hard cases** (`tests/run/casos_dificeis_*.mlc` and `tests/errors/caso_dificil_*.mlc`): the patterns that tend to break ownership checkers, each with the expected verdict. A borrow used in every iteration of a loop, and the invalid one (source rebuilt in one iteration, borrow used in the next); disjoint fields of an object; moving the whole object after moving a field; a `match` with a path that moves and another that frees, and one that forgets; generic code that frees twice; a borrow returned along two paths; moving in a loop with and without a new value; `break` after freeing; `continue` after giving a new value; `replace` in a loop with `drain`; returning a borrow of a local. All with the right verdict. One was recorded as conservatism: taking every element out with `take` in an index loop and then freeing the slice is refused, because the checker doesn't know the loop emptied everything; the right way is `for x in drain(s)`.

## Suggested order

| Phase | Effort | Payoff |
|---|---|---|
| 1. Oracle over the suite | small | high: protects everything that comes after |
| 2. Mutation | small to medium | high: realistic code |
| 3. Random generator | medium | medium to high: unlikely combinations |
| 4. Enumeration with a model | medium to large | the strongest for small programs, and the map of false positives |
| 5. Continuous | always | keeps the rest honest |

Phases 1 and 2 come first and already give the best payoff for the effort. Phases 3 and 4 depend on what the first ones show.
