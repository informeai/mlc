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
  | `X **` onde o C escreve um handle novo | `&?*X` (parâmetro de saída) |
  | qualquer um dos de cima, quando o C aceita `NULL` | `?&T`, `?&[]T`, `?&?*X` |

- **Funções variádicas** (`printf`, `open` com modo) **não são aceitas**: a forma de passar os argumentos muda conforme a plataforma.
- `link "nome"` pede a biblioteca `libnome` na ligação (`-lnome`). `mlc build -L pasta` e `mlc run -L pasta` dizem onde procurá-la. Para o resto da ligação (um `-framework` do macOS, um `-Wl,-rpath,pasta`, uma biblioteca estática pelo caminho), use `-ldflags "..."` ou a variável `LDFLAGS`; veja [O compilador C](../README.md#o-compilador-c).

## O que atravessa a fronteira

| Tipo | Como | Regra |
|---|---|---|
| números, `bool` | por valor | |
| `string` | `const char *` | os literais do MLC terminam em zero; o C só lê |
| `&[]T` (`T` número ou `bool`) | o ponteiro para os dados | o tamanho vai num parâmetro à parte; o C não pode guardar o ponteiro |
| `&T` (`T` número, `bool`, struct só de campos escalares, ou handle) | ponteiro | o C não pode guardar o ponteiro |
| `*X` (`X` um `extern "C" type`) | o handle | a função de C **recebe a posse** (fecha ou libera o handle) |
| retorno `?*X` | o handle (ou `NULL`) | um dono novo, que o verificador obriga a fechar |
| `&?*X` | o endereço de uma variável `?*X` (`X **`) | a função de C escreve um handle novo nela; a variável não pode ter handle antes e, depois, precisa ser fechada |
| `?&T`, `?&[]T`, `?&?*X` | `NULL` (com `nil`) ou o mesmo que `&T`, `&[]T`, `&?*X` | as mesmas regras do empréstimo ou do handle, quando não é `nil` |

**Nada que seja dono de memória do MLC atravessa:** um slice dono (`[]byte`), um ponteiro dono (`*P`) ou uma struct com donos são recusados. O C não saberia liberar essa memória e poderia guardá-la. Para entregar dados, empreste com `&`.

## Handles de C

`extern "C" type Banco` declara um tipo opaco. Ele só existe atrás de ponteiro (`?*Banco`, `*Banco`, `&Banco`), e é um dono como os outros:

- vem de uma função de C (`?*Banco`, que pode ser `NULL`);
- é emprestado com `&b` às funções que só o usam;
- precisa ser **passado a uma função de C que o recebe por posse** (`fn fecha(b *Banco)`); esquecer é vazamento, detectado na compilação;
- não dá para usar `free` nele, ver o conteúdo, criá-lo com `alloc` ou guardá-lo num `Vec` genérico (o `drop` não saberia que função de C o fecha).

## Handles devolvidos por ponteiro (`&?*X`)

Muitas bibliotecas devolvem o handle num parâmetro e usam o retorno para o código de erro:

```
link "sqlite3"

extern "C" type sqlite3
extern "C" fn sqlite3_open(name string, db &?*sqlite3) int32     // int sqlite3_open(const char *, sqlite3 **)
extern "C" fn sqlite3_close(db *sqlite3) int32

fn main() {
    var db ?*sqlite3 = nil
    rc := sqlite3_open("dados.db", &db)
    if db != nil {
        // ... usa &db ...
        sqlite3_close(db)                   // o verificador cobra este fechamento
    }
    io.print("rc", rc)
}
```

- `&?*X` só existe em parâmetros de `extern "C" fn`, e só para handles (`X` um `extern "C" type`).
- O argumento é `&h`, com `h` uma variável ou campo `?*X`. **`h` não pode ter um handle aberto** antes da chamada (precisa ser `nil` ou já ter sido fechado), porque a função de C escreveria outro por cima e o antigo vazaria.
- Depois da chamada, `h` pode ter um handle ou ser `nil` (a função pode falhar). Como qualquer `?*X`, ele precisa de `if h != nil` antes de ser fechado, e esquecer de fechar é vazamento detectado na compilação.
- A mesma variável não pode receber dois handles na mesma chamada (`f(&h, &h)`).

## Ponteiros que podem ser `NULL` (`?&T`)

Em C, `NULL` num parâmetro costuma querer dizer "não me importo" ou "use o padrão". Passar outra coisa no lugar (uma struct zerada, um ponteiro para uma variável que ninguém lê) às vezes dá o mesmo resultado e às vezes não, conforme o sistema. Com `?&T`, a declaração diz que o parâmetro aceita `NULL`, e a chamada passa `nil` ou um empréstimo:

```
extern "C" type AddrInfo
extern "C" type Msg

extern "C" fn getaddrinfo(node string, service string, hints ?&Hints, res &?*AddrInfo) int32
extern "C" fn time(t ?&int) int
extern "C" fn sqlite3_exec(db &sqlite3, sql string, callback ?&byte, arg ?&byte, errmsg ?&?*Msg) int32
extern "C" fn sqlite3_free(m *Msg)

getaddrinfo("localhost", "80", nil, &res)      // hints NULL: o padrão do sistema
now := time(nil)

var msg ?*Msg = nil
rc := sqlite3_exec(&db, sql, nil, nil, &msg)   // com &msg, o SQLite aloca a mensagem de erro
if msg != nil {
    sqlite3_free(msg)                           // e o verificador cobra este free
}
```

- `?&T` só existe em parâmetros de `extern "C" fn`. Numa função MLC, use `?*T` ou duas funções.
- `?&T` e `?&[]T` aceitam `nil` ou o mesmo empréstimo que `&T` e `&[]T`, com as mesmas regras: o C só usa o ponteiro durante a chamada.
- `?&?*X` é o handle devolvido por ponteiro opcional. Com `nil`, o C não devolve nada. Com `&h`, valem as regras do `&?*X`: `h` não pode ter handle antes e precisa ser fechado depois.
- Para um parâmetro de um tipo que o MLC não representa (um ponteiro de função, por exemplo) e que você sempre passa como `NULL`, qualquer `?&T` serve (`?&byte`), já que só o `NULL` atravessa.

## Erros de ligação

Se uma biblioteca de `link "..."` ou uma função de C não existir, `mlc build` diz que não foi possível ligar o programa e mostra a mensagem do ligador. Esse é um erro do programa, não do compilador.

## O que fica de fora nesta versão

- Ponteiros devolvidos pelo C para memória que o MLC liberaria (`char *` de `strdup`): use um handle opaco e a função de C que libera.
- Callbacks do C para o MLC (ponteiros de função na fronteira). Um parâmetro de callback que sempre recebe `NULL` pode ser declarado como `?&byte`.
- Structs por valor na fronteira e funções variádicas.
