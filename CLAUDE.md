# MLC — regras do projeto

## Princípio central: toda alocação é explícita

A ideia do MLC é que quem programa **entenda e saiba quanto e onde está alocando memória**. Por isso:

- Toda alocação no heap aparece no código-fonte como uma chamada explícita (`alloc`, `realloc`, e as que vierem). Nenhuma construção da linguagem pode alocar escondido: nada de concatenação de strings que aloca, `append` embutido que realoca por conta própria, closures que capturam no heap, boxing automático ou cópias implícitas para o heap.
- Toda liberação também é explícita (`free`), e o verificador de posse garante na compilação que ela acontece em todos os caminhos.
- Se uma funcionalidade nova precisar de memória, ela recebe a memória de quem chama, ou vira uma função com nome que deixa claro que aloca. Na dúvida, a funcionalidade não entra.
- Conveniências que alocam (como um `append` com capacidade) só podem existir como funções comuns, escritas em MLC e visíveis no código, nunca como parte implícita da linguagem.

## Convenções

- Funções de runtime e de biblioteca expostas na linguagem são sempre em inglês (`io.print`, `alloc`, `free`, `realloc`, `len`, `str.copy`).
- Funções de biblioteca não alocam: escrevem em buffers emprestados por quem chama (veja `docs/texto.md`).
- Mensagens do compilador são em português.
- Identificadores do código do compilador (Go) e do runtime (C) são em inglês; comentários e documentação, em português.
- Commits sem linha de co-autoria.

## Como trabalhar

- Toda mudança de comportamento vem com teste em `tests/` (`run`, `panic` ou `errors`). Os testes compilam o C gerado com `-Werror`, e o runtime acusa erro interno se sobrar alocação no fim do programa.
- O verificador de posse (`internal/checker/ownership.go`) precisa continuar conservador: na dúvida, recusa o programa.
- Ordem de avaliação: sempre da esquerda para a direita, escrita explicitamente no C gerado.
- Antes de considerar um marco pronto: `gofmt -l .`, `go vet ./...` e `go test ./...` limpos, e `README.md`, `docs/gramatica.md` e `docs/memoria.md` atualizados.
