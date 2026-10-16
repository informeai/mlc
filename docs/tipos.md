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

## Bits e contas que dão a volta

| Operador | O que faz |
|---|---|
| `a & b`, `a \| b`, `a ^ b` | e, ou, ou exclusivo, bit a bit |
| `^a` | inverte todos os bits (`^0` é -1; num `byte`, `^0xF0` é `0x0F`) |
| `a << n`, `a >> n` | desloca `n` bits; `>>` com sinal repete o bit de sinal |
| `a +% b`, `a -% b`, `a *% b` | soma, subtração e multiplicação **módulo 2^bits**: dão a volta em vez de dar panic |
| `&= \|= ^= <<= >>= +%= -%= *%=` | as atribuições compostas |

- Só valem para inteiros, e os dois lados de `& | ^ +% -% *%` precisam ser do mesmo tipo.
- Num deslocamento, `n` pode ser de qualquer tipo inteiro; fora de `0..bits-1` é panic (erro de compilação se `n` for constante). Os bits que saem pela esquerda num `<<` são descartados.
- A precedência é a do Go: `<< >> &` junto com `* / %`, e `| ^` junto com `+ -`. Assim `a & b == c` é `(a & b) == c`, e `1 + 2 << 1` é 5.
- A conta comum continua checada: quem quer que ela dê a volta (hash, criptografia, geradores aleatórios) escreve `+%`, e isso fica visível no código.

```
var h uint64 = 0xCBF2_9CE4_8422_2325       // FNV-1a
for i := 0; i < len(s); i++ {
    h ^= uint64(s[i])
    h *%= 0x0000_0100_0000_01B3
}
```

## Literais inteiros

`255`, `0xFF`, `0b1111_1111`, `0o377` e `1_000_000`: o `_` só pode ficar entre dígitos. Um zero à esquerda não muda a base (`010` é dez).

## Funções como valor

`fn(int, int) bool` é o tipo de um valor função: o endereço de uma função, sem variáveis capturadas (não há closures), então não aloca e não tem dono.

```
fn dobro(x int) int { return x * 2 }

fn aplica(f fn(int) int, x int) int {
    return f(x)
}

f := dobro                      // o nome da função é o valor
io.print(aplica(f, 21))
var g fn(int, int) int = maior[int]   // genérica: com os tipos
sort.sort_by(&pessoas, por_idade)     // fn(&Pessoa, &Pessoa) bool
```

- Os tipos dos parâmetros dizem o que a chamada faz com cada argumento (copia, empresta `&T`, move `*T`), e o verificador de posse confere como numa chamada direta.
- O valor zero de um fn (variável sem valor, campo não preenchido) é nulo, e chamá-lo dá panic com a linha da chamada.
- Métodos e `extern fn` não viram valor; uma função genérica precisa dos tipos (`maior[int]`). Valores fn não podem ser comparados.

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

## Genéricos

```
fn maior[T ordered](a T, b T) T {
    if a > b { return a }
    return b
}

struct Par[A any, B any] {
    primeiro A
    segundo B
}

fn (p Par[A, B]) troca() Par[B, A] {
    return Par[B, A]{primeiro: p.segundo, segundo: p.primeiro}
}

maior(3, 7)              // T deduzido: int
maior[int8](-3, -9)      // T explícito
p := Par[int, string]{primeiro: 1, segundo: "um"}
```

- **Restrições:** cada parâmetro de tipo declara o que o código pode fazer com ele.

  | Restrição | Tipos aceitos | Operações permitidas |
  |---|---|---|
  | `any` | qualquer tipo sem posse | atribuir, passar, devolver, imprimir |
  | `comparable` | qualquer tipo sem posse | o de `any` e `==`, `!=` |
  | `ordered` | números | o de `comparable` e `<`, `<=`, `>`, `>=` |
  | `number` | números | o de `ordered` e `+`, `-`, `*`, `/` |
  | `integer` | inteiros | o de `number` e `%` |
  | `owned` | tipos sem posse **e** donos simples (`?*X`, `[]X`, com `X` sem donos) | mover, passar, devolver, `take`/`replace`/`drain` em `[]T`, `drop(x)`; **sem** cópia nem `==` |

- **A definição é checada uma vez,** com `T` valendo só o que a restrição permite: `a < b` com `T any` é erro na própria definição, antes de qualquer uso.
- **Cada uso com tipos concretos gera uma instância** (`maior[int]`, `Par[int, string]`): uma cópia checada com os tipos reais e compilada no C com nome próprio. Não há custo em tempo de execução. Um erro que só aparece com um tipo concreto (como `x + 300` com `T = int8`) aponta a linha da definição e diz qual instância falhou.
- **Inferência:** os tipos são deduzidos dos argumentos; quando não dá (nenhum argumento usa `T`), escreva-os: `vazio[int]()`.
- **`T(x)`** converte para o parâmetro de tipo: `total := T(0)`.
- **Métodos de struct genérica** nomeiam os parâmetros no receptor: `fn (v &Vetor[T]) adiciona(x T)`.
- **Restrição no receptor:** `fn (v &Vec[T any]) get(i int) T` só existe nas instâncias em que `T` atende `any`. Chamar `get` num `Vec[[]byte]` é erro, com a sugestão de `take`/`put`.
- Com as restrições comuns, argumentos de tipo não podem ser donos nem empréstimos (`*No`, `[]int`, `&T`). Para aceitar donos, use `owned`.

### Donos em genéricos: `owned`

```
struct Vec[T owned] {
    items []T
    count int
}

fn (v &Vec[T]) push(x T) {           // x é movido para dentro
    if v.count == len(v.items) {
        v.grow()
    }
    old := replace(&v.items[v.count], x)
    drop(old)                        // a posição depois do fim estava vazia
    v.count++
}
```

- Dentro do corpo, um valor do tipo `T` é tratado como um dono: atribuir, passar ou devolver **move**, usar depois de mover é erro, e todo valor precisa de destino (mover, guardar, devolver ou `drop(x)`). Copiar e comparar com `==` não valem.
- **`drop(x)`** só existe em código genérico: libera `x` se for dono (`free`) e não faz nada se não for (com `T = int`). Fora de um genérico, use `free`.
- `take(&s[i])`, `replace(&s[i], v)` e `for x in drain(s)` valem para `[]T`, como nos slices de donos.
- `*X` não é aceito (não tem valor zero, e uma coleção precisa de posições vazias): use `?*X`. Structs com donos e donos dentro de donos (`?*Lista`, `[]?*No`) ainda não são aceitos.
- **Os erros de posse aparecem na definição:** cada genérico com `owned` também é verificado com `T = []byte`. Um erro desses diz "verificado com T dono".

## Enums com dados

```
enum Forma {
    Circulo(raio float64)
    Retangulo(largura float64, altura float64)
    Vazio
}

enum Resultado[T any] {
    Ok(valor T)
    Erro(codigo int)
}

f := Forma.Circulo(2.0)
v := Forma.Vazio

match f {
    Circulo(r) => io.print(3.14 * r * r)    // r recebe uma cópia do campo
    Retangulo(l, _) => io.print(l)          // _ ignora um campo
    Vazio => io.print("nada")
}
```

- Um valor de enum é uma das variantes, com os dados dela. Constrói-se com o nome do enum na frente: `Forma.Circulo(2.0)`, `Forma.Vazio`.
- O `match` precisa cobrir todas as variantes (ou ter `_`); esquecer uma é erro de compilação, e a mensagem diz quais faltam. Numa variante com dados, o padrão dá um nome a cada campo (ou `_`).
- **Enums genéricos:** os tipos podem ser escritos (`Resultado[int].Erro(2)`) ou deduzidos do valor (`Resultado.Ok(5)`) ou do contexto (`return Resultado.Erro(1)` numa função que devolve `Resultado[int]`).
- `==` compara variante e dados; `io.print` mostra `Circulo(2)`.
- O valor zero (`var f Forma`) é a primeira variante, com os campos zerados.
- Nesta versão os dados das variantes não podem ser donos (`*T`, `[]T`): um enum é um valor comum, copiável.
