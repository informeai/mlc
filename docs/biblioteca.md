# Biblioteca padrão

A biblioteca padrão é escrita em MLC, fica em [`std/`](../std) e vai embutida no binário do `mlc`. Cada módulo é importado com o prefixo `std/` e usado pelo último nome do caminho:

```
import "std/io"
import "std/math"
import "std/sort"

fn main() {
    var a [4]int = [4]int{3, 1, 4, 1}
    sort.sort(&a)
    io.print(a[0], a[3], math.sqrt(2.0), math.max(3, 7))
}
```

Tudo que vem com a linguagem está aqui, inclusive entrada e saída (`std/io`) e texto (`std/str`), detalhados em [texto.md](texto.md). Até a impressão é MLC: `io.print` e `io.printf` são reescritos pelo compilador em chamadas aos métodos `write` (interface `io.Display`) de cada valor, implementados em [`std/io.mlc`](../std/io.mlc). Veja [interfaces.md](interfaces.md).

## Regras da biblioteca

- **Nada aloca escondido.** As funções escrevem em buffers emprestados por quem chama (`dst &[]byte`) e devolvem quantos bytes usaram. A única exceção é `std/vec`, cujas alocações (`new`, `push` via `grow`) estão escritas em MLC, à vista em [`std/vec.mlc`](../std/vec.mlc).
- **Erros são valores.** Quem pode falhar devolve um `result.Result[T, E]`, o enum de `std/result`; `fs` e `os` devolvem `Result[int, int]` (bytes usados ou código de erro). O `match` obriga a tratar os dois casos:

  ```
  match fs.read_file(&caminho[:n], &buf) {
      Ok(lidos) => io.write(&buf[:lidos])
      Err(code) => io.print("erro", code)
  }
  ```

- **Nomes em inglês**, como todo o runtime.
- **Testada em MLC.** Cada módulo tem testes em `std/<módulo>_test.mlc`, que rodam com `mlc test std` (ou `make test-std`) e no `go test`. Veja [testes.md](testes.md).
- **O que precisa do sistema** (arquivos, relógio, `libm`) é declarado como `extern fn` e implementado no runtime C (`mlc_std_<módulo>_<nome>`). `extern fn` só é aceita dentro da biblioteca padrão; o resto é MLC comum, com as mesmas checagens de posse e de tipos de qualquer programa.

## Módulos

### `std/io` e `std/str`

Entrada e saída (`io.print`, `io.printf`, `io.eprint`, `io.eprintf`, `io.write`, `io.write_line`, `io.read_line`), a interface `io.Display` e o `io.Writer` (escrita com buffer fixo na pilha), veja [interfaces.md](interfaces.md); e texto em buffers (`str.copy`, `str.put_int`, `str.parse_int`, `str.format`, `str.format_len`, ...). Veja [texto.md](texto.md). As funções de `str` que podem dar panic por falta de espaço são `extern fn`, para o panic apontar a linha de quem chamou; as outras (`int_len`, `eq`, `eq_bytes`, `parse_int`) são MLC, em [`std/str.mlc`](../std/str.mlc).

### `std/result`

| Declaração | O que é |
|---|---|
| `enum Result[T owned, E owned] { Ok(value T); Err(err E) }` | resultado de uma operação que pode falhar. `E` pode ser um código (`int`) ou um enum com conteúdo, inclusive donos (uma mensagem alocada por quem cria o erro): o `match` que consome o `Result` entrega a posse, e quem trata libera. Não há atalho de propagação (`try`): cada saída é um `return` escrito. Veja [tipos.md](tipos.md#enums-com-donos) |

### `std/math`

| Declaração | O que faz |
|---|---|
| `PI`, `E` | constantes sem tipo (servem como `float32` ou `float64`) |
| `abs[T number](x T) T` | valor absoluto |
| `min`, `max[T ordered](a T, b T) T` | menor e maior |
| `clamp[T ordered](x T, lo T, hi T) T` | `x` limitado a `[lo, hi]` |
| `sqrt`, `floor`, `ceil`, `round`, `sin`, `cos`, `tan`, `log`, `exp` | `float64` → `float64`, da `libm` |
| `pow(x, y)`, `atan2(y, x)` | `float64` |
| `is_nan(x)`, `is_inf(x)` | `bool` |

### `std/sort`

Ordenam no lugar um `&[]T` (um slice emprestado, ou um array com `&a`).

| Função | O que faz |
|---|---|
| `sort[T ordered](s &[]T)` | ordena em ordem crescente (quicksort com inserção nos pedaços pequenos; não aloca) |
| `is_sorted[T ordered](s &[]T) bool` | já está em ordem? |
| `reverse[T any](s &[]T)` | inverte a ordem |
| `swap[T any](s &[]T, i int, j int)` | troca dois elementos |
| `sort_by[T any](s &[]T, less fn(&T, &T) bool)` | ordena pela função `less` (para structs ou outra ordem) |
| `is_sorted_by[T any](s &[]T, less fn(&T, &T) bool) bool` | já está na ordem de `less`? |

### `std/text`

Funções sobre `&[]byte` que não alocam. As que procuram devolvem um índice, ou `-1` se não acharem.

| Função | O que faz |
|---|---|
| `is_space(b)`, `is_digit(b)`, `is_letter(b)` | classificam um byte (ASCII) |
| `index_byte(s, b) int` | primeira posição de `b` |
| `index_of(s, sub string) int` | primeira posição de `sub` |
| `contains(s, sub)`, `starts_with(s, prefix)`, `ends_with(s, suffix)` | `bool` |
| `count_byte(s, b) int` | quantas vezes `b` aparece |
| `trim_start(s) int`, `trim_end(s) int` | onde o texto começa e termina sem espaços: `&s[trim_start(s):trim_end(s)]` |
| `field_end(s, sep, start) int` | onde termina o campo que começa em `start` (a posição do próximo `sep`, ou `len(s)`) |
| `to_upper(s)`, `to_lower(s)` | trocam a caixa no lugar (ASCII) |

### `std/vec`

Vetor que cresce, para quando o tamanho não é conhecido de antemão. É a parte da biblioteca que aloca, e cada alocação é visível:

```
v := vec.new[int](8)      // aloca o Vec e 8 posições
v.push(1)                 // se não couber, grow dobra a capacidade com realloc
io.print(v.get(0), v.len(), v.cap())
v.free()                  // libera os itens e o Vec; o verificador cobra este free
```

Os elementos podem ser donos (`Vec[T owned]`: `[]byte`, `?*No`...). A posse entra e sai explicitamente:

```
nomes := vec.new[[]byte](8)
nomes.push(copia)          // copia foi movida para o vetor
nome := nomes.take(0)      // a posse volta (fica o valor zero no lugar)
nomes.set(0, nome)         // e vai de novo para o vetor
nomes.free()               // libera cada nome (drop) e o vetor
```

| Declaração | `T` | O que faz |
|---|---|---|
| `new[T owned](cap int) *Vec[T]` | qualquer | aloca um vetor vazio com capacidade `cap` |
| `push(x T)` | qualquer | move `x` para o fim; chama `grow` quando está cheio |
| `grow()` | qualquer | capacidade passa a `2 * cap + 1` (`realloc`) |
| `pop() T` | qualquer | tira o último e devolve a posse; vazio dá panic |
| `take(i) T` | qualquer | tira o elemento `i` (fica o valor zero) e devolve a posse |
| `put(i, x T) T` | qualquer | põe `x` em `i` e devolve o elemento antigo |
| `set(i, x T)` | qualquer | põe `x` em `i` e libera o antigo (`drop`) |
| `swap(i, j)` | qualquer | troca dois elementos |
| `len() int`, `cap() int` | qualquer | tamanho e capacidade |
| `clear()` | qualquer | libera os elementos (`drop`) e deixa o vetor vazio |
| `free()` | qualquer | libera cada elemento (`drop`) e o vetor |
| `get(i) T` | só copiável (`any`) | devolve uma cópia |
| `at(i) &T`, `last() &T` | qualquer | empresta o elemento sem tirá-lo; o vetor fica congelado enquanto o empréstimo é usado |

Os índices são checados: fora de `0..len-1`, panic.

### `std/rand`

Escrito em MLC (splitmix64 com `+%`, `*%`, `^` e `>>`).

Gerador pseudoaleatório (splitmix64) **com estado explícito**: não há gerador global, e a mesma semente dá sempre a mesma sequência.

| Declaração | O que faz |
|---|---|
| `new(seed uint64) Rand` | cria um gerador |
| `r.next() uint64` | próximo número de 64 bits |
| `r.int_n(n int) int` | inteiro em `[0, n)` |
| `r.range(lo int, hi int) int` | inteiro em `[lo, hi)` |
| `r.float() float64` | real em `[0, 1)` |

Para uma semente diferente a cada execução, use `uint64(time.now_ms())`.

### `std/hash`

Hashes não criptográficos, em MLC, para tabelas e checagens rápidas.

| Função | O que faz |
|---|---|
| `fnv32(s &[]byte) uint32`, `fnv64(s &[]byte) uint64` | FNV-1a de 32 e 64 bits |
| `fnv64_str(s string) uint64` | FNV-1a de 64 bits de uma string fixa |
| `mix64(x uint64) uint64` | espalha os bits de um inteiro (finalizador do splitmix64), para usar como chave |
| `interface Hash { fn hash(self &Self) uint64; fn eq(self &Self, other &Self) bool }` | o que uma chave de `map.Map` precisa; dois valores iguais têm o mesmo hash |

`Hash` já vem implementada para os inteiros, `bool`, `string` e slices (`[]byte`, `[]int`...). Para uma struct, escreva `hash` e `eq` e declare `impl hash.Hash for Ponto`.

### `std/map`

Tabela de espalhamento escrita em MLC: endereçamento aberto com sondagem linear, crescendo (o dobro, com `alloc` à vista em `grow`) quando passa de 3/4 de ocupação. As chaves e os valores podem ser donos (`Map[K owned + hash.Hash, V owned]`).

```
conta := map.new[[]byte, int](16)
n := conta.get_or(&palavra, 0)            // palavra: &[]byte, emprestada
if !conta.set(&palavra, n + 1) {          // set só troca o valor de uma chave que existe
    copia := alloc([]byte, len(palavra))  // a chave nova é alocada por quem chama
    str.copy_bytes(&copia, &palavra)
    conta.put(copia, 1)                   // e movida para o mapa
}
conta.each(mostra)                        // fn mostra(k &[]byte, v &int)
conta.free()                              // libera cada chave e o mapa
```

| Método | Tipos | O que faz |
|---|---|---|
| `new[K, V](cap int) *Map[K, V]` | qualquer | aloca com espaço para cerca de `cap` entradas |
| `put(k K, v V)` | qualquer | move `k` e `v` para o mapa; se a chave já existe, troca o valor (a chave nova e o valor antigo são liberados) |
| `set(k &K, v V) bool` | qualquer | troca o valor de uma chave existente; se não existe, libera `v` e devolve `false` |
| `has(k &K) bool` | qualquer | a chave existe? |
| `get(k &K) &V` | qualquer | empresta o valor sem tirá-lo (panic se a chave não existe) |
| `index(k &K) int`, `key_ref(i) &K`, `value_ref(i) &V` | qualquer | posição de uma chave, e empréstimos por posição (também para percorrer com `next`) |
| `get_or(k &K, def V) V` | `V` copiável | cópia do valor, ou `def` |
| `take_or(k &K, def V) V` | qualquer | tira a entrada e devolve a posse do valor (a chave é liberada), ou `def` |
| `remove(k &K) bool` | qualquer | apaga a entrada (libera chave e valor) |
| `len() int`, `clear()`, `free()` | qualquer | tamanho; `clear` e `free` liberam as entradas |
| `each(f fn(&K, &V))` | qualquer | chama `f` com cada entrada |
| `next(i) int`, `key_at(i) K`, `value_at(i) V` | `key_at`: `K` copiável; `value_at`: `V` copiável | percorrer: `for i := m.next(0); i >= 0; i = m.next(i + 1)` |

### `std/os`

| Declaração | O que faz |
|---|---|
| `arg_count() int` | quantos argumentos o programa recebeu (o 0 é o nome dele) |
| `arg(i int, dst &[]byte) Result[int, int]` | copia o argumento `i` para `dst` |
| `getenv(name string, dst &[]byte) Result[int, int]` | copia o valor da variável de ambiente |
| `exit(code int)` | encerra o programa |
| `NOT_FOUND`, `TOO_LONG` | códigos de erro: não existe; não cabe em `dst` |

### `std/fs`

Os caminhos são `&[]byte` (por exemplo, vindos de `os.arg`; para um literal, copie-o antes com `str.copy`). O conteúdo é lido e escrito em buffers de quem chama: para ler um arquivo inteiro, pergunte o tamanho e aloque o buffer.

```
match fs.file_size(caminho) {
    Ok(tam) => {
        buf := alloc([]byte, tam)
        match fs.read_file(caminho, &buf) {
            Ok(n) => io.write(&buf[:n])
            Err(code) => io.print("erro", code)
        }
        free(buf)
    }
    Err(code) => io.print("erro", code)
}
```

| Declaração | O que faz |
|---|---|
| `file_size(path) Result[int, int]` | tamanho em bytes |
| `read_file(path, dst) Result[int, int]` | lê o arquivo inteiro no começo de `dst`; se não couber, nada é lido e o erro é `TOO_BIG` |
| `write_file(path, data) Result[int, int]` | cria ou substitui o arquivo |
| `append_file(path, data) Result[int, int]` | acrescenta no fim (cria se preciso) |
| `remove_file(path) Result[int, int]` | apaga o arquivo |
| `exists(path) bool` | o caminho existe? |
| `NOT_FOUND` (2), `DENIED` (13), `TOO_BIG` (1000) | códigos de erro; os demais são o `errno` do sistema |

### `std/testing`

Verificações para os testes rodados com `mlc test` (`check`, `fail`, `eq_int`, `eq_uint`, `eq_bool`, `eq_float`, `near`, `eq_str`, `eq_bytes`, `expect_panic`). São `extern fn`, para que a falha aponte a linha do teste. Veja [testes.md](testes.md).

### `std/thread`

Threads do sistema: `thread.spawn(f, ctx, stack)` e `thread.join(t)` (embutidas no compilador), `thread.cpu_count()` e as constantes `STACK_16KB` a `STACK_8MB`. O contexto vai para a thread por posse, e o resultado volta pelo `join`. Veja [concorrencia.md](concorrencia.md).

### `std/time`

| Função | O que faz |
|---|---|
| `now_ms() int` | milissegundos desde 1970 (relógio de parede) |
| `mono_ns() int` | nanossegundos de um relógio monotônico, para medir intervalos |
| `sleep_ms(ms int)` | espera |
