#include "mlc_runtime.h"

#include <float.h>
#include <inttypes.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>

void mlc_panic(const char *file, int line, const char *msg) {
    fflush(stdout);
    fprintf(stderr, "panic: %s:%d: %s\n", file, line, msg);
    exit(2);
}

void mlc_io_print_int(int64_t v) { printf("%" PRId64, v); }

void mlc_io_print_bool(bool v) { fputs(v ? "true" : "false", stdout); }

void mlc_io_print_str(mlc_str s) {
    if (s.len > 0) fwrite(s.ptr, 1, (size_t)s.len, stdout); /* ptr pode ser NULL na string zero */
}

void mlc_io_print_sep(void) { putchar(' '); }

void mlc_io_print_newline(void) { putchar('\n'); }

static int64_t mlc_live_allocs = 0;

void *mlc_alloc(size_t size, const char *file, int line) {
    void *p = calloc(1, size);
    if (p == NULL) mlc_panic(file, line, "memória esgotada");
    mlc_live_allocs++;
    return p;
}

void mlc_free(void *p) {
    if (p == NULL) return; /* free de um ?*T que vale nil */
    mlc_live_allocs--;
    free(p);
}

void mlc_check_allocs(void) {
    if (mlc_live_allocs != 0) {
        fflush(stdout);
        fprintf(stderr, "mlc: erro interno: %" PRId64 " alocação(ões) sem free no fim do programa (o verificador de posse deveria ter impedido)\n",
                mlc_live_allocs);
        exit(3);
    }
}

void *mlc_alloc_array(int64_t n, size_t size, const char *file, int line) {
    if (n < 0) mlc_panic(file, line, "tamanho negativo em alloc de slice");
    if (n == 0) return NULL; /* slice vazio: não conta como alocação */
    if ((uint64_t)n > SIZE_MAX / size) mlc_panic(file, line, "memória esgotada");
    void *p = calloc((size_t)n, size);
    if (p == NULL) mlc_panic(file, line, "memória esgotada");
    mlc_live_allocs++;
    return p;
}

void mlc_panic_index(int64_t i, int64_t len, const char *file, int line) {
    char msg[96];
    snprintf(msg, sizeof msg, "índice %" PRId64 " fora dos limites (tamanho %" PRId64 ")", i, len);
    mlc_panic(file, line, msg);
}

void *mlc_realloc_array(void *old, int64_t old_len, int64_t n, size_t size, int grow_only, const char *file, int line) {
    if (n < 0) mlc_panic(file, line, "tamanho negativo em realloc");
    if (grow_only && n < old_len) {
        char msg[160];
        snprintf(msg, sizeof msg, "realloc de slice de donos só pode crescer (de %" PRId64 " para %" PRId64 "); tire os elementos com take ou drain antes", old_len, n);
        mlc_panic(file, line, msg);
    }
    if (n == 0) {
        mlc_free(old); /* slice vazio: não conta como alocação */
        return NULL;
    }
    if (old == NULL) return mlc_alloc_array(n, size, file, line);
    if ((uint64_t)n > SIZE_MAX / size) mlc_panic(file, line, "memória esgotada");
    char *p = realloc(old, (size_t)n * size);
    if (p == NULL) mlc_panic(file, line, "memória esgotada");
    if (n > old_len) memset(p + (size_t)old_len * size, 0, (size_t)(n - old_len) * size);
    return p; /* continua sendo uma alocação viva: o contador não muda */
}

void mlc_check_slice(int64_t lo, int64_t hi, int64_t len, const char *file, int line) {
    if (lo >= 0 && lo <= hi && hi <= len) return;
    char msg[128];
    snprintf(msg, sizeof msg, "sub-slice [%" PRId64 ":%" PRId64 "] fora dos limites (tamanho %" PRId64 ")", lo, hi, len);
    mlc_panic(file, line, msg);
}

/* Copia n bytes para o começo de dst, ou dá panic se não couber. */
static int64_t mlc_put_bytes(mlc_sl_byte *dst, const void *src, int64_t n, const char *what,
                             const char *file, int line) {
    if (n > dst->len) {
        char msg[160];
        snprintf(msg, sizeof msg, "%s: o texto (%" PRId64 " bytes) não cabe no destino (%" PRId64 " bytes)",
                 what, n, dst->len);
        mlc_panic(file, line, msg);
    }
    if (n > 0) memmove(dst->ptr, src, (size_t)n); /* memmove: origem e destino podem se sobrepor */
    return n;
}

int64_t mlc_str_copy(mlc_sl_byte *dst, mlc_str src, const char *file, int line) {
    return mlc_put_bytes(dst, src.ptr, src.len, "str.copy", file, line);
}

int64_t mlc_str_copy_bytes(mlc_sl_byte *dst, mlc_sl_byte *src, const char *file, int line) {
    return mlc_put_bytes(dst, src->ptr, src->len, "str.copy_bytes", file, line);
}

int64_t mlc_str_put_int(mlc_sl_byte *dst, int64_t n, const char *file, int line) {
    char tmp[24];
    int len = snprintf(tmp, sizeof tmp, "%" PRId64, n);
    return mlc_put_bytes(dst, tmp, len, "str.put_int", file, line);
}

int64_t mlc_str_int_len(int64_t n, const char *file, int line) {
    (void)file;
    (void)line;
    char tmp[24];
    return snprintf(tmp, sizeof tmp, "%" PRId64, n);
}

static bool mlc_bytes_eq(const void *a, int64_t alen, const void *b, int64_t blen) {
    return alen == blen && (alen == 0 || memcmp(a, b, (size_t)alen) == 0);
}

bool mlc_str_eq(mlc_sl_byte *a, mlc_str b, const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_bytes_eq(a->ptr, a->len, b.ptr, b.len);
}

bool mlc_str_eq_bytes(mlc_sl_byte *a, mlc_sl_byte *b, const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_bytes_eq(a->ptr, a->len, b->ptr, b->len);
}

bool mlc_str_parse_int(mlc_sl_byte *s, int64_t *out, const char *file, int line) {
    (void)file;
    (void)line;
    int64_t i = 0, v = 0;
    bool neg = false;
    if (i < s->len && (s->ptr[i] == '-' || s->ptr[i] == '+')) {
        neg = s->ptr[i] == '-';
        i++;
    }
    if (i == s->len) return false;
    for (; i < s->len; i++) {
        uint8_t c = s->ptr[i];
        if (c < '0' || c > '9') return false;
        /* acumula negativo para alcançar INT64_MIN sem estourar */
        if (__builtin_mul_overflow(v, 10, &v) || __builtin_sub_overflow(v, (int64_t)(c - '0'), &v)) return false;
    }
    if (!neg) {
        if (v == INT64_MIN) return false;
        v = -v;
    }
    *out = v;
    return true;
}

void mlc_io_write(mlc_sl_byte *s, const char *file, int line) {
    (void)file;
    (void)line;
    if (s->len > 0) fwrite(s->ptr, 1, (size_t)s->len, stdout);
}

void mlc_io_write_line(mlc_sl_byte *s, const char *file, int line) {
    mlc_io_write(s, file, line);
    putchar('\n');
}

int64_t mlc_io_read_line(mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    fflush(stdout); /* quem lê costuma ter acabado de perguntar algo */
    int64_t n = 0;
    while (n < dst->len) {
        int c = getchar();
        if (c == EOF) return n == 0 ? -1 : n;
        if (c == '\n') return n;
        dst->ptr[n++] = (uint8_t)c;
    }
    return n;
}

void mlc_io_print_uint(uint64_t v) { printf("%" PRIu64, v); }

/* Escreve x com o menor número de dígitos que, lido de volta, dá o mesmo
 * valor (como o Go imprime: 0.1, 3, 1e+21). digits é 17 para float64 e 9 para
 * float32. Devolve o tamanho. */
static int mlc_format_float(char *buf, size_t size, double x, int digits, int is_float32) {
    if (isnan(x)) return snprintf(buf, size, "NaN");
    if (isinf(x)) return snprintf(buf, size, x > 0 ? "+Inf" : "-Inf");
    for (int p = 1; p <= digits; p++) {
        int n = snprintf(buf, size, "%.*g", p, x);
        double back = strtod(buf, NULL);
        if (is_float32 ? (float)back == (float)x : back == x) return n;
    }
    return snprintf(buf, size, "%.*g", digits, x); /* NaN e infinitos */
}

void mlc_io_print_f64(double v) {
    char buf[32];
    mlc_format_float(buf, sizeof buf, v, 17, 0);
    fputs(buf, stdout);
}

void mlc_io_print_f32(float v) {
    char buf[32];
    mlc_format_float(buf, sizeof buf, v, 9, 1);
    fputs(buf, stdout);
}

int64_t mlc_conv_i(int64_t v, int64_t lo, int64_t hi, const char *type, const char *file, int line) {
    if (v < lo || v > hi) {
        char msg[160];
        snprintf(msg, sizeof msg, "valor %" PRId64 " não cabe em %s (%" PRId64 " a %" PRId64 ")", v, type, lo, hi);
        mlc_panic(file, line, msg);
    }
    return v;
}

uint64_t mlc_conv_u(uint64_t v, uint64_t hi, const char *type, const char *file, int line) {
    if (v > hi) {
        char msg[160];
        snprintf(msg, sizeof msg, "valor %" PRIu64 " não cabe em %s (0 a %" PRIu64 ")", v, type, hi);
        mlc_panic(file, line, msg);
    }
    return v;
}

double mlc_conv_f(double v, double lo, double hi, const char *type, const char *file, int line) {
    if (!(v > lo && v < hi)) { /* NaN falha nas duas comparações */
        char num[32], msg[160];
        mlc_format_float(num, sizeof num, v, 17, 0);
        snprintf(msg, sizeof msg, "valor %s não cabe em %s", num, type);
        mlc_panic(file, line, msg);
    }
    return v;
}

float mlc_f64_to_f32(double v) {
    if (v > FLT_MAX) return INFINITY;
    if (v < -FLT_MAX) return -INFINITY;
    return (float)v;
}

int64_t mlc_str_put_float(mlc_sl_byte *dst, double x, const char *file, int line) {
    char tmp[32];
    int len = mlc_format_float(tmp, sizeof tmp, x, 17, 0);
    return mlc_put_bytes(dst, tmp, len, "str.put_float", file, line);
}

bool mlc_str_parse_float(mlc_sl_byte *s, double *out, const char *file, int line) {
    (void)file;
    (void)line;
    char tmp[64];
    if (s->len == 0 || s->len >= (int64_t)sizeof tmp) return false;
    memcpy(tmp, s->ptr, (size_t)s->len);
    tmp[s->len] = '\0';
    char *end;
    double v = strtod(tmp, &end);
    if (end != tmp + s->len) return false; /* sobrou texto que não é número */
    *out = v;
    return true;
}
