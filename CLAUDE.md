# MLC — project rules

## Core principle: every allocation is explicit

The idea of MLC is that whoever programs **understands and knows how much and where they're allocating memory**. So:

- Every heap allocation appears in the source code as an explicit call (`alloc`, `realloc`, and whatever comes next). No language construct may allocate behind your back: no string concatenation that allocates, no built-in `append` that reallocates on its own, no closures that capture on the heap, no automatic boxing and no implicit copies to the heap.
- Every release is explicit too (`free`), and the ownership checker guarantees at compile time that it happens on every path.
- If a new feature needs memory, it receives the memory from the caller, or it becomes a function whose name makes clear that it allocates. When in doubt, the feature stays out.
- Conveniences that allocate (like an `append` with capacity) may only exist as ordinary functions, written in MLC and visible in the code, never as an implicit part of the language.

## Conventions

- Runtime and library functions exposed in the language are always in English (`io.print`, `alloc`, `free`, `realloc`, `len`, `str.copy`).
- Library functions don't allocate: they write into buffers borrowed from the caller (see `docs/text.md`).
- Compiler messages, runtime panics and the tools' output are in English.
- Identifiers and comments in the compiler (Go), runtime (C) and standard library code are in English.
- Documentation (`README.md`, `docs/*.md`, `editors/*/README.md`) is in English, including the names of the files in `docs/`.
- Commits without a co-author line.

## How to work

- Every behavior change comes with a test in `tests/` (`run`, `panic` or `errors`). A change to the standard library comes with a test in `std/<module>_test.mlc` (`mlc test std`, see `docs/testing.md`). The tests compile the generated C with `-Werror`, and the runtime reports an internal error if any allocation is left at the end of the program.
- The ownership checker (`internal/checker/ownership.go`) must stay conservative: when in doubt, it refuses the program.
- Evaluation order: always left to right, written explicitly in the generated C.
- The `.mlc` files in `std/`, `tests/run`, `tests/panic` and `tests/testing` stay formatted with `mlc fmt` (`make fmt`).
- Before considering a milestone done: `make check` clean (gofmt, `mlc fmt -l`, go vet and tests), and `README.md`, `docs/grammar.md` and `docs/memory.md` updated. A change to the checker, the generator or the runtime also goes through `make sanitize` (the suite under AddressSanitizer + UBSan and ThreadSanitizer) and `make mutate` (mutants that create ownership bugs must be refused); see `docs/verification.md`.
