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
bin/mlc fmt .                            # formata os .mlc da pasta (no lugar)
bin/mlc fmt -l .                         # só lista os que mudariam
make test                                # roda os testes
make check                               # gofmt, mlc fmt -l, go vet e testes
```

## Formatação (`mlc fmt`)

`mlc fmt` reescreve os arquivos na forma canônica, como o `gofmt`: indentação de 4 espaços, um espaço em volta dos operadores binários e depois das vírgulas, nenhum depois de operador unário (`-x`, `*p`, `&s`), comentários de fim de linha alinhados. As quebras de linha, os parênteses e os comentários de quem escreveu são mantidos; linhas em branco seguidas viram uma só. Um arquivo com erro de sintaxe não é formatado. `mlc fmt -` lê da entrada padrão e escreve na saída padrão (para editores).

## Bibliotecas

Um `import "json/parse"` procura `json/parse.mlc` nesta ordem, e vale a primeira pasta que tiver o arquivo:

1. a pasta do arquivo principal;
2. a pasta `lib/` ao lado dele (bibliotecas copiadas para dentro do projeto);
3. as pastas de `-I` (`mlc build -I dir`, `mlc run -I dir`);
4. as pastas da variável `MLC_PATH`, separadas por `:` (bibliotecas compartilhadas entre projetos).

Os imports de dentro de uma biblioteca seguem a mesma busca, então uma biblioteca em `~/mlc-libs/json/` importa as próprias partes como `import "json/lexer"`. O LSP usa `lib/` e `MLC_PATH`, então o editor acha as mesmas bibliotecas. `import "std/..."` é sempre a biblioteca padrão, embutida no `mlc`.

## Editores

`mlc lsp` é um servidor Language Server Protocol (via entrada e saída padrão). Ele marca os erros na linha enquanto você digita, sem precisar salvar: sintaxe, tipos e posse de memória, os mesmos do `mlc build`. Também dá cores semânticas (palavras reservadas, tipos, funções, constantes, literais, comentários) e formata pelo `mlc fmt`. O **hover** mostra a declaração de qualquer nome (variável, parâmetro, constante, função, método, campo, tipo, variante, módulo, função embutida) com o tipo e os comentários `//` logo acima dela. **Ir para a definição** leva à declaração, inclusive em outros arquivos e na biblioteca padrão (copiada para a pasta de cache do sistema, para o editor abrir). Um módulo sem `fn main` é checado pelo `main.mlc` mais próximo que o importa, para os imports resolverem como num build.

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
| `cmd/mlc` | linha de comando (`build`, `run`, `fmt`, `lsp`) |
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
| `std` | biblioteca padrão escrita em MLC (`import "std/io"`, `import "std/math"`), embutida no binário. Veja [docs/biblioteca.md](docs/biblioteca.md) |
| `tests/run` | programas e saída esperada (`.out`); entrada opcional (`.in`) |
| `tests/panic` | programas que devem abortar (`.out` + `.err`) |
| `tests/errors` | programas que devem ser rejeitados (`// erro: ...` na 1ª linha) |

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
- [x] **Biblioteca padrão** em MLC, importada com `std/`: `io`, `str`, `result`, `math`, `sort`, `text`, `vec`, `rand`, `os`, `fs`, `time`. Nada aloca escondido; erros como `Result[T]`. Veja [docs/biblioteca.md](docs/biblioteca.md)
- [x] **`mlc fmt`:** formatador canônico (4 espaços), preservando comentários e quebras de linha; `-l` lista, `-` usa a entrada padrão
- [x] **Interfaces estáticas:** `interface`, `impl` (de uma linha, com bloco ou genérico) e `impl auto`; interfaces como restrição de genérico, com chamadas diretas (sem tabela de métodos). `io.print`/`io.printf`/`io.eprintf` sobre `io.Display`, com a formatação em MLC. Veja [docs/interfaces.md](docs/interfaces.md)
- [x] **Bits e funções como valor:** `& | ^ << >>`, `^x`, aritmética que dá a volta (`+% -% *%`), literais `0xFF`, `0b1010`, `0o17`, `1_000_000`; tipos `fn(int) bool`, funções como valor e chamadas indiretas (sem closures, sem alocação). `std/rand` passou a ser MLC; novos `std/hash` e `sort.sort_by`. Veja [docs/tipos.md](docs/tipos.md)
- [x] **Donos em genéricos:** restrição `owned` (`Vec[[]byte]`, `Vec[?*No]`), `drop(x)` em código genérico, restrição no receptor (`fn (v &Vec[T any]) get`); `std/vec` com `push`/`pop`/`take`/`put`/`set` movendo a posse e `free` liberando os elementos; erros de posse apontados na definição do genérico. Veja [docs/tipos.md](docs/tipos.md)
- [x] **Hash map em MLC:** `std/map` (`Map[K owned + hash.Hash, V owned]`, endereçamento aberto) e a interface `hash.Hash` para inteiros, `bool`, `string` e slices; restrições combinadas com `+`. Veja [docs/biblioteca.md](docs/biblioteca.md)
- [x] **Empréstimos devolvidos e guardados:** `fn (v &Vec[T]) at(i int) &T`, `x := m.get(&k)`, `x := &a[i]`; a origem fica congelada enquanto o empréstimo é usado, conferido na compilação. `vec.at`/`last` e `map.get`/`key_ref`/`value_ref`. Veja [docs/memoria.md](docs/memoria.md)
- [x] **LSP:** `mlc lsp` com diagnósticos ao digitar, cores, formatação, hover e ir para a definição; extensão para VS Code e configuração para Neovim. Veja [Editores](#editores)

### Próximos passos (em ordem de prioridade)

- [x] **Donos aninhados:** `Vec[?*vec.Vec[int]]`, `Map[[]byte, ?*vec.Vec[[]byte]]`, `Vec[[][]byte]` e árvores (`Vec[?*Valor]` dentro de `Valor`); structs com donos se liberam com `fn (x *X) free()`, que o `drop` genérico chama. Enums com dados donos ficaram para o item de erros com conteúdo. Veja [docs/tipos.md](docs/tipos.md)
- [x] **FFI para bibliotecas do usuário:** `extern "C" fn` em qualquer arquivo, com a fronteira conferida (números, `string`, `&[]T`, `&T` e handles `extern "C" type`, que o verificador obriga a fechar); `link "nome"` e `-L`; erros de ligação apontados como erro do programa. Veja [docs/ffi.md](docs/ffi.md)
- [x] **Caminho de busca de bibliotecas:** imports procurados na pasta do projeto, em `lib/`, nas pastas de `-I` e nas de `MLC_PATH`. Veja [Bibliotecas](#bibliotecas)
- [ ] **Formatação para buffer:** `str.format(dst, "...", x)`, um `io.Writer` que escreve num `[]byte` e especificadores de formato (`{:x}`, `{:.2}`, largura)
- [ ] **Callbacks com contexto:** métodos com parâmetros de tipo próprios (como `each[C](ctx &C, f fn(&C, &K, &V))`) ou funções sem captura no heap
- [ ] **Erros com conteúdo:** enums com dados donos (mensagens alocadas) e um atalho de propagação (como `try`)
- [ ] **Interfaces `Ord` e `Eq`:** `sort.sort` e `map` aceitando structs pela ordem e igualdade que elas definem
- [ ] **Iteradores como objetos:** empréstimos guardados em structs, com tempo de vida conferido
- [ ] **Interfaces dinâmicas:** `&I` e `*I`, para guardar valores de tipos diferentes juntos (sem boxing: só a partir de `&T` ou `*T`)
- [ ] **Concorrência:** threads, atomics e mutex, com a posse garantindo a ausência de data races
- [ ] **Múltiplos retornos**
