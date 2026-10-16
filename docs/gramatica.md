# Gramática do MLC

Notação EBNF: `=` define, `|` alterna, `[ ]` é opcional, `{ }` repete zero ou mais vezes, `" "` marca tokens literais.

```ebnf
(* ---------- Programa ---------- *)
program      = { import_decl | top_decl } ;
import_decl  = "import" string_lit eos ;
top_decl     = fn_decl | struct_decl | const_decl ;

(* ---------- Declarações de topo ---------- *)
fn_decl      = "fn" ident "(" [ param_list ] ")" [ type ] block ;
param_list   = param { "," param } ;
param        = ident type ;

struct_decl  = "struct" ident "{" { field_decl eos } "}" ;
field_decl   = ident type ;

const_decl   = "const" ident [ type ] "=" expr eos ;

(* ---------- Tipos ---------- *)
type         = ident | "[" "]" type | "*" type | "?" "*" type | "&" type ;

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
assign_op    = "=" | "+=" | "-=" | "*=" | "/=" | "%=" ;
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
pattern      = [ "-" ] int_lit | string_lit | ident | "_" ;   (* ident: true, false ou uma constante *)

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
add_expr     = mul_expr { ( "+" | "-" ) mul_expr } ;
mul_expr     = unary_expr { ( "*" | "/" | "%" ) unary_expr } ;
unary_expr   = ( "-" | "!" | "*" | "&" ) unary_expr | postfix_expr ;   (* *p: conteúdo; &x: empréstimo, só como argumento *)

postfix_expr = primary { call | index | selector | composite } ;
call         = "(" [ arg_list ] ")" ;
index        = "[" expr "]" ;
selector     = "." ident ;
composite    = "{" [ field_init_list ] "}" ;
arg_list     = expr { "," expr } ;
field_init_list = field_init { "," field_init } [ "," ] ;
field_init   = ident ":" expr ;

primary      = ident | literal | "(" expr ")" ;
literal      = int_lit | string_lit | float_lit ;

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
- **Structs** podem ser declaradas em qualquer ordem, mas não podem conter a si mesmas por valor, nem por um caminho indireto.
- **`_`** não é palavra reservada: é um identificador especial, válido só como padrão de `match` (e, depois, como descarte em atribuições).
- **Nomes predeclarados:** `true`, `false`, `nil` e os tipos básicos (`int`, `bool`, `string`) não são palavras-chave e não aparecem na gramática.
- **Comentários:** `//` até o fim da linha.

## Mudanças em relação ao rascunho original

- O braço de `match` aceita `break`, `continue` e `return`. No rascunho, `_ => continue` não era derivável, porque `continue` não é `simple_stmt`.
- As alternativas de `for_clause` foram reordenadas para refletir a ordem em que o parser tenta cada uma.
