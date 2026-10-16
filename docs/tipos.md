# Tipos do MLC

## Primitivos

| Tipo | Tamanho | Faixa | No C |
|---|---|---|---|
| `int8` | 1 byte | -128 a 127 | `int8_t` |
| `int16` | 2 bytes | -32768 a 32767 | `int16_t` |
| `int32` | 4 bytes | -2147483648 a 2147483647 | `int32_t` |
| `int` | 8 bytes | -9223372036854775808 a 9223372036854775807 | `int64_t` |
| `byte` | 1 byte | 0 a 255 | `uint8_t` |
| `uint16` | 2 bytes | 0 a 65535 | `uint16_t` |
| `uint32` | 4 bytes | 0 a 4294967295 | `uint32_t` |
| `uint64` | 8 bytes | 0 a 18446744073709551615 | `uint64_t` |
| `float32` | 4 bytes | precisão simples (IEEE 754) | `float` |
| `float64` | 8 bytes | precisão dupla (IEEE 754) | `double` |
| `bool` | 1 byte | `true`, `false` | `bool` |
| `string` | 16 bytes | texto fixo do programa (literais) | `{ptr, len}` |

O tamanho de cada valor é explícito: quem programa escolhe quanto um campo ou um elemento ocupa (`[]int16` com um milhão de elementos usa 2 MB; `[]int`, 8 MB).

## Regras

- **Contas só entre valores do mesmo tipo.** `a + b` com `a int32` e `b int` é erro: converta um dos lados (`int(a) + b`). Não existe conversão implícita entre números.
- **Estouro de inteiro dá panic**, em todos os tamanhos, com e sem sinal (`estouro de int8 na soma`). Divisão e resto por zero também.
- **Decimais seguem o IEEE 754:** estouro vira infinito, `1.0 / 0.0` é `+Inf`, `0.0 / 0.0` é `NaN`. Nada disso é comportamento indefinido. `%` não existe para decimais, e decimais não podem ser padrão de `match`.
- **Tipos sem sinal não têm `-` unário.**

## Conversões

`int8(x)`, `int(x)`, `byte(x)`, `uint64(x)`, `float32(x)`, `float64(x)` e as demais convertem entre quaisquer números.

| Conversão | Comportamento |
|---|---|
| para um inteiro que comporta todos os valores de origem | sempre funciona |
| para um inteiro menor, ou entre com e sem sinal | panic se o valor não couber: `valor 3000000000 não cabe em int32` |
| de decimal para inteiro | trunca em direção a zero (`int(-2.9)` é -2); panic se não couber ou se for `NaN` |
| de inteiro para decimal | sempre funciona (pode arredondar) |
| de `float64` para `float32` | arredonda; fora da faixa vira infinito |

Em C, converter um valor que não cabe é comportamento indefinido; no MLC, é panic com a linha do `.mlc`.

## Constantes sem tipo

Literais numéricos não têm tipo fixo: assumem o tipo do contexto.

```
var x int32 = 5        // o 5 é int32
var f float64 = 3      // o 3 é float64
n := 5                 // sem contexto: int
g := 2.5               // sem contexto: float64
var b int8 = 300       // erro de compilação: constante 300 não cabe em int8 (-128 a 127)
```

- Numa conta, a constante assume o tipo do outro lado: com `n int32`, em `n + 1` o `1` é `int32`.
- Expressões só com constantes são calculadas na compilação: `const MASCARA = 255 * 2 - 255` vale 255. Estouro e divisão por zero numa constante são erros de compilação.
- `const N = 100` não tem tipo e serve para qualquer número; `const N int32 = 100` tem.
- Um decimal não vira inteiro sem conversão: `var n int = 2.5` é erro. Com `int(2.5)`, a conversão é explícita e trunca.
- Literais aceitam notação científica: `1e9`, `2.5e-3`.

## Compostos

| Tipo | O que é |
|---|---|
| `[N]T` | array de tamanho fixo: um valor, na pilha ou dentro de uma struct; sem `alloc` |
| `[]T` | slice: dono de memória no heap (`alloc`/`realloc`/`free`) |
| `struct` | campos nomeados, por valor |
| `*T`, `?*T` | ponteiro dono (nunca nil / pode ser nil) |
| `&T` | empréstimo, só como parâmetro |

### Arrays `[N]T`

```
const N = 4
a := [N]int{1, 2, 3, 4}      // todos os elementos, ou nenhum: [N]int{} é o valor zero
a[2] = 30                    // índice checado; constante fora da faixa é erro de compilação
b := a                       // copia
io.print(a == b, len(a))     // igualdade elemento a elemento; len é constante

var buf [64]byte             // buffer de texto na pilha
k := str.copy(&buf, "oi")    // &buf empresta o array como &[]byte
soma(&a[1:3])                // sub-slice de array
```

- `N` é uma constante inteira maior que zero.
- Um array vai para a pilha ou para dentro de uma struct, então o tamanho é limitado a 1 MiB, para não estourar a pilha. Para mais, use `alloc([]T, n)`.
- Os elementos não podem ser donos (`[4]?*No`, `[2][]int`).
- Não existe ponteiro para array: use o array direto, ou um slice.
