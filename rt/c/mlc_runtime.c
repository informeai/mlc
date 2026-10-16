/* clock_gettime, nanosleep e afins exigem POSIX, que -std=c11 esconde. */
#define _POSIX_C_SOURCE 200809L

#include "mlc_runtime.h"

#include <errno.h>
#include <float.h>
#include <inttypes.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <time.h>

void mlc_panic(const char *file, int line, const char *msg) {
    fflush(stdout);
    fprintf(stderr, "panic: %s:%d: %s\n", file, line, msg);
    exit(2);
}

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

int64_t mlc_std_str_copy(mlc_sl_byte *dst, mlc_str src, const char *file, int line) {
    return mlc_put_bytes(dst, src.ptr, src.len, "str.copy", file, line);
}

int64_t mlc_std_str_copy_bytes(mlc_sl_byte *dst, mlc_sl_byte *src, const char *file, int line) {
    return mlc_put_bytes(dst, src->ptr, src->len, "str.copy_bytes", file, line);
}

int64_t mlc_std_str_put_int(mlc_sl_byte *dst, int64_t n, const char *file, int line) {
    char tmp[24];
    int len = snprintf(tmp, sizeof tmp, "%" PRId64, n);
    return mlc_put_bytes(dst, tmp, len, "str.put_int", file, line);
}

void mlc_std_io_write(mlc_sl_byte *s, const char *file, int line) {
    (void)file;
    (void)line;
    if (s->len > 0) fwrite(s->ptr, 1, (size_t)s->len, stdout);
}

void mlc_std_io_write_line(mlc_sl_byte *s, const char *file, int line) {
    mlc_std_io_write(s, file, line);
    putchar('\n');
}

int64_t mlc_std_io_read_line(mlc_sl_byte *dst, const char *file, int line) {
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

/* Escreve x com o menor número de dígitos que, lido de volta, dá o mesmo
 * valor (como o Go imprime: 0.1, 3, 1000, 1e+06). digits é 17 para float64 e 9 para
 * float32. Devolve o tamanho. */
static int mlc_format_float(char *buf, size_t size, double x, int digits, int is_float32) {
    if (isnan(x)) return snprintf(buf, size, "NaN");
    if (isinf(x)) return snprintf(buf, size, x > 0 ? "+Inf" : "-Inf");
    int p = 1;
    for (; p < digits; p++) {
        snprintf(buf, size, "%.*e", p - 1, x);
        double back = strtod(buf, NULL);
        if (is_float32 ? (float)back == (float)x : back == x) break;
    }
    /* Como o Go: notação exponencial só se o expoente for menor que -4 ou
     * maior ou igual a 6; senão, decimal comum (1000, não 1e+03). */
    snprintf(buf, size, "%.*e", p - 1, x);
    int exp = atoi(strchr(buf, 'e') + 1);
    if (exp < -4 || exp >= 6) return snprintf(buf, size, "%.*e", p - 1, x);
    int decimals = p - 1 - exp;
    return snprintf(buf, size, "%.*f", decimals > 0 ? decimals : 0, x);
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

int64_t mlc_std_str_put_float(mlc_sl_byte *dst, double x, const char *file, int line) {
    char tmp[32];
    int len = mlc_format_float(tmp, sizeof tmp, x, 17, 0);
    return mlc_put_bytes(dst, tmp, len, "str.put_float", file, line);
}

bool mlc_std_str_parse_float(mlc_sl_byte *s, double *out, const char *file, int line) {
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

/* ---------- biblioteca padrão (extern fn dos arquivos .mlc de std) ----------
 * Cada função recebe, no fim, o arquivo e a linha da chamada no .mlc. As que
 * podem falhar devolvem um número negativo (-errno ou um código próprio), e
 * a camada em MLC transforma isso num Result. */

static int mlc_argc;
static char **mlc_argv;

void mlc_args_init(int argc, char **argv) {
    mlc_argc = argc;
    mlc_argv = argv;
}

/* std/math */
#define MLC_MATH1(name)                                                       \
    double mlc_std_math_##name(double x, const char *file, int line) {         \
        (void)file;                                                             \
        (void)line;                                                             \
        return name(x);                                                         \
    }
MLC_MATH1(sqrt)
MLC_MATH1(floor)
MLC_MATH1(ceil)
MLC_MATH1(round)
MLC_MATH1(sin)
MLC_MATH1(cos)
MLC_MATH1(tan)
MLC_MATH1(log)
MLC_MATH1(exp)

double mlc_std_math_pow(double x, double y, const char *file, int line) {
    (void)file;
    (void)line;
    return pow(x, y);
}

double mlc_std_math_atan2(double y, double x, const char *file, int line) {
    (void)file;
    (void)line;
    return atan2(y, x);
}

bool mlc_std_math_is_nan(double x, const char *file, int line) {
    (void)file;
    (void)line;
    return isnan(x);
}

bool mlc_std_math_is_inf(double x, const char *file, int line) {
    (void)file;
    (void)line;
    return isinf(x);
}

/* std/os */
static int64_t mlc_copy_cstr(mlc_sl_byte *dst, const char *s) {
    size_t n = strlen(s);
    if ((int64_t)n > dst->len) return -2; /* TOO_LONG */
    if (n > 0) memcpy(dst->ptr, s, n);
    return (int64_t)n;
}

int64_t mlc_std_os_arg_count(const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_argc;
}

int64_t mlc_std_os_arg_copy(int64_t i, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    if (i < 0 || i >= mlc_argc) return -1; /* NOT_FOUND */
    return mlc_copy_cstr(dst, mlc_argv[i]);
}

int64_t mlc_std_os_getenv_copy(mlc_str name, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    char buf[256];
    if (name.len >= (int64_t)sizeof buf) return -1;
    if (name.len > 0) memcpy(buf, name.ptr, (size_t)name.len);
    buf[name.len] = '\0';
    const char *v = getenv(buf);
    if (v == NULL) return -1;
    return mlc_copy_cstr(dst, v);
}

void mlc_std_os_exit(int64_t code, const char *file, int line) {
    (void)file;
    (void)line;
    fflush(stdout);
    exit((int)code);
}

/* std/fs: o caminho vem de um []byte, sem '\0' no fim; é copiado para a pilha. */
static int mlc_cpath(char *out, size_t cap, mlc_sl_byte *p) {
    if (p->len <= 0 || p->len >= (int64_t)cap) return 0;
    memcpy(out, p->ptr, (size_t)p->len);
    out[p->len] = '\0';
    return 1;
}

int64_t mlc_std_fs_size_raw(mlc_sl_byte *path, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    struct stat st;
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    if (stat(p, &st) != 0) return -errno;
    return (int64_t)st.st_size;
}

int64_t mlc_std_fs_read_raw(mlc_sl_byte *path, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    struct stat st;
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    if (stat(p, &st) != 0) return -errno;
    if ((int64_t)st.st_size > dst->len) return -1000; /* TOO_BIG */
    FILE *f = fopen(p, "rb");
    if (f == NULL) return -errno;
    size_t n = st.st_size > 0 ? fread(dst->ptr, 1, (size_t)st.st_size, f) : 0;
    int failed = ferror(f);
    fclose(f);
    if (failed) return -EIO;
    return (int64_t)n;
}

int64_t mlc_std_fs_write_raw(mlc_sl_byte *path, mlc_sl_byte *data, bool append, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    FILE *f = fopen(p, append ? "ab" : "wb");
    if (f == NULL) return -errno;
    size_t n = data->len > 0 ? fwrite(data->ptr, 1, (size_t)data->len, f) : 0;
    int failed = fclose(f) != 0 || (int64_t)n != data->len;
    if (failed) return -EIO;
    return (int64_t)n;
}

int64_t mlc_std_fs_remove_raw(mlc_sl_byte *path, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    if (remove(p) != 0) return -errno;
    return 0;
}

bool mlc_std_fs_exists(mlc_sl_byte *path, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    struct stat st;
    return mlc_cpath(p, sizeof p, path) && stat(p, &st) == 0;
}

/* std/time */
int64_t mlc_std_time_now_ms(const char *file, int line) {
    (void)file;
    (void)line;
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

int64_t mlc_std_time_mono_ns(const char *file, int line) {
    (void)file;
    (void)line;
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000 + ts.tv_nsec;
}

void mlc_std_time_sleep_ms(int64_t ms, const char *file, int line) {
    (void)file;
    (void)line;
    if (ms <= 0) return;
    struct timespec ts = {(time_t)(ms / 1000), (long)(ms % 1000) * 1000000};
    nanosleep(&ts, NULL);
}

/* std/vec */
void mlc_std_vec_fail(mlc_str msg, const char *file, int line) {
    char buf[160];
    int64_t n = msg.len < (int64_t)sizeof buf - 1 ? msg.len : (int64_t)sizeof buf - 1;
    if (n > 0) memcpy(buf, msg.ptr, (size_t)n);
    buf[n] = '\0';
    mlc_panic(file, line, buf);
}

/* std/io: escrita do Writer e formatação de números reais */
void mlc_std_io_write_fd(int64_t fd, mlc_sl_byte *b, const char *file, int line) {
    (void)file;
    (void)line;
    if (b->len > 0) fwrite(b->ptr, 1, (size_t)b->len, fd == 2 ? stderr : stdout);
}

int64_t mlc_std_io_format_f64(mlc_sl_byte *dst, double x, const char *file, int line) {
    char buf[32];
    int n = mlc_format_float(buf, sizeof buf, x, 17, 0);
    return mlc_put_bytes(dst, buf, n, "io.put_float", file, line);
}

int64_t mlc_std_io_format_f32(mlc_sl_byte *dst, float x, const char *file, int line) {
    char buf[32];
    int n = mlc_format_float(buf, sizeof buf, x, 9, 1);
    return mlc_put_bytes(dst, buf, n, "io.put_f32", file, line);
}

void mlc_shift_panic(int64_t n, int bits, const char *file, int line) {
    char msg[128];
    snprintf(msg, sizeof msg, "deslocamento de %" PRId64 " bits num inteiro de %d (use de 0 a %d)", n, bits, bits - 1);
    mlc_panic(file, line, msg);
}

void mlc_null_fn(const char *file, int line) {
    mlc_panic(file, line, "chamada de um valor fn nulo (o valor zero de um fn: a variável ou o campo não recebeu uma função)");
}
