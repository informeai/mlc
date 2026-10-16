/* Runtime da linguagem MLC. Compilado junto com o C gerado pelo mlc. */
#ifndef MLC_RUNTIME_H
#define MLC_RUNTIME_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

/* string: ponteiro + tamanho. Não precisa terminar em '\0'. */
typedef struct {
    const char *ptr;
    int64_t len;
} mlc_str;

#define MLC_STR_LIT(s, n) ((mlc_str){(s), (n)})

static inline bool mlc_str_eq(mlc_str a, mlc_str b) {
    return a.len == b.len && (a.len == 0 || memcmp(a.ptr, b.ptr, (size_t)a.len) == 0);
}

/* Encerra o programa com uma mensagem apontando para a linha do .mlc. */
_Noreturn void mlc_panic(const char *file, int line, const char *msg);

void mlc_io_print_int(int64_t v);
void mlc_io_print_bool(bool v);
void mlc_io_print_str(mlc_str s);
void mlc_io_print_sep(void);
void mlc_io_print_newline(void);

/* Memória. mlc_alloc devolve memória zerada e dá panic se faltar memória,
 * então um ponteiro *T nunca é nulo. O contador de alocações vivas é uma
 * rede de segurança: o compilador já garante que todo alloc tem seu free, e
 * mlc_check_allocs, chamada no fim do main, acusa erro interno se não tiver. */
void *mlc_alloc(size_t size, const char *file, int line);
void mlc_free(void *p);
void mlc_check_allocs(void);

#define MLC_ALLOC(T) ((T *)mlc_alloc(sizeof(T), __FILE__, __LINE__))

/* Slices. mlc_alloc_array devolve n elementos zerados (NULL se n == 0) e dá
 * panic com tamanho negativo ou grande demais. mlc_index dá panic fora dos
 * limites. */
void *mlc_alloc_array(int64_t n, size_t size, const char *file, int line);
_Noreturn void mlc_panic_index(int64_t i, int64_t len, const char *file, int line);

static inline int64_t mlc_index(int64_t i, int64_t len, const char *file, int line) {
    if (i < 0 || i >= len) mlc_panic_index(i, len, file, line);
    return i;
}

#define MLC_INDEX(i, len) mlc_index((i), (len), __FILE__, __LINE__)

/* realloc(s, n): copia os elementos antigos e zera os novos. Com n == 0, libera
 * e devolve NULL (slice vazio). */
void *mlc_realloc_array(void *old, int64_t old_len, int64_t n, size_t size, const char *file, int line);

/* Sub-slice s[a:b]: exige 0 <= a <= b <= len. */
void mlc_check_slice(int64_t lo, int64_t hi, int64_t len, const char *file, int line);

/* Aritmética inteira checada: estouro e divisão por zero viram panic em vez
 * de comportamento indefinido do C. file/line vêm de __FILE__/__LINE__, que
 * as diretivas #line do código gerado fazem apontar para o .mlc. */

static inline int64_t mlc_add(int64_t a, int64_t b, const char *file, int line) {
    int64_t r;
    if (__builtin_add_overflow(a, b, &r)) mlc_panic(file, line, "estouro de inteiro na soma");
    return r;
}

static inline int64_t mlc_sub(int64_t a, int64_t b, const char *file, int line) {
    int64_t r;
    if (__builtin_sub_overflow(a, b, &r)) mlc_panic(file, line, "estouro de inteiro na subtração");
    return r;
}

static inline int64_t mlc_mul(int64_t a, int64_t b, const char *file, int line) {
    int64_t r;
    if (__builtin_mul_overflow(a, b, &r)) mlc_panic(file, line, "estouro de inteiro na multiplicação");
    return r;
}

static inline int64_t mlc_div(int64_t a, int64_t b, const char *file, int line) {
    if (b == 0) mlc_panic(file, line, "divisão por zero");
    if (a == INT64_MIN && b == -1) mlc_panic(file, line, "estouro de inteiro na divisão");
    return a / b;
}

static inline int64_t mlc_mod(int64_t a, int64_t b, const char *file, int line) {
    if (b == 0) mlc_panic(file, line, "resto de divisão por zero");
    if (b == -1) return 0; /* INT64_MIN % -1 é indefinido em C */
    return a % b;
}

static inline int64_t mlc_neg(int64_t a, const char *file, int line) {
    if (a == INT64_MIN) mlc_panic(file, line, "estouro de inteiro na negação");
    return -a;
}

#define MLC_ADD(a, b) mlc_add((a), (b), __FILE__, __LINE__)
#define MLC_SUB(a, b) mlc_sub((a), (b), __FILE__, __LINE__)
#define MLC_MUL(a, b) mlc_mul((a), (b), __FILE__, __LINE__)
#define MLC_DIV(a, b) mlc_div((a), (b), __FILE__, __LINE__)
#define MLC_MOD(a, b) mlc_mod((a), (b), __FILE__, __LINE__)
#define MLC_NEG(a) mlc_neg((a), __FILE__, __LINE__)

#endif
