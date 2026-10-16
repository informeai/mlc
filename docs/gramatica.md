# Gramática do MLC

Notação EBNF: `=` define, `|` alterna, `[ ]` é opcional, `{ }` repete zero ou mais vezes, `" "` marca tokens literais.

```ebnf
(* ---------- Programa ---------- *)
program      = { import_decl | top_decl } ;
import_decl  = "import" string_lit eos ;           (* "std/io", "std/math"... ou o caminho de um arquivo, sem .mlc *)
top_decl     = [ "pub" ] ( fn_decl | extern_decl | struct_decl | enum_decl | const_decl | interface_decl ) | impl_decl ;
interface_decl = "interface" ident "{" { "fn" ident "(" param_list ")" [ type ] eos } "}" ;   (* o 1º parâmetro é self &Self *)
impl_decl    = "impl" [ type_params ] [ "auto" ] type "for" type [ "{" { fn_decl } "}" ] eos ;
enum_decl    = "enum" ident [ type_params ] "{" { variant eos } "}" ;
variant      = ident [ "(" field_decl { "," field_decl } ")" ] ;

(* ---------- Declarações de topo ---------- *)
fn_decl      = "fn" [ "(" param ")" ] ident [ type_params ] "(" [ param_list ] ")" [ type ] block ;   (* com receptor: método *)
type_params  = "[" ident constraint { "," ident constraint } "]" ;   (* [T ordered, U io.Display] *)
constraint   = ident [ "." ident ] ;   (* any, comparable, ordered, number, integer, owned ou uma interface *)
(* no receptor de um método, um argumento de tipo pode ter restrição: fn (v &Vec[T any]) get(i int) T *)
extern_decl  = "extern" "fn" ident "(" [ param_list ] ")" [ type ] eos ;   (* só na biblioteca padrão; sem corpo *)
param_list   = param { "," param } ;
param        = ident type ;

struct_decl  = "struct" ident [ type_params ] "{" { field_decl eos } "}" ;
field_decl   = ident type ;

const_decl   = "const" ident [ type ] "=" expr eos ;

(* ---------- Tipos ---------- *)
type         = [ ident "." ] ident [ "[" type { "," type } "]" ] | "[" "]" type | "[" expr "]" type | "*" type | "?" "*" type | "&" type
             | "fn" "(" [ type { "," type } ] ")" [ type ] ;   (* geometria.Ponto, [N]T, fn(int, &T) bool *)

(* ---------- Blocos e comandos ---------- *)
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

(* ---------- Controle de fluxo ---------- *)
if_stmt      = "if" expr block
               { "else" "if" expr block }
               [ "else" block ] ;

match_stmt   = "match" expr "{" { match_arm } "}" ;
match_arm    = pattern_list "=>" arm_body eos ;
arm_body     = block | simple_stmt | "break" | "continue" | "return" [ expr ] ;
pattern_list = pattern { "," pattern } ;
pattern      = [ "-" ] int_lit | string_lit | ident | ident "(" ident { "," ident } ")" | "_" ;   (* Circulo(r, _): variante de enum *)   (* ident: true, false ou uma constante *)

for_stmt     = "for" [ for_clause ] block ;
for_clause   = ident "in" expr                              (* for x in lista { } *)
             | simple_stmt ";" [ expr ] ";" [ simple_stmt ] (* for i := 0; i < n; i++ { } *)
             | expr ;                                       (* for cond { } *)

(* ---------- Expressões (precedência de cima para baixo) ---------- *)
expr         = or_expr ;
or_expr      = and_expr { "||" and_expr } ;
and_expr     = cmp_expr { "&&" cmp_expr } ;
cmp_expr     = add_expr { cmp_op add_expr } ;
cmp_op       = "==" | "!=" | "<" | "<=" | ">" | ">=" ;
add_expr     = mul_expr { ( "+" | "-" | "|" | "^" | "+%" | "-%" ) mul_expr } ;
mul_expr     = unary_expr { ( "*" | "/" | "%" | "<<" | ">>" | "&" | "*%" ) unary_expr } ;   (* como no Go: a & b == c é (a & b) == c *)
unary_expr   = ( "-" | "!" | "*" | "&" | "^" ) unary_expr | postfix_expr ;   (* *p: conteúdo; &x: empréstimo, só como argumento; ^x: inverte os bits *)

postfix_expr = primary { call | index | slice | selector | composite } ;
slice        = "[" [ expr ] ":" [ expr ] "]" ;
call         = "(" [ arg_list ] ")" ;   (* em alloc, o primeiro argumento pode ser um tipo: alloc([]int, n) *)
index        = "[" expr "]" ;
selector     = "." ident ;
composite    = "{" [ field_init_list ] "}" ;
arg_list     = expr { "," expr } ;
field_init_list = field_init { "," field_init } [ "," ] ;
field_init   = ident ":" expr ;

primary      = ident | literal | array_lit | "(" expr ")" ;
array_lit    = "[" expr "]" type "{" [ expr { "," expr } [ "," ] ] "}" ;
literal      = int_lit | string_lit | float_lit | char_lit ;   (* char_lit: 'a', um byte; float_lit: 2.5, 1e9 *)
int_lit      = digits | "0x" hex_digits | "0b" bin_digits | "0o" oct_digits ;   (* _ entre dígitos: 1_000_000, 0xFF_FF *)

(* ---------- Terminador de comando ---------- *)
eos          = ";" | (* inserido pelo lexer no fim da linha *) ;
```

## Regras que a gramática não expressa

- **`;` automático:** o lexer insere `;` numa quebra de linha quando o último token é identificador, literal, `)`, `]`, `}`, `++`, `--`, `break`, `continue` ou `return`. É a regra do Go.
- **Consequência:** `else` precisa ficar na mesma linha do `}` que fecha o `if` (`} else {`). Em outra linha, o `}` já terminou o comando.
- **Antes de `}`** o `;` pode ser omitido: `fn f() { return 1 }` é válido.
- **`for x in`** e **`for cond`** começam ambos com identificador. O parser decide olhando o segundo token (`in`).
- **Literal composto no cabeçalho:** `if`, `for` e `match` não aceitam `composite` no nível mais alto do cabeçalho. Para usar um literal ali, escreva-o entre parênteses: `if (p == Ponto{x: 1, y: 2}) { ... }`.
- **`match`:** só um braço executa, sem fallthrough. Padrões repetidos são erro, um braço depois de `_` é erro, e o `match` precisa cobrir todos os valores: `_` é obrigatório para `int` e `string`; para `bool` bastam `true` e `false`.
- **`break` dentro de `match`** sai do `for` que envolve o `match`, porque o `match` não é um laço.
- **Braço do `match`:** um comando simples, um bloco, `break`, `continue` ou `return`. Para `if`, `for`, `var` ou outro `match`, use `{ }`. Vários braços podem ficar na mesma linha, separados por `;`.
- **Literal de struct:** lista todos os campos, ou nenhum (`Ponto{}` é o valor zero). Num literal em várias linhas, todo campo termina com vírgula, inclusive o último.
- **`const`:** o valor é avaliado na compilação e só pode usar literais, outras constantes e operadores. No topo do arquivo, uma constante só usa as declaradas antes dela. Constantes podem ser usadas como padrão de `match`.
- **`for x in s`:** `s` precisa ser uma variável ou campo slice; `x` recebe uma cópia de cada elemento. O nome `_` descarta o elemento.
- **Índice:** `s[i]` em slice dá o elemento; em string, o byte como `int`. Fora dos limites, panic.
- **`for x in drain(s)`:** só para slices de donos; cada elemento sai do slice para `x`, que precisa ser liberado ou movido na volta. `drain` só existe nessa forma.
- **Métodos:** `fn (p &Ponto) area() int` declara o método `area` de `Ponto`, chamado como `p.area()`. O receptor diz o que a chamada faz com `p`: `Ponto` recebe uma cópia, `&Ponto` empresta e `*Ponto` recebe a posse; o empréstimo ou o move acontece sem `&` na chamada. Métodos só existem em structs, declarados no arquivo da struct, e `pub` os exporta.
- **Arquivos:** `import "geometria"` carrega `geometria.mlc`; o caminho é relativo à pasta do arquivo principal (`import "util/texto"` carrega `util/texto.mlc`, e o módulo se chama `texto`). O que o arquivo declara com `pub` é usado com o nome do módulo na frente: `geometria.area(r)`, `geometria.Ponto{...}`, `geometria.PI`, inclusive em tipos e em padrões de `match`. Importação circular é erro, e `fn main` só existe no arquivo principal.
- **Interfaces:** `interface Medida { fn tamanho(self &Self) int }` só serve de restrição de genérico (`fn f[T Medida](x &T)`), nunca de tipo de variável. Um tipo implementa com `impl Medida for Quadrado` (struct com os métodos de receptor `&S`), com bloco (`impl Medida for int { fn (x &int) tamanho() int {...} }`) ou genérico (`impl[T Medida] Medida for []T {...}`). O impl fica no arquivo do tipo ou no da interface. `impl auto io.Display for T` gera o método `write` de uma struct ou enum. Veja [interfaces.md](interfaces.md).
- **Bits e contas que dão a volta:** `& | ^ << >>`, `^x` e `+% -% *%` só valem para inteiros. `+ - *` continuam dando panic no estouro; `+%`, `-%` e `*%` calculam módulo 2^bits. O deslocamento por `n` fora de `0..bits-1` dá panic (erro de compilação se `n` for constante); a quantidade de bits pode ser de qualquer tipo inteiro, e `1 << n` com `n` variável é `int`. Um `<<` descarta os bits que saem; `>>` com sinal repete o bit de sinal. Constantes que estouram continuam sendo erro de compilação.
- **Donos em genéricos:** a restrição `owned` aceita donos simples (`?*X`, `[]X`); dentro do corpo, um `T owned` só se move, e `drop(x)` (só em código genérico) o libera. O receptor de um método pode apertar a restrição: `fn (v &Vec[T any]) get(i int) T` só existe quando `T` é copiável. Num argumento de tipo, `?*X` e `fn(...)` podem ser escritos direto: `vec.new[?*No](8)`.
- **Funções como valor:** `fn(int, &T) bool` é o tipo de um valor função. O nome de uma função (`dobro`, `geo.dobro`, `maior[int]`) vira valor; não há closures, métodos como valor nem `extern fn` como valor. Chamar um valor fn nulo (o valor zero) dá panic. Valores fn não podem ser comparados.
- **Impressão:** `io.print(a, b)`, `io.eprint`, `io.printf("x = {}\n", x)` e `io.eprintf` aceitam qualquer quantidade de valores que implementem `io.Display`; o formato de `printf` é uma string literal, conferida na compilação (`{}` para cada valor, `{{` e `}}` para chaves).
- **Biblioteca padrão:** `import "std/math"` carrega o módulo `math` da biblioteca embutida no `mlc` (veja [biblioteca.md](biblioteca.md)); `io` e `str` também vêm dela (`import "std/io"`). `io.print`, `io.eprint`, `io.printf` e `io.eprintf` aceitam qualquer quantidade de argumentos: o compilador reescreve a chamada em chamadas aos métodos `write` de `io.Display`. `extern fn` declara uma função implementada no runtime C e só é aceita nos arquivos da biblioteca padrão.
- **Conversões:** `int8(x)`, `int(x)`, `byte(x)`, `float64(x)` e as demais têm a forma de chamada; as que podem não caber dão panic (veja [tipos.md](tipos.md)).
- **Constantes sem tipo:** literais numéricos assumem o tipo do contexto; sem contexto, viram `int` ou `float64`.
- **Literal de array:** lista todos os elementos ou nenhum (`[3]int{}` é o valor zero).
- **Sub-slice:** `s[a:b]`, com `a` e `b` opcionais, só existe emprestado como argumento de chamada: `f(&s[a:b])`.
- **Structs** podem ser declaradas em qualquer ordem, mas não podem conter a si mesmas por valor, nem por um caminho indireto.
- **`_`** não é palavra reservada: é um identificador especial, válido só como padrão de `match` (e, depois, como descarte em atribuições).
- **Nomes predeclarados:** `true`, `false`, `nil` e os tipos básicos (`int`, `bool`, `string`) não são palavras-chave e não aparecem na gramática.
- **Comentários:** `//` até o fim da linha.

## Mudanças em relação ao rascunho original

- O braço de `match` aceita `break`, `continue` e `return`. No rascunho, `_ => continue` não era derivável, porque `continue` não é `simple_stmt`.
- As alternativas de `for_clause` foram reordenadas para refletir a ordem em que o parser tenta cada uma.
