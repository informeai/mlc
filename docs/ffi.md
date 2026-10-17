# Funções de C (FFI)

Qualquer arquivo pode chamar funções de bibliotecas de C. A fronteira é conferida pelo compilador: o que passa de um lado para o outro tem regras de posse claras.

```
import "std/io"

link "m"                                            // -lm na ligação

extern "C" type FILE                                // handle opaco de C

extern "C" fn strlen(s string) uint64
extern "C" fn cbrt(x float64) float64
extern "C" fn memcmp(a &[]byte, b &[]byte, n uint64) int32
extern "C" fn fopen(caminho string, modo string) ?*FILE
extern "C" fn fputs(s string, f &FILE) int32
extern "C" fn fclose(f *FILE) int32                 // recebe a posse do handle

fn main() {
    io.print(strlen("olá"), cbrt(27.0))
    f := fopen("saida.txt", "w")
    if f != nil {
        fputs("oi\n", &f)
        fclose(f)                                   // o verificador cobra este fechamento
    }
}
```

## Declarar

- `extern "C" fn nome(...)` chama o símbolo de C `nome`. Não tem corpo, não é genérica nem método.
- **A assinatura precisa bater com a do C.** O compilador não lê cabeçalhos de C, e um tipo errado é comportamento indefinido do lado do C.

  | C | MLC |
  |---|---|
  | `int` | `int32` |
  | `long`, `int64_t` (Linux e macOS) | `int` |
  | `size_t`, `uint64_t` | `uint64` |
  | `unsigned char`, `uint8_t` | `byte` |
  | `double`, `float` | `float64`, `float32` |
  | `_Bool` | `bool` |
  | `const char *` (texto que o C só lê) | `string` |
  | `T *` para dados que o C lê ou escreve | `&[]T` (o ponteiro para os dados) ou `&T` |

- **Funções variádicas** (`printf`, `open` com modo) **não são aceitas**: a forma de passar os argumentos muda conforme a plataforma.
- `link "nome"` pede a biblioteca `libnome` na ligação (`-lnome`). `mlc build -L pasta` e `mlc run -L pasta` dizem onde procurá-la.

## O que atravessa a fronteira

| Tipo | Como | Regra |
|---|---|---|
| números, `bool` | por valor | |
| `string` | `const char *` | os literais do MLC terminam em zero; o C só lê |
| `&[]T` (`T` número ou `bool`) | o ponteiro para os dados | o tamanho vai num parâmetro à parte; o C não pode guardar o ponteiro |
| `&T` (`T` número, `bool`, struct só de campos escalares, ou handle) | ponteiro | o C não pode guardar o ponteiro |
| `*X` (`X` um `extern "C" type`) | o handle | a função de C **recebe a posse** (fecha ou libera o handle) |
| retorno `?*X` | o handle (ou `NULL`) | um dono novo, que o verificador obriga a fechar |

**Nada que seja dono de memória do MLC atravessa:** um slice dono (`[]byte`), um ponteiro dono (`*P`) ou uma struct com donos são recusados. O C não saberia liberar essa memória e poderia guardá-la. Para entregar dados, empreste com `&`.

## Handles de C

`extern "C" type Banco` declara um tipo opaco. Ele só existe atrás de ponteiro (`?*Banco`, `*Banco`, `&Banco`), e é um dono como os outros:

- vem de uma função de C (`?*Banco`, que pode ser `NULL`);
- é emprestado com `&b` às funções que só o usam;
- precisa ser **passado a uma função de C que o recebe por posse** (`fn fecha(b *Banco)`); esquecer é vazamento, detectado na compilação;
- não dá para usar `free` nele, ver o conteúdo, criá-lo com `alloc` ou guardá-lo num `Vec` genérico (o `drop` não saberia que função de C o fecha).

## Erros de ligação

Se uma biblioteca de `link "..."` ou uma função de C não existir, `mlc build` diz que não foi possível ligar o programa e mostra a mensagem do ligador. Esse é um erro do programa, não do compilador.

## O que fica de fora nesta versão

- Ponteiros devolvidos pelo C para memória que o MLC liberaria (`char *` de `strdup`): use um handle opaco e a função de C que libera.
- Callbacks do C para o MLC (ponteiros de função na fronteira).
- Structs por valor na fronteira e funções variádicas.
