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

## Memória sem heap: arrays

Um array `[N]T` tem tamanho fixo, conhecido na compilação, e é um valor: vive na pilha (ou dentro de uma struct) e some quando o escopo termina, sem `alloc` nem `free`. É a forma mais explícita de memória: o tipo diz exatamente quantos bytes ocupa. Para não estourar a pilha, um array tem no máximo 1 MiB; para mais, use um slice.

## Slices

```
s := alloc([]int, n)       // []int: dono de n elementos zerados
s[i] = 1                   // índice checado em tempo de execução
for v in s { }             // v é uma cópia de cada elemento
free(s)                    // obrigatório, como para *T

fn soma(s &[]int) int      // empréstimo: lê e escreve elementos, não libera
var vazio []int            // valor zero: slice vazio, sem obrigação de free
```

### Slices de donos

```
nos := alloc([]?*No, n)          // elementos começam nil (por isso ?*T, e não *T)
velho := replace(&nos[i], novo)  // põe novo no lugar e devolve o antigo
n := take(&nos[i])               // tira o elemento e deixa nil no lugar
for n in drain(nos) {            // cada elemento sai do slice para n...
    free(n)                      // ...e precisa ser liberado ou movido na volta
}
free(nos)                        // aceito na compilação: drain tirou todos

m := alloc([][]int, 3)           // matriz: linhas começam como slice vazio
m[1][2] = 5                      // indexar não move nada
```

O verificador não acompanha elemento por elemento (o índice só existe em tempo de execução). Ele sabe, para cada slice de donos, se os elementos estão **todos vazios** ou se **podem ter donos**:

- logo depois de `alloc([]?*T, n)`, estão todos vazios;
- depois de `replace` com um dono, podem ter donos;
- depois de um `for x in drain(s)` completo (sem `break`), estão todos vazios;
- um slice recebido por parâmetro ou devolvido por função pode ter donos.

`free(s)` só é aceito com os elementos todos vazios, então vazamento de elemento é **erro de compilação**. Para manter a contabilidade certa:

- não se copia nem se sobrescreve um elemento dono direto (`x := s[i]`, `s[i] = x`): use `take` e `replace`;
- não se usa o conteúdo de `s[i]` direto, porque ele pode ser `nil` e o índice é dinâmico: tire com `take`, use, devolva com `replace`;
- dentro de um `for x in drain(s)`, `s` não recebe elementos nem é emprestado.

`realloc` num slice de donos só pode crescer: encolher descartaria elementos que podem ter memória, e isso dá panic em tempo de execução. É uma precondição, como a checagem de limites; vazamento continua sendo sempre erro de compilação.

### Crescer e emprestar parte

```
s = realloc(s, len(s) * 2)   // consome o slice antigo e devolve um novo
soma(&s[1:3])                // empresta os elementos 1 e 2
soma(&s[2:])                 // do 2 até o fim
```

`realloc` se comporta como uma função que recebe a posse do slice antigo e devolve a de um slice novo: os elementos que já existiam são copiados, e os novos vêm zerados. Por isso não há como ficar com um nome apontando para o bloco antigo, o bug clássico do `realloc` em C. Encolher descarta os elementos do fim; `realloc(s, 0)` libera e devolve o slice vazio. Não existe crescimento automático (`append`): cada realocação aparece no código.

Um sub-slice `&s[a:b]` não é dono de nada: é um empréstimo dos elementos de `a` até `b-1` e segue as regras dos empréstimos (só como argumento de chamada). Os limites são checados em tempo de execução: `0 <= a <= b <= len(s)`. `a` e `b` podem ser omitidos (`&s[:b]`, `&s[a:]`, `&s[:]`).

Um slice é dono, como `*T`, e segue as mesmas regras de posse: move ao ser atribuído ou passado para um parâmetro `[]T`, e precisa de `free` em todos os caminhos. Liberar um slice dentro do `for in` que o percorre é erro. `s[i]` num slice vazio não precisa de checagem de `nil`: a checagem de limites dá panic. Os elementos podem ser donos; veja a seção seguinte.

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

- `&x` aparece como **argumento de chamada**, como valor de uma variável (`x := &a.campo`) ou num `return`. `&T` não pode ser guardado em campos, slices ou arrays.
- Um empréstimo recebido pode ser repassado direto a outro parâmetro `&T`, sem `&`.
- Na mesma chamada, não se pode emprestar e mover partes do mesmo objeto: `f(&p, p)` e `f(&p, p.prox)` são erros.
- Quem recebe um empréstimo não pode liberá-lo nem reatribuí-lo.

### Devolver e guardar empréstimos

```
fn (v &Vec[T]) at(i int) &T {
    return &v.items[i]          // o resultado vem do receptor
}

x := v.at(0)                    // x: &T, guardado numa variável
x.campo = 3
io.print(*v.at(1), v.len())     // lido na hora: o valor é copiado ali
```

- **De onde vem o empréstimo devolvido:** do receptor `&`, num método, ou do único parâmetro `&`, numa função. O `return` é conferido: `&v.items[i]` vale; `&local` (sumiria no fim da função), um sub-slice (a visão é criada na hora) ou uma origem ambígua (dois parâmetros `&`) são erros.
- **Congelamento:** enquanto uma variável emprestada é usada (da declaração até o último uso; num laço, até o fim do laço), a origem não pode ser mencionada a não ser através dela. Não dá para crescer, liberar, mover ou passar de novo a origem enquanto um pedaço dela está emprestado. Dois parâmetros `&` podem apontar para o mesmo objeto, então emprestar de um congela os dois.
- **Na mesma instrução:** uma chamada que devolve um empréstimo de `m` não pode dividir a instrução com outra menção de `m`, a não ser que o empréstimo seja só lido na hora (`*m.get(&k)`, `m.at(0).campo`).
- **Donos dentro de um empréstimo guardado** podem sair e precisam voltar antes do fim do escopo da variável, como num parâmetro `&T`.

Com essas regras, um empréstimo nunca sobrevive ao dono nem enxerga memória que mudou de lugar, e por isso não existe ponteiro pendente.

## Coleções que crescem

A linguagem não tem `append` nem coleção que cresce sozinha. Para isso existe `std/vec`, escrito em MLC: `vec.new[T](cap)` aloca, `push` chama `grow` (que faz `realloc` dobrando a capacidade) quando falta espaço, e `free()` libera. A alocação fica à vista no código da biblioteca, e o `*Vec[T]` devolvido por `new` é um dono comum, cobrado pelo verificador. Veja [biblioteca.md](biblioteca.md).

## Donos em genéricos

Um genérico com `[T owned]` pode guardar donos (`Vec[[]byte]`, `Vec[?*No]`), inclusive aninhados (`Vec[?*vec.Vec[int]]`, `Vec[[][]byte]`, árvores), quando cada struct com donos tem um método `fn (x *X) free()` que libera os campos e o próprio objeto: o `drop` genérico o chama, e o verificador confere o `free` como qualquer outra função. Dentro dele, um valor `T` é tratado como um `?*X`: atribuir move, usar depois de mover é erro, e todo valor precisa de destino. `drop(x)` é a forma genérica de liberar: vira `free` quando `T` é dono e não faz nada quando não é. Como em todo genérico, cada instância concreta passa pelo verificador; além disso, cada genérico com `owned` é verificado com `T = []byte`, para os erros aparecerem na definição. Coleções como `Vec` liberam os elementos no `free()`, escrito em MLC e à vista na biblioteca.

## Funções de C e handles

Uma função de C (`extern "C" fn`) só recebe o que não é dono de memória do MLC: números, `string`, empréstimos (`&[]T`, `&T`) e handles de C. Um handle (`?*X`, com `X` um `extern "C" type`) é um dono como os outros: vem de uma função de C e precisa ser passado a uma função de C que o recebe por posse (`*X`); `free` não vale para ele. Assim o verificador também garante que arquivos, conexões e outros recursos de C são fechados. Veja [ffi.md](ffi.md).

## Funções como valor

Um valor `fn(T) R` é só o endereço de uma função: como não captura variáveis, não aloca nem tem dono, e pode ser copiado livremente. Uma chamada através dele segue os tipos dos parâmetros (`&T` empresta, `*T` move), e o verificador confere como numa chamada direta. O valor zero é nulo; chamá-lo dá panic, nunca comportamento indefinido.

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
| string de formato (`printf`) | formato de `io.printf` conferido contra os valores; cada um escrito pelo seu `io.Display` | compilação |
| buffer overflow | checagem de limites em slices e strings | execução (panic) |

## O que o MLC não garante

- **Vazamento lógico:** guardar memória por mais tempo que o necessário é decisão do programador, e continua sendo possível.
- **Falta de memória:** `alloc` encerra o programa, não se recupera.
- **Concorrência:** não há threads na linguagem por enquanto, então data races ficam fora do escopo.
- **Código C externo:** as `extern fn` da biblioteca padrão são implementadas no runtime C e não passam pelo verificador; o código delas é revisado à mão. Programas comuns não podem declarar `extern fn`.
- **Moves numa chamada** valem quando a chamada acontece, depois de todos os argumentos: `f(s, len(s))` é aceito, mas mover duas vezes (`f(s, s)`) ou mover e emprestar (`f(s, &s)`) na mesma chamada é erro.
- **Estruturas compartilhadas:** com um único dono por objeto, grafos com ciclos ou nós com dois pais (lista duplamente ligada) não são expressáveis com ponteiros donos. Isso exigiria outro mecanismo, como índices num slice ou contagem de referências explícita.
