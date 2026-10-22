# MLC grammar

EBNF notation: `=` defines, `|` alternates, `[ ]` is optional, `{ }` repeats zero or more times, `" "` marks literal tokens.

```ebnf
(* ---------- Program ---------- *)
program      = { import_decl | top_decl } ;
import_decl  = "import" string_lit eos ;           (* "std/io", "std/math"... or a file path, without .mlc *)
top_decl     = [ "pub" ] ( fn_decl | extern_decl | struct_decl | enum_decl | const_decl | interface_decl ) | impl_decl ;
interface_decl = "interface" ident "{" { "fn" ident "(" param_list ")" [ type ] eos } "}" ;   (* the 1st parameter is self &Self *)
impl_decl    = "impl" [ type_params ] [ "auto" ] type "for" type [ "{" { fn_decl } "}" ] eos ;
enum_decl    = "enum" ident [ type_params ] "{" { variant eos } "}" ;
variant      = ident [ "(" field_decl { "," field_decl } ")" ] ;

(* ---------- Top-level declarations ---------- *)
fn_decl      = "fn" [ "(" param ")" ] ident [ type_params ] "(" [ param_list ] ")" [ type ] block ;   (* with a receiver: a method *)
type_params  = "[" ident constraint { "," ident constraint } "]" ;   (* [T ordered, U io.Display, C ref]; also on methods: fn (m &Map[K, V]) each_with[C ref](...) *)
constraint   = ident [ "." ident ] { "+" ident [ "." ident ] } ;   (* any, comparable, ordered, number, integer, owned, an interface, or one of each: owned + hash.Hash *)
(* in a method's receiver, a type argument can have a constraint: fn (v &Vec[T any]) get(i int) T *)
extern_decl  = "extern" [ "\"C\"" ] "fn" ident "(" [ param_list ] ")" [ type ] eos   (* extern fn: std only; extern "C" fn: a C function *)
             | "extern" "\"C\"" "type" ident eos ;   (* opaque C handle *)
link_decl    = "link" string_lit eos ;   (* C library: link "m" becomes -lm *)
param_list   = param { "," param } ;
param        = ident [ "kept" ] type ;   (* kept: only in an extern "C" fn, on a callback or its context that C keeps *)

struct_decl  = "struct" ident [ type_params ] "{" { field_decl eos } "}" ;
field_decl   = ident type ;

const_decl   = "const" ident [ type ] "=" expr eos ;

(* ---------- Types ---------- *)
type         = [ ident "." ] ident [ "[" type { "," type } "]" ] | "[" "]" type | "[" expr "]" type | "*" type | "?" "*" type | "&" type | "?" "&" type
             | "fn" "(" [ type { "," type } ] ")" [ type ] ;   (* geometry.Point, [N]T, fn(int, &T) bool *)

(* ---------- Blocks and statements ---------- *)
block        = "{" { statement } "}" ;

statement    = var_decl
             | const_decl
             | if_stmt
             | match_stmt
             | for_stmt
             | return_stmt
             | jump_stmt
             | block
             | simple_stmt eos ;

jump_stmt    = ( "break" | "continue" ) eos ;
var_decl     = "var" ident [ type ] [ "=" expr ] eos ;

simple_stmt  = short_decl
             | assign_stmt
             | inc_dec_stmt
             | expr ;

short_decl   = ident ":=" expr ;
assign_stmt  = postfix_expr assign_op expr ;
assign_op    = "=" | "+=" | "-=" | "*=" | "/=" | "%=" | "&=" | "|=" | "^=" | "<<=" | ">>=" | "+%=" | "-%=" | "*%=" ;
inc_dec_stmt = postfix_expr ( "++" | "--" ) ;

return_stmt  = "return" [ expr ] eos ;

(* ---------- Control flow ---------- *)
if_stmt      = "if" expr block
               { "else" "if" expr block }
               [ "else" block ] ;

match_stmt   = "match" expr "{" { match_arm } "}" ;
match_arm    = pattern_list "=>" arm_body eos ;
arm_body     = block | simple_stmt | "break" | "continue" | "return" [ expr ] ;
pattern_list = pattern { "," pattern } ;
pattern      = [ "-" ] int_lit | string_lit | ident | ident "(" ident { "," ident } ")" | "_" ;   (* Circle(r, _): an enum variant *)   (* ident: true, false or a constant *)

for_stmt     = "for" [ for_clause ] block ;
for_clause   = ident "in" expr                              (* for x in list { } *)
             | simple_stmt ";" [ expr ] ";" [ simple_stmt ] (* for i := 0; i < n; i++ { } *)
             | expr ;                                       (* for cond { } *)

(* ---------- Expressions (precedence from top to bottom) ---------- *)
expr         = or_expr ;
or_expr      = and_expr { "||" and_expr } ;
and_expr     = cmp_expr { "&&" cmp_expr } ;
cmp_expr     = add_expr { cmp_op add_expr } ;
cmp_op       = "==" | "!=" | "<" | "<=" | ">" | ">=" ;
add_expr     = mul_expr { ( "+" | "-" | "|" | "^" | "+%" | "-%" ) mul_expr } ;
mul_expr     = unary_expr { ( "*" | "/" | "%" | "<<" | ">>" | "&" | "*%" ) unary_expr } ;   (* as in Go: a & b == c is (a & b) == c *)
unary_expr   = ( "-" | "!" | "*" | "&" | "^" ) unary_expr | postfix_expr ;   (* *p: contents; &x: borrow, only as an argument; ^x: flips the bits *)

postfix_expr = primary { call | index | slice | selector | composite } ;
slice        = "[" [ expr ] ":" [ expr ] "]" ;
call         = "(" [ arg_list ] ")" ;   (* in alloc, the first argument can be a type: alloc([]int, n) *)
index        = "[" expr "]" ;
selector     = "." ident ;
composite    = "{" [ field_init_list ] "}" ;
arg_list     = expr { "," expr } ;
field_init_list = field_init { "," field_init } [ "," ] ;
field_init   = ident ":" expr ;

primary      = ident | literal | array_lit | "(" expr ")" ;
array_lit    = "[" expr "]" type "{" [ expr { "," expr } [ "," ] ] "}" ;
literal      = int_lit | string_lit | float_lit | char_lit ;   (* char_lit: 'a', one byte; float_lit: 2.5, 1e9 *)
int_lit      = digits | "0x" hex_digits | "0b" bin_digits | "0o" oct_digits ;   (* _ between digits: 1_000_000, 0xFF_FF *)

(* ---------- Statement terminator ---------- *)
eos          = ";" | (* inserted by the lexer at the end of the line *) ;
```

## Rules the grammar doesn't express

- **Automatic `;`:** the lexer inserts `;` at a line break when the last token is an identifier, a literal, `)`, `]`, `}`, `++`, `--`, `break`, `continue` or `return`. It's Go's rule.
- **Consequence:** `else` must be on the same line as the `}` that closes the `if` (`} else {`). On another line, the `}` has already ended the statement.
- **Before `}`** the `;` can be left out: `fn f() { return 1 }` is valid.
- **`for x in`** and **`for cond`** both start with an identifier. The parser decides by looking at the second token (`in`).
- **Composite literal in a header:** `if`, `for` and `match` don't accept a `composite` at the top level of the header. To use a literal there, put it in parentheses: `if (p == Point{x: 1, y: 2}) { ... }`.
- **`match`:** only one arm runs, with no fallthrough. Repeated patterns are an error, an arm after `_` is an error, and the `match` must cover every value: `_` is required for `int` and `string`; for `bool`, `true` and `false` are enough.
- **`break` inside a `match`** leaves the `for` around the `match`, because `match` isn't a loop.
- **A `match` arm:** a simple statement, a block, `break`, `continue` or `return`. For `if`, `for`, `var` or another `match`, use `{ }`. Several arms can share a line, separated by `;`.
- **Struct literal:** lists every field, or none (`Point{}` is the zero value). In a multi-line literal, every field ends with a comma, including the last one.
- **`const`:** the value is evaluated at compile time and can only use literals, other constants and operators. At the top of the file, a constant only uses the ones declared before it. Constants can be used as `match` patterns.
- **`for x in s`:** `s` must be a slice variable or field; `x` gets a copy of each element. The name `_` discards the element.
- **`match` over an enum with owners:** over an owning variable or a temporary, it consumes (each owning field gets an owning name, not `_`, and a `_` arm can't cover a variant with owners); over `*e`, a field or a borrow, it only reads (owning fields come as `&T`). See [types.md](types.md#enums-with-owners).
- **Test files:** a `name_test.mlc` doesn't declare `fn main`; the `fn test_name()` functions (no parameters or result, not generic) are the tests, run by `mlc test`. See [testing.md](testing.md).
- **Threads:** `thread.spawn(f, ctx, stack)` and `thread.join(t)` are built in (like `io.print`); `thread.Thread[R]` is the handle type, only behind a pointer. `chan.new[T](n)` and the methods of the `chan.Sender[T]` and `chan.Receiver[T]` ends are built in too, as are `mutex.new(v)` and the methods of `mutex.Mutex[T]` and `mutex.Guard[T]`. See [concurrency.md](concurrency.md).
- **Indexing:** `s[i]` on a slice gives the element; on a string, the byte. Out of bounds, a panic.
- **`for x in drain(s)`:** only for slices of owners; each element leaves the slice into `x`, which must be freed or moved in that iteration. `drain` only exists in this form.
- **Methods:** `fn (p &Point) area() int` declares the `area` method of `Point`, called as `p.area()`. The receiver says what the call does with `p`: `Point` gets a copy, `&Point` borrows and `*Point` takes ownership; the borrow or the move happens without `&` in the call. Methods only exist on structs, declared in the struct's file, and `pub` exports them.
- **Files:** `import "geometry"` loads `geometry.mlc`, looked up in the main file's directory, in `lib/` next to it, in the `-I` directories and in the `MLC_PATH` directories, in that order (`import "util/text"` loads `util/text.mlc`, and the module is called `text`). What the file declares with `pub` is used with the module name in front: `geometry.area(r)`, `geometry.Point{...}`, `geometry.PI`, including in types and in `match` patterns. Circular imports are an error, and `fn main` only exists in the main file.
- **Interfaces:** `interface Measure { fn size(self &Self) int }` only serves as a generic constraint (`fn f[T Measure](x &T)`), never as a variable type. A type implements it with `impl Measure for Square` (a struct with the `&S` receiver methods), with a block (`impl Measure for int { fn (x &int) size() int {...} }`) or generically (`impl[T Measure] Measure for []T {...}`). The impl lives in the type's file or the interface's file. `impl auto io.Display for T` generates the `write` method of a struct or enum, and `impl auto cmp.Eq`, `cmp.Ord` and `hash.Hash` generate `eq`, `cmp` and `hash` from the fields. See [interfaces.md](interfaces.md).
- **Bits and wrapping arithmetic:** `& | ^ << >>`, `^x` and `+% -% *%` only apply to integers. `+ - *` still panic on overflow; `+%`, `-%` and `*%` compute modulo 2^bits. A shift by `n` outside `0..bits-1` panics (a compile error if `n` is a constant); the shift amount can be any integer type, and `1 << n` with a variable `n` is `int`. `<<` drops the bits that go out; a signed `>>` repeats the sign bit. Constants that overflow are still a compile error.
- **C functions:** `extern "C" fn` declares a C function in any file; only numbers, `bool`, `string`, `&[]T`, `&T` and handles (`extern "C" type`) cross, including handles returned through a pointer (`&?*X`, only in `extern "C" fn` parameters); `?&T` is an `extern "C" fn` parameter that accepts `nil` (`NULL`). A parameter of fn type is a C callback: it receives the name of an MLC function, and the compiler passes a C wrapper of it; `kept` marks the callback and its context (`kept *T`, moved to C) when C keeps them after the call. `kept` is a word only there, before a type. `link "name"` asks for the library. See [ffi.md](ffi.md#callbacks-from-c).
- **Returned and stored borrows:** a function can return `&T` when the result comes from the `&` receiver (a method) or from the only `&` parameter; `x := v.at(0)` and `x := &a.field` store a borrow in a variable, which freezes the source while it's used. See [memory.md](memory.md).
- **Owners in generics:** the `owned` constraint accepts simple owners (`?*X`, `[]X`); inside the body, a `T owned` can only be moved, and `drop(x)` (only in generic code) frees it. A method's receiver can tighten the constraint: `fn (v &Vec[T any]) get(i int) T` only exists when `T` is copyable. In a type argument, `?*X` and `fn(...)` can be written directly: `vec.new[?*Node](8)`.
- **Functions as values:** `fn(int, &T) bool` is the type of a function value. A function's name (`double`, `geo.double`, `larger[int]`) becomes a value; there are no closures, methods as values or `extern fn` as values. Calling a nil fn value (the zero value) panics. Fn values can't be compared.
- **Printing:** `io.print(a, b)`, `io.eprint`, `io.printf("x = {}\n", x)` and `io.eprintf` accept any number of values that implement `io.Display`; the `printf` format is a string literal, checked at compile time (`{}` for each value, `{{` and `}}` for braces). `str.format(&dst, "...", x)` and `str.format_len("...", x)` use the same format. Specification: `{:[[fill]align][#][0][width][.places][x|X|b|o|s]}`, checked against the value's type (see [text.md](text.md#format-specifications)).
- **Standard library:** `import "std/math"` loads the `math` module of the library built into `mlc` (see [library.md](library.md)); `io` and `str` come from it too (`import "std/io"`). `io.print`, `io.eprint`, `io.printf` and `io.eprintf` accept any number of arguments: the compiler rewrites the call into calls to the `write` methods of `io.Display`. `extern fn` declares a function implemented in the C runtime and is only accepted in standard library files.
- **Conversions:** `int8(x)`, `int(x)`, `byte(x)`, `float64(x)` and the others have the shape of a call; the ones that may not fit panic (see [types.md](types.md)).
- **Untyped constants:** numeric literals take the type of the context; with no context, they become `int` or `float64`.
- **Array literal:** lists every element or none (`[3]int{}` is the zero value).
- **Sub-slice:** `s[a:b]`, with optional `a` and `b`, only exists borrowed as a call argument: `f(&s[a:b])`.
- **Structs** can be declared in any order, but can't contain themselves by value, directly or through an indirect path (including through an array).
- **`_`** isn't a reserved word: it's a special identifier, valid only as a `match` pattern (and, later, as a discard in assignments).
- **Predeclared names:** `true`, `false`, `nil` and the basic types (`int`, `bool`, `string`) aren't keywords and don't appear in the grammar.
- **Comments:** `//` to the end of the line.

## Changes from the original draft

- A `match` arm accepts `break`, `continue` and `return`. In the draft, `_ => continue` couldn't be derived, because `continue` isn't a `simple_stmt`.
- The `for_clause` alternatives were reordered to match the order in which the parser tries each one.
