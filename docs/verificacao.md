# Como testar o verificador

Plano para atacar o verificador de posse e de empréstimos (`internal/checker/ownership.go`, `borrows.go` e o que eles usam) de propósito, antes de chamar a linguagem de beta. Nada aqui está feito ainda; cada fase diz o que entregar e quando ela está pronta.

## O que procurar

O verificador pode errar de dois jeitos:

| Erro | O que acontece | Gravidade |
|---|---|---|
| **Aceitar um programa inseguro** (falso negativo) | vazamento, uso depois de `free`, `free` duplo, empréstimo pendurado ou data race passam pela compilação | grave: quebra a promessa da linguagem |
| **Recusar um programa seguro** (falso positivo) | quem programa precisa reescrever código correto | incômodo: piora a ergonomia |

A prioridade é o primeiro. O segundo é registrado para melhorar a ergonomia, sem afrouxar o verificador às cegas: o `CLAUDE.md` pede que ele continue conservador.

## O oráculo: a regra que todas as fases usam

O verificador decide na compilação. Para saber se ele acertou, o programa aceito é **rodado** sob ferramentas que detectam o erro de verdade:

- **AddressSanitizer:** uso depois de `free`, `free` duplo, acesso fora dos limites;
- **o contador de alocações do runtime** (`mlc_check_allocs`): sobra de memória no fim vira "erro interno";
- **UndefinedBehaviorSanitizer:** estouro, alinhamento e o resto do comportamento indefinido do C;
- **ThreadSanitizer**, para programas com threads, canais ou mutex;
- **Valgrind**, no Linux, como segunda opinião.

> **Todo programa que o verificador aceita precisa rodar limpo sob essas ferramentas.** Se não roda, achamos um buraco no verificador (ou no gerador de C, o que também é bug).

Os sanitizers pedem compilar o C gerado com flags próprias (`-fsanitize=address,undefined` ou `-fsanitize=thread`, `-g`, `-O1`). Hoje isso é feito à mão com `mlc build -emit-c`; a fase 1 transforma isso num modo do driver.

Cada problema achado em qualquer fase vira um teste permanente: em `tests/errors` (se o programa devia ser recusado) ou em `tests/run` (se devia ser aceito e o C gerado estava errado), com o menor exemplo que reproduz o caso.

## Fase 1: o oráculo sobre a suíte atual

**O quê:** rodar toda a suíte de execução (`tests/run`, `tests/panic`, os testes da `std` e `tests/ffi`) com os sanitizers.

**Entregas:**
- uma opção no driver para compilar com sanitizers (`Options.Sanitize`, com os valores `address`, `thread` e `none`), usada pelo `mlc build`, `mlc run` e `mlc test` com `-sanitize address|thread`;
- `make sanitize`: roda `go test` com os testes de execução sob AddressSanitizer + UndefinedBehaviorSanitizer, e os que usam threads também sob ThreadSanitizer;
- a CI (quando existir) roda o `make sanitize` além do `make check`.

**Pronto quando:** toda a suíte passa sob os sanitizers, e o `make sanitize` está documentado no `CLAUDE.md` como parte de "antes de considerar um marco pronto".

**Esforço:** pequeno. Parte do trabalho já foi feito à mão nos testes de concorrência.

## Fase 2: mutação de programas corretos

**O quê:** pegar programas que o verificador aceita e aplicar mutações que **sabidamente** introduzem um bug de posse. O verificador precisa recusar o mutante.

| Mutação | Bug que cria |
|---|---|
| apagar um `free` (ou `close`, `unlock`, `thread.join`) | vazamento, trava ou thread esquecida |
| duplicar um `free` | `free` duplo |
| usar a variável depois de passá-la a um parâmetro `*T` ou movê-la | uso depois de mover |
| mover um `free` para dentro de um `if` | vazamento num dos caminhos |
| trocar `&x` por `x` num argumento | move onde era empréstimo |
| usar um empréstimo guardado (`x := v.at(0)`) depois de liberar ou crescer a origem | empréstimo pendurado |
| tirar o `if h != nil` antes de fechar um `?*X` | uso de `nil` |
| mandar um valor por um canal e usá-lo depois | uso depois de mover |

**Como decidir:**
- o verificador recusa → certo;
- o verificador aceita → roda o mutante sob o oráculo:
  - dá erro → **buraco no verificador**: vira teste em `tests/errors`;
  - não dá erro → a mutação não criou bug de fato naquele ponto (por exemplo, o `free` estava num caminho que nunca roda); é descartada.

**Entregas:**
- um mutador em Go (`internal/fuzz/mutate.go`) que trabalha sobre a AST (não sobre o texto) e escreve cada mutante com a mutação aplicada num comentário;
- `make mutate`: aplica as mutações aos `tests/run`, aos testes da `std` e a `tests/ffi`, e escreve um relatório com quantos mutantes foram recusados, quantos foram aceitos sem bug e quantos foram buracos.

**Pronto quando:** o relatório não mostra nenhum buraco, e cada buraco achado no caminho virou teste.

**Esforço:** pequeno a médio.

## Fase 3: gerador de programas focado em posse

**O quê:** gerar programas pequenos e aleatórios, restritos ao que importa para o verificador:

- `alloc`, `free`, atribuição (move), empréstimo `&`, `take` e `replace`;
- `if`, `for` com `break` e `continue`, `match` sobre enums com donos;
- funções que recebem `*T`, `&T` e `T` e que devolvem donos e empréstimos;
- campos de struct, slices de donos, `?*T`, genéricos com `owned` e `ref`;
- numa segunda etapa: `thread.spawn`, canais e mutex.

**Para cada programa:**
- **aceito** → roda sob o oráculo; qualquer erro é um buraco;
- **recusado** → sem conclusão automática. Uma amostra vai para revisão manual, para achar os falsos positivos mais incômodos.

**Entregas:**
- `internal/fuzz/gen.go`, com uma semente para reproduzir cada programa;
- `make fuzz` (com `FUZZTIME=10m`, por exemplo), que guarda cada programa problemático, já reduzido ao menor que reproduz o caso, em `fuzz/achados/`;
- um redutor automático (tirar comandos enquanto o problema continua) para os achados.

**Pronto quando:** uma rodada longa (por exemplo, uma hora) não acha buracos novos.

**Esforço:** médio.

## Fase 4: enumeração exaustiva com um modelo de referência

**O quê:** o fuzzing aleatório pode nunca sortear o caso certo. Para programas **pequenos**, dá para gerar **todos**: por exemplo, duas ou três variáveis e até seis comandos entre `alloc`, `free`, mover, usar, emprestar, `if` e laço.

Em vez de rodar cada programa, um **modelo de referência** decide se ele é seguro. É um interpretador abstrato, em Go, bem menor que o verificador, que segue todos os caminhos e acompanha o estado de cada variável (sem memória, dona, liberada, movida, emprestada).

**Comparação:**
- o verificador aceita e o modelo diz inseguro → **buraco**;
- o verificador recusa e o modelo diz seguro → **excesso de conservadorismo**, já com o exemplo mínimo, para orientar melhorias de ergonomia (como o empréstimo que vale até o último uso no texto).

**Entregas:**
- `internal/fuzz/model.go` (o modelo) e `internal/fuzz/enum.go` (a enumeração);
- um teste em Go que roda a enumeração até um tamanho pequeno a cada `go test` e até um tamanho maior com uma flag;
- a lista dos falsos positivos encontrados, ordenada pela frequência, como entrada para melhorias no verificador.

**Pronto quando:** a enumeração até o tamanho combinado não mostra buracos, e os falsos positivos estão catalogados.

**Esforço:** médio a grande. É a técnica com a garantia mais forte, ainda que só para programas pequenos, que é onde os erros de lógica costumam aparecer.

## Fase 5: contínuo

- **Testes metamórficos:** transformações que não mudam a segurança não podem mudar o veredito (renomear variáveis, envolver num bloco, trocar a ordem de comandos independentes, extrair para uma função, trocar `if` por um `match` equivalente). Um veredito diferente mostra um caso tratado pela forma do código, não pelo significado.
- **Cobertura:** `go test -coverprofile` sobre `ownership.go`, `borrows.go`, `owned.go` e `threads.go`; cada ramo que nenhum teste alcança ganha um teste dirigido.
- **Casos difíceis conhecidos:** a história do verificador de empréstimos do Rust tem uma lista de casos difíceis (empréstimos em laços, aliasing por campos, moves dentro de `match`, genéricos instanciados com donos, empréstimos devolvidos por caminhos diferentes). Cada um vira um teste.
- **Robustez:** `go test -fuzz` sobre o parser e o checker com textos aleatórios, para garantir que o compilador nunca dá panic (como o do `match *(&e)`, achado no item de erros com conteúdo).

## Ordem sugerida

| Fase | Esforço | Retorno |
|---|---|---|
| 1. Oráculo sobre a suíte | pequeno | alto: protege tudo que vier depois |
| 2. Mutação | pequeno a médio | alto: código realista |
| 3. Gerador aleatório | médio | médio a alto: combinações improváveis |
| 4. Enumeração com modelo | médio a grande | o mais forte para programas pequenos, e o mapa dos falsos positivos |
| 5. Contínuo | sempre | mantém o resto honesto |

As fases 1 e 2 vêm primeiro e já dão o maior retorno pelo esforço. A 3 e a 4 dependem do que as primeiras mostrarem.
