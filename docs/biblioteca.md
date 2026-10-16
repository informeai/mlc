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
- **Erros são valores.** Quem pode falhar devolve `result.Result[int]`, um enum de `std/result`. O `match` obriga a tratar os dois casos:

  ```
  match fs.read_file(&caminho[:n], &buf) {
      Ok(lidos) => io.write(&buf[:lidos])
      Err(code) => io.print("erro", code)
  }
  ```

- **Nomes em inglês**, como todo o runtime.
- **O que precisa do sistema** (arquivos, relógio, `libm`) é declarado como `extern fn` e implementado no runtime C (`mlc_std_<módulo>_<nome>`). `extern fn` só é aceita dentro da biblioteca padrão; o resto é MLC comum, com as mesmas checagens de posse e de tipos de qualquer programa.

## Módulos

### `std/io` e `std/str`

Entrada e saída (`io.print`, `io.printf`, `io.eprint`, `io.eprintf`, `io.write`, `io.write_line`, `io.read_line`), a interface `io.Display` e o `io.Writer` (escrita com buffer fixo na pilha), veja [interfaces.md](interfaces.md); e texto em buffers (`str.copy`, `str.put_int`, `str.parse_int`, ...). Veja [texto.md](texto.md). As funções de `str` que podem dar panic por falta de espaço são `extern fn`, para o panic apontar a linha de quem chamou; as outras (`int_len`, `eq`, `eq_bytes`, `parse_int`) são MLC, em [`std/str.mlc`](../std/str.mlc).

### `std/result`

| Declaração | O que é |
|---|---|
| `enum Result[T any] { Ok(value T); Err(code int) }` | resultado de uma operação que pode falhar; `code` é um código de erro do módulo que o devolveu |

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

| Declaração | O que faz |
|---|---|
| `new[T any](cap int) *Vec[T]` | aloca um vetor vazio com capacidade `cap` |
| `push(x)` | acrescenta no fim; chama `grow` quando está cheio |
| `grow()` | capacidade passa a `2 * cap + 1` (`realloc`) |
| `get(i) T`, `set(i, x)` | acesso checado: fora de `0..len-1` dá panic |
| `pop() T` | remove e devolve o último; vazio dá panic |
| `len() int`, `cap() int`, `clear()` | tamanho, capacidade, esvazia sem liberar |
| `free()` | libera tudo (consome o `*Vec[T]`) |

Os elementos não podem ser donos (como em todo genérico, nesta versão). Para ver os elementos como slice dentro do módulo, use `&v.items[:v.count]`.

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

### `std/os`

| Declaração | O que faz |
|---|---|
| `arg_count() int` | quantos argumentos o programa recebeu (o 0 é o nome dele) |
| `arg(i int, dst &[]byte) Result[int]` | copia o argumento `i` para `dst` |
| `getenv(name string, dst &[]byte) Result[int]` | copia o valor da variável de ambiente |
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
| `file_size(path) Result[int]` | tamanho em bytes |
| `read_file(path, dst) Result[int]` | lê o arquivo inteiro no começo de `dst`; se não couber, nada é lido e o erro é `TOO_BIG` |
| `write_file(path, data) Result[int]` | cria ou substitui o arquivo |
| `append_file(path, data) Result[int]` | acrescenta no fim (cria se preciso) |
| `remove_file(path) Result[int]` | apaga o arquivo |
| `exists(path) bool` | o caminho existe? |
| `NOT_FOUND` (2), `DENIED` (13), `TOO_BIG` (1000) | códigos de erro; os demais são o `errno` do sistema |

### `std/time`

| Função | O que faz |
|---|---|
| `now_ms() int` | milissegundos desde 1970 (relógio de parede) |
| `mono_ns() int` | nanossegundos de um relógio monotônico, para medir intervalos |
| `sleep_ms(ms int)` | espera |
