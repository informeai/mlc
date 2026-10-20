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

- **Um só espaço de códigos de erro.** Abaixo de 1000 são os do sistema (`errno`: `fs.NOT_FOUND` e `os.NOT_FOUND` são 2, `fs.DENIED` é 13); de 1000 em diante são do MLC e iguais em todos os sistemas (`fs.TOO_BIG` 1000, `fs.NOT_EMPTY` 1001, `os.TOO_LONG`/`process.TOO_LONG`/`bufio.TOO_LONG` 1002, `io.EOF` 1003, `net.REFUSED`... 2001–2005). `os.error_text(code, &msg)` escreve o texto de qualquer um deles:

  ```
  Err(code) => {
      var msg [160]byte                  // sempre basta
      k := os.error_text(code, &msg)     // "não existe", "fim dos dados", "conexão recusada: ..."
      io.eprintf("erro: {:s}\n", &msg[:k])
  }
  ```

- **Nomes em inglês**, como todo o runtime.
- **Testada em MLC.** Cada módulo tem testes em `std/<módulo>_test.mlc`, que rodam com `mlc test std` (ou `make test-std`) e no `go test`. Veja [testes.md](testes.md).
- **O que precisa do sistema** (arquivos, relógio, `libm`) é declarado como `extern fn` e implementado no runtime C (`mlc_std_<módulo>_<nome>`). `extern fn` só é aceita dentro da biblioteca padrão; o resto é MLC comum, com as mesmas checagens de posse e de tipos de qualquer programa.

## Módulos

### `std/io` e `std/str`

Entrada e saída (`io.print`, `io.printf`, `io.eprint`, `io.eprintf`, `io.write`, `io.write_line`, `io.read_line`), a interface `io.Display` e o `io.Writer` (escrita com buffer fixo na pilha), veja [interfaces.md](interfaces.md); e texto em buffers (`str.copy`, `str.put_int`, `str.parse_int`, `str.format`, `str.format_len`, ...). Veja [texto.md](texto.md). As funções de `str` que podem dar panic por falta de espaço são `extern fn`, para o panic apontar a linha de quem chamou; as outras (`int_len`, `eq`, `eq_bytes`, `parse_int`) são MLC, em [`std/str.mlc`](../std/str.mlc).

**Leitura genérica:** a interface `io.Reader` é qualquer fonte de bytes, e o `std/bufio` lê linhas e blocos de qualquer uma delas.

```
pub interface Reader {
    fn read(self &Self, dst &[]byte) result.Result[int, int]   // Ok(0): fim dos dados
}
```

| Declaração | O que é |
|---|---|
| `io.Reader` | implementada por `fs.File`, `net.Conn`, `io.Stdin` e pelos tipos do programa (`impl io.Reader for MinhaFonte`) |
| `io.stdin() Stdin` | a entrada padrão como `Reader` (um valor na pilha, nada para liberar); `read` para depois de um `\n`. Usa o mesmo buffer de `io.read_line`, então os dois podem ser misturados |
| `io.EOF` (1003) | o erro dos leitores com buffer no fim dos dados |

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
| `float64_bits(x) uint64`, `float64_from_bits(u) float64` | os bits IEEE 754 de um real, e de volta (`float32_bits`, `float32_from_bits` para `float32`) |

### `std/sort`

Ordenam no lugar um `&[]T` (um slice emprestado, ou um array com `&a`). A ordem é a de `cmp.Ord`: números, `bool`, `string`, slices (`[][]byte`, linhas de texto) e structs com `impl cmp.Ord` (ou `impl auto cmp.Ord`). Os elementos trocam de lugar por posse, sem cópias, então donos também são ordenados.

| Função | O que faz |
|---|---|
| `sort[T owned + cmp.Ord](s &[]T)` | ordena em ordem crescente (quicksort com inserção nos pedaços pequenos; não aloca) |
| `is_sorted[T owned + cmp.Ord](s &[]T) bool` | já está em ordem? |
| `reverse[T owned](s &[]T)` | inverte a ordem |
| `swap[T owned](s &[]T, i int, j int)` | troca dois elementos (por posse) |
| `sort_by[T any](s &[]T, less fn(&T, &T) bool)` | ordena pela função `less` (para structs ou outra ordem) |
| `sort_by_with(s, &ctx, less)` | ordena por `less(&ctx, &a, &b)`, que consulta o contexto (os ids pela prioridade guardada num `map`, por exemplo) |
| `is_sorted_by[T any](s &[]T, less fn(&T, &T) bool) bool` | já está na ordem de `less`? |

### `std/cmp`

As interfaces `Eq` (`eq(self &Self, other &Self) bool`) e `Ord` (`cmp(self &Self, other &Self) int`: negativo, zero ou positivo), já implementadas para os números, `bool`, `string` e os slices (ordem lexicográfica). Para structs e enums, `impl auto cmp.Eq` e `impl auto cmp.Ord`. Veja [interfaces.md](interfaces.md#igualdade-ordem-e-hash-cmpeq-cmpord-e-hashhash).

### `std/slices`

Operações sobre `&[]T` que comparam por empréstimo (servem para donos e para structs com `cmp.Eq`/`cmp.Ord`). Não alocam.

| Função | O que faz |
|---|---|
| `index(s, &x) int` | posição do primeiro igual a `x`, ou `-1` |
| `contains(s, &x) bool`, `count(s, &x) int` | |
| `equal(a, b) bool` | mesmo tamanho e mesmos elementos |
| `search(s, &x) int` | num slice ordenado: onde `x` está ou entraria |
| `binary_search(s, &x) int` | num slice ordenado: a posição de `x`, ou `-1` |
| `min_index(s) int`, `max_index(s) int` | o menor e o maior (o primeiro, no empate); `-1` se vazio |
| `dedup(s) int` | num slice ordenado, junta os únicos em `s[:n]` (as repetições ficam depois, nada é liberado) |
| `index_by(s, &ctx, pred) int`, `count_by(s, &ctx, pred) int` | pelo resultado de `pred(&ctx, &x)`: o contexto leva o que procurar (um limite, um nome) |
| `fill(s, x)` | põe `x` em todas as posições (só para tipos copiáveis) |

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
| `split_next(s, sep, &pos) Span` | o próximo campo separado por `sep` (`&s[f.start:f.end]`) e avança `pos`; acabou quando `pos > len(s)`. Campos vazios contam |
| `word_next(s, &pos) Span` | a próxima palavra (separada por espaços); um `Span` vazio no fim |
| `to_upper(s)`, `to_lower(s)` | trocam a caixa no lugar (ASCII) |

```
pos := 0
for pos <= len(line) {
    f := text.split_next(&line, ',', &pos)
    ... &line[f.start:f.end] ...
}
```

### `std/utf8`

| Função | O que faz |
|---|---|
| `decode(s, i) Rune` | o caractere que começa em `s[i]`: `r.code` e `r.size` (1 a 4 bytes). Bytes inválidos dão `INVALID` (U+FFFD) com tamanho 1 |
| `count(s) int` | quantos caracteres (cada byte inválido conta como um) |
| `valid(s) bool` | `s` é UTF-8 válido? (formas longas demais e surrogates não são) |
| `encode(dst, code) int` | escreve o caractere no começo de `dst` e devolve quantos bytes usou |

### `std/bytes`

Texto que cresce, com o crescimento à vista em [`std/bytes.mlc`](../std/bytes.mlc), como o `vec`: `new` aloca, `reserve` dobra a capacidade com `realloc`.

```
b := bytes.new(64)
b.push_str("total: ")
b.push_int(42)
b.reserve(str.format_len(" ({:.1}%)", p))          // formatar direto no Buf
b.len += str.format(&b.data[b.len:], " ({:.1}%)", p)
io.write_line(&b.data[:b.len])                     // o texto é &b.data[:b.len]
b.free()
```

| Método | O que faz |
|---|---|
| `bytes.new(cap) *Buf` | aloca um `Buf` vazio |
| `reserve(n)` | garante espaço para mais `n` bytes |
| `push_byte`, `push_str`, `push_bytes`, `push_int` | acrescentam no fim |
| `truncate(n)`, `clear()` | encurtam o texto (a capacidade fica) |
| `eq(s string) bool` | compara com um texto |
| `free()` | libera |

O texto é `&b.data[:b.len]` (os campos são públicos): um método não pode devolver esse sub-slice, porque a visão é criada na hora.

### `std/arena`

Memória de rascunho para uma tarefa (um pedido HTTP, um arquivo, um lote): um `alloc` no começo, os pedaços saem dela em sequência sem um `alloc` cada, `reset` devolve tudo para a próxima tarefa, e `free` no fim. O custo de alocação fica constante, e esquecer de liberar um pedaço não existe: o `reset` leva todos.

```mlc
a := arena.new(64 * 1024)               // o único alloc
for ... cada pedido ... {
    nome := a.copy(&campo)              // um pedaço (Span)
    if a.eq(nome, "admin") { ... }
    a.reserve(64)
    k := str.format(&a.buf[a.used:a.used + 64], "id={}", id)
    chave := a.commit(k)                // escrito direto na arena
    ...
    a.reset()                           // tudo de volta; o buffer fica
}
a.free()
```

Um pedaço é uma `Span`: a posição, o tamanho e a geração da arena. Não é um empréstimo, então a arena pode crescer (`realloc`, dobrando) sem invalidar nada. Cada `reset` muda a geração, e usar uma `Span` de antes do `reset` dá panic: é o uso depois de liberar, pego na execução, em vez de ler os dados de outra tarefa. Para ler o buffer direto, confira antes com `a.check(s)` e use `&a.buf[s.at:s.at + s.n]`.

| Declaração | O que faz |
|---|---|
| `new(cap) *Arena`, `new_fixed(cap) *Arena`, `a.free()` | cria (a fixa nunca cresce: passar do limite dá panic) e libera |
| `a.alloc(n) Span`, `a.copy(&src) Span`, `a.copy_str(s) Span` | um pedaço zerado ou copiado |
| `a.reserve(n)`, `a.commit(n) Span` | escrever direto em `&a.buf[a.used:]` e transformar em pedaço |
| `a.check(s)`, `a.len(s)`, `a.text(s, &dst)`, `a.eq(s, texto)`, `a.get(s, i)`, `a.set(s, i, b)` | usar um pedaço (todos conferem a geração) |
| `a.reset()` | devolve tudo; os pedaços de antes deixam de valer |
| `a.in_use()`, `a.capacity()`, `a.high_water()` | o uso, o tamanho do buffer e o máximo já usado (o tamanho certo para o próximo `new`) |

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
| `each_with(&ctx, f fn(&C, &T))` | qualquer | chama `f` com cada elemento, em ordem, e o contexto |

Os índices são checados: fora de `0..len-1`, panic.

### `std/deque`

Fila com as duas pontas rápidas (buffer circular): `push` e `pop` no começo e no fim em O(1). É a fila de uma thread só (busca em largura, tarefas pendentes, histórico com limite, janela deslizante); entre threads, use `std/chan`. Aloca como o `vec`: `new` aloca, os `push` chamam `grow` quando falta espaço, e `free` libera. Os elementos podem ser donos: `push` move para dentro, `pop` devolve a posse.

```
q := deque.new[int](16)
q.push_back(start)
for q.len() > 0 {
    p := q.pop_front()
    ... q.push_back(vizinho) ...
}
q.free()
```

| Método | O que faz |
|---|---|
| `deque.new[T owned](cap) *Deque[T]` | cria (aloca o buffer) |
| `push_back(x)`, `push_front(x)` | põe no fim ou no começo (move `x`) |
| `pop_front() T`, `pop_back() T` | tira e devolve a posse; panic se vazia |
| `front() &T`, `back() &T`, `at(i) &T` | empresta, sem tirar (a fila fica congelada enquanto o empréstimo é usado) |
| `get(i) T` | cópia do elemento `i` (só `T` copiável) |
| `len()`, `cap()`, `grow()` | tamanho, capacidade, dobrar a capacidade |
| `clear()`, `free()` | liberam os elementos; `free` libera também a fila |

Para guardar só os últimos N, tire o mais antigo antes de pôr (`if q.len() == N { old := q.pop_front(); drop(old) }`): com `new[T](N)`, a fila nunca cresce.

### `std/heap`

Fila de prioridade: `pop` devolve sempre o menor elemento (ou o maior, com `new_max`), em O(log n). A ordem é a de `cmp.Ord`: números, `string`, slices e structs com `impl cmp.Ord` (à mão, para ordenar por um campo) ou `impl auto cmp.Ord`. Aloca como o `vec`, e os elementos podem ser donos.

```
struct Timer {
    due int
    id int
}
fn (t &Timer) cmp(o &Timer) int { return t.due.cmp(&o.due) }
impl cmp.Ord for Timer

timers := heap.new[Timer](16)
timers.push(Timer{due: 30, id: 1})
timers.push(Timer{due: 10, id: 2})
next := timers.pop()                    // o de due 10
timers.free()
```

| Método | O que faz |
|---|---|
| `heap.new[T owned + cmp.Ord](cap) *Heap[T]`, `heap.new_max[...]` | cria: o menor sai primeiro, ou o maior |
| `push(x)` | põe (move `x`) |
| `pop() T` | tira o primeiro e devolve a posse; panic se vazio |
| `peek() &T` | empresta o primeiro, sem tirar |
| `replace_top(x) T` | troca o primeiro por `x` e devolve o antigo, numa passada (o passo de um top-K: com `new` e K elementos, cada valor maior que o topo entra no lugar dele) |
| `len()`, `cap()`, `grow()`, `clear()`, `free()` | como no `vec` |

Elementos equivalentes (`cmp` igual a 0) saem em qualquer ordem entre si; para desempatar, compare mais um campo. Um ponteiro `?*Job` não implementa `cmp.Ord` nesta versão: para itens com donos, o heap guarda a chave e o índice (o `Timer` acima, com `id` sendo a posição num `vec.Vec[?*Job]`).

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

Para uma semente diferente a cada execução, use `rand.seeded()` (ou `uint64(time.now_ms())`).

O `Rand` é previsível: quem vê alguns números adivinha os próximos. Para segurança (tokens, chaves, senhas temporárias, identificadores que não podem ser adivinhados), use os bytes do sistema operacional:

| Declaração | O que faz |
|---|---|
| `secure(dst &[]byte)` | preenche `dst` com bytes da fonte criptográfica do kernel (`getentropy`); panic se o sistema não entregar |
| `secure_u64() uint64` | um `uint64` seguro |
| `secure_int_n(n int) int` | inteiro seguro em `[0, n)`, sem viés (os valores que favoreceriam alguns restos são sorteados de novo) |
| `seeded() Rand` | um `Rand` com semente segura |

```mlc
var token [32]byte
rand.secure(&token)
var texto [64]byte
n := encoding.base64_url(&texto, &token)   // para uma URL ou um cookie
```

### `std/hash`

Hashes em MLC: FNV para tabelas, CRC32 e SHA-256 para integridade e identificadores.

| Função | O que faz |
|---|---|
| `sha256() Sha256` | começa um SHA-256; `h.write(&dados)` quantas vezes precisar, `h.sum(&dst)` escreve os 32 bytes (e não muda o estado) |
| `sha256_sum(dados, dst)` | o SHA-256 de uma vez |
| `crc32(s) uint32`, `crc32_update(crc, s) uint32` | CRC-32 (IEEE, o de zip, gzip e PNG), também em pedaços |
| `fnv32(s &[]byte) uint32`, `fnv64(s &[]byte) uint64` | FNV-1a de 32 e 64 bits |
| `fnv64_str(s string) uint64` | FNV-1a de 64 bits de uma string fixa |
| `mix64(x uint64) uint64` | espalha os bits de um inteiro (finalizador do splitmix64), para usar como chave |
| `interface Hash { fn hash(self &Self) uint64; fn eq(self &Self, other &Self) bool }` | o que uma chave de `map.Map` precisa; dois valores iguais têm o mesmo hash |

`Hash` já vem implementada para os inteiros, `bool`, `string` e slices (`[]byte`, `[]int`...). Para uma struct ou um enum, `impl auto hash.Hash for Ponto` gera `hash` e `eq` a partir dos campos; ou escreva os dois métodos e declare `impl hash.Hash for Ponto`.

```
h := hash.sha256()
for { ... h.write(&buf[:n]) ... }       // um arquivo lido em pedaços
var digest [32]byte
h.sum(&digest)
var txt [64]byte
n := encoding.hex(&txt, &digest)        // "ba7816bf..."
```

O SHA-256 serve para integridade e identificadores, mas não é criptografia completa: assinaturas, senhas e cifras pedem uma biblioteca de C pela FFI.

### `std/hmac`

HMAC-SHA256 (RFC 2104) em MLC, sobre o `hash.sha256`: a assinatura de uma mensagem com uma chave secreta. É o que confere um webhook (Stripe, GitHub), assina um token (JWT com HS256) ou um cookie de sessão. Nada aloca.

| Declaração | O que faz |
|---|---|
| `sha256(key, data, dst &[]byte)` | o HMAC de uma vez, em `dst` (`SIZE` = 32 bytes) |
| `verify(key, data, mac &[]byte) bool` | confere uma assinatura, em tempo constante |
| `equal(a, b &[]byte) bool` | compara em tempo constante: o tempo não depende de onde está a primeira diferença |
| `new(key) Hmac`, `h.write(data)`, `h.sum(dst)` | o mesmo em pedaços |

```mlc
if !hmac.verify(&segredo, &corpo, &assinatura) {
    http.respond_str(&c, 401, "text/plain", "assinatura inválida\n")
    return
}
```

Para conferir uma assinatura ou um token, use `verify` ou `equal`, nunca `str.eq_bytes`: uma comparação comum para no primeiro byte diferente, e o tempo que ela leva diz a quem ataca quantos bytes ele já acertou. A chave pode ter qualquer tamanho; uma maior que 64 bytes é trocada pelo SHA-256 dela, como manda a RFC. Testado com os vetores da RFC 4231.

### `std/uuid`

UUIDs (RFC 9562), identificadores de 128 bits que não se repetem, sem um servidor central. O `UUID` é um valor de 16 bytes; o texto (36 caracteres) vai para um buffer de quem chama. Os bits aleatórios vêm de `rand.secure`.

| Declaração | O que faz |
|---|---|
| `v4() UUID` | aleatório (122 bits) |
| `v7() UUID`, `v7_at(ms) UUID` | ordenado pelo tempo: os 48 bits de cima são os milissegundos desde 1970 |
| `u.text(dst) int` | escreve `0192a3c4-5d6e-7f80-9a1b-2c3d4e5f6a7b` (`TEXT_LEN` = 36) |
| `parse(s, &u) bool` | lê o texto (hex maiúsculo ou minúsculo) |
| `u.version() int`, `u.ms() int`, `u.equal(&o) bool` | a versão, o horário de um v7, igualdade |

Para chave de banco, prefira `v7`: UUIDs criados depois ficam depois na ordem, então o índice cresce no fim em vez de espalhado, e as inserções ficam bem mais rápidas.

### `std/binary`

Números em bytes, para formatos de arquivo e protocolos binários: inteiros e reais de tamanho fixo em big-endian (`_be`, a ordem das redes) ou little-endian (`_le`, a do x86 e do ARM), e inteiros de tamanho variável (varint, o formato do protobuf e do Go). Quem lê recebe um slice emprestado e lê o começo dele; quem escreve escreve no começo de `dst`. A posição fica com quem chama, num sub-slice. Um slice curto demais é panic.

```
var hdr [14]byte
binary.put_u32_be(&hdr, MAGIC)
binary.put_u16_le(&hdr[4:], version)
binary.put_f64_le(&hdr[6:], ratio)

size := binary.u32_be(&buf[off:])
```

| Declaração | O que faz |
|---|---|
| `u16_be(src) uint16`, `u32_be`, `u64_be` e os `_le` | lê um inteiro sem sinal |
| `put_u16_be(dst, v uint16)`, `put_u32_be`, `put_u64_be` e os `_le` | escreve |
| `i16_be(src) int16`, `i32_be(src) int32`, `i64_be(src) int`, `put_i16_be`... e os `_le` | com sinal, em complemento de dois |
| `f32_be(src) float32`, `f64_be(src) float64`, `put_f32_be`, `put_f64_be` e os `_le` | reais IEEE 754 |
| `put_uvarint(dst, v uint64) int`, `put_varint(dst, v int) int` | varint (7 bits por byte; com sinal, em zigzag: números pequenos ocupam poucos bytes); devolve quantos bytes usou |
| `uvarint(src, &v) int`, `varint(src, &v) int` | lê um varint; devolve os bytes usados, `0` se `src` acabou no meio (leia mais e tente de novo) ou `-1` se não cabe em 64 bits |
| `uvarint_len(v) int`, `varint_len(v) int`, `MAX_VARINT` (10) | o tamanho de antemão; um `[binary.MAX_VARINT]byte` sempre basta |

### `std/encoding`

| Função | O que faz |
|---|---|
| `hex(dst, src) int` | hexadecimal minúsculo (2 bytes por byte) |
| `unhex(dst, src) int` | lê hex (maiúsculo ou minúsculo); `-1` se for inválido |
| `base64(dst, src) int` | base64 padrão, com `+`, `/` e `=` |
| `base64_url(dst, src) int` | base64 para URLs e nomes de arquivo (`-` e `_`, sem `=`) |
| `base64_len(n) int` | o tamanho do base64 de `n` bytes, para alocar |
| `unbase64(dst, src) int` | lê as duas formas, com ou sem `=`; `-1` se for inválido. Um `dst` de `len(src) * 3 / 4` bytes sempre basta |

As funções que escrevem dão panic se `dst` for pequeno demais.

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
| `each_with(&ctx, f fn(&C, &K, &V))` | qualquer | o mesmo, com um contexto onde `f` acumula (somas, contagens, um `vec` de resultados) |
| `next(i) int`, `key_at(i) K`, `value_at(i) V` | `key_at`: `K` copiável; `value_at`: `V` copiável | percorrer: `for i := m.next(0); i >= 0; i = m.next(i + 1)` |

### `std/os`

| Declaração | O que faz |
|---|---|
| `arg_count() int` | quantos argumentos o programa recebeu (o 0 é o nome dele) |
| `arg(i int, dst &[]byte) Result[int, int]` | copia o argumento `i` para `dst` |
| `getenv(name string, dst &[]byte) Result[int, int]` | copia o valor da variável de ambiente |
| `setenv(name string, value &[]byte) Result[int, int]` | define a variável de ambiente (vale também para os programas rodados com `std/process`) |
| `cwd(dst) Result[int, int]` | copia o diretório atual |
| `chdir(path) Result[int, int]` | muda o diretório atual |
| `exit(code int)` | encerra o programa |
| `error_text(code int, dst) int` | escreve o texto (em português) de qualquer código de erro da biblioteca; um `[160]byte` sempre basta |
| `NOT_FOUND` (2), `TOO_LONG` (1002) | códigos de erro: não existe; não cabe em `dst` |

### `std/args`

Opções de linha de comando sem alocação: o argumento atual fica num buffer dentro do `Args` (na pilha), e os valores vão para buffers de quem chama.

```
a := args.new()
for a.next() {
    if a.is("-v", "--verbose") {
        verbose = true
    } else if a.value("-o", "--out", &out, &out_n) {   // -o arq, --out arq, --out=arq
        if out_n < 0 { ... faltou o valor ... }
    } else if a.int_value("-n", "", &n, &ok) {
        if !ok { ... não é um número ... }
    } else if a.is_option() {
        ... opção desconhecida ...
    } else {
        k := a.text(&file)                              // posicional
    }
}
```

| Método | O que faz |
|---|---|
| `args.new() Args` | começa a ler os argumentos (sem o nome do programa) |
| `next() bool` | avança; depois de `--`, tudo é posicional |
| `is(short, long string) bool` | o argumento é essa opção? (`""` para não ter uma das formas) |
| `value(short, long, dst, &n) bool` | opção com valor; `n` recebe o tamanho, ou `-1` se o valor faltou |
| `int_value(short, long, &v, &ok) bool` | opção com valor inteiro |
| `is_option() bool` | parece uma opção ainda não reconhecida |
| `text(dst) int` | copia o argumento atual |

### `std/process`

Roda outros programas, sem shell (os argumentos vão direto, sem `*` nem `$VAR` e sem risco de injeção). Os argumentos ficam num buffer fixo dentro do `Cmd`, e a saída capturada vai para um buffer de quem chama.

```
c := process.cmd("git")
c.arg("log")
c.arg_bytes(&branch[:n])
var out [65536]byte
status := 0
match c.run(&out, &status) {
    Ok(k) => io.write(&out[:k])          // status tem o código de saída
    Err(code) => ...                     // NOT_FOUND, TOO_BIG
}
```

| Declaração | O que faz |
|---|---|
| `cmd(name string) Cmd` | começa a linha de comando (o programa é procurado no `PATH`) |
| `arg(s string)`, `arg_bytes(b)` | acrescentam argumentos (até 8 KB e 255 argumentos) |
| `run(out, &status) Result[int, int]` | roda e espera; a saída padrão vai para `out`, o erro padrão continua no terminal |
| `run_inherit(&status) Result[int, int]` | roda com a saída indo direto para o terminal |
| `NOT_FOUND`, `TOO_BIG`, `TOO_LONG` | o programa não existe; a saída não coube (o resto foi descartado e o programa rodou até o fim); os argumentos não cabem |

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
| `stat(path, &info) Result[int, int]` | preenche um `fs.Info` (`size`, `is_dir`, `modified_ms`) |
| `mkdir(path)`, `remove_dir(path)`, `rename(from, to)` | diretórios e nomes (`remove_dir` exige o diretório vazio) |
| `EXISTS`, `NOT_DIR`, `IS_DIR`, `NOT_EMPTY` | mais códigos de erro, iguais em todos os sistemas |

**Arquivos abertos**, para ler e escrever em pedaços (arquivos grandes, logs):

```
err := 0
f := fs.open(&path, fs.READ, &err)          // nil, com o código em err, se falhar
if f != nil {
    var buf [4096]byte
    for {
        match f.read(&buf) {
            Ok(n) => {
                if n == 0 { break }          // fim do arquivo
                ... &buf[:n] ...
            }
            Err(_) => break
        }
    }
    f.close()                                // o verificador cobra este close
}
```

| Declaração | O que faz |
|---|---|
| `open(path, mode, &err) ?*File` | modos `READ`, `WRITE` (cria ou esvazia), `APPEND`, `READ_WRITE` (cria se preciso, sem esvaziar) |
| `f.read(dst) Result[int, int]` | lê até `len(dst)` bytes; `Ok(0)` é o fim |
| `f.write(data) Result[int, int]` | escreve tudo |
| `f.seek(pos) Result[int, int]` | vai para `pos` (do começo); com `pos` negativo, para o fim |
| `f.close()` | fecha |

Um arquivo aberto é um `io.Reader`: `std/bufio` lê linhas dele.

**Diretórios:**

```
d := fs.open_dir(&path, &err)
if d != nil {
    var name [256]byte                       // um [256]byte sempre basta
    for {
        n := d.next(&name)                   // -1 quando acabam; sem . e ..
        if n < 0 { break }
        ... &name[:n] ...
    }
    d.close()
}
```
| `NOT_FOUND` (2), `DENIED` (13), `TOO_BIG` (1000) | códigos de erro; os demais são o `errno` do sistema |

#### Árvores de diretórios

| Declaração | O que faz |
|---|---|
| `walk(&root, &ctx, visit) Result` | visita `root` e tudo dentro, em profundidade; `visit(&ctx, &path, &info)` devolve `CONTINUE`, `SKIP` (não entra no diretório) ou `STOP` |
| `glob(&root, pattern, &ctx, visit) Result` | o mesmo, só com quem casa com o padrão (o caminho a partir de `root`) |
| `glob_match(pattern, &path) bool` | `*` (sem `/`), `?`, `[abc]`, `[a-z]`, `[!x]`, `**` (qualquer coisa, inclusive `/`; `**/` também casa com nada) |
| `mkdir_all(&path)` | cria o diretório e os pais (como `mkdir -p`); o que já existe não é erro |
| `remove_all(&path)` | apaga tudo (como `rm -r`); o que não existe não é erro |
| `is_link(&path) bool` | link simbólico (o `Info` de `walk` e `glob` também tem `is_link`) |
| `temp_dir(&dst)`, `temp_path(prefix, &dst)` | a pasta temporária (`TMPDIR` ou `/tmp`) e um caminho novo nela, com 16 dígitos aleatórios de `rand.secure` |

```mlc
struct Total {
    bytes int
}

fn soma(t &Total, path &[]byte, info &fs.Info) int {
    if !info.is_dir {
        t.bytes += info.size
    }
    return fs.CONTINUE
}

var t Total
fs.glob(&pasta, "**/*.log", &t, soma)
```

Links simbólicos não são seguidos: `walk` visita o link mas não entra num diretório apontado por ele (nada de ciclos), e `remove_all` apaga o link, não o que ele aponta. A profundidade máxima é `MAX_DEPTH` (64), e passar disso dá `TOO_DEEP`.

### `std/bufio`

Leitura com buffer de qualquer `io.Reader`. O `bufio.Reader` guarda um buffer fixo de 4096 bytes dentro dele mesmo (na pilha, se for uma variável local): nada aloca. A fonte **não fica guardada**: cada chamada a recebe emprestada, então quem abriu o arquivo continua dono dele e o fecha quando quiser (o verificador cobra o `close` como sempre).

```
f := fs.open(&path, fs.READ, &err)
if f != nil {
    br := bufio.new[fs.File]()
    var line [1024]byte
    for {
        match br.read_line(&f, &line) {
            Ok(n) => io.printf("{:s}\n", &line[:n])
            Err(code) => {
                if code == bufio.TOO_LONG {
                    continue                     // line ficou cheio; o resto da linha vem na próxima
                }
                if code != io.EOF {
                    ... erro da fonte ...
                }
                break
            }
        }
    }
    f.close()
}
```

| Declaração | O que faz |
|---|---|
| `new[R ref + io.Reader]() Reader[R]` | um leitor vazio para fontes do tipo `R` (`bufio.new[fs.File]()`, `bufio.new[net.Conn]()`, `bufio.new[io.Stdin]()`) |
| `br.read_line(src, dst) Result[int, int]` | uma linha, sem o `\n` (nem o `\r` de um `\r\n`); a última pode não ter `\n` |
| `br.read_until(src, delim, dst) Result[int, int]` | até o byte `delim`, inclusive; sem ele, o que houver até o fim |
| `br.read(src, dst) Result[int, int]` | o que estiver no buffer, ou uma leitura da fonte; pode ler menos que `len(dst)` |
| `br.read_exact(src, dst) Result[int, int]` | enche `dst` inteiro, ou `Err(io.EOF)` se os dados acabarem antes |
| `br.read_byte(src) Result[byte, int]` | um byte |
| `br.buffered() int` | quantos bytes já estão no buffer |
| `TOO_LONG` (1002) | a linha (ou o pedaço até `delim`) não coube: `dst` fica cheio e o resto vem na próxima chamada |

O fim dos dados é `Err(io.EOF)`, e continua valendo nas chamadas seguintes. Os erros da fonte chegam como vieram, depois dos dados que já estavam no buffer. Use sempre a mesma fonte com o mesmo `Reader`: os bytes no buffer são dela.

### `std/config`

Configuração em arquivo: `.env` e INI. O texto fica num buffer de 16 KB dentro da `Config`, com até 256 entradas; os valores são copiados para buffers de quem chama.

```mlc
var cfg config.Config
match config.load_str(".env", &cfg) {
    Ok(_) => {}
    Err(code) => io.eprintf("config: linha {} inválida\n", cfg.bad_line)
}
porta := 8080
cfg.int("PORT", &porta)              // fica o padrão se não existe
var url [256]byte
n := cfg.env("DATABASE_URL", &url)   // a variável de ambiente, se existir, ganha do arquivo
```

| Declaração | O que faz |
|---|---|
| `load(&path, &c)`, `load_str(path, &c)`, `c.parse(&text)` | lê o arquivo (ou o texto); `Err(BAD_LINE)` com `c.bad_line` |
| `c.get(key, &dst) int`, `c.has(key)`, `c.is(key, s)` | o valor (-1 se não existe); numa seção, a chave é `"seção.chave"` |
| `c.int(key, &out) bool`, `c.bool(key, &out) bool` | convertem; sem a chave, `out` fica com o padrão que você pôs |
| `c.env(key, &dst) int` | a variável de ambiente `key`, ou o valor do arquivo |
| `c.len()`, `c.key(i, &dst)`, `c.value_at(i, &dst)` | para listar tudo |

O formato: `chave=valor` por linha; `#` e `;` no começo da linha são comentários; `export ` antes da chave é aceito; `[seção]` começa uma seção. Entre aspas duplas, o valor aceita `\n \t \\ \"` e pode ter `#`; entre aspas simples, fica como está; sem aspas, ` #` ou ` ;` começa um comentário. Uma chave repetida vale pela última vez. `c.bool` aceita `true/false`, `1/0`, `yes/no` e `on/off`.

### `std/csv`

CSV (RFC 4180). O leitor lê em fluxo de qualquer `io.Reader`, um registro por vez, com os campos num buffer de 16 KB: um arquivo de gigabytes passa com memória fixa. O escritor monta as linhas num buffer de 16 KB, com aspas onde precisa.

```mlc
r := csv.new[fs.File]()
r.sep = ';'                          // o Excel em português usa ;
for {
    match r.next(&f) {
        Ok(n) => {
            var nome [128]byte
            k := r.field(0, &nome)
            preco := 0
            r.int(2, &preco)
        }
        Err(code) => break           // io.EOF no fim; BAD_QUOTE ou TOO_LONG
    }
}
```

| Declaração | O que faz |
|---|---|
| `new[R]()`, `r.sep` | o leitor (separador `,`, ou outro) |
| `r.next(&src) Result` | o próximo registro: quantos campos; `Err(io.EOF)` no fim |
| `r.field(i, &dst) int`, `r.is(i, s)`, `r.int(i, &out)`, `r.float(i, &out)`, `r.len()`, `r.line` | os campos (o primeiro é 0) e a linha do registro |
| `Writer` com `w.sep`, `w.crlf`, `w.add(&s)`, `add_str`, `add_int`, `add_float`, `w.end()`, `w.reset()` | escreve em `&w.buf[:w.n]` |

Campos entre aspas podem ter o separador, quebras de linha e aspas (`""`); linhas vazias são puladas.

### `std/path`

Caminhos com barras (`/`), só pelo texto (nada olha o disco). Cada função escreve o resultado num buffer de quem chama e devolve quantos bytes usou; o resultado nunca é maior que a entrada (no `join`, que a soma das duas mais a barra), e não caber é panic.

```
var out [4096]byte
n := path.join_str(&out, &dir[:k], "config.json")   // "docs/" + "config.json" → "docs/config.json"
var e [16]byte
m := path.ext(&e, &out[:n])                          // ".json"
```

| Declaração | O que faz |
|---|---|
| `clean(dst, p) int` | a forma mais curta equivalente, com as regras do `path.Clean` do Go: junta barras repetidas, tira os `.`, resolve os `..` (num caminho relativo, os que sobram no começo ficam; num absoluto, somem) e tira a barra do fim; vazio vira `.` (`"a//b/./c/../d/"` → `"a/b/d"`) |
| `join(dst, a, b) int`, `join_str(dst, a, b string) int` | `a/b`, limpo; se `b` for absoluto, o resultado é `b` |
| `dir(dst, p) int` | tudo menos o último elemento, limpo (`"a/b/c.txt"` → `"a/b"`, `"c.txt"` → `"."`) |
| `base(dst, p) int` | o último elemento (`"a/b/"` → `"b"`, `"/"` → `"/"`, `""` → `"."`) |
| `ext(dst, p) int` | a extensão do último elemento, com o ponto (`"x.tar.gz"` → `".gz"`; sem extensão, 0) |
| `is_abs(p) bool` | começa em `/` |

### `std/testing`

Verificações para os testes rodados com `mlc test` (`check`, `fail`, `eq_int`, `eq_uint`, `eq_bool`, `eq_float`, `near`, `eq_str`, `eq_bytes`, `expect_panic`). São `extern fn`, para que a falha aponte a linha do teste. Veja [testes.md](testes.md).

### `std/thread`

Threads do sistema: `thread.spawn(f, ctx, stack)` e `thread.join(t)` (embutidas no compilador), `thread.cpu_count()` e as constantes `STACK_16KB` a `STACK_8MB`. O contexto vai para a thread por posse, e o resultado volta pelo `join`. Veja [concorrencia.md](concorrencia.md).

### `std/chan`

Canais com capacidade fixa: `chan.new[T](n)`, as pontas `chan.Sender[T]` (`send`, `clone`, `close`) e `chan.Receiver[T]` (`recv`, `sender`, `clone`, `close`), e o resultado `chan.Recv[T] { Got(value T); Closed }`. Os valores passam por posse. Veja [concorrencia.md](concorrencia.md#canais-import-stdchan).

### `std/mutex` e `std/atomic`

`mutex.new(v)`, com `lock`/`get`/`unlock`/`wait`/`notify` e handles que precisam de `close`; `atomic.new_int` e `atomic.new_bool`, com `load`, `store`, `add`, `swap` e `compare_swap`. Veja [concorrencia.md](concorrencia.md#mutex-import-stdmutex).

### `std/time`

| Função | O que faz |
|---|---|
| `now_ms() int` | milissegundos desde 1970 (relógio de parede) |
| `mono_ns() int` | nanossegundos de um relógio monotônico, para medir intervalos |
| `sleep_ms(ms int)` | espera |
| `utc(ms) Date`, `local(ms) Date` | decompõem um instante em UTC ou no fuso do sistema |
| `to_ms(&d) int` | de volta para milissegundos (no fuso da data) |
| `iso(dst, &d) int` | escreve ISO 8601: `2026-10-17T14:05:09Z`, com `.123` e `-03:00` quando houver |
| `parse_iso(s, &ms) bool` | lê ISO 8601 (com `T` ou espaço, milissegundos e fuso opcionais, ou só a data) |
| `days_in_month(ano, mês) int` | com os anos bissextos |

`Date` tem `year`, `month` (1 a 12), `day`, `hour`, `minute`, `second`, `ms`, `weekday` (0 é domingo) e `offset_min` (o fuso, em minutos a leste de UTC). As contas são do calendário gregoriano para qualquer ano, em MLC, sem alocar.

```
d := time.local(time.now_ms())
var buf [32]byte
n := time.iso(&buf, &d)                     // 2026-10-17T11:05:09-03:00
```

### `std/net`

TCP com handles que o verificador obriga a fechar. Os dados passam por buffers de quem chama.

```
err := 0
l := net.listen(&host, 8080, &err)          // host vazio: todas as interfaces; porta 0: o sistema escolhe
for {
    c := l.accept(&err)
    if c == nil { break }
    t := thread.spawn(handle, c, thread.STACK_64KB)   // uma thread por conexão
    thread.join(t)
}
l.close()
```

| Declaração | O que faz |
|---|---|
| `listen(host, port, &err) ?*Listener` | escuta em `host:port` |
| `l.accept(&err) ?*Conn` | espera a próxima conexão |
| `l.port() int` | a porta (útil com a porta 0) |
| `l.set_timeout(ms)` | limite de espera do `accept` (-1: sem limite); passado o tempo, `accept` devolve `nil` com `err` `TIMEOUT`, e o laço pode conferir um `signal.received` antes de voltar a esperar |
| `connect(host, port, &err) ?*Conn` | conecta (nomes são resolvidos, IPv4 e IPv6) |
| `c.read(dst) Result[int, int]` | lê o que chegar; `Ok(0)`: o outro lado fechou |
| `c.write(data) Result[int, int]` | manda tudo |
| `c.set_timeout(ms) Result[int, int]` | limite de espera de `read` e `write` (erro `TIMEOUT`) |
| `c.shutdown() Result[int, int]` | avisa que não vai mandar mais nada (o outro lado lê `Ok(0)`) |
| `close()` | fecha o listener ou a conexão |
| `REFUSED`, `NO_HOST`, `IN_USE`, `TIMEOUT`, `RESET` | códigos de erro iguais em todos os sistemas |

Uma conexão é um `io.Reader`: `bufio.new[net.Conn]()` lê linhas dela (protocolos de texto, como HTTP e SMTP).

Escrever numa conexão que o outro lado fechou devolve `RESET`, em vez de encerrar o programa com `SIGPIPE`. `tests/run/servidor.mlc` tem um serviço completo: uma thread por conexão, pedido e resposta em JSON.

### `std/signal`

Sinais do sistema: Ctrl+C (`INT`), o pedido de parada do Docker, do systemd e do Kubernetes (`TERM`), `HUP`, `QUIT`, `USR1` e `USR2`. Sem `listen`, o sinal faz o que sempre fez (`INT` e `TERM` encerram o programa na hora). Com `listen`, ele só é **anotado**: o programa confere (`received`) ou espera (`wait`) e decide, no ponto que escolher, como encerrar (fechar conexões, gravar o que falta). O tratador de verdade, no C, só marca o sinal e acorda quem está em `wait`.

```
signal.listen_shutdown()                     // INT e TERM
l.set_timeout(200)                           // o accept acorda para conferir
for !signal.received(signal.TERM) && !signal.received(signal.INT) {
    c := l.accept(&err)
    if c == nil { continue }                 // TIMEOUT
    ...
}
... encerra com calma ...
```

| Declaração | O que faz |
|---|---|
| `listen(sig)`, `listen_shutdown()` | passa a anotar o sinal (`listen_shutdown`: `INT` e `TERM`) |
| `received(sig) bool`, `clear(sig)` | o sinal chegou? (vale para todas as threads e fica até `clear`) |
| `wait(ms) int` | espera um sinal anotado (ou `ms` milissegundos; `-1`: sem limite) e devolve o código, ou `0` se o tempo acabou; volta na hora se um já chegou |
| `ignore(sig)`, `restore(sig)` | não ver o sinal; voltar ao comportamento normal |
| `raise(sig)` | manda o sinal para o próprio programa (útil em testes) |
| `HUP` (1), `INT` (2), `QUIT` (3), `USR1` (10), `USR2` (12), `TERM` (15) | os códigos, iguais em todos os sistemas (o runtime traduz: `SIGUSR1` é 30 no macOS) |

### `std/log`

Registro com nível, horário e linhas inteiras mesmo com várias threads. O logger é um **objeto**, não um estado global: quem o cria passa adiante (emprestado, ou um `clone` para outra thread) e fecha.

```
lg := log.new(log.STDERR, log.INFO)
lg.info("servidor ouvindo na porta {}", port)
lg.warn("conexão lenta: {} ms", ms)
lg.debug("não sai: abaixo de INFO")
t := thread.spawn(worker, lg.clone(), thread.STACK_64KB)
...
lg.close()
```

```
2026-10-19T14:03:22.123-03:00 INFO  servidor ouvindo na porta 8080
2026-10-19T14:03:22.124-03:00 WARN  conexão lenta: 812 ms
```

| Declaração | O que faz |
|---|---|
| `new(dest, level) *Logger` | logger em `STDOUT` ou `STDERR`, do nível `level` para cima |
| `open(path, level, &err) ?*Logger` | logger num arquivo (acrescenta no fim; cria se preciso) |
| `lg.debug(...)`, `lg.info(...)`, `lg.warn(...)`, `lg.error(...)` | uma linha, com o formato do `io.printf` (conferido na compilação) |
| `lg.write(level, msg)` | uma linha já pronta |
| `lg.enabled(level) bool`, `lg.level()`, `lg.set_level(level)` | o nível mínimo (vale para todos os handles) |
| `lg.set_time(on)` | liga ou desliga o horário (desligado, a saída fica igual em toda execução) |
| `lg.clone() *Logger`, `lg.close()` | outro handle para outra thread; cada um precisa de `close`, e o último fecha o arquivo |
| `DEBUG`, `INFO`, `WARN`, `ERROR` | os níveis |

Os métodos com formato são escritos pelo compilador como o `w.printf`: os valores são calculados sempre (da esquerda para a direita), mas só são formatados se o nível estiver ativo. A linha é montada num buffer da thread no runtime (2048 bytes; o que passar fica de fora) e escrita de uma vez, com a trava do logger. Nada aloca por linha.

### `std/sync`

Um grupo de threads esperadas de uma vez. Veja [concorrencia.md](concorrencia.md#grupo-de-threads-import-stdsync).

| Declaração | O que faz |
|---|---|
| `group(cap) *Group` | cria o grupo, com espaço para `cap` threads |
| `g.add(t *thread.Thread)` | o grupo fica com a thread (só threads sem resultado); cheio, recolhe as que terminaram ou espera a mais antiga |
| `g.reap() int` | espera as que já terminaram (sem bloquear nas outras) e devolve quantas |
| `g.len() int` | quantas threads estão no grupo |
| `g.wait()` | espera todas, na ordem em que entraram, e libera o grupo (o verificador cobra) |

### `std/sqlite`

Banco de dados num arquivo, com SQL e transações. A biblioteca do sistema é carregada na primeira chamada (`dlopen`), então quem não usa o módulo não precisa dela, e quem usa não precisa de flags de ligação. `MLC_SQLITE_LIB` escolhe outro arquivo; `available()` diz se ela foi achada.

O banco (`DB`) e a consulta preparada (`Stmt`) são handles: o verificador cobra o `close` de cada um. O texto entra emprestado (o SQLite guarda uma cópia) e sai copiado para buffers de quem chama, então nada aloca do lado do MLC.

```mlc
err := 0
db := sqlite.open_str("loja.db", &err)          // ":memory:" para um banco só na memória
if db == nil {
    return
}
db.exec_str("CREATE TABLE IF NOT EXISTS item (id INTEGER PRIMARY KEY, nome TEXT, preco INTEGER)")

st := db.prepare_str("INSERT INTO item (nome, preco) VALUES (?, ?)", &err)
if st != nil {
    db.begin()
    st.bind_str(1, "café")
    st.bind_int(2, 1250)                          // dinheiro em centavos
    st.run()                                      // 0, ou sqlite.CONSTRAINT...
    db.commit()
    st.close()
}

q := db.prepare_str("SELECT id, nome FROM item WHERE preco > ?", &err)
if q != nil {
    q.bind_int(1, 1000)
    var nome [256]byte
    for q.next() {
        n := q.text(1, &nome)
        io.printf("{} {:s}\n", q.int(0), &nome[:n])
    }
    q.close()
}
db.close()
```

| Declaração | O que faz |
|---|---|
| `open(&path, &err)`, `open_str(path, &err)`, `open_read_only(&path, &err)` | abre (ou cria) o banco; espera até 5 s por um lock de outro processo |
| `db.exec(&sql)`, `db.exec_str(sql)` | comandos sem resultado; devolve 0 ou o código do erro |
| `db.prepare(&sql, &err)`, `db.prepare_str(sql, &err)` | compila um comando com `?` |
| `db.begin()`, `commit()`, `rollback()` | transação |
| `db.changes()`, `db.last_insert_id()`, `db.error(&dst)` | linhas mudadas, o último id, a mensagem do último erro |
| `st.bind_int`, `bind_float`, `bind_text`, `bind_str`, `bind_blob`, `bind_null` | os parâmetros (o primeiro `?` é 1); panic com um índice que não existe |
| `st.next() bool` | a próxima linha do resultado |
| `st.run() int` | roda um INSERT, UPDATE ou DELETE e volta ao começo |
| `st.reset()` | volta ao começo e limpa os parâmetros |
| `st.int(i)`, `float(i)`, `text(i, &dst)`, `blob(i, &dst)`, `text_len(i)`, `kind(i)`, `is_null(i)` | as colunas da linha atual (a primeira é 0); `text` dá panic se não couber |
| `st.columns()`, `st.column_name(i, &dst)` | as colunas do resultado |

Os erros são 4000 mais o código do SQLite: `ERROR` (4001), `BUSY` (4005), `CANTOPEN` (4014), `CONSTRAINT` (4019), `MISUSE` (4021), e `UNAVAILABLE` (4900) quando a biblioteca não existe. `os.error_text` dá o texto, e `db.error(&buf)` a mensagem do SQLite. Use sempre parâmetros (`?`) para os valores, nunca SQL montado com texto de fora: é o que impede injeção de SQL.

### `std/http`

HTTP/1.1 sobre qualquer `http.Stream` (a interface `read` + `write`, implementada por `net.Conn`; nos testes, por uma conexão falsa). O servidor lê pedidos e escreve respostas, o cliente escreve pedidos e lê respostas. **Nada aloca:** a mensagem recebida (`Message`) guarda um buffer fixo de 8 KB dentro dela, onde ficam o cabeçalho e o começo do corpo; a que sai (`Builder`) monta o cabeçalho num buffer de 4 KB; o corpo vai e vem por buffers de quem chama. Os textos (método, caminho, campos) são copiados para buffers seus ou comparados sem cópia.

```
// servidor: uma conexão (o accept e as threads ficam com você)
var m http.Message
for {
    match m.read_request(&c) {
        Ok(_) => {}
        Err(_) => break                          // io.EOF: o cliente fechou
    }
    if m.method_is("GET") && m.path_is("/health") {
        http.respond_str(&c, 200, "text/plain", "ok\n")
    } else {
        http.respond_str(&c, 404, "text/plain", "não encontrado\n")
    }
    if !m.keep_alive() { break }
}

// cliente
var b http.Builder
b.request("POST", "/api/itens")
b.header("Host", "exemplo.com")
b.header("Content-Type", "application/json")
b.send(&c, &json[:n])                            // Content-Length automático
var r http.Message
r.read_response(&c)
match r.read_all(&c, &body) { ... }              // r.status() é o código
```

**Mensagem recebida (`Message`):**

| Método | O que faz |
|---|---|
| `read_request(&c)`, `read_response(&c)` | lê o cabeçalho; na mesma conexão (keep-alive), lê o próximo, descartando o corpo que sobrou |
| `method_is(s)`, `method(dst)` | o método |
| `path_is(s)`, `path_has_prefix(s)`, `path(dst)` | o caminho, sem a consulta (sem decodificar) |
| `target(dst)`, `query(dst)` | o alvo inteiro; a consulta |
| `query_param(name, dst) int` | o valor de um parâmetro, decodificado (`%41`, `+`); `-1` se não veio |
| `header(name, dst) int`, `has_header(name)`, `header_is(name, value)` | um campo (nome sem diferenciar maiúsculas); `-1` se não veio |
| `header_count()`, `header_name(i, dst)`, `header_value(i, dst)` | todos os campos |
| `status()`, `keep_alive()`, `content_length()` | o código da resposta; se a conexão continua; o tamanho declarado (`-1`) |
| `read_body(&c, dst)` | até `len(dst)` bytes do corpo, já sem o `chunked`; `Ok(0)` é o fim |
| `read_all(&c, dst)` | o corpo inteiro; `Err(TOO_BIG)` se não couber |
| `skip_body(&c)` | descarta o resto do corpo |

**Mensagem que sai (`Builder`):**

| Método | O que faz |
|---|---|
| `response(status)` | começa uma resposta (`HTTP/1.1 200 OK`) |
| `request(method, target)`, `request_bytes(method, target)` | começa um pedido (lembre do `Host`) |
| `header(name, value)`, `header_bytes`, `header_int` | um campo |
| `send(&c, body)`, `send_str(&c, body)` | termina o cabeçalho (com o `Content-Length`) e manda tudo; um corpo pequeno vai na mesma escrita |
| `start(&c, length)` | manda só o cabeçalho; o corpo vai depois, em pedaços, com `c.write` |

| Função | O que faz |
|---|---|
| `respond(&c, status, content_type, body)`, `respond_str(...)` | uma resposta completa numa linha |
| `status_text(code) string` | `"Not Found"` |
| `escape(dst, src)`, `unescape(dst, src)` | codificação de URL (`%XX`; `unescape` também troca `+` por espaço e devolve `-1` se for inválida) |
| `BAD_MESSAGE` (3001), `HEAD_TOO_BIG` (3002), `TOO_BIG` (1000) | erros: não é HTTP/1.x; o cabeçalho não cabe nos 8 KB (ou tem mais de 64 campos); o corpo não cabe em `dst` |

O corpo pode vir com `Content-Length`, `Transfer-Encoding: chunked` ou, numa resposta sem tamanho, até a conexão fechar. Respostas `1xx`, `204` e `304` não têm corpo. Fica de fora nesta versão: TLS (para HTTPS, ponha o serviço atrás de um proxy, como nginx, Caddy ou o balanceador da nuvem), `Expect: 100-continue`, HTTP/2 e respostas a `HEAD` no cliente.

`tests/run/http_servico.mlc` tem um serviço completo: rotas, JSON, uma thread por conexão (no máximo 64 ao mesmo tempo, pelo `sync.group`), log e encerramento limpo com `TERM`.

`tests/run/http_rotas.mlc` monta o equivalente a um `http.ListenAndServe` à mão: handlers `fn(&net.Conn, &http.Message, &Ctx)` numa tabela fixa de rotas (`Router`, com prefixo para `/itens/{id}`, 404 e 405), o laço de keep-alive com timeout, 431 para cabeçalho grande, 413 para corpo grande e `skip_body` depois de cada handler.

#### Roteador

`Router[C]` escolhe a função de cada pedido pelo método e pelo caminho, numa tabela fixa (até 64 rotas), sem alocação. `C` é o estado que cada função recebe (um logger, uma conexão de banco, contadores). No padrão, `{nome}` casa com um pedaço entre barras e `{nome...}`, no fim, com todo o resto; os valores chegam decodificados em `Params`.

```mlc
fn get_item(c &net.Conn, m &http.Message, p &http.Params, app &App) {
    id := 0
    if !p.int("id", &id) {
        http.respond_str(c, 400, "text/plain", "id inválido\n")
        return
    }
    ...
}

var r http.Router[App]
r.handle("GET", "/itens/{id}", get_item)
r.handle("POST", "/itens", create_item)
r.handle("GET", "/arquivos/{caminho...}", serve_file)
...
// numa thread por conexão (o accept, o limite de conexões e o timeout ficam com você):
c.set_timeout(5000)
http.serve_conn(&c, &r, &app)
c.close()
```

| Declaração | O que faz |
|---|---|
| `r.handle(method, pattern, h)` | acrescenta uma rota (`method` vazio: qualquer um); tentadas na ordem |
| `r.dispatch(&c, &m, &ctx)` | chama a função da rota; 404 sem rota para o caminho, 405 com rota para o caminho mas não para o método |
| `serve_conn(&c, &r, &ctx)` | atende uma conexão até ela fechar: lê cada pedido, chama `dispatch`, descarta o corpo não lido; 431 e 400 para pedidos ruins |
| `p.get(name, &dst) int`, `p.int(name, &out) bool`, `p.is(name, s) bool` | os parâmetros da rota |
| `match_path(pattern, &path, &p) bool` | o casamento sozinho, para testar ou montar outro roteador |

#### Cliente

| Declaração | O que faz |
|---|---|
| `get(url, &m, &body)` | GET; `m` guarda o código e os cabeçalhos, o corpo vai para `body`; devolve o tamanho |
| `post(url, content_type, &data, &m, &body)` | POST |
| `request(method, &url, content_type, &data, &m, &body)` | qualquer método, com a URL montada em tempo de execução |
| `dial(&u, &err) ?*net.Conn` e `b.request_to(method, &u)` | para cabeçalhos próprios: abre a conexão e começa o pedido (linha e `Host`) |

```mlc
var m http.Message
var body [4096]byte
match http.get("http://127.0.0.1:8080/itens/7", &m, &body) {
    Ok(n) => io.printf("{} {:s}\n", m.status(), &body[:n])
    Err(code) => io.printf("erro {}\n", code)   // url.BAD_URL, UNSUPPORTED_SCHEME, net.REFUSED, TOO_BIG...
}
```

Os erros são `url.BAD_URL`, `UNSUPPORTED_SCHEME` (https precisa de TLS, que esta versão não tem), os de `net.connect` e `TOO_BIG` (o corpo não cabe em `body`). Cada chamada abre e fecha a conexão (`Connection: close`).

### `std/url`

URLs (RFC 3986). A `URL` guarda uma cópia do texto num buffer fixo de 2 KB, e as partes são copiadas para buffers de quem chama (ou comparadas sem cópia).

| Declaração | O que faz |
|---|---|
| `parse(&s, &u) bool`, `parse_str(s, &u) bool` | lê `esquema://[user@]host[:porta]/caminho?consulta#fragmento`, ou só `/caminho?consulta` |
| `u.scheme(&dst)`, `u.scheme_is(s)`, `u.host(&dst)`, `u.host_is(s)` | esquema e host (os `_is` sem diferenciar maiúsculas) |
| `u.port() int`, `u.has_port() bool` | a porta escrita, ou a do esquema (80, 443, 21); -1 se não há como saber |
| `u.path(&dst)`, `u.query(&dst)`, `u.fragment(&dst)`, `u.user(&dst)` | as partes, sem decodificar (-1 quando não existem) |
| `u.query_param(name, &dst) int` | um parâmetro da consulta, decodificado |
| `u.target(&dst)` | o alvo de um pedido HTTP: caminho (`/` se vazio) e consulta |
| `u.dial_host(&dst)` | o host como `net.connect` espera (IPv6 sem colchetes) |
| `Query` com `q.add(name, &value)`, `add_str`, `add_int` | monta uma consulta já codificada em `&q.buf[:q.n]` |
| `query_value(&query, name, &dst)`, `escape`, `unescape` | as peças soltas (o `http.escape` e o `http.unescape` são estas) |

