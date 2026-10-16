# MLC — My Lang C

Linguagem compilada com sintaxe simples, inspirada no Go, que gera C e usa o compilador C do sistema para produzir o binário. A memória é gerenciada à mão, como no C (`alloc`/`free` explícitos, sem coletor de lixo), mas o compilador recusa programas com os bugs clássicos do C antes de gerar o código. Veja [docs/memoria.md](docs/memoria.md).

```
arquivo.mlc  →  mlc (Go)  →  program.c  →  cc + runtime C  →  binário
```

## Exemplo

```
import "io"

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
go build -o mlc ./cmd/mlc
./mlc run exemplo.mlc                  # compila e executa
./mlc build -o exemplo exemplo.mlc     # gera o executável
./mlc build -emit-c exemplo.mlc        # mostra o C gerado
```

O compilador C usado é o da variável `CC` (padrão: `cc`).

## Estrutura

| Pasta | Papel |
|---|---|
| `cmd/mlc` | linha de comando (`build`, `run`) |
| `internal/token` | tokens, posições e erros de compilação |
| `internal/lexer` | tokens, com `;` automático no fim da linha |
| `internal/parser` | parser recursive descent → AST |
| `internal/ast` | árvore sintática |
| `internal/checker` | nomes e tipos |
| `internal/codegen` | AST → C11 |
| `internal/driver` | encadeia as fases e chama o `cc` |
| `rt/c` | runtime em C, embutido no binário do `mlc` |
| `tests/run` | programas e saída esperada (`.out`) |
| `tests/panic` | programas que devem abortar (`.out` + `.err`) |
| `tests/errors` | programas que devem ser rejeitados (`// erro: ...` na 1ª linha) |

```sh
go test ./...
```

## Garantias atuais

- `realloc` e `free` são as únicas formas de mudar memória no heap: não há crescimento automático.
- Estouro de inteiro, divisão por zero e índice fora dos limites viram panic com arquivo e linha do `.mlc`, nunca comportamento indefinido do C.
- Diretivas `#line` no C gerado: panics e erros apontam para o código MLC.
- Sem `printf` exposto: `io.print` gera chamadas tipadas por argumento.
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
- [ ] **Marco 7:** strings alocadas (concatenação, conversões)
