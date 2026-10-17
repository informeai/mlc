/* clock_gettime, nanosleep e afins exigem POSIX, que -std=c11 esconde;
 * _SC_NPROCESSORS_ONLN (thread.cpu_count) é extensão do Linux e do macOS. */
#define _POSIX_C_SOURCE 200809L
#define _DEFAULT_SOURCE
#define _DARWIN_C_SOURCE

#include "mlc_runtime.h"

#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <spawn.h>
#include <stdarg.h>
#include <float.h>
#include <inttypes.h>
#include <math.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <stddef.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

/* Atômicos: os testes e as alocações podem acontecer em várias threads. */
static _Atomic int64_t mlc_test_failures = 0;        /* verificações que falharam no teste */
static _Atomic bool mlc_test_panic_expected = false; /* testing.expect_panic() */

void mlc_panic(const char *file, int line, const char *msg) {
    fflush(stdout);
    if (mlc_test_panic_expected) {
        /* o teste pediu este panic: termina como sucesso (se nada mais falhou) */
        fprintf(stderr, "panic esperado: %s:%d: %s\n", file, line, msg);
        exit(mlc_test_failures > 0 ? 1 : 0);
    }
    fprintf(stderr, "panic: %s:%d: %s\n", file, line, msg);
    exit(2);
}

static _Atomic int64_t mlc_live_allocs = 0;

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
                (int64_t)mlc_live_allocs);
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

/* Erros de fs portáveis: os números do errno que mudam entre sistemas viram
 * constantes do MLC (veja std/fs.mlc). */
static int64_t mlc_fs_err(int e) {
    if (e == ENOTEMPTY) return -1001; /* fs.NOT_EMPTY */
    return -e;
}

/* Um arquivo aberto: o descritor do sistema. */
typedef struct mlc_file {
    int fd;
} mlc_file;

mlc_file *mlc_std_fs_open(mlc_sl_byte *path, int64_t mode, int64_t *err, const char *file, int line) {
    char p[4096];
    *err = 0;
    if (!mlc_cpath(p, sizeof p, path)) {
        *err = ENAMETOOLONG;
        return NULL;
    }
    int flags;
    switch (mode) {
    case 0: flags = O_RDONLY; break;                     /* fs.READ */
    case 1: flags = O_WRONLY | O_CREAT | O_TRUNC; break; /* fs.WRITE */
    case 2: flags = O_WRONLY | O_CREAT | O_APPEND; break; /* fs.APPEND */
    case 3: flags = O_RDWR | O_CREAT; break;             /* fs.READ_WRITE */
    default: mlc_panic(file, line, "fs.open: modo desconhecido (use fs.READ, fs.WRITE, fs.APPEND ou fs.READ_WRITE)");
    }
    int fd = open(p, flags | O_CLOEXEC, 0644);
    if (fd < 0) {
        *err = -mlc_fs_err(errno);
        return NULL;
    }
    mlc_file *f = mlc_alloc(sizeof *f, file, line);
    f->fd = fd;
    return f;
}

int64_t mlc_std_fs_file_read(mlc_file *f, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    for (;;) {
        ssize_t n = read(f->fd, dst->ptr, (size_t)dst->len);
        if (n >= 0) return n;
        if (errno != EINTR) return mlc_fs_err(errno);
    }
}

int64_t mlc_std_fs_file_write(mlc_file *f, mlc_sl_byte *data, const char *file, int line) {
    (void)file;
    (void)line;
    int64_t done = 0;
    while (done < data->len) {
        ssize_t n = write(f->fd, data->ptr + done, (size_t)(data->len - done));
        if (n < 0) {
            if (errno == EINTR) continue;
            return mlc_fs_err(errno);
        }
        done += n;
    }
    return done;
}

int64_t mlc_std_fs_file_seek(mlc_file *f, int64_t pos, const char *file, int line) {
    (void)file;
    (void)line;
    off_t r = pos >= 0 ? lseek(f->fd, (off_t)pos, SEEK_SET) : lseek(f->fd, 0, SEEK_END);
    return r < 0 ? mlc_fs_err(errno) : (int64_t)r;
}

void mlc_std_fs_file_close(mlc_file *f, const char *file, int line) {
    (void)file;
    (void)line;
    close(f->fd);
    mlc_free(f);
}

/* Um diretório aberto. */
typedef struct mlc_dir {
    DIR *d;
} mlc_dir;

mlc_dir *mlc_std_fs_open_dir(mlc_sl_byte *path, int64_t *err, const char *file, int line) {
    char p[4096];
    *err = 0;
    if (!mlc_cpath(p, sizeof p, path)) {
        *err = ENAMETOOLONG;
        return NULL;
    }
    DIR *d = opendir(p);
    if (d == NULL) {
        *err = -mlc_fs_err(errno);
        return NULL;
    }
    mlc_dir *r = mlc_alloc(sizeof *r, file, line);
    r->d = d;
    return r;
}

/* O próximo nome (sem . e ..), ou -1 no fim. */
int64_t mlc_std_fs_dir_next(mlc_dir *d, mlc_sl_byte *dst, const char *file, int line) {
    for (;;) {
        struct dirent *e = readdir(d->d);
        if (e == NULL) return -1;
        if (strcmp(e->d_name, ".") == 0 || strcmp(e->d_name, "..") == 0) continue;
        int64_t n = mlc_copy_cstr(dst, e->d_name);
        if (n < 0) {
            char msg[160];
            snprintf(msg, sizeof msg, "fs.Dir.next: o nome tem %zu bytes e o buffer, %" PRId64 " (um [256]byte sempre basta)",
                     strlen(e->d_name), dst->len);
            mlc_panic(file, line, msg);
        }
        return n;
    }
}

void mlc_std_fs_dir_close(mlc_dir *d, const char *file, int line) {
    (void)file;
    (void)line;
    closedir(d->d);
    mlc_free(d);
}

/* stat: tamanho, se é diretório e a data de modificação (ms desde 1970). */
int64_t mlc_std_fs_stat_raw(mlc_sl_byte *path, int64_t *size, bool *is_dir, int64_t *modified, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    struct stat st;
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    if (stat(p, &st) != 0) return mlc_fs_err(errno);
    *size = (int64_t)st.st_size;
    *is_dir = S_ISDIR(st.st_mode);
#ifdef __APPLE__
    *modified = (int64_t)st.st_mtimespec.tv_sec * 1000 + st.st_mtimespec.tv_nsec / 1000000;
#else
    *modified = (int64_t)st.st_mtim.tv_sec * 1000 + st.st_mtim.tv_nsec / 1000000;
#endif
    return 0;
}

int64_t mlc_std_fs_mkdir_raw(mlc_sl_byte *path, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    return mkdir(p, 0755) == 0 ? 0 : mlc_fs_err(errno);
}

int64_t mlc_std_fs_remove_dir_raw(mlc_sl_byte *path, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    return rmdir(p) == 0 ? 0 : mlc_fs_err(errno);
}

int64_t mlc_std_fs_rename_raw(mlc_sl_byte *from, mlc_sl_byte *to, const char *file, int line) {
    (void)file;
    (void)line;
    char a[4096], b[4096];
    if (!mlc_cpath(a, sizeof a, from) || !mlc_cpath(b, sizeof b, to)) return -ENAMETOOLONG;
    return rename(a, b) == 0 ? 0 : mlc_fs_err(errno);
}

/* std/os: ambiente e diretório atual. */
int64_t mlc_std_os_setenv_raw(mlc_str name, mlc_sl_byte *value, const char *file, int line) {
    (void)file;
    (void)line;
    char n[256], v[4096];
    if (name.len <= 0 || name.len >= (int64_t)sizeof n || value->len >= (int64_t)sizeof v) return -2; /* TOO_LONG */
    memcpy(n, name.ptr, (size_t)name.len);
    n[name.len] = '\0';
    if (value->len > 0) memcpy(v, value->ptr, (size_t)value->len);
    v[value->len] = '\0';
    return setenv(n, v, 1) == 0 ? 0 : -errno;
}

int64_t mlc_std_os_cwd_raw(mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    char buf[4096];
    if (getcwd(buf, sizeof buf) == NULL) return -errno;
    return mlc_copy_cstr(dst, buf);
}

int64_t mlc_std_os_chdir_raw(mlc_sl_byte *path, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    if (!mlc_cpath(p, sizeof p, path)) return -ENAMETOOLONG;
    return chdir(p) == 0 ? 0 : -errno;
}

/* std/process: roda um programa. args tem os argumentos separados por '\0'
 * (count deles, o primeiro é o programa, procurado no PATH). Com capture, a
 * saída padrão do programa vai para out; o que não couber é lido e
 * descartado, e o resultado é -1000 (TOO_BIG). status recebe o código de
 * saída (128 + o sinal, se o programa morreu por sinal). */
extern char **environ;

int64_t mlc_std_process_run_raw(mlc_sl_byte *args, int64_t count, mlc_sl_byte *out, bool capture, int64_t *status,
                                const char *file, int line) {
    char *argv[256];
    char buf[8193];
    if (count < 1 || count > 255 || args->len > 8192) mlc_panic(file, line, "process: argumentos demais");
    memcpy(buf, args->ptr, (size_t)args->len);
    buf[args->len] = '\0';
    int64_t at = 0;
    for (int64_t i = 0; i < count; i++) {
        argv[i] = buf + at;
        at += (int64_t)strlen(buf + at) + 1;
    }
    argv[count] = NULL;
    int fds[2] = {-1, -1};
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    if (capture) {
        if (pipe(fds) != 0) {
            posix_spawn_file_actions_destroy(&fa);
            return -errno;
        }
        posix_spawn_file_actions_adddup2(&fa, fds[1], 1);
        posix_spawn_file_actions_addclose(&fa, fds[0]);
        posix_spawn_file_actions_addclose(&fa, fds[1]);
    }
    fflush(stdout); /* a saída do programa não passa na frente da nossa */
    pid_t pid;
    int err = posix_spawnp(&pid, argv[0], &fa, NULL, argv, environ);
    posix_spawn_file_actions_destroy(&fa);
    if (capture) close(fds[1]);
    if (err != 0) {
        if (capture) close(fds[0]);
        return -err;
    }
    int64_t got = 0;
    bool overflow = false;
    if (capture) {
        char tmp[4096];
        for (;;) {
            ssize_t n = read(fds[0], tmp, sizeof tmp);
            if (n < 0 && errno == EINTR) continue;
            if (n <= 0) break;
            int64_t room = out->len - got;
            int64_t k = n < room ? n : room;
            if (k > 0) memcpy(out->ptr + got, tmp, (size_t)k);
            got += k;
            if (k < n) overflow = true;
        }
        close(fds[0]);
    }
    int st = 0;
    while (waitpid(pid, &st, 0) < 0) {
        if (errno != EINTR) return -errno;
    }
    *status = WIFEXITED(st) ? WEXITSTATUS(st) : WIFSIGNALED(st) ? 128 + WTERMSIG(st) : 1;
    return overflow ? -1000 : got;
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

/* Real com prec casas decimais ({:.2}). */
int64_t mlc_std_io_format_fixed(mlc_sl_byte *dst, double x, int64_t prec, const char *file, int line) {
    char buf[352];
    int n;
    if (isnan(x)) n = snprintf(buf, sizeof buf, "NaN");
    else if (isinf(x)) n = snprintf(buf, sizeof buf, x > 0 ? "+Inf" : "-Inf");
    else n = snprintf(buf, sizeof buf, "%.*f", (int)prec, x);
    return mlc_put_bytes(dst, buf, n, "io.put_float", file, line);
}

/* Destinos do str.format: uma pilha, porque um write pode chamar outro
 * str.format. O Writer guarda só o índice, conferido aqui; o texto que não
 * cabe é contado e, no fim, dá panic na linha do str.format. */
typedef struct {
    uint8_t *ptr;
    int64_t cap, len, needed;
} mlc_sink;

/* Por thread: cada thread tem os próprios str.format em andamento. */
static _Thread_local mlc_sink mlc_sinks[64];
static _Thread_local int64_t mlc_sink_depth = 0;

int64_t mlc_sink_push(mlc_sl_byte *dst, const char *file, int line) {
    if (mlc_sink_depth == (int64_t)(sizeof mlc_sinks / sizeof mlc_sinks[0]))
        mlc_panic(file, line, "str.format aninhado em mais de 64 níveis");
    mlc_sinks[mlc_sink_depth] = (mlc_sink){dst->ptr, dst->len, 0, 0};
    return mlc_sink_depth++;
}

void mlc_std_io_sink_put(int64_t sink, mlc_sl_byte *b, const char *file, int line) {
    if (sink < 0 || sink >= mlc_sink_depth) mlc_panic(file, line, "Writer com um destino de str.format que não existe");
    mlc_sink *s = &mlc_sinks[sink];
    int64_t room = s->cap - s->len;
    int64_t n = b->len < room ? b->len : room;
    if (n > 0) memcpy(s->ptr + s->len, b->ptr, (size_t)n);
    s->len += n;
    s->needed += b->len;
}

int64_t mlc_sink_pop(int64_t sink, const char *file, int line) {
    if (sink != mlc_sink_depth - 1) mlc_panic(file, line, "erro interno: destinos de str.format fora de ordem");
    mlc_sink s = mlc_sinks[--mlc_sink_depth];
    if (s.needed > s.cap) {
        char msg[160];
        snprintf(msg, sizeof msg, "str.format: o texto tem %" PRId64 " bytes e o destino, %" PRId64, s.needed, s.cap);
        mlc_panic(file, line, msg);
    }
    return s.len;
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

/* std/map */
void mlc_std_map_fail(mlc_str msg, const char *file, int line) {
    mlc_std_vec_fail(msg, file, line);
}

/* std/testing. Cada teste roda num processo: o mlc test passa o índice do
 * teste como argumento, e o código de saída diz o resultado (0 ok, 1
 * verificação falhou, 2 panic, 3 vazamento). As falhas não interrompem o
 * teste; cada uma aponta a linha da verificação. */

int64_t mlc_test_selected(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "este programa de teste é rodado pelo mlc test\n");
        exit(2);
    }
    return atoll(argv[1]);
}

void mlc_test_finish(void) {
    if (mlc_test_panic_expected) {
        fflush(stdout);
        fprintf(stderr, "o teste terminou sem o panic esperado (testing.expect_panic)\n");
        mlc_test_failures++;
    }
    if (mlc_test_failures > 0) {
        fflush(stdout);
        exit(1);
    }
}

static void mlc_test_fail(const char *file, int line, const char *fmt, ...) {
    fflush(stdout);
    fprintf(stderr, "%s:%d: ", file, line);
    va_list ap;
    va_start(ap, fmt);
    vfprintf(stderr, fmt, ap);
    va_end(ap);
    fputc('\n', stderr);
    mlc_test_failures++;
}

/* Escreve um texto entre aspas, com os bytes de controle escapados. */
static void mlc_test_quote(const uint8_t *p, int64_t n) {
    fputc('"', stderr);
    for (int64_t i = 0; i < n; i++) {
        uint8_t c = p[i];
        if (c == '"' || c == '\\') fprintf(stderr, "\\%c", c);
        else if (c == '\n') fputs("\\n", stderr);
        else if (c == '\t') fputs("\\t", stderr);
        else if (c < 0x20 || c == 0x7f) fprintf(stderr, "\\x%02x", c);
        else fputc(c, stderr);
    }
    fputc('"', stderr);
}

void mlc_std_testing_check(bool ok, mlc_str msg, const char *file, int line) {
    if (!ok) mlc_test_fail(file, line, "%.*s", (int)msg.len, msg.ptr);
}

void mlc_std_testing_fail(mlc_str msg, const char *file, int line) {
    mlc_test_fail(file, line, "%.*s", (int)msg.len, msg.ptr);
}

void mlc_std_testing_eq_int(int64_t got, int64_t want, const char *file, int line) {
    if (got != want) mlc_test_fail(file, line, "veio %" PRId64 ", esperado %" PRId64, got, want);
}

void mlc_std_testing_eq_uint(uint64_t got, uint64_t want, const char *file, int line) {
    if (got != want) mlc_test_fail(file, line, "veio %" PRIu64 " (0x%" PRIx64 "), esperado %" PRIu64 " (0x%" PRIx64 ")", got, got, want, want);
}

void mlc_std_testing_eq_bool(bool got, bool want, const char *file, int line) {
    if (got != want) mlc_test_fail(file, line, "veio %s, esperado %s", got ? "true" : "false", want ? "true" : "false");
}

void mlc_std_testing_eq_float(double got, double want, const char *file, int line) {
    if (got != want) {
        char a[32], b[32];
        mlc_format_float(a, sizeof a, got, 17, 0);
        mlc_format_float(b, sizeof b, want, 17, 0);
        mlc_test_fail(file, line, "veio %s, esperado %s", a, b);
    }
}

void mlc_std_testing_near(double got, double want, double tol, const char *file, int line) {
    if (!(fabs(got - want) <= tol)) { /* NaN também falha */
        char a[32], b[32], t[32];
        mlc_format_float(a, sizeof a, got, 17, 0);
        mlc_format_float(b, sizeof b, want, 17, 0);
        mlc_format_float(t, sizeof t, tol, 17, 0);
        mlc_test_fail(file, line, "veio %s, esperado %s (tolerância %s)", a, b, t);
    }
}

static void mlc_test_eq_text(const uint8_t *got, int64_t gn, const uint8_t *want, int64_t wn,
                             const char *file, int line) {
    if (gn == wn && (gn == 0 || memcmp(got, want, (size_t)gn) == 0)) return;
    mlc_test_fail(file, line, "texto diferente");
    fputs("    veio:     ", stderr);
    mlc_test_quote(got, gn);
    fputs("\n    esperado: ", stderr);
    mlc_test_quote(want, wn);
    fputc('\n', stderr);
}

void mlc_std_testing_eq_str(mlc_sl_byte *got, mlc_str want, const char *file, int line) {
    mlc_test_eq_text(got->ptr, got->len, (const uint8_t *)want.ptr, want.len, file, line);
}

void mlc_std_testing_eq_bytes(mlc_sl_byte *got, mlc_sl_byte *want, const char *file, int line) {
    mlc_test_eq_text(got->ptr, got->len, want->ptr, want->len, file, line);
}

void mlc_std_testing_expect_panic(const char *file, int line) {
    (void)file;
    (void)line;
    mlc_test_panic_expected = true;
}

/* std/thread. O registro da thread (contexto, resultado e o mlc_thread no
 * começo) é alocado pelo código gerado com mlc_alloc e liberado no join; a
 * pilha é do sistema (pthread), com o tamanho pedido no spawn. */

static void *mlc_thread_entry(void *p) {
    mlc_thread *t = p;
    t->run(t);
    return NULL;
}

void mlc_thread_start(mlc_thread *t, void (*run)(mlc_thread *), int64_t stack, const char *file, int line) {
    if (stack < 16384) mlc_panic(file, line, "thread.spawn: a pilha precisa ter pelo menos 16 KB (thread.STACK_64KB, por exemplo)");
    long page = sysconf(_SC_PAGESIZE);
    if (page <= 0) page = 4096;
    size_t size = (size_t)((stack + page - 1) / page * page); /* o macOS exige múltiplo da página */
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    if (pthread_attr_setstacksize(&attr, size) != 0) {
        pthread_attr_destroy(&attr);
        mlc_panic(file, line, "thread.spawn: tamanho de pilha recusado pelo sistema");
    }
    t->run = run;
    int err = pthread_create(&t->th, &attr, mlc_thread_entry, t);
    pthread_attr_destroy(&attr);
    if (err != 0) mlc_panic(file, line, "thread.spawn: o sistema não criou a thread (limite de threads ou de memória)");
}

void mlc_thread_join(mlc_thread *t, const char *file, int line) {
    if (pthread_join(t->th, NULL) != 0) mlc_panic(file, line, "erro interno: thread.join falhou");
}

int64_t mlc_std_thread_cpu_count(const char *file, int line) {
    (void)file;
    (void)line;
#ifdef _SC_NPROCESSORS_ONLN
    long n = sysconf(_SC_NPROCESSORS_ONLN);
    return n > 0 ? n : 1;
#else
    return 1;
#endif
}

/* std/chan. Um canal é um buffer em anel de cap posições de elem bytes,
 * protegido por uma trava. send copia o valor para o anel (a posse passa ao
 * canal) e recv o copia para fora (a posse passa a quem recebe). As pontas
 * são contadas: sem Sender aberto, recv num canal vazio devolve "fechado";
 * sem Receiver aberto, send libera o valor (drop) e devolve false. O último
 * close libera o que sobrou no anel e o próprio canal. O canal e o anel são
 * alocados com mlc_alloc no chan.new, então contam como alocações. */

struct mlc_chan {
    pthread_mutex_t mu;
    pthread_cond_t not_empty, not_full;
    int64_t cap, head, count, senders, receivers;
    size_t elem;
    void (*drop)(void *);
    unsigned char *buf;
};

mlc_chan *mlc_chan_new(int64_t cap, size_t elem, void (*drop)(void *), const char *file, int line) {
    if (cap < 1) mlc_panic(file, line, "chan.new: a capacidade precisa ser pelo menos 1");
    mlc_chan *c = mlc_alloc(sizeof *c, file, line);
    c->buf = mlc_alloc_array(cap, elem > 0 ? elem : 1, file, line);
    pthread_mutex_init(&c->mu, NULL);
    pthread_cond_init(&c->not_empty, NULL);
    pthread_cond_init(&c->not_full, NULL);
    c->cap = cap;
    c->elem = elem;
    c->drop = drop;
    c->receivers = 1;
    return c;
}

mlc_chan *mlc_chan_add(mlc_chan *c, int end) {
    pthread_mutex_lock(&c->mu);
    if (end == 1) c->senders++;
    else c->receivers++;
    pthread_mutex_unlock(&c->mu);
    return c;
}

void mlc_chan_close(mlc_chan *c, int end) {
    pthread_mutex_lock(&c->mu);
    if (end == 1) {
        c->senders--;
        pthread_cond_broadcast(&c->not_empty); /* quem espera em recv pode ver o fim */
    } else {
        c->receivers--;
        pthread_cond_broadcast(&c->not_full); /* quem espera em send desiste */
    }
    bool last = c->senders == 0 && c->receivers == 0;
    pthread_mutex_unlock(&c->mu);
    if (!last) return;
    for (int64_t i = 0; i < c->count; i++) {
        if (c->drop) c->drop(c->buf + (size_t)((c->head + i) % c->cap) * c->elem);
    }
    pthread_mutex_destroy(&c->mu);
    pthread_cond_destroy(&c->not_empty);
    pthread_cond_destroy(&c->not_full);
    mlc_free(c->buf);
    mlc_free(c);
}

bool mlc_chan_send(mlc_chan *c, void *v) {
    pthread_mutex_lock(&c->mu);
    while (c->count == c->cap && c->receivers > 0) pthread_cond_wait(&c->not_full, &c->mu);
    if (c->receivers == 0) {
        pthread_mutex_unlock(&c->mu);
        if (c->drop) c->drop(v); /* ninguém mais recebe: o valor é liberado */
        return false;
    }
    memcpy(c->buf + (size_t)((c->head + c->count) % c->cap) * c->elem, v, c->elem);
    c->count++;
    pthread_cond_signal(&c->not_empty);
    pthread_mutex_unlock(&c->mu);
    return true;
}

bool mlc_chan_recv(mlc_chan *c, void *out) {
    pthread_mutex_lock(&c->mu);
    while (c->count == 0 && c->senders > 0) pthread_cond_wait(&c->not_empty, &c->mu);
    if (c->count == 0) {
        pthread_mutex_unlock(&c->mu);
        return false; /* todos os Sender fecharam e o canal esvaziou */
    }
    memcpy(out, c->buf + (size_t)c->head * c->elem, c->elem);
    c->head = (c->head + 1) % c->cap;
    c->count--;
    pthread_cond_signal(&c->not_full);
    pthread_mutex_unlock(&c->mu);
    return true;
}

/* std/mutex. O mutex guarda o valor logo depois do cabeçalho; a trava é do
 * tipo que confere erros, então travar duas vezes na mesma thread dá panic em
 * vez de travar o programa para sempre. Os handles e os guards são contados:
 * o último a sair libera o valor (drop) e o mutex. */

struct mlc_mutex {
    pthread_mutex_t mu;
    pthread_cond_t cv;
    _Atomic int64_t refs;
    void (*drop)(void *);
    _Alignas(max_align_t) unsigned char value[];
};

mlc_mutex *mlc_mutex_new(void *v, size_t size, void (*drop)(void *), const char *file, int line) {
    mlc_mutex *m = mlc_alloc(sizeof *m + size, file, line);
    pthread_mutexattr_t attr;
    pthread_mutexattr_init(&attr);
    pthread_mutexattr_settype(&attr, PTHREAD_MUTEX_ERRORCHECK);
    pthread_mutex_init(&m->mu, &attr);
    pthread_mutexattr_destroy(&attr);
    pthread_cond_init(&m->cv, NULL);
    m->refs = 1;
    m->drop = drop;
    memcpy(m->value, v, size);
    return m;
}

mlc_mutex *mlc_mutex_clone(mlc_mutex *m) {
    m->refs++;
    return m;
}

void mlc_mutex_close(mlc_mutex *m) {
    if (--m->refs > 0) return;
    if (m->drop) m->drop(m->value);
    pthread_mutex_destroy(&m->mu);
    pthread_cond_destroy(&m->cv);
    mlc_free(m);
}

mlc_mutex *mlc_mutex_lock(mlc_mutex *m, const char *file, int line) {
    m->refs++; /* o guard mantém o mutex vivo */
    int err = pthread_mutex_lock(&m->mu);
    if (err != 0) {
        m->refs--;
        mlc_panic(file, line, err == EDEADLK ? "mutex já travado por esta thread (destrave o guard antes de travar de novo)"
                                             : "erro interno: lock falhou");
    }
    return m;
}

void mlc_mutex_unlock(mlc_mutex *m) {
    pthread_mutex_unlock(&m->mu);
    mlc_mutex_close(m);
}

void *mlc_mutex_value(mlc_mutex *m) { return m->value; }

void mlc_mutex_wait(mlc_mutex *m) { pthread_cond_wait(&m->cv, &m->mu); }

void mlc_mutex_notify(mlc_mutex *m, bool all) {
    if (all) pthread_cond_broadcast(&m->cv);
    else pthread_cond_signal(&m->cv);
}

/* std/atomic: um inteiro (ou bool) atômico, com os handles contados. */

typedef struct mlc_atomic {
    _Atomic int64_t v;
    _Atomic int64_t refs;
} mlc_atomic;

mlc_atomic *mlc_std_atomic_new_int(int64_t v, const char *file, int line) {
    mlc_atomic *a = mlc_alloc(sizeof *a, file, line);
    a->v = v;
    a->refs = 1;
    return a;
}

mlc_atomic *mlc_std_atomic_new_bool(bool v, const char *file, int line) { return mlc_std_atomic_new_int(v, file, line); }

static mlc_atomic *mlc_atomic_clone(mlc_atomic *a) {
    a->refs++;
    return a;
}

static void mlc_atomic_close(mlc_atomic *a) {
    if (--a->refs == 0) mlc_free(a);
}

#define MLC_ATOMIC_HANDLE(kind)                                                              \
    mlc_atomic *mlc_std_atomic_clone_##kind(mlc_atomic *a, const char *file, int line) {      \
        (void)file;                                                                          \
        (void)line;                                                                          \
        return mlc_atomic_clone(a);                                                          \
    }                                                                                        \
    void mlc_std_atomic_close_##kind(mlc_atomic *a, const char *file, int line) {            \
        (void)file;                                                                          \
        (void)line;                                                                          \
        mlc_atomic_close(a);                                                                 \
    }
MLC_ATOMIC_HANDLE(int)
MLC_ATOMIC_HANDLE(bool)

int64_t mlc_std_atomic_load_int(mlc_atomic *a, const char *file, int line) {
    (void)file;
    (void)line;
    return atomic_load(&a->v);
}

void mlc_std_atomic_store_int(mlc_atomic *a, int64_t v, const char *file, int line) {
    (void)file;
    (void)line;
    atomic_store(&a->v, v);
}

int64_t mlc_std_atomic_add_int(mlc_atomic *a, int64_t n, const char *file, int line) {
    (void)file;
    (void)line;
    /* como as contas do MLC, estouro dá panic: confere antes de trocar */
    int64_t old = atomic_load(&a->v);
    for (;;) {
        int64_t sum;
        if (__builtin_add_overflow(old, n, &sum)) mlc_panic(file, line, "atomic.Int.add: estouro de int");
        if (atomic_compare_exchange_weak(&a->v, &old, sum)) return sum;
    }
}

int64_t mlc_std_atomic_swap_int(mlc_atomic *a, int64_t v, const char *file, int line) {
    (void)file;
    (void)line;
    return atomic_exchange(&a->v, v);
}

bool mlc_std_atomic_cas_int(mlc_atomic *a, int64_t expected, int64_t v, const char *file, int line) {
    (void)file;
    (void)line;
    return atomic_compare_exchange_strong(&a->v, &expected, v);
}

bool mlc_std_atomic_load_bool(mlc_atomic *a, const char *file, int line) { return mlc_std_atomic_load_int(a, file, line) != 0; }

void mlc_std_atomic_store_bool(mlc_atomic *a, bool v, const char *file, int line) { mlc_std_atomic_store_int(a, v, file, line); }

bool mlc_std_atomic_swap_bool(mlc_atomic *a, bool v, const char *file, int line) { return mlc_std_atomic_swap_int(a, v, file, line) != 0; }

bool mlc_std_atomic_cas_bool(mlc_atomic *a, bool expected, bool v, const char *file, int line) {
    return mlc_std_atomic_cas_int(a, expected, v, file, line);
}

void mlc_std_process_fail(mlc_str msg, const char *file, int line) {
    char buf[256];
    snprintf(buf, sizeof buf, "%.*s", (int)msg.len, msg.ptr);
    mlc_panic(file, line, buf);
}

void mlc_std_args_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_bytes_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

/* std/net: TCP. Cada handle guarda o descritor do socket. Os erros que mudam
 * entre sistemas viram códigos do MLC (veja std/net.mlc); os outros são o
 * errno. Escrever num socket fechado do outro lado devolve erro em vez de
 * matar o programa com SIGPIPE. */

typedef struct mlc_sock {
    int fd;
} mlc_sock;

static int64_t mlc_net_err(int e) {
    switch (e) {
    case ECONNREFUSED: return 2001; /* net.REFUSED */
    case EADDRINUSE: return 2003;   /* net.IN_USE */
    case ETIMEDOUT:
    case EAGAIN: return 2004; /* net.TIMEOUT (SO_RCVTIMEO devolve EAGAIN) */
    case ECONNRESET:
    case EPIPE: return 2005; /* net.RESET */
    }
    return e;
}

static int mlc_net_host(char *out, size_t cap, mlc_sl_byte *host) {
    if (host->len >= (int64_t)cap) return 0;
    if (host->len > 0) memcpy(out, host->ptr, (size_t)host->len);
    out[host->len] = '\0';
    return 1;
}

static void mlc_net_nosigpipe(int fd) {
#ifdef SO_NOSIGPIPE
    int one = 1;
    setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
#else
    (void)fd;
#endif
}

static mlc_sock *mlc_net_wrap(int fd, const char *file, int line) {
    mlc_sock *s = mlc_alloc(sizeof *s, file, line);
    s->fd = fd;
    return s;
}

/* Resolve host:port (host vazio: todas as interfaces, para listen). */
static int mlc_net_resolve(mlc_sl_byte *host, int64_t port, bool passive, struct addrinfo **res, int64_t *err) {
    char h[256], p[16];
    if (!mlc_net_host(h, sizeof h, host) || port < 0 || port > 65535) {
        *err = EINVAL;
        return 0;
    }
    snprintf(p, sizeof p, "%d", (int)port);
    struct addrinfo hints;
    memset(&hints, 0, sizeof hints);
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;
    if (passive) hints.ai_flags = AI_PASSIVE;
    int r = getaddrinfo(h[0] ? h : NULL, p, &hints, res);
    if (r != 0) {
        *err = 2002; /* net.NO_HOST */
        return 0;
    }
    return 1;
}

mlc_sock *mlc_std_net_listen(mlc_sl_byte *host, int64_t port, int64_t *err, const char *file, int line) {
    struct addrinfo *res;
    *err = 0;
    if (!mlc_net_resolve(host, port, true, &res, err)) return NULL;
    int fd = -1;
    int last = 0;
    for (struct addrinfo *a = res; a != NULL; a = a->ai_next) {
        fd = socket(a->ai_family, a->ai_socktype, a->ai_protocol);
        if (fd < 0) {
            last = errno;
            continue;
        }
        int one = 1;
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
        if (bind(fd, a->ai_addr, a->ai_addrlen) == 0 && listen(fd, 128) == 0) break;
        last = errno;
        close(fd);
        fd = -1;
    }
    freeaddrinfo(res);
    if (fd < 0) {
        *err = mlc_net_err(last);
        return NULL;
    }
    return mlc_net_wrap(fd, file, line);
}

mlc_sock *mlc_std_net_accept_raw(mlc_sock *l, int64_t *err, const char *file, int line) {
    *err = 0;
    for (;;) {
        int fd = accept(l->fd, NULL, NULL);
        if (fd >= 0) {
            mlc_net_nosigpipe(fd);
            return mlc_net_wrap(fd, file, line);
        }
        if (errno != EINTR) {
            *err = mlc_net_err(errno);
            return NULL;
        }
    }
}

int64_t mlc_std_net_port_raw(mlc_sock *l, const char *file, int line) {
    (void)file;
    (void)line;
    struct sockaddr_storage ss;
    socklen_t n = sizeof ss;
    if (getsockname(l->fd, (struct sockaddr *)&ss, &n) != 0) return -1;
    if (ss.ss_family == AF_INET) return ntohs(((struct sockaddr_in *)&ss)->sin_port);
    if (ss.ss_family == AF_INET6) return ntohs(((struct sockaddr_in6 *)&ss)->sin6_port);
    return -1;
}

mlc_sock *mlc_std_net_connect(mlc_sl_byte *host, int64_t port, int64_t *err, const char *file, int line) {
    struct addrinfo *res;
    *err = 0;
    if (!mlc_net_resolve(host, port, false, &res, err)) return NULL;
    int fd = -1;
    int last = 0;
    for (struct addrinfo *a = res; a != NULL; a = a->ai_next) {
        fd = socket(a->ai_family, a->ai_socktype, a->ai_protocol);
        if (fd < 0) {
            last = errno;
            continue;
        }
        int r;
        do {
            r = connect(fd, a->ai_addr, a->ai_addrlen);
        } while (r != 0 && errno == EINTR);
        if (r == 0) break;
        last = errno;
        close(fd);
        fd = -1;
    }
    freeaddrinfo(res);
    if (fd < 0) {
        *err = mlc_net_err(last);
        return NULL;
    }
    mlc_net_nosigpipe(fd);
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    return mlc_net_wrap(fd, file, line);
}

int64_t mlc_std_net_read_raw(mlc_sock *c, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    for (;;) {
        ssize_t n = recv(c->fd, dst->ptr, (size_t)dst->len, 0);
        if (n >= 0) return n;
        if (errno != EINTR) return -mlc_net_err(errno);
    }
}

int64_t mlc_std_net_write_raw(mlc_sock *c, mlc_sl_byte *data, const char *file, int line) {
    (void)file;
    (void)line;
#ifdef MSG_NOSIGNAL
    int flags = MSG_NOSIGNAL;
#else
    int flags = 0;
#endif
    int64_t done = 0;
    while (done < data->len) {
        ssize_t n = send(c->fd, data->ptr + done, (size_t)(data->len - done), flags);
        if (n < 0) {
            if (errno == EINTR) continue;
            return -mlc_net_err(errno);
        }
        done += n;
    }
    return done;
}

int64_t mlc_std_net_timeout_raw(mlc_sock *c, int64_t ms, const char *file, int line) {
    (void)file;
    (void)line;
    struct timeval tv;
    tv.tv_sec = (time_t)(ms / 1000);
    tv.tv_usec = (suseconds_t)((ms % 1000) * 1000);
    if (setsockopt(c->fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv) != 0) return -errno;
    if (setsockopt(c->fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv) != 0) return -errno;
    return 0;
}

int64_t mlc_std_net_shutdown_raw(mlc_sock *c, const char *file, int line) {
    (void)file;
    (void)line;
    return shutdown(c->fd, SHUT_WR) == 0 ? 0 : -mlc_net_err(errno);
}

void mlc_std_net_close_raw(mlc_sock *s, const char *file, int line) {
    (void)file;
    (void)line;
    close(s->fd);
    mlc_free(s);
}

void mlc_std_net_close_listener(mlc_sock *s, const char *file, int line) { mlc_std_net_close_raw(s, file, line); }

/* std/time: o deslocamento do fuso local (segundos a leste de UTC) no
 * instante ms. */
int64_t mlc_std_time_local_offset(int64_t ms, const char *file, int line) {
    (void)file;
    (void)line;
    time_t t = (time_t)(ms / 1000);
    struct tm lt;
    if (localtime_r(&t, &lt) == NULL) return 0;
#if defined(__APPLE__) || defined(__USE_MISC) || defined(_DEFAULT_SOURCE)
    return (int64_t)lt.tm_gmtoff;
#else
    return 0;
#endif
}
