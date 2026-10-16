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

/* []byte: buffer de texto. Definido aqui porque as funções de texto do runtime
 * o recebem; o código gerado não declara de novo. */
typedef struct {
    uint8_t *ptr;
    int64_t len;
} mlc_sl_byte;

static inline bool mlc_string_eq(mlc_str a, mlc_str b) {
    return a.len == b.len && (a.len == 0 || memcmp(a.ptr, b.ptr, (size_t)a.len) == 0);
}

/* Encerra o programa com uma mensagem apontando para a linha do .mlc. */
_Noreturn void mlc_panic(const char *file, int line, const char *msg);

void mlc_io_print_int(int64_t v);
void mlc_io_print_uint(uint64_t v);
void mlc_io_print_f64(double v);
void mlc_io_print_f32(float v);
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
 * e devolve NULL (slice vazio). Com grow_only (slice de donos), n menor que o
 * tamanho atual dá panic: encolher descartaria elementos que podem ter memória. */
void *mlc_realloc_array(void *old, int64_t old_len, int64_t n, size_t size, int grow_only, const char *file, int line);

/* Sub-slice s[a:b]: exige 0 <= a <= b <= len. */
void mlc_check_slice(int64_t lo, int64_t hi, int64_t len, const char *file, int line);

/* Aritmética inteira checada, para cada tipo inteiro (sufixos i8, i16, i32,
 * i64, u8, u16, u32, u64): estouro e divisão por zero viram panic em vez de
 * comportamento indefinido do C. file/line vêm de __FILE__/__LINE__, que as
 * diretivas #line do código gerado fazem apontar para o .mlc.
 * __builtin_*_overflow checa se o resultado exato cabe no tipo de r. */

#define MLC_INT_COMMON(T, S, NAME)                                                     \
    static inline T mlc_add_##S(T a, T b, const char *file, int line) {               \
        T r;                                                                           \
        if (__builtin_add_overflow(a, b, &r)) mlc_panic(file, line, "estouro de " NAME " na soma"); \
        return r;                                                                      \
    }                                                                                  \
    static inline T mlc_sub_##S(T a, T b, const char *file, int line) {               \
        T r;                                                                           \
        if (__builtin_sub_overflow(a, b, &r)) mlc_panic(file, line, "estouro de " NAME " na subtração"); \
        return r;                                                                      \
    }                                                                                  \
    static inline T mlc_mul_##S(T a, T b, const char *file, int line) {               \
        T r;                                                                           \
        if (__builtin_mul_overflow(a, b, &r)) mlc_panic(file, line, "estouro de " NAME " na multiplicação"); \
        return r;                                                                      \
    }

/* Com sinal: MIN / -1 estoura, MIN % -1 é indefinido em C, -MIN estoura. */
#define MLC_INT_SIGNED(T, S, MIN, NAME)                                                \
    MLC_INT_COMMON(T, S, NAME)                                                         \
    static inline T mlc_div_##S(T a, T b, const char *file, int line) {               \
        if (b == 0) mlc_panic(file, line, "divisão por zero");                        \
        if (a == MIN && b == -1) mlc_panic(file, line, "estouro de " NAME " na divisão"); \
        return (T)(a / b);                                                             \
    }                                                                                  \
    static inline T mlc_mod_##S(T a, T b, const char *file, int line) {               \
        if (b == 0) mlc_panic(file, line, "resto de divisão por zero");               \
        if (b == -1) return 0;                                                         \
        return (T)(a % b);                                                             \
    }                                                                                  \
    static inline T mlc_neg_##S(T a, const char *file, int line) {                    \
        if (a == MIN) mlc_panic(file, line, "estouro de " NAME " na negação");        \
        return (T)(-a);                                                                \
    }

#define MLC_INT_UNSIGNED(T, S, NAME)                                                   \
    MLC_INT_COMMON(T, S, NAME)                                                         \
    static inline T mlc_div_##S(T a, T b, const char *file, int line) {               \
        if (b == 0) mlc_panic(file, line, "divisão por zero");                        \
        return (T)(a / b);                                                             \
    }                                                                                  \
    static inline T mlc_mod_##S(T a, T b, const char *file, int line) {               \
        if (b == 0) mlc_panic(file, line, "resto de divisão por zero");               \
        return (T)(a % b);                                                             \
    }

MLC_INT_SIGNED(int8_t, i8, INT8_MIN, "int8")
MLC_INT_SIGNED(int16_t, i16, INT16_MIN, "int16")
MLC_INT_SIGNED(int32_t, i32, INT32_MIN, "int32")
MLC_INT_SIGNED(int64_t, i64, INT64_MIN, "inteiro")
MLC_INT_UNSIGNED(uint8_t, u8, "byte")
MLC_INT_UNSIGNED(uint16_t, u16, "uint16")
MLC_INT_UNSIGNED(uint32_t, u32, "uint32")
MLC_INT_UNSIGNED(uint64_t, u64, "uint64")

/* Conversões que podem não caber dão panic. mlc_conv_i: valor com sinal na
 * faixa [lo, hi]; mlc_conv_u: valor sem sinal até hi; mlc_conv_f: decimal
 * que, truncado, cabe no inteiro (lo < x < hi, limites exclusivos; NaN nunca
 * cabe). Converter fora da faixa é comportamento indefinido em C. */
int64_t mlc_conv_i(int64_t v, int64_t lo, int64_t hi, const char *type, const char *file, int line);
uint64_t mlc_conv_u(uint64_t v, uint64_t hi, const char *type, const char *file, int line);
double mlc_conv_f(double v, double lo, double hi, const char *type, const char *file, int line);

/* float64 para float32: fora da faixa vira infinito (em C seria indefinido). */
float mlc_f64_to_f32(double v);

/* Argumentos da linha de comando, guardados pelo main gerado para std/os. */
void mlc_args_init(int argc, char **argv);

#endif
