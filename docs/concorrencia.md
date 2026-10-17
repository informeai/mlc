# Concorrência

O MLC usa **threads do sistema operacional, criadas explicitamente**, e a posse garante na compilação que duas threads nunca mexem na mesma memória. Não há escalonador próprio, green threads nem `async`: essas coisas alocam escondido (pilhas que crescem sozinhas, máquinas de estado no heap), o que vai contra a regra central da linguagem.

O modelo vem em fases:

| Fase | Conteúdo | Situação |
|---|---|---|
| A | `thread.spawn` e `thread.join`, com os dados atravessando por movimento | pronta |
| B | canais com capacidade fixa (`std/chan`) | a fazer |
| C | `Mutex[T]` com guard, e atomics | a fazer |
| D | threads com escopo que recebem empréstimos | depois |

## Threads (`import "std/thread"`)

```
import "std/io"
import "std/thread"

fn sum(part []int) int {
    t := 0
    for x in part {
        t += x
    }
    free(part)
    return t
}

fn main() {
    a := alloc([]int, 1000)
    // ... preenche a ...
    t := thread.spawn(sum, a, thread.STACK_64KB)   // a é MOVIDA para a thread
    r := thread.join(t)                            // o resultado volta por posse
    io.print(r)
}
```

| Função | O que faz |
|---|---|
| `thread.spawn(f, ctx, stack) *thread.Thread[R]` | cria uma thread que roda `f(ctx)`. `f` é `fn(C) R` ou `fn(C)`; com `fn(C)`, o handle é `*thread.Thread` |
| `thread.join(t) R` | espera a thread, devolve o resultado e libera o registro da thread |
| `thread.cpu_count() int` | quantos processadores o sistema tem |
| `thread.STACK_16KB` … `thread.STACK_8MB` | tamanhos de pilha |

### As regras

- **O contexto é movido.** Depois do `spawn`, quem chamou não tem mais o `ctx`. Para levar várias coisas, junte-as numa struct (`alloc(Job{...})`), e a função da thread fica dona dela.
- **A função da thread não recebe empréstimos** (`fn(&T)` é recusada) e não é closure. Como o MLC não tem variáveis globais mutáveis, não existe outra porta para estado compartilhado.
- **Toda thread tem dono.** O handle precisa ir para `thread.join`, e esquecer é vazamento detectado na compilação, como um `free` esquecido. Ele não aceita `free` nem `alloc`, e não entra num `Vec` genérico (o `drop` não saberia esperar a thread). Para várias threads, use um slice `[]?*thread.Thread[R]` com `replace` e `drain`.
- **O resultado volta por posse.** Se `R` é dono (`[]byte`, `*T`), quem chama o `join` passa a ser dono dele e precisa liberá-lo.
- **A pilha é escolhida por quem cria a thread**, em bytes (pelo menos 16 KB). É memória, e quem programa deve saber quanto usa.
- **Um panic em qualquer thread encerra o programa inteiro**, com a mesma mensagem e o mesmo código de saída de um panic comum.

### Várias threads

```
ts := alloc([]?*thread.Thread[int], 4)
for p := 0; p < 4; p++ {
    job := alloc(Job{data: parte(p), from: 0, to: 250})
    old := replace(&ts[p], thread.spawn(partial_sum, job, thread.STACK_64KB))
    if old != nil {             // a posição estava vazia; o verificador pede o tratamento
        thread.join(old)
    }
}
total := 0
for t in drain(ts) {
    if t != nil {
        total += thread.join(t)
    }
}
free(ts)
```

## O que o modelo garante

- **Sem data race:** dados só chegam a outra thread por movimento (o contexto, o resultado).
- **Sem thread esquecida:** todo `spawn` tem seu `join`.
- **Toda memória à vista:** o registro da thread (o contexto e o resultado) é alocado no `spawn` e liberado no `join`, e o tamanho da pilha está no código.

## O que não garante

- **Deadlock.** Ainda não há como duas threads esperarem uma pela outra, mas isso passa a ser possível com canais e `Mutex`, e o verificador não impede.
- **O que passa pela FFI.** Uma biblioteca de C que cria threads ou compartilha memória por conta própria fica fora da garantia.

## Como funciona

`thread.spawn` e `thread.join` são reescritos pelo compilador, como o `io.print`. Para cada tipo de função de thread, o C gerado ganha um registro (o `mlc_thread` do runtime, o resultado, a função e o contexto) e uma função de entrada que chama `f(ctx)` e guarda o resultado. O `spawn` aloca o registro com `mlc_alloc` e cria a thread com `pthread_create`, com o tamanho de pilha pedido. O `join` faz `pthread_join`, tira o resultado e libera o registro.

O runtime é seguro para threads: o contador de alocações é atômico, e os destinos do `str.format` são por thread.
