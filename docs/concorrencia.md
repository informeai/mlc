# Concorrência

O MLC usa **threads do sistema operacional, criadas explicitamente**, e a posse garante na compilação que duas threads nunca mexem na mesma memória. Não há escalonador próprio, green threads nem `async`: essas coisas alocam escondido (pilhas que crescem sozinhas, máquinas de estado no heap), o que vai contra a regra central da linguagem.

O modelo vem em fases:

| Fase | Conteúdo | Situação |
|---|---|---|
| A | `thread.spawn` e `thread.join`, com os dados atravessando por movimento | pronta |
| B | canais com capacidade fixa (`std/chan`) | pronta |
| C | `Mutex[T]` com guard, e atomics | pronta |
| D | threads com escopo que recebem empréstimos | fora do roteiro (veja [O que ficou de fora](#o-que-ficou-de-fora)) |

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

### Grupo de threads (`import "std/sync"`)

Quando as threads não devolvem resultado (o resultado vai por um canal, um mutex ou um atomic), um `sync.Group` espera todas de uma vez:

```
g := sync.group(8)                      // espaço para 8 threads (aloca)
for i := 0; i < 8; i++ {
    g.add(thread.spawn(worker, ctx(i), thread.STACK_64KB))   // o grupo fica com o handle
}
g.wait()                                // espera todas e libera o grupo
```

O verificador cobra o `wait`, como cobra o `join` de cada thread, e `add` só aceita `*thread.Thread` (sem resultado): um resultado dono não pode sumir sem ninguém recebê-lo. Com o grupo cheio, `add` espera a thread e dá panic.

## Canais (`import "std/chan"`)

Um canal passa valores de uma thread para outra **por posse**. Ele é usado por várias threads ao mesmo tempo, mas no MLC cada coisa tem um dono só. Por isso o canal tem **pontas**, e cada ponta é um dono:

```
rx := chan.new[[]byte](16)    // aloca o canal (16 posições) e devolve a ponta que recebe
tx := rx.sender()             // uma ponta que envia, ligada ao mesmo canal
tx2 := tx.clone()             // mais um produtor
```

```
fn produce(tx *chan.Sender[[]byte]) {
    for i := 0; i < 3; i++ {
        b := alloc([]byte, str.format_len("pedido {}", i))
        str.format(&b, "pedido {}", i)
        if !tx.send(b) {          // b foi movida; false: ninguém mais recebe
            break
        }
    }
    tx.close()                    // obrigatório: sem ele, o consumidor esperaria para sempre
}

fn main() {
    rx := chan.new[[]byte](4)
    t := thread.spawn(produce, rx.sender(), thread.STACK_64KB)
    for {
        match rx.recv() {
            Got(b) => {
                io.write_line(&b)
                free(b)                // a mensagem agora é deste lado
            }
            Closed => break
        }
    }
    rx.close()
    thread.join(t)
}
```

| Operação | O que faz | Memória | Bloqueia |
|---|---|---|---|
| `chan.new[T](n) *chan.Receiver[T]` | cria o canal, com `n >= 1` posições | aloca as `n` posições e o cabeçalho | não |
| `rx.sender() *chan.Sender[T]` | uma ponta que envia | nada (só conta a ponta) | não |
| `tx.clone()`, `rx.clone()` | outra ponta do mesmo lado (vários produtores ou consumidores) | nada | não |
| `tx.send(v) bool` | move `v` para o canal | `v` passa ao canal | se estiver cheio |
| `rx.recv() chan.Recv[T]` | `Got(v)`, com a posse de `v`, ou `Closed` | `v` passa a quem recebe | se estiver vazio e houver `Sender` aberto |
| `x.close()` | fecha a ponta (consome) | o último `close` libera o canal e dá `drop` no que sobrou nele | não |

- **Toda ponta precisa de `close()`**, e o verificador cobra como cobra um `free`. Ela não aceita `free` nem `alloc`. Numa struct com pontas, o `free` da struct fecha as pontas (a regra de donos aninhados). Num `Vec`, o `drop` de cada ponta a fecha.
- **`recv` devolve um enum com dados donos**, `chan.Recv[T] { Got(value T); Closed }`. O `match` consome o resultado, e o verificador obriga a tratar a mensagem recebida.
- **`Closed`** chega quando todos os `Sender` foram fechados e o canal esvaziou, inclusive se nenhum `Sender` foi criado ainda. Crie as pontas que enviam antes de começar a receber.
- **`send` sem nenhum `Receiver` aberto** libera o valor (`drop`) e devolve `false`, para o produtor parar. Nada vaza.
- **Não há canal sem limite.** Um canal cheio faz o `send` esperar, o que dá controle de fluxo. Um canal que crescesse sozinho seria alocação escondida.

### Vários trabalhadores

O `spawn` recebe um contexto só. Para levar várias pontas, elas vão juntas numa struct:

```
struct Worker {
    jobs *chan.Receiver[int]
    results *chan.Sender[int]
}

fn (w *Worker) free() {
    w.jobs.close()
    w.results.close()
    free(w)
}

fn work(w *Worker) {
    for {
        match w.jobs.recv() {
            Got(n) => w.results.send(n * n)
            Closed => break
        }
    }
    w.free()
}
```

```
w := alloc(Worker{jobs: jobs.clone(), results: results.sender()})
t := thread.spawn(work, w, thread.STACK_64KB)
```

**Cuidado com deadlock.** Se o `main` envia todas as tarefas antes de ler os resultados, e o canal de resultados enche, os trabalhadores param no `send` e o `main` para no envio das tarefas. Dimensione os canais pelo padrão de uso, ou consuma os resultados em outra thread. O verificador não detecta isso.

## Mutex (`import "std/mutex"`)

Um mutex **é dono do valor** que protege: o valor só é alcançado com a trava.

```
struct Account {
    balance int
}

fn deposit(m *mutex.Mutex[*Account]) {
    g := m.lock()                // espera a trava; g é um *mutex.Guard[*Account]
    a := g.get()                 // &Account, emprestado do guard
    a.balance += 10
    g.unlock()                   // obrigatório; usar a depois daqui é erro
    m.close()
}

fn main() {
    m := mutex.new(alloc(Account{balance: 0}))   // a conta vai para dentro do mutex
    t := thread.spawn(deposit, m.clone(), thread.STACK_64KB)
    thread.join(t)
    m.close()                                     // o último close libera a conta
}
```

| Operação | O que faz |
|---|---|
| `mutex.new(v) *mutex.Mutex[T]` | aloca o mutex com `v` dentro (`v` é movido); `mutex.new[T](v)` escolhe o tipo |
| `m.lock() *mutex.Guard[T]` | espera a trava e devolve o guard |
| `m.clone() *mutex.Mutex[T]` | outro handle para o mesmo mutex, para levar a outra thread |
| `m.close()` | fecha o handle; o último a sair (handle ou guard) libera o valor |
| `g.get() &T` | empresta o valor |
| `g.unlock()` | destrava (consome o guard) |
| `g.wait()` | solta a trava, espera um `notify` e trava de novo |
| `notify()`, `notify_all()` | acordam quem espera em `wait` (no handle ou no guard) |

- **A trava é um recurso.** O guard é um dono: esquecer o `unlock` é erro de compilação, como um vazamento.
- **O valor só existe dentro do lock.** `get()` empresta do guard, e o guard fica congelado enquanto o empréstimo é usado. Por isso, usar o valor depois do `unlock` é erro de compilação. O empréstimo vale do `get` até o último uso no texto, então destrave depois do último uso, inclusive nos caminhos com `break`.
- **O guard é da thread que travou.** Destravar em outra thread é comportamento indefinido no pthread, então o guard não vai para o contexto de um `spawn`, para canais, campos, slices ou coleções.
- **O guard mantém o mutex vivo.** Fechar o último handle com um guard aberto é seguro: o valor só é liberado no `unlock`.
- **Travar duas vezes na mesma thread** sem destravar dá panic com uma mensagem clara, em vez de travar o programa para sempre.
- **Vários handles:** cada `clone()` precisa de `close()`. Num `Vec`, o `drop` de cada handle o fecha.

### Esperar uma condição

```
fn ready(q &Queue) bool {
    return q.count > 0 || q.done
}

g := m.lock()
for !ready(g.get()) {     // o empréstimo de get() dura só a chamada
    g.wait()
}
```

Quem muda a condição chama `g.notify()` (ou `notify_all()`) antes do `unlock`.

## Atomics (`import "std/atomic"`)

Para contadores e sinais simples, sem mutex:

```
n := atomic.new_int(0)
t := thread.spawn(count, n.clone(), thread.STACK_64KB)   // outro handle, para a thread
n.add(1)
thread.join(t)
io.print(n.load())
n.close()
```

| Tipo | Métodos |
|---|---|
| `atomic.Int` (de `atomic.new_int(v)`) | `load`, `store`, `add` (devolve o valor novo; estouro dá panic), `swap` (devolve o antigo), `compare_swap(esperado, novo) bool`, `clone`, `close` |
| `atomic.Bool` (de `atomic.new_bool(v)`) | `load`, `store`, `swap`, `compare_swap`, `clone`, `close` |

Todas as operações são sequencialmente consistentes. Cada handle precisa de `close()`, e o último libera o valor. O módulo é MLC comum, em [`std/atomic.mlc`](../std/atomic.mlc), sobre funções do runtime.

## O que o modelo garante

- **Sem data race:** dados só chegam a outra thread por movimento (o contexto, o resultado, os valores de um canal) ou dentro de um mutex, com a trava. Os atomics são a outra forma de compartilhar.
- **Sem thread, canal, mutex ou trava esquecidos:** todo `spawn` tem seu `join`, toda ponta de canal e todo handle de mutex ou atomic, seu `close`, e todo guard, seu `unlock`.
- **Sem uso do valor protegido fora da trava:** o empréstimo do `get()` não sobrevive ao `unlock`.
- **Toda memória à vista:** o registro da thread (o contexto e o resultado) é alocado no `spawn` e liberado no `join`, e o tamanho da pilha está no código.

## O que não garante

- **Deadlock.** Duas threads esperando uma pela outra (dois canais cheios, um `recv` com o próprio `Sender` aberto na mesma thread, dois mutex travados em ordens diferentes) travam o programa, e o verificador não impede. A única exceção é travar o mesmo mutex duas vezes na mesma thread, que dá panic.
- **O que passa pela FFI.** Uma biblioteca de C que cria threads ou compartilha memória por conta própria fica fora da garantia.

## O que ficou de fora

**Threads com escopo que recebem empréstimos**, como várias threads processando pedaços disjuntos de um mesmo buffer no lugar (`&s[0:n/2]` para uma, `&s[n/2:]` para outra). Hoje o mesmo trabalho é feito de três jeitos:

- com os dados já divididos desde o início (um slice por trabalhador, movido para a thread e devolvido no `join`), que é o jeito idiomático e não custa nada a mais;
- copiando os pedaços e juntando depois, com a cópia visível no código;
- com tudo dentro de um `Mutex`, de forma segura mas sem paralelismo de verdade.

Ganhar o caso "no lugar, sem cópia" exigiria provar na compilação que os pedaços não se sobrepõem e que nenhum empréstimo sobrevive às threads. Isso é muita complexidade no verificador para um caso de nicho (processamento numérico pesado sobre um buffer único). O modelo atual se explica numa frase: **os dados atravessam por movimento, por canal ou dentro de um mutex**.

Se um programa real precisar de mais, o primeiro passo seria um **empréstimo só de leitura** (hoje todo `&T` permite alterar), que deixaria várias threads lerem os mesmos dados e serviria à linguagem inteira, não só às threads.

## Como funciona

`thread.spawn` e `thread.join` são reescritos pelo compilador, como o `io.print`. Para cada tipo de função de thread, o C gerado ganha um registro (o `mlc_thread` do runtime, o resultado, a função e o contexto) e uma função de entrada que chama `f(ctx)` e guarda o resultado. O `spawn` aloca o registro com `mlc_alloc` e cria a thread com `pthread_create`, com o tamanho de pilha pedido. O `join` faz `pthread_join`, tira o resultado e libera o registro.

Um canal é um buffer em anel no runtime, com uma trava e duas variáveis de condição (pthread). Cada ponta é um ponteiro para ele, e o runtime conta as pontas. `send` copia os bytes do valor para o anel e `recv` os copia para fora: a posse passa de um lado para o outro sem cópia escondida no heap. O `chan.new` entrega ao runtime a função que libera um valor do tipo, usada no último `close` e no `send` sem receptor.

Um mutex é um `pthread_mutex_t` do tipo que confere erros, mais uma variável de condição e o valor, logo depois do cabeçalho. Handles e guards são contados com um atômico. `get()` devolve um ponteiro para o valor dentro do mutex, e o verificador faz dele um empréstimo do guard.

O runtime é seguro para threads: o contador de alocações é atômico, e os destinos do `str.format` são por thread.
