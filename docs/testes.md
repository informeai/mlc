# Testes (`mlc test`)

Um arquivo `nome_test.mlc` é um programa de teste: em vez de `fn main`, ele declara funções `fn test_nome()`, e o `mlc test` roda cada uma.

```
import "std/testing"
import "std/vec"

fn test_push() {
    v := vec.new[int](2)
    v.push(7)
    testing.eq_int(v.len(), 1)
    testing.eq_int(v.get(0), 7)
    v.free()
}

fn test_pop_empty() {
    testing.expect_panic()      // o teste só passa se terminar em panic
    v := vec.new[int](2)
    x := v.pop()
    testing.eq_int(x, 0)
    v.free()
}
```

```sh
mlc test                       # todos os *_test.mlc da pasta atual e das de dentro
mlc test std                   # os de uma pasta
mlc test std/vec_test.mlc      # os de um arquivo
mlc test -run pop std          # só os testes cujo nome contém "pop"
mlc test -v std/vec_test.mlc   # mostra cada teste e a saída dele
make test-std                  # os testes da biblioteca padrão
```

```
--- FALHOU: test_push (0.00s)
    std/vec_test.mlc:8: veio 2, esperado 1
FALHOU	std/vec_test.mlc	1 de 2 testes falharam	0.31s
ok	std/map_test.mlc	7 testes	0.28s
```

## Regras

- Um teste é uma função `test_*` do arquivo de teste, sem parâmetros nem retorno, e não pode ser genérica. As outras funções do arquivo são auxiliares comuns.
- O arquivo de teste não declara `fn main`: o `mlc test` gera o `main`. Ele não pode ser importado (`import "x_test"` é erro), e `mlc build` e `mlc run` o recusam.
- **Cada teste roda num processo só dele.** Um panic, uma falha ou um vazamento ficam no teste que os causou, e os outros continuam rodando. O teste começa sem argumentos de linha de comando (`os.arg_count()` é 1).
- **O teste é um programa MLC comum.** As regras de posse valem dentro dele, então o verificador cobra o `free` de tudo que o teste aloca.
- Os testes testam o módulo de fora, pelo que ele exporta (`pub`).
- A saída do teste (`io.print`) só aparece quando ele falha, ou com `-v`.

## `std/testing`

Uma verificação que falha mostra a linha dela no teste e conta como falha, mas o teste continua até o fim. Nenhuma das funções aloca: os textos são comparados onde estão.

| Função | Falha quando |
|---|---|
| `check(ok bool, msg string)` | `ok` é `false`; mostra `msg` |
| `fail(msg string)` | sempre; mostra `msg` |
| `eq_int(got int, want int)` | os inteiros são diferentes |
| `eq_uint(got uint64, want uint64)` | idem, sem sinal (mostra também em hexadecimal) |
| `eq_bool(got bool, want bool)` | os bools são diferentes |
| `eq_float(got float64, want float64)` | os floats são diferentes (comparação exata) |
| `near(got float64, want float64, tol float64)` | a diferença passa de `tol` (ou um deles é NaN) |
| `eq_str(got &[]byte, want string)` | o texto do buffer é diferente; mostra os dois entre aspas, com `\n` e os outros bytes de controle escapados |
| `eq_bytes(got &[]byte, want &[]byte)` | os buffers são diferentes |
| `expect_panic()` | o teste termina **sem** panic; com o panic, o teste passa (se nada antes dele falhou) |

`testing.B` é o estado de um benchmark: o campo `n` diz quantas vezes repetir (veja [Benchmarks](#benchmarks)).

Para structs, enums e o resto, use `check(a == b, "...")` ou um `match`.

## Resultado de cada teste

| Resultado | Quando |
|---|---|
| `ok` | terminou sem falhas (ou com o panic pedido por `expect_panic`) |
| `FALHOU` | uma verificação falhou, ou faltou o panic esperado |
| `PANIC` | panic que o teste não esperava; mostra a linha do panic |
| `VAZAMENTO` | sobrou memória no fim do processo. Isso não deveria acontecer, porque o verificador de posse impede; se acontecer, é bug do `mlc` |

O `mlc test` sai com código 1 se algum teste falhar ou algum arquivo não compilar.

## Benchmarks

Uma função `fn bench_nome(b &testing.B)` repete o trecho medido `b.n` vezes:

```
fn bench_sort_1000(b &testing.B) {
    s := alloc([]int, 1000)
    for i := 0; i < b.n; i++ {
        preenche(&s)
        sort.sort(&s)
    }
    free(s)
}
```

```sh
mlc test -bench . std                   # todos os benchmarks (depois dos testes)
mlc test -bench sort std/sort_test.mlc  # só os que têm "sort" no nome
mlc test -benchtime 2000 -bench . std   # medir cada um por 2 s (o padrão é 500 ms)
```

```
ok	std/sort_test.mlc	7 testes	0.30s
bench_sort_1000	        6730 vezes	       33049.3 ns/op
```

- O `mlc test -bench` roda o benchmark com `b.n` = 1, depois um `n` maior, até a medida passar do tempo pedido. É o mesmo método do `go test -bench`.
- Cada rodada é um processo, e o tempo é medido dentro do programa, sem contar a inicialização. Um panic ou uma falha param o benchmark e aparecem como num teste.
- Os benchmarks só rodam com `-bench`, e só se os testes do arquivo passaram. Um arquivo pode ter só benchmarks.
- O compilador C pode eliminar um laço que não faz nada (o resultado é `0.0 ns/op`). Use o resultado do trabalho (conferir uma soma, por exemplo) para o laço não sumir.

## Biblioteca padrão

Cada módulo da `std` tem testes ao lado do código, em `std/<módulo>_test.mlc`. Eles rodam no `go test ./...` (por `TestStdLibrary`) e com `make test-std`. Uma mudança na `std` vem com os testes dela.
