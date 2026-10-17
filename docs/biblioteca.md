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
| `setenv(name string, value &[]byte) Result[int, int]` | define a variável de ambiente (vale também para os programas rodados com `std/process`) |
| `cwd(dst) Result[int, int]` | copia o diretório atual |
| `chdir(path) Result[int, int]` | muda o diretório atual |
| `exit(code int)` | encerra o programa |
| `NOT_FOUND`, `TOO_LONG` | códigos de erro: não existe; não cabe em `dst` |

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
| `connect(host, port, &err) ?*Conn` | conecta (nomes são resolvidos, IPv4 e IPv6) |
| `c.read(dst) Result[int, int]` | lê o que chegar; `Ok(0)`: o outro lado fechou |
| `c.write(data) Result[int, int]` | manda tudo |
| `c.set_timeout(ms) Result[int, int]` | limite de espera de `read` e `write` (erro `TIMEOUT`) |
| `c.shutdown() Result[int, int]` | avisa que não vai mandar mais nada (o outro lado lê `Ok(0)`) |
| `close()` | fecha o listener ou a conexão |
| `REFUSED`, `NO_HOST`, `IN_USE`, `TIMEOUT`, `RESET` | códigos de erro iguais em todos os sistemas |

Escrever numa conexão que o outro lado fechou devolve `RESET`, em vez de encerrar o programa com `SIGPIPE`. `tests/run/servidor.mlc` tem um serviço completo: uma thread por conexão, pedido e resposta em JSON.

### `std/json`

JSON sem alocação: um leitor que devolve um token por vez, sem árvore. Quem lê decide o que guardar e onde alocar.

```
r := json.reader()
for {
    match r.next(&doc) {
        Key(a, b) => {
            if str.eq(&doc[a:b], "name") {
                match r.next(&doc) {
                    String(x, y) => n := json.unescape(&name, &doc[x:y])
                    _ => ...
                }
            } else {
                r.skip(&doc)                    // ignora o valor desta chave
            }
        }
        End => break
        Error(pos) => ...                       // texto inválido em pos
        _ => {}
    }
}
```

| Declaração | O que faz |
|---|---|
| `reader() Reader` | começa a ler (o estado fica na pilha) |
| `r.next(s) Token` | o próximo token: `ObjectStart`, `ObjectEnd`, `ArrayStart`, `ArrayEnd`, `Key(a, b)`, `String(a, b)`, `Number(a, b)`, `True`, `False`, `Null`, `End` ou `Error(at)` |
| `r.skip(s) bool` | pula o próximo valor inteiro (objetos e arrays com tudo dentro) |
| `unescape(dst, raw) int` | tira os escapes de uma string (`\n`, `\u00e9`, pares de surrogates); um `dst` do tamanho de `raw` sempre basta |
| `escape(dst, s) int`, `escape_len(s) int` | põem os escapes, para montar JSON com `str.format` |

`&doc[a:b]` é o conteúdo de uma string (sem as aspas, com os escapes) ou o texto de um número (para `str.parse_int` ou `str.parse_float`). O leitor confere a estrutura e a gramática, e aceita até 64 níveis de profundidade.

Para escrever:

```
n := json.escape(&esc, &name[:k])
m := str.format(&out, "{{\"name\": \"{:s}\", \"age\": {}}}", &esc[:n], 30)
```
