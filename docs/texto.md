# Texto no MLC

Texto é montado num **buffer `[]byte` que você aloca**. As funções de texto e de entrada e saída **nunca alocam**: escrevem no buffer emprestado, devolvem quantos bytes usaram e dão panic se não couber. Assim, toda alocação de texto aparece no código como `alloc` ou `realloc`, e o tamanho de cada buffer é escolhido por quem programa.

```
import "io"
import "str"

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
- `byte` não faz contas. Para isso, converta: `int(b)`; e de volta: `byte(n)`, que dá panic se `n` estiver fora de 0 a 255.
- Bytes podem ser comparados (`==`, `<`, ...) e usados em `match` com literais: `'a', 'e' => ...`.

## Funções (`import "str"`)

| Função | O que faz |
|---|---|
| `str.copy(dst &[]byte, s string) int` | copia `s` para o começo de `dst`; devolve quantos bytes copiou |
| `str.copy_bytes(dst &[]byte, src &[]byte) int` | copia `src` para o começo de `dst` (podem se sobrepor) |
| `str.put_int(dst &[]byte, n int) int` | escreve `n` em decimal no começo de `dst` |
| `str.int_len(n int) int` | quantos bytes `n` ocupa em decimal (para saber quanto alocar) |
| `str.eq(a &[]byte, s string) bool` | `a` tem o mesmo texto que `s`? |
| `str.eq_bytes(a &[]byte, b &[]byte) bool` | `a` e `b` têm o mesmo texto? |
| `str.parse_int(s &[]byte, out &int) bool` | lê um inteiro decimal (com sinal opcional) de `s` inteiro; em caso de sucesso grava em `out` e devolve `true` |

As funções que escrevem dão panic se o texto não couber em `dst`, com uma mensagem como `str.copy: o texto (3 bytes) não cabe no destino (2 bytes)`.

## Entrada e saída (`import "io"`)

| Função | O que faz |
|---|---|
| `io.print(a, b, ...)` | imprime valores separados por espaço, com quebra de linha no fim |
| `io.write(s &[]byte)` | escreve os bytes de `s`, sem separador nem quebra de linha |
| `io.write_line(s &[]byte)` | escreve `s` e uma quebra de linha |
| `io.read_line(dst &[]byte) int` | lê uma linha (sem o `\n`) em `dst` e devolve quantos bytes leu; `-1` no fim da entrada |

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

Uma `concat(a, b)` precisaria decidir sozinha quanto alocar, e um texto que cresce sozinho é um `append` embutido. As duas coisas escondem alocação, o que vai contra a regra central do MLC. Montar texto num buffer deixa visível **onde** está a memória (o `alloc`) e **quanto** ela ocupa (o tamanho passado ao `alloc`, que `str.int_len` e `len` ajudam a calcular).
