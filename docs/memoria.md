# Modelo de memória do MLC

> **Status:** implementado (Marcos 5a, 5b e 6): ponteiros donos e slices em variáveis, parâmetros e campos de struct.

## Princípio

O MLC usa o modelo do C: **toda alocação e toda liberação são escritas pelo programador**. Não existe coletor de lixo, contagem de referências escondida nem destrutor automático. O que muda em relação ao C é que o compilador **rejeita o programa quando não consegue provar** que o uso da memória está correto.

O verificador é conservador, por desenho. Ele pode recusar um programa correto que não sabe analisar, mas nunca aceita um programa com os bugs da tabela abaixo. Quando ele recusar código correto, a saída é reescrever o código de forma mais explícita, nunca desligar a checagem.

## Sintaxe

```
p := alloc(Ponto)              // *Ponto: ponteiro dono, memória zerada
q := alloc(Ponto{x: 1, y: 2})  // alloc com valor inicial
n := alloc(42)                 // *int
p.x = 10                       // campo através do ponteiro
*n += 1                        // conteúdo do ponteiro
free(p)                        // obrigatório em todos os caminhos

fn soma(p &Ponto) int          // &T: empréstimo; lê e escreve, não libera
fn destroi(p *Ponto)           // *T em parâmetro: a função recebe a posse

soma(&p)                       // emprestar um dono
soma(&local)                   // emprestar uma struct local

struct No {                    // campo dono: a lista é dona do próximo nó
    valor int
    prox ?*No
}
```

`alloc` encerra o programa com panic se faltar memória, para que `*T` nunca seja nulo. `free` de um `?*T` que vale `nil` não faz nada, como no C.

## Slices

```
s := alloc([]int, n)       // []int: dono de n elementos zerados
s[i] = 1                   // índice checado em tempo de execução
for v in s { }             // v é uma cópia de cada elemento
free(s)                    // obrigatório, como para *T

fn soma(s &[]int) int      // empréstimo: lê e escreve elementos, não libera
var vazio []int            // valor zero: slice vazio, sem obrigação de free
```

Um slice é dono, como `*T`, e segue as mesmas regras de posse: move ao ser atribuído ou passado para um parâmetro `[]T`, e precisa de `free` em todos os caminhos. Liberar um slice dentro do `for in` que o percorre é erro. `s[i]` num slice vazio não precisa de checagem de `nil`: a checagem de limites dá panic. Nesta versão os elementos não podem ser donos (`[]*No`, `[][]int`).

## Posse (ownership)

Todo ponteiro dono tem **exatamente um dono**: uma variável ou um campo. O verificador acompanha cada **caminho** dono, como `p`, `p.prox`, `p.prox.prox` ou `l.cabeca`, e conhece, em cada ponto do código, o conjunto de estados possíveis dele:

| Estado | Significado |
|---|---|
| dona | tem memória e precisa liberá-la ou passá-la adiante |
| nil | vale `nil` (só `?*T`), sem obrigação |
| liberada | `free` foi chamado, ou a posse passou adiante (movida) |

Onde dois caminhos de execução se juntam (fim de `if`, braços de `match`, saída de laço), os conjuntos se unem. Se um caminho libera e outro não, o dono fica "dona ou liberada", e as regras abaixo tratam isso como erro.

Regras:

1. **Usar** um dono que pode estar liberado é erro: uso depois de `free` ou de mover.
2. **`free`** em um dono que pode estar liberado é erro: `free` duplo.
3. Sair do escopo de uma variável dona (fim do bloco, `return`, `break`, `continue`) enquanto ela pode ter memória é erro de **vazamento**. A mensagem aponta a linha da alocação e por onde a saída aconteceu.
4. **Sobrescrever** uma variável ou campo que pode ter memória é erro, porque a memória antiga vazaria.
5. **Mover** a posse: `q := p`, `q = p`, `x := p.prox`, passar para um parâmetro `*T`, `return p`. Depois disso, a origem conta como liberada.
6. **Valor dono descartado** é vazamento: chamar `novo()` sem guardar o resultado, ou usar `novo().x`.
7. **Laços:** o corpo é analisado até o estado do começo da volta estabilizar (ponto fixo). Assim, a segunda volta em diante também é verificada: liberar dentro do laço um dono que veio de fora vira "`free` duplo em algum caminho", e alocar sem liberar vira "seria sobrescrita".

## `nil` e o fluxo

Usar o conteúdo de um `?*T` (`q.x`, `*q`, `&q`) ou passá-lo onde se espera `*T` só é permitido onde ele **não pode ser `nil` naquele ponto**. O verificador aprende isso das condições:

```
if q != nil { q.x = 1 }                  // dentro do if, q não é nil
for cur != nil { ...; cur = prox }       // dentro do for, cur não é nil
if p != nil && p.x > 0 { }               // o lado direito do && já sabe
if n.prox != nil { n.prox.valor }        // vale também para campos
```

O que o verificador aprendeu deixa de valer quando o valor muda: depois de `q = nil`, ou depois de passar `&p` para uma função que pode ter alterado `p.prox`, é preciso checar de novo.

Um valor que sai de `alloc` nunca é `nil`, mesmo guardado num `?*T`, e o verificador sabe disso.

## Ponteiros dentro de structs

- **Invariante:** todo campo dono de um objeto vale `nil` ou é dono de memória.
- **Campos de um objeto novo:** o verificador conhece o estado dos campos de um objeto que acabou de sair de `alloc`. Em `alloc(No)`, os campos ponteiro valem `nil`. Em `alloc(No{...})`, valem o que o literal diz.
- **Campos de um objeto vindo de fora** (parâmetro, resultado de função): cada campo "pode ser dono ou `nil`".
- **`free(p)`** só é aceito quando nenhum campo dono de `*p` ainda pode ter memória. Os campos precisam ser liberados ou movidos antes, então liberar um objeto nunca deixa memória pendurada.
- **Objeto inteiro:** um objeto com um campo movido para fora só pode ser usado como um todo (movido, emprestado, devolvido, liberado) depois que esse campo recebe um valor novo.
- **Empréstimo:** um campo de um objeto emprestado (`&T`) pode ser movido para fora, desde que receba um valor antes de a função terminar. Quem emprestou recebe o objeto inteiro de volta.

Uma struct com campo dono **contém posse** e só existe atrás de um ponteiro. Ela não pode ser variável local por valor, parâmetro ou retorno por valor, nem ser copiada (`*p`), comparada ou impressa. O literal dela só aparece dentro de `alloc(...)`. Sem essa regra, copiar a struct duplicaria os donos.

Exemplo, liberar uma lista inteira:

```
for cur != nil {
    prox := cur.prox   // move o campo para fora: cur.prox fica liberado
    free(cur)          // aceito: o único campo dono já foi movido
    cur = prox         // cur estava liberada, então pode receber outro dono
}
```

E inserir na frente de uma lista emprestada:

```
fn push(l &Lista, v int) {
    n := alloc(No)
    n.valor = v
    n.prox = l.cabeca      // move a cabeça para fora do empréstimo...
    l.cabeca = n           // ...e devolve um valor antes de sair
}
```

## Empréstimos (`&T`)

Para manter a análise simples e correta, os empréstimos são restritos:

- `&x` só aparece como **argumento de chamada**. `&T` só existe como tipo de parâmetro: não pode ser guardado em variável, em campo, nem devolvido.
- Um empréstimo recebido pode ser repassado direto a outro parâmetro `&T`, sem `&`.
- Na mesma chamada, não se pode emprestar e mover partes do mesmo objeto: `f(&p, p)` e `f(&p, p.prox)` são erros.
- Quem recebe um empréstimo não pode liberá-lo nem reatribuí-lo.

Com essas restrições, um empréstimo nunca sobrevive ao dono, e por isso não existe ponteiro pendente. A consequência é que percorrer uma estrutura só para ler é feito com recursão (`soma(&n.prox)`), e não com uma variável que aponta para o nó atual. Empréstimos guardados em variáveis, com análise de tempo de vida, podem vir depois, se fizerem falta.

## O que é proibido

- Aritmética de ponteiros e conversão entre tipos de ponteiro.
- Ponteiro para ponteiro.
- Comparar ponteiros entre si (só com `nil`) e imprimir ponteiros.
- Tomar endereço para guardar: `&` só existe como empréstimo em chamada.
- Variável sem inicialização: toda variável começa com valor zero, e `var p *T` exige valor inicial.
- `alloc(T)` zerado quando `T` tem um campo `*T`, que não pode ser `nil`. Nesse caso, use `alloc(T{...})`.

## Rede de segurança em tempo de execução

O runtime conta as alocações vivas. No fim do `main`, se sobrou alguma, o programa acusa **erro interno** e sai com código 3. Pelas regras acima isso nunca deveria acontecer: o contador existe para pegar bugs do próprio verificador. Todos os testes que executam programas passam por essa checagem.

## Bugs do C que o MLC elimina

| Bug do C | Como o MLC trata | Quando |
|---|---|---|
| use-after-free | estado de cada caminho dono | compilação |
| double free | estado de cada caminho dono | compilação |
| vazamento de memória | dono vivo ao sair do escopo; `free` exige campos liberados | compilação |
| ponteiro nulo | `*T` nunca é nulo; `?*T` exige checagem, acompanhada pelo fluxo | compilação |
| ponteiro pendente para a pilha | empréstimo não pode ser guardado | compilação |
| variável não inicializada | valor zero obrigatório | compilação |
| estouro de inteiro com sinal | `__builtin_*_overflow` | execução (panic) |
| divisão por zero | checagem antes de dividir | execução (panic) |
| ordem de avaliação indefinida | esquerda para a direita, escrita no C gerado | compilação |
| string de formato (`printf`) | `io.print` gera chamadas tipadas | compilação |
| buffer overflow | checagem de limites em slices e strings | execução (panic) |

## O que o MLC não garante

- **Vazamento lógico:** guardar memória por mais tempo que o necessário é decisão do programador, e continua sendo possível.
- **Falta de memória:** `alloc` encerra o programa, não se recupera.
- **Concorrência:** não há threads na linguagem por enquanto, então data races ficam fora do escopo.
- **Código C externo:** quando houver FFI, o que acontece do lado do C não é verificado.
- **Estruturas compartilhadas:** com um único dono por objeto, grafos com ciclos ou nós com dois pais (lista duplamente ligada) não são expressáveis com ponteiros donos. Isso exigiria outro mecanismo, como índices num slice ou contagem de referências explícita.
