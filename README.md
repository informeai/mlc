# MLC — My Lang C

Linguagem compilada com sintaxe simples, inspirada no Go, que gera C e usa o compilador C do sistema para produzir o binário. A memória é gerenciada à mão, como no C (`alloc`/`free` explícitos, sem coletor de lixo), mas o compilador recusa programas com os bugs clássicos do C antes de gerar o código. Veja [docs/memoria.md](docs/memoria.md).

```
arquivo.mlc  →  mlc (Go)  →  program.c  →  cc + runtime C  →  binário
```

## Exemplo

```
import "std/io"

fn primo(n int) bool {
    if n < 2 {
        return false
    }
    for d := 2; d * d <= n; d++ {
        if n % d == 0 {
            return false
        }
    }
    return true
}

fn main() {
    total := 0
    for n := 0; n <= 50; n++ {
        if primo(n) {
            total++
        }
    }
    io.print("primos até 50:", total)
}
```

```sh
make                                     # gera o compilador em bin/mlc
bin/mlc run exemplo.mlc                  # compila e executa
bin/mlc build -o exemplo exemplo.mlc     # gera o executável
bin/mlc build -emit-c exemplo.mlc        # mostra o C gerado
bin/mlc build -L /opt/lib prog.mlc       # -L: onde procurar as bibliotecas de C de link "nome"
bin/mlc build -I ~/mlc-libs prog.mlc     # -I: onde procurar os imports, além do projeto
bin/mlc build -cflags "-O3 -march=native" prog.mlc               # flags a mais para o compilador C
bin/mlc build -ldflags "-framework Cocoa" prog.mlc               # e para a ligação
bin/mlc test -cflags "-fsanitize=address" -ldflags "-fsanitize=address" std   # testes com AddressSanitizer
bin/mlc fmt .                            # formata os .mlc da pasta (no lugar)
bin/mlc fmt -l .                         # só lista os que mudariam
bin/mlc test std                         # roda os testes dos *_test.mlc (aqui, os da std)
bin/mlc test -bench . std                # e mede os benchmarks (fn bench_*)
bin/mlc doc http.Message                 # a documentação de um tipo, função ou módulo
bin/mlc run -sanitize address prog.mlc   # roda sob AddressSanitizer + UBSan (thread: ThreadSanitizer)
make sanitize                            # a suíte sob os sanitizers
make mutate                              # mutantes com bugs de posse: o verificador precisa recusar
make fuzz FUZZTIME=10m                   # programas aleatórios, válidos e com bug, contra o verificador
make enum ENUM=6                         # todos os programas pequenos, contra um modelo de referência
make robust ROBUSTTIME=5m                # fuzz do compilador e do formatador: nunca dá panic
make cover                               # cobertura do checker e as regras sem teste
make test                                # roda os testes
make check                               # gofmt, mlc fmt -l, go vet e testes
```

## O compilador C

O `mlc build` gera C e chama o compilador C em duas etapas: compila o programa e o runtime (`-std=c11 -O2 -Wall -pthread`) e liga com as bibliotecas (`link "nome"`, `-L`) e a `libm`. Dá para acrescentar flags, que vêm depois das do `mlc` (e por isso ganham numa contradição, como `-O3` depois do `-O2`):

| Onde | Compilação | Ligação |
|---|---|---|
| variável de ambiente | `CFLAGS` | `LDFLAGS` |
| opção do `build`, `run` e `test` | `-cflags "..."` | `-ldflags "..."` |

`CC` escolhe o compilador (`CC=gcc mlc build ...`). As flags são separadas por espaço, sem aspas. As que o `mlc` precisa (`-std=c11`, `-pthread`, `-lm`) continuam lá. Se o compilador recusar uma flag, a mensagem diz que pode ter sido ela, em vez de acusar um erro interno do `mlc`.

## Formatação (`mlc fmt`)

`mlc fmt` reescreve os arquivos na forma canônica, como o `gofmt`: indentação de 4 espaços, um espaço em volta dos operadores binários e depois das vírgulas, nenhum depois de operador unário (`-x`, `*p`, `&s`), comentários de fim de linha alinhados. As quebras de linha, os parênteses e os comentários de quem escreveu são mantidos; linhas em branco seguidas viram uma só. Um arquivo com erro de sintaxe não é formatado. `mlc fmt -` lê da entrada padrão e escreve na saída padrão (para editores).

## Documentação (`mlc doc`)

`mlc doc` mostra no terminal a documentação tirada dos comentários `//` logo acima de cada declaração e do comentário do começo do arquivo (a descrição do módulo), como o `go doc`:

```sh
mlc doc                          # os módulos da biblioteca padrão, com a descrição de cada um
mlc doc http                     # o módulo: descrição, constantes, tipos com os métodos, funções
mlc doc http.Message             # um tipo: a declaração (com os campos), a descrição e os métodos
mlc doc http.Message.read_body   # um método (mlc doc http Message.read_body também vale)
mlc doc http.respond             # uma função; constantes, interfaces e variantes de enum também
mlc doc util/texto.limpa         # uma biblioteca sua, procurada como o import (-I e MLC_PATH valem)
mlc doc prog.mlc                 # um arquivo
mlc doc alloc                    # um embutido (io.printf, thread.spawn, owned...); mlc doc builtin lista todos
```

Só aparece o que é `pub`, a não ser num arquivo sem nada `pub` (um programa), que mostra tudo; `-all` mostra tudo sempre. Os nomes dos símbolos não diferenciam maiúsculas. O comentário de uma declaração é o bloco de linhas `//` colado nela; uma linha em branco o separa. Linhas recuadas em 4 espaços dentro do comentário são exemplos de código. O hover do LSP usa os mesmos comentários.

## Testes (`mlc test`)

Um arquivo `nome_test.mlc` declara funções `fn test_nome()` e usa as verificações de `std/testing` (`testing.eq_int(v.len(), 1)`, `testing.eq_str(&buf[:n], "oi")`, `testing.expect_panic()`). O `mlc test` roda cada teste num processo separado, então um panic ou uma falha fica no teste que o causou. Uma verificação que falha aponta a linha dela. A biblioteca padrão tem os testes dela em `std/*_test.mlc`. Veja [docs/testes.md](docs/testes.md).

## Bibliotecas

Um `import "json/parse"` procura `json/parse.mlc` nesta ordem, e vale a primeira pasta que tiver o arquivo:

1. a pasta do arquivo principal;
2. a pasta `lib/` ao lado dele (bibliotecas copiadas para dentro do projeto);
3. as pastas de `-I` (`mlc build -I dir`, `mlc run -I dir`);
4. as pastas da variável `MLC_PATH`, separadas por `:` (bibliotecas compartilhadas entre projetos).

Os imports de dentro de uma biblioteca seguem a mesma busca, então uma biblioteca em `~/mlc-libs/json/` importa as próprias partes como `import "json/lexer"`. O LSP usa `lib/` e `MLC_PATH`, então o editor acha as mesmas bibliotecas. `import "std/..."` é sempre a biblioteca padrão, embutida no `mlc`.

## Editores

`mlc lsp` é um servidor Language Server Protocol (via entrada e saída padrão). Ele marca os erros na linha enquanto você digita, sem precisar salvar: sintaxe, tipos e posse de memória, os mesmos do `mlc build`. Também dá cores semânticas (palavras reservadas, tipos, funções, constantes, literais, comentários) e formata pelo `mlc fmt`. O **hover** mostra a declaração de qualquer nome (variável, parâmetro, constante, função, método, campo, tipo, variante, módulo, função embutida) com o tipo e os comentários `//` logo acima dela. **Ir para a definição** leva à declaração, inclusive em outros arquivos e na biblioteca padrão (copiada para a pasta de cache do sistema, para o editor abrir). As **sugestões ao digitar** olham o contexto: depois de `io.` ou `vec.`, o que o módulo exporta (inclusive as embutidas, como `io.printf` e `thread.spawn`); depois de `x.`, os campos e métodos do tipo de `x` (inclusive os de `impl` e os de structs genéricas, e os de canais, mutex e `io.Writer`); dentro de `import "...`, os módulos; dentro de um `match` sobre enum, as variantes; e, no resto, as variáveis visíveis naquele ponto, as declarações do arquivo, os módulos importados, as funções embutidas, os tipos e as palavras reservadas. Elas funcionam com o texto pela metade: se a linha atual ainda não compila, a análise usa um nome provisório no cursor, a linha em branco ou a última análise que deu certo. Um módulo sem `fn main` é checado pelo `main.mlc` mais próximo que o importa, para os imports resolverem como num build.

**VS Code** (`editors/vscode`): extensão com gramática TextMate (cores imediatas) e cliente LSP.

```sh
cd editors/vscode
npm install --omit=dev
npx @vscode/vsce package --allow-missing-repository --skip-license
code --install-extension mlc-0.1.0.vsix
```

O `mlc` precisa estar no `PATH`, ou configurado em `mlc.path`.

**Neovim 0.11+** (`editors/nvim`): tipo de arquivo, sintaxe, indentação de 4 espaços e o LSP via `vim.lsp.config`/`vim.lsp.enable`, sem nvim-lspconfig. Com lazy.nvim (LazyVim):

```lua
-- ~/.config/nvim/lua/plugins/mlc.lua
return {
  { dir = "~/Projects/mlc/editors/nvim", lazy = false },
}
```

Para outro caminho do executável: `vim.lsp.config("mlc", { cmd = { "/caminho/mlc", "lsp" } })`. Os erros aparecem pelo `vim.diagnostic` e a formatação por `vim.lsp.buf.format()` (o LazyVim já formata ao salvar).

O compilador C usado é o da variável `CC` (padrão: `cc`).

## Estrutura

| Pasta | Papel |
|---|---|
| `cmd/mlc` | linha de comando (`build`, `run`, `test`, `fmt`, `lsp`) |
| `internal/token` | tokens, posições e erros de compilação |
| `internal/lexer` | tokens, com `;` automático no fim da linha |
| `internal/parser` | parser recursive descent → AST |
| `internal/ast` | árvore sintática |
| `internal/checker` | nomes e tipos |
| `internal/codegen` | AST → C11 |
| `internal/driver` | encadeia as fases e chama o `cc` |
| `internal/format` | `mlc fmt`: formatador sobre os tokens, com os comentários |
| `internal/lsp` | `mlc lsp`: diagnósticos, cores semânticas e formatação para editores |
| `editors/` | extensão do VS Code e plugin do Neovim |
| `rt/c` | runtime em C, embutido no binário do `mlc` |
| `std` | biblioteca padrão escrita em MLC (`import "std/io"`, `import "std/math"`), embutida no binário, com os testes em `std/*_test.mlc`. Veja [docs/biblioteca.md](docs/biblioteca.md) |
| `tests/run` | programas e saída esperada (`.out`); entrada opcional (`.in`) |
| `tests/panic` | programas que devem abortar (`.out` + `.err`) |
| `tests/errors` | programas que devem ser rejeitados (`// erro: ...` na 1ª linha) |
| `tests/testing` | arquivo de teste com resultados conhecidos, para conferir o próprio `mlc test` |

Um programa de vários arquivos fica numa pasta própria, com o principal em `main.mlc` (por exemplo `tests/run/projeto/main.mlc`) e os arquivos importados ao lado.

```sh
go test ./...
```

## Garantias atuais

- `realloc` e `free` são as únicas formas de mudar memória no heap: não há crescimento automático.
- Conversão entre números que não cabe vira panic (em C seria comportamento indefinido); constante que não cabe é erro de compilação.
- Estouro de inteiro, divisão por zero e índice fora dos limites viram panic com arquivo e linha do `.mlc`, nunca comportamento indefinido do C.
- Diretivas `#line` no C gerado: panics e erros apontam para o código MLC.
- Impressão checada na compilação: `io.printf("x = {}\n", x)` confere o formato e se cada valor implementa `io.Display`, e vira chamadas diretas, escritas em MLC, sem `printf` do C e sem alocação.
- Variável declarada e nunca lida é erro de compilação, como no Go.
- `match` sem fallthrough e com cobertura obrigatória: esquecer um caso é erro de compilação.
- Literal de struct lista todos os campos (`Ponto{}` é o valor zero explícito): campo novo esquecido é erro de compilação.
- Estouro e divisão por zero em `const` são erros de compilação.
- Memória explícita (`alloc`/`free`) com verificador de posse na compilação: uso depois de `free`, `free` duplo, vazamento, ponteiro nulo e ponteiro pendente são erros de compilação. Veja [docs/memoria.md](docs/memoria.md).
- Ordem de avaliação definida: da esquerda para a direita, sempre. Os argumentos de uma chamada, inclusive de `io.print`, são todos calculados antes da chamada.
- Toda variável começa com valor zero (`0`, `false`, `""`): não existe leitura de memória não inicializada.

## Princípio: toda alocação é explícita

A ideia do MLC é que quem programa entenda e saiba **quanto e onde** está alocando memória. Toda alocação aparece no código como uma chamada explícita (`alloc`, `realloc`), e toda liberação como `free`. Nenhuma construção da linguagem aloca escondido: não há concatenação de strings que aloca, `append` embutido nem cópias implícitas para o heap. O compilador verifica que cada alocação é liberada exatamente uma vez.

## Convenções

- Funções de biblioteca e runtime expostas na linguagem são sempre em inglês (`io.print`).
- Mensagens do compilador são em português.

## Roteiro

- [x] **Marco 1:** `import`, funções com parâmetros e retorno, `int`/`bool`/`string` literal, aritmética checada, `io.print`
- [x] **Marco 2:** `var`, `:=`, atribuição (`=`, `+=`...), `++`/`--`, `if`/`else if`/`else`, `for` (infinito, condição e três cláusulas), `break`/`continue`
- [x] **Marco 3:** `match` com padrões literais e `_`, checagem de cobertura; vira `switch` em C sobre `int` e cadeia de `if` sobre `string`/`bool`; `==`/`!=` entre strings
- [x] **Marco 4:** `struct` (por valor, em qualquer ordem de declaração), literais compostos com todos os campos explícitos, leitura e atribuição de campos, `==` e `io.print` de structs, `const` avaliada na compilação (também como padrão de `match`)
- [x] **Ordem de avaliação:** argumentos, operandos e campos de literal são avaliados da esquerda para a direita, com a ordem escrita no C gerado (temporárias + operador vírgula), sem depender do compilador C
- [x] **Marco 5a:** `alloc`/`free`, `*T`, `?*T`, `&T` e o verificador de posse para variáveis e parâmetros
- [x] **Marco 5b:** ponteiros dentro de structs (listas, árvores), com posse rastreada por campo; checagem de `nil` pelo fluxo (`if`, `for`, `&&`, `||`); laços analisados até o ponto fixo
- [x] **Marco 6:** slices `[]T` donos (`alloc([]T, n)`, `free`), checagem de limites, `len`, `for x in s`, `&[]T`; `len` e índice em strings
- [x] **Slices, parte 2:** `realloc(s, n)` (consome o slice e devolve outro, novos elementos zerados) e sub-slices emprestados `&s[a:b]`
- [x] **Slices de donos:** `[]?*T` e `[][]T`, com `take(&s[i])`, `replace(&s[i], v)` e `for x in drain(s)`; `free(s)` provado seguro na compilação
- [x] **Marco 7:** texto em buffers explícitos: tipo `byte` e literais `'a'`, conversões `int(x)`/`byte(x)`, módulo `str` (`copy`, `put_int`, `int_len`, `eq`, `parse_int`) e `io.write`/`io.read_line`, nenhum deles aloca. Veja [docs/texto.md](docs/texto.md)
- [x] **Vários arquivos:** `import "geometria"` (caminho relativo à pasta do arquivo principal), visibilidade explícita com `pub`, nomes qualificados (`geometria.area`, `geometria.Ponto`), importação circular recusada
- [x] **Métodos:** `fn (p &Ponto) area() int`, chamado como `p.area()`; o receptor (`Ponto`, `&Ponto` ou `*Ponto`) diz se a chamada copia, empresta ou move
- [x] **Genéricos:** `fn maior[T ordered](a T, b T) T`, `struct Par[A any, B any]`, métodos genéricos, inferência; restrições `any`, `comparable`, `ordered`, `number`, `integer`; monomorfização no C
- [x] **Enums com dados:** `enum Forma { Circulo(raio float64); Vazio }`, `match` com desestruturação e cobertura obrigatória, enums genéricos (`Resultado[T]`) com inferência pelo valor ou pelo contexto
- [x] **Números:** `int8`, `int16`, `int32`, `uint16`, `uint32`, `uint64`, `float32`, `float64`; constantes sem tipo; conversões checadas. Veja [docs/tipos.md](docs/tipos.md)
- [x] **Arrays `[N]T`:** tamanho fixo, por valor, na pilha ou em structs, sem `alloc`; `&a` empresta como `&[]T`
- [x] **Biblioteca padrão** em MLC, importada com `std/`: `io`, `str`, `result`, `math`, `sort`, `text`, `vec`, `rand`, `os`, `fs`, `time`, `testing`, `thread`, `chan`, `mutex`, `atomic`, `args`, `process`, `bytes`, `utf8`, `net`, `json`, `encoding`, `cmp`, `slices`, `hash`, `map`, `bufio`, `path`, `binary`, `deque`, `heap`, `signal`, `log`, `sync`, `http`. Nada aloca escondido; erros como `Result[T, E]`. Veja [docs/biblioteca.md](docs/biblioteca.md)
- [x] **`mlc fmt`:** formatador canônico (4 espaços), preservando comentários e quebras de linha; `-l` lista, `-` usa a entrada padrão
- [x] **Interfaces estáticas:** `interface`, `impl` (de uma linha, com bloco ou genérico) e `impl auto`; interfaces como restrição de genérico, com chamadas diretas (sem tabela de métodos). `io.print`/`io.printf`/`io.eprintf` sobre `io.Display`, com a formatação em MLC. Veja [docs/interfaces.md](docs/interfaces.md)
- [x] **Bits e funções como valor:** `& | ^ << >>`, `^x`, aritmética que dá a volta (`+% -% *%`), literais `0xFF`, `0b1010`, `0o17`, `1_000_000`; tipos `fn(int) bool`, funções como valor e chamadas indiretas (sem closures, sem alocação). `std/rand` passou a ser MLC; novos `std/hash` e `sort.sort_by`. Veja [docs/tipos.md](docs/tipos.md)
- [x] **Donos em genéricos:** restrição `owned` (`Vec[[]byte]`, `Vec[?*No]`), `drop(x)` em código genérico, restrição no receptor (`fn (v &Vec[T any]) get`); `std/vec` com `push`/`pop`/`take`/`put`/`set` movendo a posse e `free` liberando os elementos; erros de posse apontados na definição do genérico. Veja [docs/tipos.md](docs/tipos.md)
- [x] **Hash map em MLC:** `std/map` (`Map[K owned + hash.Hash, V owned]`, endereçamento aberto) e a interface `hash.Hash` para inteiros, `bool`, `string` e slices; restrições combinadas com `+`. Veja [docs/biblioteca.md](docs/biblioteca.md)
- [x] **Empréstimos devolvidos e guardados:** `fn (v &Vec[T]) at(i int) &T`, `x := m.get(&k)`, `x := &a[i]`; a origem fica congelada enquanto o empréstimo é usado, conferido na compilação. `vec.at`/`last` e `map.get`/`key_ref`/`value_ref`. Veja [docs/memoria.md](docs/memoria.md)
- [x] **Testes:** `mlc test` roda as funções `test_*` dos arquivos `*_test.mlc`, cada uma num processo, e aceita `-run` e `-v`. As verificações ficam em `std/testing` (`eq_int`, `eq_str`, `near`, `expect_panic`, ...) e apontam a linha que falhou. Cada módulo da `std` tem os testes dele. Veja [docs/testes.md](docs/testes.md)
- [x] **Documentação no terminal:** `mlc doc` (módulo, tipo, função, método ou embutido, da biblioteca padrão ou de bibliotecas suas), com toda declaração `pub` da `std` comentada (conferido em teste). Veja [Documentação](#documentação-mlc-doc)
- [x] **LSP:** `mlc lsp` com diagnósticos ao digitar, cores, formatação, hover, ir para a definição e sugestões ao digitar; extensão para VS Code e configuração para Neovim. Veja [Editores](#editores)

### Próximos passos (em ordem de prioridade)

- [x] **Donos aninhados:** `Vec[?*vec.Vec[int]]`, `Map[[]byte, ?*vec.Vec[[]byte]]`, `Vec[[][]byte]` e árvores (`Vec[?*Valor]` dentro de `Valor`); structs com donos se liberam com `fn (x *X) free()`, que o `drop` genérico chama. Enums com dados donos vieram no item de erros com conteúdo. Veja [docs/tipos.md](docs/tipos.md)
- [x] **FFI para bibliotecas do usuário:** `extern "C" fn` em qualquer arquivo, com a fronteira conferida (números, `string`, `&[]T`, `&T` e handles `extern "C" type`, que o verificador obriga a fechar); `link "nome"` e `-L`; erros de ligação apontados como erro do programa. Veja [docs/ffi.md](docs/ffi.md)
- [x] **Caminho de busca de bibliotecas:** imports procurados na pasta do projeto, em `lib/`, nas pastas de `-I` e nas de `MLC_PATH`. Veja [Bibliotecas](#bibliotecas)
- [x] **Formatação para buffer:** `str.format(&dst, "...", x)`, `str.format_len` e `w.printf` (num Writer reaproveitado, sem flush a cada chamada) sobre o mesmo `io.Writer` do `io.printf`, com o destino conferido no runtime. Especificações `{:x}`, `{:#x}`, `{:.2}`, `{:>8}`, `{:*^8}`, `{:08}` e `{:s}`, com a largura contada em caracteres. Veja [docs/texto.md](docs/texto.md)
- [x] **Parâmetros de saída na FFI:** `extern "C" fn sqlite3_open(nome string, db &?*sqlite3) int32` e `sqlite3_open("x.db", &db)`. O verificador exige que `db` não tenha handle antes da chamada e cobra o fechamento depois. Veja [docs/ffi.md](docs/ffi.md#handles-devolvidos-por-ponteiro-x)
- [x] **Biblioteca para ferramentas de linha de comando:** `fs` com arquivos abertos (`open`, `read`, `write`, `seek`), diretórios, `stat`, `mkdir` e `rename`; `args` (opções e posicionais); `process` (rodar programas, sem shell); `bytes.Buf` (texto que cresce, à vista); `utf8`; `text.split_next` e `word_next`; `os.setenv`, `cwd` e `chdir`. Veja [docs/biblioteca.md](docs/biblioteca.md)
- [x] **Rede, JSON e datas:** `std/net` (TCP: `listen`, `accept`, `connect`, `read`, `write`, timeouts, erros portáveis, sem `SIGPIPE`); `std/json` (leitor sem alocação que devolve tokens como enum, `skip`, `escape`/`unescape`); `std/time` com datas (`utc`, `local`, `iso`, `parse_iso`). Veja [docs/biblioteca.md](docs/biblioteca.md)
- [x] **Hash, encoding e benchmarks:** SHA-256 (incremental) e CRC32 em `std/hash`; `std/encoding` com hex e base64; benchmarks no `mlc test -bench` (`fn bench_*(b &testing.B)`). Veja [docs/testes.md](docs/testes.md#benchmarks)
- [x] **Ponteiros `NULL` opcionais na FFI:** `?&T`, `?&[]T` e `?&?*X` em parâmetros de `extern "C" fn`, que aceitam `nil` ou um empréstimo (`getaddrinfo(host, porta, nil, &res)`, o `errmsg` do `sqlite3_exec`). Veja [docs/ffi.md](docs/ffi.md#ponteiros-que-podem-ser-null-t)
- [x] **Leitura genérica, caminhos e texto dos erros:** a interface `io.Reader` (implementada por `fs.File`, `net.Conn` e `io.stdin()`), `std/bufio` (`read_line`, `read_until`, `read_exact`, `read_byte`, com buffer fixo dentro do leitor e a fonte emprestada a cada chamada), `std/path` (`join`, `clean`, `dir`, `base`, `ext`), um só espaço de códigos de erro e `os.error_text`. Tipos de C opacos como argumento de genéricos `[T ref]`. Veja [docs/biblioteca.md](docs/biblioteca.md#stdbufio)
- [x] **Números em bytes:** `std/binary` com inteiros de 16, 32 e 64 bits (com e sem sinal) e reais em big-endian e little-endian, e varints (com zigzag para os com sinal); `math.float64_bits`/`float64_from_bits`. Veja [docs/biblioteca.md](docs/biblioteca.md#stdbinary)
- [x] **Filas:** `std/deque` (buffer circular, push e pop nas duas pontas em O(1)) e `std/heap` (fila de prioridade sobre `cmp.Ord`, `new_max`, `replace_top` para top-K), com alocação à vista e donos como elementos. Veja [docs/biblioteca.md](docs/biblioteca.md#stddeque)
- [x] **Serviços:** `std/signal` (Ctrl+C e `TERM` anotados, `received`, `wait`), `std/log` (logger como objeto, sem estado global, com nível, horário, `lg.info("...", x)` conferido na compilação e linhas inteiras entre threads), `std/sync` (grupo de threads com `wait` cobrado pelo verificador) e timeout no `accept`. `tests/run/servico.mlc` encerra com calma ao receber `TERM`. Veja [docs/biblioteca.md](docs/biblioteca.md#stdsignal)
- [x] **HTTP/1.1:** `std/http` com servidor e cliente sobre `net.Conn` (ou qualquer `http.Stream`), sem alocação: `Message` com buffer fixo, `Content-Length`, `chunked`, keep-alive, consulta decodificada, `Builder` com `Content-Length` automático; `sync.group` como limite de conexões simultâneas (`add` espera uma vaga, `reap`). Sem TLS (use um proxy). Veja [docs/biblioteca.md](docs/biblioteca.md#stdhttp)
- [x] **Segurança básica:** `rand.secure` (bytes da fonte criptográfica do sistema, `secure_int_n` sem viés, `seeded`), `std/hmac` (HMAC-SHA256 com `verify` e comparação em tempo constante, testado com a RFC 4231) e `std/uuid` (v4 e v7 ordenado pelo tempo). Veja [docs/biblioteca.md](docs/biblioteca.md#stdhmac)
- [x] **URL, cliente HTTP e roteador:** `std/url` (partes, porta padrão, IPv6, consulta decodificada, `Query` para montar), `http.get`/`post`/`request` e `dial` + `request_to` para cabeçalhos próprios, `http.Router[C]` com `{id}` e `{resto...}`, 404/405, e `serve_conn` com o laço de keep-alive. Veja [docs/biblioteca.md](docs/biblioteca.md#roteador)
- [x] **Pilha conferida:** o checker estima a pilha de cada função (quadro mais a maior pilha das funções chamadas) e recusa um `thread.spawn` ou um `main` que não cabe, com a variável que mais pesa e a constante sugerida; funções de quadro grande conferem na entrada se cabem no que resta da pilha e dão panic em vez de pular a página de guarda (recursão, valores `fn`, tamanho calculado). Veja [docs/memoria.md](docs/memoria.md#pilha)
- [x] **Testar o verificador de propósito:** oráculo em tempo de execução (AddressSanitizer, UndefinedBehaviorSanitizer, ThreadSanitizer e o contador de alocações) sobre a suíte, mutação de programas corretos, gerador de programas focado em posse, enumeração exaustiva de programas pequenos contra um modelo de referência, e testes metamórficos e de cobertura. Veja [docs/verificacao.md](docs/verificacao.md)
  - [x] fase 1: oráculo sobre a suíte atual (`-sanitize address|thread`, `make sanitize`): a suíte inteira passa sob AddressSanitizer + UBSan e ThreadSanitizer
  - [x] fase 2: mutação de programas corretos (`make mutate`): 1.944 mutantes de 116 programas, todos recusados ou sem bug; achou e corrigiu um buraco (empréstimo de um enum com donos já liberado)
  - [x] fase 3: gerador de programas focado em posse (`make fuzz`): programas válidos e com um bug injetado que a execução vê; um bug aceito é reduzido ao menor exemplo, e os válidos recusados formam o catálogo de falsos positivos
  - [x] fase 4: enumeração exaustiva com um modelo de referência (`make enum`): todos os programas até o tamanho 6 (12,7 milhões) de um alfabeto de posse, julgados também por um interpretador abstrato que segue cada caminho; nenhum buraco, e o catálogo de falsos positivos (todos do empréstimo que vale até o último uso no texto)
  - [x] fase 5: robustez (`make robust`: `go test -fuzz` sobre o compilador e o formatador; achou e corrigiu 4 panics e 4 erros do formatador), cobertura (`make cover`, com testes para as regras de segurança que nenhum teste disparava), testes metamórficos e casos difíceis clássicos
- [ ] **Callbacks de C para MLC na FFI:** ponteiros de função na fronteira (`qsort`, `sqlite3_exec` com função), com o `void *ctx` das APIs de C
- [x] **Callbacks com contexto:** métodos com parâmetros de tipo próprios (`fn (m &Map[K, V]) each_with[C ref](ctx &C, f fn(&C, &K, &V))`), deduzidos na chamada ou escritos; a restrição `ref` para o contexto, que só é emprestado; `map.each_with`, `vec.each_with`, `sort.sort_by_with`, `slices.index_by` e `count_by`. Veja [docs/tipos.md](docs/tipos.md#métodos-genéricos-e-callbacks-com-contexto)
- [x] **Erros com conteúdo:** enums com dados donos (mensagens alocadas explicitamente por quem cria o erro, liberadas por quem o trata); `match` que consome ou só lê, `free` de enum; `Result[T, E]` com donos nos dois lados. Sem atalho de propagação (`try`): toda saída da função continua escrita como `return`. Veja [docs/tipos.md](docs/tipos.md#enums-com-donos)
- [x] **Interfaces `Ord` e `Eq`:** `std/cmp` com `Eq` e `Ord` (já implementadas para números, `bool`, `string` e slices); `impl auto cmp.Eq`, `cmp.Ord` e `hash.Hash` para structs e enums; `sort.sort` sobre `cmp.Ord`, trocando por posse (ordena structs e donos); `std/slices` (`index`, `contains`, `count`, `equal`, `search`, `binary_search`, `min_index`, `max_index`, `dedup`, `fill`). Veja [docs/interfaces.md](docs/interfaces.md)
- [ ] **Iteradores como objetos:** empréstimos guardados em structs, com tempo de vida conferido
- [ ] **Interfaces dinâmicas:** `&I` e `*I`, para guardar valores de tipos diferentes juntos (sem boxing: só a partir de `&T` ou `*T`)
- [ ] **Concorrência:** threads do sistema criadas explicitamente, com a posse garantindo a ausência de data races. Veja [docs/concorrencia.md](docs/concorrencia.md)
  - [x] fase A: `thread.spawn(f, ctx, stack)` e `thread.join(t)`; o contexto e o resultado atravessam por movimento; a pilha tem tamanho explícito; todo `spawn` precisa de `join`
  - [x] fase B: canais com capacidade fixa (`std/chan`): `chan.new[T](n)`, pontas `Sender`/`Receiver` que precisam de `close()`, `recv` devolvendo `Got(v)`/`Closed`, `send` sem receptor liberando o valor, vários produtores e consumidores
  - [x] fase C: `mutex.new(v)`, dono do valor, com `lock()` devolvendo um guard que precisa de `unlock()` e `get()` emprestando o valor só enquanto a trava existe; `wait`/`notify`; travar duas vezes na mesma thread dá panic; `atomic.Int` e `atomic.Bool`
  - Fora do roteiro: threads com escopo que recebem empréstimos (pedaços disjuntos de um mesmo buffer). Só entram se um programa real precisar; antes disso viria um empréstimo só de leitura, útil para a linguagem inteira. Veja [docs/concorrencia.md](docs/concorrencia.md#o-que-ficou-de-fora)
