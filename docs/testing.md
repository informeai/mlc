# Tests (`mlc test`)

A `name_test.mlc` file is a test program: instead of `fn main`, it declares `fn test_name()` functions, and `mlc test` runs each one.

```
import "std/testing"
import "std/vec"

fn test_push() {
    v := vec.new[int](2)
    v.push(7)
    testing.eq_int(v.len(), 1)
    testing.eq_int(v.get(0), 7)
    v.free()
}

fn test_pop_empty() {
    testing.expect_panic()      // the test only passes if it ends in a panic
    v := vec.new[int](2)
    x := v.pop()
    testing.eq_int(x, 0)
    v.free()
}
```

```sh
mlc test                       # every *_test.mlc in the current directory and below
mlc test std                   # the ones in a directory
mlc test std/vec_test.mlc      # the ones in a file
mlc test -run pop std          # only tests whose name contains "pop"
mlc test -v std/vec_test.mlc   # show each test and its output
make test-std                  # the standard library tests
```

```
--- FALHOU: test_push (0.00s)
    std/vec_test.mlc:8: veio 2, esperado 1
FALHOU	std/vec_test.mlc	1 de 2 testes falharam	0.31s
ok	std/map_test.mlc	7 testes	0.28s
```

(The tool's messages are still in Portuguese: `FALHOU` is "failed", `veio ... esperado ...` is "got ... want ...".)

## Rules

- A test is a `test_*` function in the test file, with no parameters and no result, and it can't be generic. The other functions in the file are ordinary helpers.
- The test file doesn't declare `fn main`: `mlc test` generates the `main`. It can't be imported (`import "x_test"` is an error), and `mlc build` and `mlc run` refuse it.
- **Each test runs in its own process.** A panic, a failure or a leak stays with the test that caused it, and the others keep running. The test starts with no command-line arguments (`os.arg_count()` is 1).
- **A test is an ordinary MLC program.** The ownership rules apply inside it, so the checker requires the `free` of everything the test allocates.
- Tests test the module from the outside, through what it exports (`pub`).
- A test's output (`io.print`) only shows up when it fails, or with `-v`.

## `std/testing`

A failing check shows its line in the test and counts as a failure, but the test keeps going to the end. None of the functions allocate: text is compared where it is.

| Function | Fails when |
|---|---|
| `check(ok bool, msg string)` | `ok` is `false`; shows `msg` |
| `fail(msg string)` | always; shows `msg` |
| `eq_int(got int, want int)` | the integers differ |
| `eq_uint(got uint64, want uint64)` | same, unsigned (also shown in hexadecimal) |
| `eq_bool(got bool, want bool)` | the bools differ |
| `eq_float(got float64, want float64)` | the floats differ (exact comparison) |
| `near(got float64, want float64, tol float64)` | the difference is above `tol` (or one of them is NaN) |
| `eq_str(got &[]byte, want string)` | the buffer's text differs; shows both in quotes, with `\n` and the other control bytes escaped |
| `eq_bytes(got &[]byte, want &[]byte)` | the buffers differ |
| `expect_panic()` | the test ends **without** a panic; with the panic, the test passes (if nothing before it failed) |

`testing.B` is a benchmark's state: the `n` field says how many times to repeat (see [Benchmarks](#benchmarks)).

For structs, enums and everything else, use `check(a == b, "...")` or a `match`.

## Result of each test

| Result | When |
|---|---|
| `ok` | it finished without failures (or with the panic asked for by `expect_panic`) |
| `FALHOU` (failed) | a check failed, or the expected panic didn't happen |
| `PANIC` | a panic the test didn't expect; shows the line of the panic |
| `VAZAMENTO` (leak) | memory was left over at the end of the process. This shouldn't happen, because the ownership checker prevents it; if it does, it's a bug in `mlc` |

`mlc test` exits with code 1 if any test fails or any file doesn't compile.

## Benchmarks

A `fn bench_name(b &testing.B)` function repeats the measured section `b.n` times:

```
fn bench_sort_1000(b &testing.B) {
    s := alloc([]int, 1000)
    for i := 0; i < b.n; i++ {
        fill(&s)
        sort.sort(&s)
    }
    free(s)
}
```

```sh
mlc test -bench . std                   # every benchmark (after the tests)
mlc test -bench sort std/sort_test.mlc  # only the ones with "sort" in the name
mlc test -benchtime 2000 -bench . std   # measure each one for 2 s (the default is 500 ms)
```

```
ok	std/sort_test.mlc	7 testes	0.30s
bench_sort_1000	        6730 vezes	       33049.3 ns/op
```

- `mlc test -bench` runs the benchmark with `b.n` = 1, then a larger `n`, until the measurement passes the requested time. It's the same method as `go test -bench`.
- Each round is a process, and time is measured inside the program, without counting startup. A panic or a failure stops the benchmark and is reported as in a test.
- Benchmarks only run with `-bench`, and only if the file's tests passed. A file can have only benchmarks.
- The C compiler can remove a loop that does nothing (the result is `0.0 ns/op`). Use the result of the work (check a sum, for example) so the loop doesn't disappear.

## Standard library

Every `std` module has tests next to its code, in `std/<module>_test.mlc`. They run under `go test ./...` (through `TestStdLibrary`) and with `make test-std`. A change to the `std` comes with its tests.
