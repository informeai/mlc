# Texto no MLC

Texto é montado num **buffer `[]byte` que você aloca**. As funções de texto e de entrada e saída **nunca alocam**: escrevem no buffer emprestado, devolvem quantos bytes usaram e dão panic se não couber. Assim, toda alocação de texto aparece no código como `alloc` ou `realloc`, e o tamanho de cada buffer é escolhido por quem programa.

```
import "std/io"
import "std/str"

fn main() {
    nome := alloc([]byte, 32)
    n := io.read_line(&nome)                  // lê no buffer, sem alocar

    precisa := len("Olá, ") + n               // quanto espaço a frase ocupa
    buf := alloc([]byte, precisa)             // a única alocação, com tamanho conhecido
    k := 0
    k += str.copy(&buf[k:], "Olá, ")
    k += str.copy_bytes(&buf[k:], &nome[:n])
    io.write_line(&buf[:k])

    free(nome)
    free(buf)
}
```

## Tipos

| Tipo | O que é |
|---|---|
| `string` | texto fixo do programa (literais). Não aloca e não precisa de `free`. |
| `byte` | um byte, de 0 a 255. Literal: `'a'`, `'\n'`, `'\''`. |
| `[]byte` | buffer de texto: dono, criado com `alloc([]byte, n)`, cresce com `realloc`, liberado com `free`. |

- `s[i]` numa `string` ou num `[]byte` dá um `byte`.
- `len(s)` dá o tamanho em **bytes**: `len("olá")` é 4, porque `á` ocupa dois bytes em UTF-8.
- `byte` é um inteiro sem sinal de 8 bits: faz contas com outros `byte` (estouro dá panic), e converte com `int(b)` e `byte(n)`, que dá panic se `n` estiver fora de 0 a 255. Veja [tipos.md](tipos.md).
- Bytes podem ser comparados (`==`, `<`, ...) e usados em `match` com literais: `'a', 'e' => ...`.

Para buffers pequenos, um array `[N]byte` dispensa o `alloc` e o `free`: ele vive na pilha e `&buf` o empresta como `&[]byte`.

```
var buf [32]byte
k := str.copy(&buf, "olá, ")
k += str.put_int(&buf[k:], 42)
io.write_line(&buf[:k])
```

## Funções (`import "std/str"`)

| Função | O que faz |
|---|---|
| `str.copy(dst &[]byte, s string) int` | copia `s` para o começo de `dst`; devolve quantos bytes copiou |
| `str.copy_bytes(dst &[]byte, src &[]byte) int` | copia `src` para o começo de `dst` (podem se sobrepor) |
| `str.put_int(dst &[]byte, n int) int` | escreve `n` em decimal no começo de `dst` |
| `str.int_len(n int) int` | quantos bytes `n` ocupa em decimal (para saber quanto alocar) |
| `str.eq(a &[]byte, s string) bool` | `a` tem o mesmo texto que `s`? |
| `str.eq_bytes(a &[]byte, b &[]byte) bool` | `a` e `b` têm o mesmo texto? |
| `str.parse_int(s &[]byte, out &int) bool` | lê um inteiro decimal (com sinal opcional) de `s` inteiro; em caso de sucesso grava em `out` e devolve `true` |
| `str.parse_uint(s &[]byte, out &uint64) bool` | lê um inteiro sem sinal em decimal (para valores acima do maior `int`) |
| `str.parse_int_base(s &[]byte, base int, out &int) bool` | lê um inteiro na base dada (2 a 36), com sinal opcional; com 16, 2 ou 8 aceita o prefixo `0x`, `0b` ou `0o`; com base 0, o prefixo escolhe a base e, sem prefixo, é decimal |
| `str.parse_bool(s &[]byte, out &bool) bool` | lê exatamente `true` ou `false` |
| `str.put_float(dst &[]byte, x float64) int` | escreve `x` com o menor número de dígitos que o representa exatamente (como o `io.print`) |
| `str.parse_float(s &[]byte, out &float64) bool` | lê um decimal (`2.5`, `1e9`) de `s` inteiro |

As funções `parse_*` leem o texto inteiro (sem espaços em volta: apare antes com `text.trim_start` e `text.trim_end`) e devolvem `false`, sem panic, para texto inválido ou para um número que não cabe no tipo.

As funções que escrevem dão panic se o texto não couber em `dst`, com uma mensagem como `str.copy: o texto (3 bytes) não cabe no destino (2 bytes)`.

## Formatar num buffer: `str.format`

```
n := str.format_len("{} itens, R$ {:.2}", qtd, total)   // quantos bytes o texto ocupa
linha := alloc([]byte, n)                                // a alocação, com o tamanho exato
str.format(&linha, "{} itens, R$ {:.2}", qtd, total)
```

| Função | O que faz |
|---|---|
| `str.format(dst &[]byte, "...", a, ...) int` | escreve o formato em `dst`, como o `io.printf`, e devolve quantos bytes usou; dá panic se não couber, apontando a linha do `str.format` |
| `str.format_len("...", a, ...) int` | quantos bytes o texto ocupa, sem escrever nada |

- O formato e os valores seguem as regras do `io.printf`, inclusive as especificações (abaixo) e os tipos com `io.Display`: um `write` escrito para o `io.printf` também serve para o `str.format`.
- Para continuar escrevendo depois do que já está no buffer: `k += str.format(&buf[k:], "...", x)`.
- Para escrever muito na saída (um laço com milhares de linhas), reaproveite um `io.Writer` com `w.printf(...)` em vez de chamar `io.printf` a cada linha. Veja [interfaces.md](interfaces.md#iowriter).
- `dst` não pode ser também um dos valores (`str.format(&b, "{}", b)` é erro), porque o texto seria lido enquanto é sobrescrito.
- Nada aloca. O texto passa por um `io.Writer` com buffer na pilha, que o entrega a `dst`. O `Writer` não guarda `dst`: ele guarda um índice de destino que o runtime confere, e esse destino só existe durante o `str.format`. Por isso não há um `Writer` sobre `[]byte` que se possa guardar numa variável: isso precisaria de empréstimos guardados em structs, item 8 do roteiro.

## Especificações de formato

Valem no `io.printf`, `io.eprintf`, `str.format` e `str.format_len`: `{:[[preenchimento]alinhamento][#][0][largura][.casas][tipo]}`.

| Exemplo | Resultado | Vale para |
|---|---|---|
| `{:x}`, `{:X}`, `{:b}`, `{:o}` | `ff`, `FF`, `101`, `10` | inteiros (negativos com `-`: `-ff`) |
| `{:#x}`, `{:#b}`, `{:#o}` | `0xff`, `0b101`, `0o10` | inteiros |
| `{:.2}` | `3.14` | `float64`, `float32` |
| `{:8}`, `{:<8}`, `{:>8}`, `{:^8}` | largura mínima de 8 caracteres, alinhado à esquerda, à direita ou no centro | qualquer valor; sem alinhamento, números vão à direita e o resto à esquerda |
| `{:*^8}` | preenche com `*` em vez de espaço | qualquer valor |
| `{:08}`, `{:08.2}`, `{:#010x}` | zeros depois do sinal e do prefixo: `-0000042` | números |
| `{:s}`, `{:>10s}` | os bytes de um `[]byte` (ou `[N]byte`) como texto, em vez de `[104 105]` | `[]byte`, `&[]byte`, `[N]byte` |

- **A largura conta caracteres, não bytes.** `"café"` ocupa 4. Um valor maior que a largura não é cortado.
- Uma especificação que não serve para o tipo é erro de compilação (`{:x}` num `float64`, `{:.2}` num `int`).
- Um valor pode ser um empréstimo, escrito sem cópia: `&b[:n]` ou uma variável `&T`.
- Num tipo com `write` próprio, a largura e o alinhamento valem para o texto inteiro que o `write` escreve. Base, casas e zeros valem para os números escritos por `w.put_int`, `w.put_uint` e `w.put_float`.

## Entrada e saída (`import "std/io"`)

| Função | O que faz |
|---|---|
| `io.print(a, b, ...)` | escreve valores separados por espaço, com quebra de linha no fim |
| `io.printf("x = {}\n", x)` | escreve o formato trocando cada `{}` por um valor (formato conferido na compilação; especificações como `{:>8.2}`, veja acima) |
| `io.eprint(...)`, `io.eprintf(...)` | o mesmo, no erro padrão |
| `io.write(s &[]byte)` | escreve os bytes de `s`, sem separador nem quebra de linha |
| `io.write_line(s &[]byte)` | escreve `s` e uma quebra de linha |
| `io.read_line(dst &[]byte) int` | lê uma linha (sem o `\n`) em `dst` e devolve quantos bytes leu; `-1` no fim da entrada |

Os valores de `io.print` e `io.printf` precisam implementar `io.Display`: os tipos embutidos, slices e arrays já implementam, e structs e enums ganham a implementação com `impl auto io.Display for T`. Veja [interfaces.md](interfaces.md).

Se a linha não couber em `dst`, `read_line` enche o buffer e o resto da linha fica para a próxima chamada. Para ler linhas de qualquer tamanho, cresça o buffer com `realloc` e continue lendo no espaço novo:

```
usados := 0
for {
    lidos := io.read_line(&linha[usados:])
    if lidos < 0 { break }
    usados += lidos
    if usados == len(linha) {
        linha = realloc(linha, len(linha) * 2)   // a linha não coube: dobra
        continue
    }
    // linha completa em linha[:usados]
    usados = 0
}
```

## Por que não há concatenação

Uma `concat(a, b)` precisaria decidir sozinha quanto alocar, e um texto que cresce sozinho é um `append` embutido. As duas coisas escondem alocação, o que vai contra a regra central do MLC. Montar texto num buffer deixa visível **onde** está a memória (o `alloc`) e **quanto** ela ocupa (o tamanho passado ao `alloc`, que `str.format_len`, `str.int_len` e `len` ajudam a calcular).
