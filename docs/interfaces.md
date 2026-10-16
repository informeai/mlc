# Interfaces e impressão

## Interfaces estáticas

Uma interface lista métodos que um tipo pode implementar:

```
interface Medida {
    fn tamanho(self &Self) int
    fn escala(self &Self, k int) int
}
```

- `Self` é o tipo que implementa. O primeiro parâmetro é sempre `self &Self`: o método recebe o valor **emprestado**.
- **Nesta versão, uma interface só serve como restrição de genérico**, nunca como tipo de variável:

```
fn maior[T Medida](a &T, b &T) int {     // ok
    if a.tamanho() > b.tamanho() {
        return a.tamanho()
    }
    return b.tamanho()
}

var m Medida                              // erro: interfaces só servem de restrição de genérico
```

Cada uso concreto (`maior[Quadrado]`, `maior[int]`) vira uma instância própria, e a chamada `a.tamanho()` dentro dela é uma **chamada direta** ao método do tipo, sem tabela de métodos, ponteiro de função nem alocação. É a mesma monomorfização dos outros genéricos.

## impl: a implementação é explícita

Ter os métodos não basta: um `impl` declara a intenção, e o compilador confere as assinaturas.

**Uma struct com os métodos já declarados** (receptor `&S`):

```
struct Quadrado {
    lado int
}

fn (q &Quadrado) tamanho() int {
    return q.lado * q.lado
}

fn (q &Quadrado) escala(k int) int {
    return q.tamanho() * k
}

impl Medida for Quadrado
```

**Com bloco**, para tipos que não têm métodos próprios (tipos embutidos, slices) ou quando os métodos só fazem sentido pela interface:

```
impl Medida for int {
    fn (x &int) tamanho() int {
        return *x
    }
    fn (x &int) escala(k int) int {
        return *x * k
    }
}
```

**Genérico**, para todo um formato de tipo:

```
impl[T Medida] Medida for []T {
    fn (s &[]T) tamanho() int {
        total := 0
        for i := 0; i < len(s); i++ {
            total += s[i].tamanho()
        }
        return total
    }
    fn (s &[]T) escala(k int) int {
        return len(s) * k
    }
}
```

Um array `[N]T` usa o impl de `[]T` (através de uma visão do array, sem cópia).

Implementar uma interface torna públicos os métodos dela: um tipo de um módulo pode ser chave de um `map.Map` de outro módulo mesmo sem `pub fn` nos métodos.

Os métodos de um impl também podem ser chamados direto: `n.tamanho()` com `n int`, `s.tamanho()` com `s []Quadrado`.

### Regras

- O impl fica **no arquivo do tipo ou no arquivo da interface**: dois módulos não podem implementar a mesma coisa de jeitos diferentes.
- Um tipo implementa cada interface uma vez só.
- O impl precisa ter todos os métodos da interface, com as assinaturas certas, e nenhum outro.
- Ponteiros e empréstimos não implementam interfaces (implemente para o tipo apontado). Slices e structs com donos podem: os métodos só recebem emprestado.
- Um dono (`*T`, `[]T`) não pode ser **argumento de tipo** de um genérico, como em toda a linguagem nesta versão: `maior(&s, &s)` com `s []int` é recusado, mas `s.tamanho()` funciona.

## Impressão: io.Display

`io.print` e `io.printf` escrevem qualquer valor que implemente `io.Display`:

```
// std/io.mlc
pub interface Display {
    fn write(self &Self, w &Writer)
}
```

Os tipos embutidos (`int`, `float64`, `bool`, `string`, `byte`...), os slices e os arrays já implementam, em MLC, na própria biblioteca ([std/io.mlc](../std/io.mlc)).

Para structs e enums, `impl auto` gera o método no formato de sempre:

```
impl auto io.Display for Ponto       // {1 2}
impl auto io.Display for Forma       // Circulo(2), Retangulo(3, 4), Vazio
impl auto io.Display for Par         // genérico: Par[int, string] escreve {1 um}
```

Cada campo precisa implementar `io.Display`; senão o erro aponta o campo. Também dá para escrever o método à mão:

```
fn (f &Fracao) write(w &io.Writer) {
    w.put_int(f.num)
    w.put_byte('/')
    w.put_int(f.den)
}

impl io.Display for Fracao
```

### io.printf

```
io.printf("ponto {} tem área {}\n", p, area(p))
io.printf("chaves: {{ e }}\n")
io.eprintf("erro: {}\n", code)          // no erro padrão
io.print(a, b, c)                       // valores separados por espaço, e \n
io.eprint(a, b, c)
```

- O formato é uma **string literal**, conferida na compilação: a quantidade de `{}` precisa bater com a de valores, e `{` ou `}` sozinhos são erro (`{{` e `}}` escrevem chaves).
- Um valor sem `io.Display` é erro de compilação, com a sugestão do `impl auto`.
- Os valores são calculados antes de escrever, da esquerda para a direita.
- Um empréstimo (`x` com `x &T`) é escrito sem cópia; dentro de um genérico `[T io.Display]`, `io.print(x)` também funciona.

### O que o compilador faz

Só a reescrita da chamada. `io.printf("x = {}\n", x)` vira, em essência:

```
w := io.stdout()              // Writer com buffer [256]byte na pilha
w.put_str("x = ")
x.write(&w)                   // chamada direta ao write do tipo de x
w.put_str("\n")
w.flush()
```

Toda a formatação é MLC, em `std/io.mlc`: o `Writer`, a escrita de inteiros, os `write` dos tipos embutidos. Só a formatação de números reais (o menor número de dígitos que representa o valor) e a escrita no arquivo ficam no runtime C. Nada aloca no heap.

### io.Writer

| Método | O que faz |
|---|---|
| `io.stdout()`, `io.stderr()` | criam um Writer (valor na pilha) |
| `w.put_str(s string)`, `w.put_bytes(b &[]byte)`, `w.put_byte(b byte)` | texto |
| `w.put_int(n int)`, `w.put_uint(n uint64)` | inteiros em decimal |
| `w.put_float(x float64)`, `w.put_f32(x float32)` | reais, no formato do Go |
| `w.put_bool(b bool)` | `true` / `false` |
| `w.flush()` | escreve o que está no buffer (também acontece quando ele enche) |

## O que fica para depois

- Interfaces dinâmicas (`&Medida`, `*Medida`), para guardar valores de tipos diferentes juntos.
- Especificadores de formato (`{:x}`, `{:.2}`, largura) e formatar para um buffer (`str.format`).
- Interfaces `Ord` e `Eq` para `sort.sort` aceitar structs.
- Métodos de interface que recebem `Self` (cópia) ou `*Self` (posse).
