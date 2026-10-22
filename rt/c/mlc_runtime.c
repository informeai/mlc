/* clock_gettime, nanosleep and the like require POSIX, which -std=c11 hides;
 * _SC_NPROCESSORS_ONLN (thread.cpu_count) is a Linux and macOS extension. */
#define _POSIX_C_SOURCE 200809L
#define _DEFAULT_SOURCE
#define _DARWIN_C_SOURCE
#ifndef _GNU_SOURCE /* ESP-IDF already defines it */
#define _GNU_SOURCE /* pthread_getattr_np, to find the stack on Linux */
#endif

/* ESP-IDF (ESP32): the program runs in a FreeRTOS task, not in a process.
 * The modules that need a desktop system (fs, process, net, signal, sqlite
 * and tls) stay out, and mlc build -target esp32 refuses their import. See
 * docs/esp32.md. */
#if defined(ESP_PLATFORM)
#define MLC_EMBEDDED 1
#endif

#include "mlc_runtime.h"

#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <float.h>
#include <inttypes.h>
#include <math.h>
#include <stddef.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#ifdef MLC_EMBEDDED
#include <sys/poll.h> /* ESP-IDF's poll (over lwIP's select) */

#include "esp_netif.h"
#include "esp_random.h"
#include "esp_rom_sys.h" /* esp_rom_printf: works in an interrupt */
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#else
#include <dirent.h>
#include <dlfcn.h> /* std/sqlite: the library is loaded only when used */
#include <poll.h>
#include <signal.h>
#include <spawn.h>
#if defined(__APPLE__) || defined(__linux__)
#include <sys/random.h> /* getentropy */
#endif
#include <sys/wait.h>
#endif

/* Atomics: tests and allocations can happen in several threads. */
static _Atomic int64_t mlc_test_failures = 0;        /* checks that failed in the test */
static _Atomic bool mlc_test_panic_expected = false; /* testing.expect_panic() */

void mlc_panic(const char *file, int line, const char *msg) {
    fflush(stdout);
    if (mlc_test_panic_expected) {
        /* the test asked for this panic: ends as a success (if nothing else failed) */
        fprintf(stderr, "expected panic: %s:%d: %s\n", file, line, msg);
        exit(mlc_test_failures > 0 ? 1 : 0);
    }
    fprintf(stderr, "panic: %s:%d: %s\n", file, line, msg);
    exit(2);
}

static _Atomic int64_t mlc_live_allocs = 0;
static _Atomic int64_t mlc_kept = 0;   /* contexts given to C with kept (mlc_keep) */
static _Thread_local bool mlc_own_thread; /* main or a thread.spawn: MLC prepared its runtime */

void *mlc_alloc(size_t size, const char *file, int line) {
    void *p = calloc(1, size);
    if (p == NULL) mlc_panic(file, line, "out of memory");
    mlc_live_allocs++;
    return p;
}

void mlc_free(void *p) {
    if (p == NULL) return; /* free of a ?*T that is nil */
    mlc_live_allocs--;
    free(p);
}

void *mlc_keep(void *p) {
    mlc_kept++;
    return p;
}

void mlc_check_allocs(void) {
    /* a kept context (and what it owns) stays with C until the end, out of
     * the count: with one, the count says nothing */
    if (mlc_live_allocs != 0 && mlc_kept == 0) {
        fflush(stdout);
        fprintf(stderr, "mlc: internal error: %" PRId64 " allocation(s) without free at the end of the program (the ownership checker should have prevented it)\n",
                (int64_t)mlc_live_allocs);
        exit(3);
    }
}

void *mlc_alloc_array(int64_t n, size_t size, const char *file, int line) {
    if (n < 0) mlc_panic(file, line, "negative size in slice alloc");
    if (n == 0) return NULL; /* empty slice: doesn't count as an allocation */
    if ((uint64_t)n > SIZE_MAX / size) mlc_panic(file, line, "out of memory");
    void *p = calloc((size_t)n, size);
    if (p == NULL) mlc_panic(file, line, "out of memory");
    mlc_live_allocs++;
    return p;
}

void mlc_panic_index(int64_t i, int64_t len, const char *file, int line) {
    char msg[96];
    snprintf(msg, sizeof msg, "index %" PRId64 " out of bounds (length %" PRId64 ")", i, len);
    mlc_panic(file, line, msg);
}

void *mlc_realloc_array(void *old, int64_t old_len, int64_t n, size_t size, int grow_only, const char *file, int line) {
    if (n < 0) mlc_panic(file, line, "negative size in realloc");
    if (grow_only && n < old_len) {
        char msg[160];
        snprintf(msg, sizeof msg, "realloc of a slice of owners can only grow (from %" PRId64 " to %" PRId64 "); take the elements out with take or drain first", old_len, n);
        mlc_panic(file, line, msg);
    }
    if (n == 0) {
        mlc_free(old); /* empty slice: doesn't count as an allocation */
        return NULL;
    }
    if (old == NULL) return mlc_alloc_array(n, size, file, line);
    if ((uint64_t)n > SIZE_MAX / size) mlc_panic(file, line, "out of memory");
    char *p = realloc(old, (size_t)n * size);
    if (p == NULL) mlc_panic(file, line, "out of memory");
    if (n > old_len) memset(p + (size_t)old_len * size, 0, (size_t)(n - old_len) * size);
    return p; /* still a live allocation: the counter doesn't change */
}

void mlc_check_slice(int64_t lo, int64_t hi, int64_t len, const char *file, int line) {
    if (lo >= 0 && lo <= hi && hi <= len) return;
    char msg[128];
    snprintf(msg, sizeof msg, "sub-slice [%" PRId64 ":%" PRId64 "] out of bounds (length %" PRId64 ")", lo, hi, len);
    mlc_panic(file, line, msg);
}

/* Copies n bytes to the start of dst, or panics if it doesn't fit. */
static int64_t mlc_put_bytes(mlc_sl_byte *dst, const void *src, int64_t n, const char *what,
                             const char *file, int line) {
    if (n > dst->len) {
        char msg[160];
        snprintf(msg, sizeof msg, "%s: the text (%" PRId64 " bytes) doesn't fit in the destination (%" PRId64 " bytes)",
                 what, n, dst->len);
        mlc_panic(file, line, msg);
    }
    if (n > 0) memmove(dst->ptr, src, (size_t)n); /* memmove: source and destination may overlap */
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
    fflush(stdout); /* whoever reads has usually just asked something */
    int64_t n = 0;
    while (n < dst->len) {
        int c = getchar();
        if (c == EOF) return n == 0 ? -1 : n;
        if (c == '\n') return n;
        dst->ptr[n++] = (uint8_t)c;
    }
    return n;
}

int64_t mlc_std_io_read_stdin(mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    fflush(stdout); /* as in read_line */
    int64_t n = 0;
    while (n < dst->len) {
        int c = getchar();
        if (c == EOF) {
            if (n == 0 && ferror(stdin)) return -EIO;
            return n;
        }
        dst->ptr[n++] = (uint8_t)c;
        if (c == '\n') break;
    }
    return n;
}

/* Writes x with the fewest digits that, read back, give the same
 * value (as Go prints: 0.1, 3, 1000, 1e+06). digits is 17 for float64 and 9 for
 * float32. Returns the length. */
static int mlc_format_float(char *buf, size_t size, double x, int digits, int is_float32) {
    if (isnan(x)) return snprintf(buf, size, "NaN");
    if (isinf(x)) return snprintf(buf, size, x > 0 ? "+Inf" : "-Inf");
    int p = 1;
    for (; p < digits; p++) {
        snprintf(buf, size, "%.*e", p - 1, x);
        double back = strtod(buf, NULL);
        if (is_float32 ? (float)back == (float)x : back == x) break;
    }
    /* Like Go: exponential notation only if the exponent is less than -4 or
     * greater than or equal to 6; otherwise, plain decimal (1000, not 1e+03). */
    snprintf(buf, size, "%.*e", p - 1, x);
    int exp = atoi(strchr(buf, 'e') + 1);
    if (exp < -4 || exp >= 6) return snprintf(buf, size, "%.*e", p - 1, x);
    int decimals = p - 1 - exp;
    return snprintf(buf, size, "%.*f", decimals > 0 ? decimals : 0, x);
}

int64_t mlc_conv_i(int64_t v, int64_t lo, int64_t hi, const char *type, const char *file, int line) {
    if (v < lo || v > hi) {
        char msg[160];
        snprintf(msg, sizeof msg, "value %" PRId64 " doesn't fit in %s (%" PRId64 " to %" PRId64 ")", v, type, lo, hi);
        mlc_panic(file, line, msg);
    }
    return v;
}

uint64_t mlc_conv_u(uint64_t v, uint64_t hi, const char *type, const char *file, int line) {
    if (v > hi) {
        char msg[160];
        snprintf(msg, sizeof msg, "value %" PRIu64 " doesn't fit in %s (0 to %" PRIu64 ")", v, type, hi);
        mlc_panic(file, line, msg);
    }
    return v;
}

double mlc_conv_f(double v, double lo, double hi, const char *type, const char *file, int line) {
    if (!(v > lo && v < hi)) { /* NaN fails both comparisons */
        char num[32], msg[160];
        mlc_format_float(num, sizeof num, v, 17, 0);
        snprintf(msg, sizeof msg, "value %s doesn't fit in %s", num, type);
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
    if (end != tmp + s->len) return false; /* text left over that isn't a number */
    *out = v;
    return true;
}

/* ---------- standard library (extern fn of the std .mlc files) ----------
 * Each function receives, at the end, the file and line of the call in the .mlc. The ones that
 * can fail return a negative number (-errno or a code of their own), and
 * the MLC layer turns that into a Result. */

static int mlc_argc;
static char **mlc_argv;

void mlc_args_init(int argc, char **argv) {
    mlc_stack_init(); /* called at the start of main: the main thread's stack */
    mlc_own_thread = true;
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

uint64_t mlc_std_math_float64_bits(double x, const char *file, int line) {
    (void)file;
    (void)line;
    uint64_t u;
    memcpy(&u, &x, sizeof u);
    return u;
}

double mlc_std_math_float64_from_bits(uint64_t u, const char *file, int line) {
    (void)file;
    (void)line;
    double x;
    memcpy(&x, &u, sizeof x);
    return x;
}

uint32_t mlc_std_math_float32_bits(float x, const char *file, int line) {
    (void)file;
    (void)line;
    uint32_t u;
    memcpy(&u, &x, sizeof u);
    return u;
}

float mlc_std_math_float32_from_bits(uint32_t u, const char *file, int line) {
    (void)file;
    (void)line;
    float x;
    memcpy(&x, &u, sizeof x);
    return x;
}

bool mlc_std_math_is_inf(double x, const char *file, int line) {
    (void)file;
    (void)line;
    return isinf(x);
}

/* std/os */
static int64_t mlc_copy_cstr(mlc_sl_byte *dst, const char *s) {
    size_t n = strlen(s);
    if ((int64_t)n > dst->len) return -1002; /* os.TOO_LONG */
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
    if (i < 0 || i >= mlc_argc) return -ENOENT; /* os.NOT_FOUND */
    return mlc_copy_cstr(dst, mlc_argv[i]);
}

int64_t mlc_std_os_getenv_copy(mlc_str name, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    char buf[256];
    if (name.len >= (int64_t)sizeof buf) return -1002; /* os.TOO_LONG */
    if (name.len > 0) memcpy(buf, name.ptr, (size_t)name.len);
    buf[name.len] = '\0';
    const char *v = getenv(buf);
    if (v == NULL) return -ENOENT; /* os.NOT_FOUND */
    return mlc_copy_cstr(dst, v);
}

void mlc_std_os_exit(int64_t code, const char *file, int line) {
    (void)file;
    (void)line;
    fflush(stdout);
    exit((int)code);
}

#ifndef MLC_EMBEDDED
/* std/fs: the path comes from a []byte, without '\0' at the end; it's copied to the stack. */
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

/* Portable fs errors: the errno numbers that change between systems become
 * MLC constants (see std/fs.mlc). */
static int64_t mlc_fs_err(int e) {
    if (e == ENOTEMPTY) return -1001; /* fs.NOT_EMPTY */
    return -e;
}

/* An open file: the system's descriptor. */
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
    default: mlc_panic(file, line, "fs.open: unknown mode (use fs.READ, fs.WRITE, fs.APPEND or fs.READ_WRITE)");
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

/* An open directory. */
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

/* The next name (without . and ..), or -1 at the end. */
int64_t mlc_std_fs_dir_next(mlc_dir *d, mlc_sl_byte *dst, const char *file, int line) {
    for (;;) {
        struct dirent *e = readdir(d->d);
        if (e == NULL) return -1;
        if (strcmp(e->d_name, ".") == 0 || strcmp(e->d_name, "..") == 0) continue;
        int64_t n = mlc_copy_cstr(dst, e->d_name);
        if (n < 0) {
            char msg[160];
            snprintf(msg, sizeof msg, "fs.Dir.next: the name has %zu bytes and the buffer, %" PRId64 " (a [256]byte is always enough)",
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

/* stat: size, whether it's a directory and the modification date (ms since 1970). */
/* is_link says whether the path is a symbolic link (lstat: without following the link). */
bool mlc_std_fs_is_link(mlc_sl_byte *path, const char *file, int line) {
    (void)file;
    (void)line;
    char p[4096];
    struct stat st;
    if (!mlc_cpath(p, sizeof p, path)) return false;
    return lstat(p, &st) == 0 && S_ISLNK(st.st_mode);
}

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

#endif /* !MLC_EMBEDDED */

/* std/os: environment and current directory. */
int64_t mlc_std_os_setenv_raw(mlc_str name, mlc_sl_byte *value, const char *file, int line) {
    (void)file;
    (void)line;
    char n[256], v[4096];
    if (name.len <= 0 || name.len >= (int64_t)sizeof n || value->len >= (int64_t)sizeof v) return -1002; /* os.TOO_LONG */
    memcpy(n, name.ptr, (size_t)name.len);
    n[name.len] = '\0';
    if (value->len > 0) memcpy(v, value->ptr, (size_t)value->len);
    v[value->len] = '\0';
    return setenv(n, v, 1) == 0 ? 0 : -errno;
}

#ifdef MLC_EMBEDDED
int64_t mlc_std_os_cwd_raw(mlc_sl_byte *dst, const char *file, int line) {
    (void)dst;
    (void)file;
    (void)line;
    return -ENOSYS;
}

int64_t mlc_std_os_chdir_raw(mlc_sl_byte *path, const char *file, int line) {
    (void)path;
    (void)file;
    (void)line;
    return -ENOSYS;
}
#else
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
#endif

#ifndef MLC_EMBEDDED
/* std/process: runs a program. args has the arguments separated by '\0'
 * (count of them, the first is the program, looked up in the PATH). With capture, the
 * program's standard output goes into out; what doesn't fit is read and
 * discarded, and the result is -1000 (TOO_BIG). status receives the exit
 * code (128 + the signal, if the program died from a signal). */
extern char **environ;

int64_t mlc_std_process_run_raw(mlc_sl_byte *args, int64_t count, mlc_sl_byte *out, bool capture, int64_t *status,
                                const char *file, int line) {
    char *argv[256];
    char buf[8193];
    if (count < 1 || count > 255 || args->len > 8192) mlc_panic(file, line, "process: too many arguments");
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
    fflush(stdout); /* the program's output doesn't jump ahead of ours */
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

#endif /* !MLC_EMBEDDED */

/* std/rand: random bytes from the operating system (getentropy, the kernel's
 * cryptographic source), in pieces of up to 256 bytes, its limit. If
 * the system doesn't deliver, it panics: there's no safe alternative. */
void mlc_std_rand_secure(mlc_sl_byte *dst, const char *file, int line) {
    int64_t done = 0;
    while (done < dst->len) {
        size_t n = (size_t)(dst->len - done);
        if (n > 256) n = 256;
#if defined(MLC_EMBEDDED)
        (void)file;
        (void)line;
        esp_fill_random(dst->ptr + done, n); /* the hardware generator (true random with the radio on) */
#elif defined(__APPLE__) || defined(__linux__)
        if (getentropy(dst->ptr + done, n) != 0) mlc_panic(file, line, "rand.secure: the system didn't deliver random bytes");
#else
        FILE *f = fopen("/dev/urandom", "rb");
        if (f == NULL || fread(dst->ptr + done, 1, n, f) != n) mlc_panic(file, line, "rand.secure: the system didn't deliver random bytes");
        fclose(f);
#endif
        done += (int64_t)n;
    }
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
#if defined(MLC_EMBEDDED)
    /* ESP-IDF has no nanosleep; its usleep yields to FreeRTOS (vTaskDelay)
     * for the whole ticks. One second at a time keeps the microseconds in
     * usleep's 32 bits. */
    while (ms > 0) {
        int64_t part = ms < 1000 ? ms : 1000;
        usleep((useconds_t)(part * 1000));
        ms -= part;
    }
#else
    struct timespec ts = {(time_t)(ms / 1000), (long)(ms % 1000) * 1000000};
    nanosleep(&ts, NULL);
#endif
}

/* std/vec */
void mlc_std_vec_fail(mlc_str msg, const char *file, int line) {
    char buf[160];
    int64_t n = msg.len < (int64_t)sizeof buf - 1 ? msg.len : (int64_t)sizeof buf - 1;
    if (n > 0) memcpy(buf, msg.ptr, (size_t)n);
    buf[n] = '\0';
    mlc_panic(file, line, buf);
}

/* std/io: the Writer's output and the formatting of real numbers */
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

/* A real with prec decimal places ({:.2}). */
int64_t mlc_std_io_format_fixed(mlc_sl_byte *dst, double x, int64_t prec, const char *file, int line) {
    char buf[352];
    int n;
    if (isnan(x)) n = snprintf(buf, sizeof buf, "NaN");
    else if (isinf(x)) n = snprintf(buf, sizeof buf, x > 0 ? "+Inf" : "-Inf");
    else n = snprintf(buf, sizeof buf, "%.*f", (int)prec, x);
    return mlc_put_bytes(dst, buf, n, "io.put_float", file, line);
}

/* str.format destinations: a stack, because a write can call another
 * str.format. The Writer keeps only the index, checked here; the text that doesn't
 * fit is counted and, at the end, panics at the str.format's line. */
typedef struct {
    uint8_t *ptr;
    int64_t cap, len, needed;
} mlc_sink;

/* Per thread: each thread has its own str.format calls in progress. The
 * array comes from the heap at the thread's first str.format and goes back
 * when the thread ends (mlc_thread_buffers_free): ESP-IDF reserves the
 * _Thread_local variables in the stack of every FreeRTOS task, the system's
 * too, and some of those have less than 2 KB. */
#define MLC_SINK_MAX 64
static _Thread_local mlc_sink *mlc_sinks = NULL;
static _Thread_local int64_t mlc_sink_depth = 0;

int64_t mlc_sink_push(mlc_sl_byte *dst, const char *file, int line) {
    if (mlc_sink_depth == MLC_SINK_MAX) mlc_panic(file, line, "str.format nested more than 64 levels deep");
    if (mlc_sinks == NULL && (mlc_sinks = malloc(MLC_SINK_MAX * sizeof *mlc_sinks)) == NULL)
        mlc_panic(file, line, "out of memory");
    mlc_sinks[mlc_sink_depth] = (mlc_sink){dst->ptr, dst->len, 0, 0};
    return mlc_sink_depth++;
}

void mlc_std_io_sink_put(int64_t sink, mlc_sl_byte *b, const char *file, int line) {
    if (sink < 0 || sink >= mlc_sink_depth) mlc_panic(file, line, "Writer with a str.format destination that doesn't exist");
    mlc_sink *s = &mlc_sinks[sink];
    int64_t room = s->cap - s->len;
    int64_t n = b->len < room ? b->len : room;
    if (n > 0) memcpy(s->ptr + s->len, b->ptr, (size_t)n);
    s->len += n;
    s->needed += b->len;
}

/* mlc_sink_pop_trunc is the log's mlc_sink_pop: what didn't fit is left out,
 * without a panic. */
int64_t mlc_sink_pop_trunc(int64_t sink, const char *file, int line) {
    if (sink != mlc_sink_depth - 1) mlc_panic(file, line, "internal error: str.format destinations out of order");
    return mlc_sinks[--mlc_sink_depth].len;
}

int64_t mlc_sink_pop(int64_t sink, const char *file, int line) {
    if (sink != mlc_sink_depth - 1) mlc_panic(file, line, "internal error: str.format destinations out of order");
    mlc_sink s = mlc_sinks[--mlc_sink_depth];
    if (s.needed > s.cap) {
        char msg[160];
        snprintf(msg, sizeof msg, "str.format: the text has %" PRId64 " bytes and the destination, %" PRId64, s.needed, s.cap);
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
    snprintf(msg, sizeof msg, "shift of %" PRId64 " bits on a %d-bit integer (use 0 to %d)", n, bits, bits - 1);
    mlc_panic(file, line, msg);
}

void mlc_null_fn(const char *file, int line) {
    mlc_panic(file, line, "call of a null fn value (the zero value of an fn: the variable or field didn't receive a function)");
}

_Thread_local uintptr_t mlc_stack_low = 0;

/* mlc_stack_init finds the current thread's stack. The reserve is the same calculation
 * as the checker's (checker.StackReserve): 1/8 of the stack, between 4 KB and 32 KB. */
void mlc_stack_init(void) {
    uintptr_t low = 0, size = 0;
#if defined(MLC_EMBEDDED)
    /* a FreeRTOS task: the stack starts (lowest address) at pxTaskGetStackStart
     * and grows down; this is called at the task's start, so almost all of it is
     * between here and there */
    uint8_t here;
    low = (uintptr_t)pxTaskGetStackStart(NULL);
    size = (uintptr_t)&here - low;
#elif defined(__APPLE__)
    pthread_t self = pthread_self();
    size = (uintptr_t)pthread_get_stacksize_np(self);
    low = (uintptr_t)pthread_get_stackaddr_np(self) - size; /* the address is the top */
#elif defined(__linux__)
    pthread_attr_t attr;
    if (pthread_getattr_np(pthread_self(), &attr) == 0) {
        void *addr;
        size_t n;
        if (pthread_attr_getstack(&attr, &addr, &n) == 0) {
            low = (uintptr_t)addr;
            size = n;
        }
        pthread_attr_destroy(&attr);
    }
#endif
    if (size == 0) return; /* unknown system: unchecked */
    uintptr_t reserve = size / 8;
    if (reserve < 4096) reserve = 4096;
    if (reserve > 32768) reserve = 32768;
    mlc_stack_low = low + reserve;
}

void mlc_stack_overflow(int64_t need, const char *file, int line) {
    char msg[256];
    /* in a callback, the thread (and its stack) is C's: thread.spawn doesn't help */
    const char *hint = mlc_own_thread ? "ask for a larger stack in thread.spawn, or put the large buffers on the heap with alloc"
                                      : "the thread is C's, which called back: put the large buffers on the heap with alloc";
    snprintf(msg, sizeof msg,
             "stack overflow: the function's frame (about %" PRId64 " KB) doesn't fit in what's left of this thread's stack (%s)",
             (need + 1023) / 1024, hint);
    mlc_panic(file, line, msg);
}

/* std/map */
void mlc_std_map_fail(mlc_str msg, const char *file, int line) {
    mlc_std_vec_fail(msg, file, line);
}

void mlc_std_uuid_fail(mlc_str msg, const char *file, int line) {
    mlc_std_vec_fail(msg, file, line);
}

void mlc_std_rand_fail(mlc_str msg, const char *file, int line) {
    mlc_std_vec_fail(msg, file, line);
}

void mlc_std_deque_fail(mlc_str msg, const char *file, int line) {
    mlc_std_vec_fail(msg, file, line);
}

void mlc_std_heap_fail(mlc_str msg, const char *file, int line) {
    mlc_std_vec_fail(msg, file, line);
}

/* std/testing. Each test runs in a process: mlc test passes the test's
 * index as an argument, and the exit code says the result (0 ok, 1
 * a check failed, 2 panic, 3 leak). Failures don't interrupt the
 * test; each one points at the check's line. */

int64_t mlc_test_selected(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "this test program is run by mlc test\n");
        exit(2);
    }
    return atoll(argv[1]);
}

/* Benchmark: "bench i n" runs benchmark i with b.n = n. */
bool mlc_bench_selected(int argc, char **argv, int64_t *bench, int64_t *n) {
    if (argc != 4 || strcmp(argv[1], "bench") != 0) return false;
    *bench = atoll(argv[2]);
    *n = atoll(argv[3]);
    return true;
}

int64_t mlc_bench_now(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000 + ts.tv_nsec;
}

/* The time goes on a line of its own at the end, which mlc test looks for. */
void mlc_bench_report(int64_t ns) {
    fflush(stdout);
    fprintf(stderr, "\nmlc-bench-ns %" PRId64 "\n", ns);
}

void mlc_test_finish(void) {
    if (mlc_test_panic_expected) {
        fflush(stdout);
        fprintf(stderr, "the test ended without the expected panic (testing.expect_panic)\n");
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

/* Writes a text between quotes, with the control bytes escaped. */
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
    if (got != want) mlc_test_fail(file, line, "got %" PRId64 ", expected %" PRId64, got, want);
}

void mlc_std_testing_eq_uint(uint64_t got, uint64_t want, const char *file, int line) {
    if (got != want) mlc_test_fail(file, line, "got %" PRIu64 " (0x%" PRIx64 "), expected %" PRIu64 " (0x%" PRIx64 ")", got, got, want, want);
}

void mlc_std_testing_eq_bool(bool got, bool want, const char *file, int line) {
    if (got != want) mlc_test_fail(file, line, "got %s, expected %s", got ? "true" : "false", want ? "true" : "false");
}

void mlc_std_testing_eq_float(double got, double want, const char *file, int line) {
    if (got != want) {
        char a[32], b[32];
        mlc_format_float(a, sizeof a, got, 17, 0);
        mlc_format_float(b, sizeof b, want, 17, 0);
        mlc_test_fail(file, line, "got %s, expected %s", a, b);
    }
}

void mlc_std_testing_near(double got, double want, double tol, const char *file, int line) {
    if (!(fabs(got - want) <= tol)) { /* NaN fails too */
        char a[32], b[32], t[32];
        mlc_format_float(a, sizeof a, got, 17, 0);
        mlc_format_float(b, sizeof b, want, 17, 0);
        mlc_format_float(t, sizeof t, tol, 17, 0);
        mlc_test_fail(file, line, "got %s, expected %s (tolerance %s)", a, b, t);
    }
}

static void mlc_test_eq_text(const uint8_t *got, int64_t gn, const uint8_t *want, int64_t wn,
                             const char *file, int line) {
    if (gn == wn && (gn == 0 || memcmp(got, want, (size_t)gn) == 0)) return;
    mlc_test_fail(file, line, "different text");
    fputs("    got:      ", stderr);
    mlc_test_quote(got, gn);
    fputs("\n    expected: ", stderr);
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

/* std/thread. The thread's record (context, result and the mlc_thread at the
 * start) is allocated by the generated code with mlc_alloc and freed on join; the
 * stack is the system's (pthread), with the size asked for in spawn. */

static void mlc_thread_buffers_free(void);

static void *mlc_thread_entry(void *p) {
    mlc_thread *t = p;
    mlc_stack_init();
    mlc_own_thread = true;
    t->run(t);
    mlc_thread_buffers_free();
    t->done = 1;
    return NULL;
}

void mlc_thread_start(mlc_thread *t, void (*run)(mlc_thread *), int64_t stack, const char *file, int line) {
    if (stack < 16384) mlc_panic(file, line, "thread.spawn: the stack must have at least 16 KB (thread.STACK_64KB, for example)");
#ifdef MLC_EMBEDDED
    size_t size = (size_t)stack; /* a FreeRTOS task: no pages */
#else
    long page = sysconf(_SC_PAGESIZE);
    if (page <= 0) page = 4096;
    size_t size = (size_t)((stack + page - 1) / page * page); /* macOS requires a multiple of the page */
#endif
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    if (pthread_attr_setstacksize(&attr, size) != 0) {
        pthread_attr_destroy(&attr);
        mlc_panic(file, line, "thread.spawn: stack size refused by the system");
    }
    t->run = run;
    t->done = 0;
    int err = pthread_create(&t->th, &attr, mlc_thread_entry, t);
    pthread_attr_destroy(&attr);
    if (err != 0) mlc_panic(file, line, "thread.spawn: the system didn't create the thread (thread or memory limit)");
}

void mlc_thread_join(mlc_thread *t, const char *file, int line) {
    if (pthread_join(t->th, NULL) != 0) mlc_panic(file, line, "internal error: thread.join failed");
}

int64_t mlc_std_thread_cpu_count(const char *file, int line) {
    (void)file;
    (void)line;
#if defined(MLC_EMBEDDED) && defined(configNUMBER_OF_CORES)
    return configNUMBER_OF_CORES;
#elif defined(MLC_EMBEDDED)
    return portNUM_PROCESSORS;
#elif defined(_SC_NPROCESSORS_ONLN)
    long n = sysconf(_SC_NPROCESSORS_ONLN);
    return n > 0 ? n : 1;
#else
    return 1;
#endif
}

/* std/chan. A channel is a ring buffer of cap slots of elem bytes,
 * protected by a lock. send copies the value into the ring (ownership passes to the
 * channel) and recv copies it out (ownership passes to the receiver). The ends
 * are counted: with no Sender open, recv on an empty channel returns "closed";
 * with no Receiver open, send frees the value (drop) and returns false. The last
 * close frees what's left in the ring and the channel itself. The channel and the ring are
 * allocated with mlc_alloc in chan.new, so they count as allocations. */

struct mlc_chan {
    pthread_mutex_t mu;
    pthread_cond_t not_empty, not_full;
    int64_t cap, head, count, senders, receivers;
    size_t elem;
    void (*drop)(void *);
    unsigned char *buf;
};

mlc_chan *mlc_chan_new(int64_t cap, size_t elem, void (*drop)(void *), const char *file, int line) {
    if (cap < 1) mlc_panic(file, line, "chan.new: the capacity must be at least 1");
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
        pthread_cond_broadcast(&c->not_empty); /* whoever waits in recv can see the end */
    } else {
        c->receivers--;
        pthread_cond_broadcast(&c->not_full); /* whoever waits in send gives up */
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
        if (c->drop) c->drop(v); /* nobody receives anymore: the value is freed */
        return false;
    }
    memcpy(c->buf + (size_t)((c->head + c->count) % c->cap) * c->elem, v, c->elem);
    c->count++;
    pthread_cond_signal(&c->not_empty);
    pthread_mutex_unlock(&c->mu);
    return true;
}

/* try_send never waits: 0 the value went in, 1 the channel is full, 2 no
 * Receiver is open. In 1 and 2 the value stays with the caller (it comes
 * back in the result), so nothing is freed here. */
int32_t mlc_chan_try_send(mlc_chan *c, void *v) {
    pthread_mutex_lock(&c->mu);
    int32_t r = c->receivers == 0 ? 2 : c->count == c->cap ? 1 : 0;
    if (r == 0) {
        memcpy(c->buf + (size_t)((c->head + c->count) % c->cap) * c->elem, v, c->elem);
        c->count++;
        pthread_cond_signal(&c->not_empty);
    }
    pthread_mutex_unlock(&c->mu);
    return r;
}

bool mlc_chan_recv(mlc_chan *c, void *out) {
    pthread_mutex_lock(&c->mu);
    while (c->count == 0 && c->senders > 0) pthread_cond_wait(&c->not_empty, &c->mu);
    if (c->count == 0) {
        pthread_mutex_unlock(&c->mu);
        return false; /* every Sender closed and the channel is empty */
    }
    memcpy(out, c->buf + (size_t)c->head * c->elem, c->elem);
    c->head = (c->head + 1) % c->cap;
    c->count--;
    pthread_cond_signal(&c->not_full);
    pthread_mutex_unlock(&c->mu);
    return true;
}

/* std/mutex. The mutex keeps the value right after the header; the lock is of
 * the error-checking kind, so locking twice on the same thread panics instead of
 * locking the program forever. The handles and the guards are counted:
 * the last one to leave frees the value (drop) and the mutex. */

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
    m->refs++; /* the guard keeps the mutex alive */
    int err = pthread_mutex_lock(&m->mu);
    if (err != 0) {
        m->refs--;
        mlc_panic(file, line, err == EDEADLK ? "mutex already locked by this thread (unlock the guard before locking again)"
                                             : "internal error: lock failed");
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

/* std/atomic: an atomic integer (or bool), with the handles counted. */

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
    /* like MLC's arithmetic, overflow panics: checks before swapping */
    int64_t old = atomic_load(&a->v);
    for (;;) {
        int64_t sum;
        if (__builtin_add_overflow(old, n, &sum)) mlc_panic(file, line, "atomic.Int.add: int overflow");
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

/* std/net: TCP. Each handle keeps the socket's descriptor. The errors that change
 * between systems become MLC codes (see std/net.mlc); the others are
 * errno. Writing to a socket closed on the other side returns an error instead of
 * killing the program with SIGPIPE. On the ESP32 the sockets are lwIP's,
 * with the same calls; without Wi-Fi only 127.0.0.1 answers. */

typedef struct mlc_sock {
    int fd;
    int64_t accept_ms; /* the accept's maximum time (listener); -1: no limit */
} mlc_sock;

static int64_t mlc_net_err(int e) {
    switch (e) {
    case ECONNREFUSED: return 2001; /* net.REFUSED */
    case EADDRINUSE: return 2003;   /* net.IN_USE */
    case ETIMEDOUT:
    case EAGAIN: return 2004; /* net.TIMEOUT (SO_RCVTIMEO returns EAGAIN) */
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
    s->accept_ms = -1;
    return s;
}

#ifdef MLC_EMBEDDED
/* lwIP's thread (tcpip) starts with esp_netif_init, which the Wi-Fi also
 * calls and which does nothing the second time. Before it, a socket call
 * stops the chip with an assert. */
static pthread_once_t mlc_net_once = PTHREAD_ONCE_INIT;
static void mlc_net_start_once(void) { esp_netif_init(); }
#endif

/* Resolves host:port (empty host: all interfaces, for listen). */
static int mlc_net_resolve(mlc_sl_byte *host, int64_t port, bool passive, struct addrinfo **res, int64_t *err) {
#ifdef MLC_EMBEDDED
    pthread_once(&mlc_net_once, mlc_net_start_once);
#endif
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
        if (l->accept_ms >= 0) {
            struct pollfd p = {l->fd, POLLIN, 0};
            int r = poll(&p, 1, (int)l->accept_ms);
            if (r == 0) {
                *err = 2004; /* net.TIMEOUT */
                return NULL;
            }
            if (r < 0) {
                if (errno == EINTR) {
                    *err = 2004; /* a signal: the caller checks and tries again */
                    return NULL;
                }
                *err = mlc_net_err(errno);
                return NULL;
            }
        }
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

/* mlc_tcp_dial opens a TCP connection to host:port and returns the descriptor,
 * or -1 with the code in err (also used by std/tls). */
static int mlc_tcp_dial(mlc_sl_byte *host, int64_t port, int64_t *err) {
    struct addrinfo *res;
    *err = 0;
    if (!mlc_net_resolve(host, port, false, &res, err)) return -1;
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
        /* the RST that answers the SYN is a refusal; lwIP (ESP32) reports
         * it as ECONNRESET, the desktops as ECONNREFUSED */
        if (last == ECONNRESET) last = ECONNREFUSED;
        close(fd);
        fd = -1;
    }
    freeaddrinfo(res);
    if (fd < 0) {
        *err = mlc_net_err(last);
        return -1;
    }
    mlc_net_nosigpipe(fd);
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    return fd;
}

mlc_sock *mlc_std_net_connect(mlc_sl_byte *host, int64_t port, int64_t *err, const char *file, int line) {
    int fd = mlc_tcp_dial(host, port, err);
    if (fd < 0) return NULL;
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

void mlc_std_net_listener_timeout_raw(mlc_sock *l, int64_t ms, const char *file, int line) {
    (void)file;
    (void)line;
    l->accept_ms = ms > INT32_MAX ? INT32_MAX : ms;
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

/* std/time: the local time zone's offset (seconds east of UTC) at the
 * instant ms. */
int64_t mlc_std_time_local_offset(int64_t ms, const char *file, int line) {
    (void)file;
    (void)line;
    time_t t = (time_t)(ms / 1000);
    struct tm lt;
    if (localtime_r(&t, &lt) == NULL) return 0;
#if defined(MLC_EMBEDDED)
    /* newlib's struct tm has no tm_gmtoff: the UTC breakdown read back as
     * local time is behind by exactly the offset */
    struct tm g;
    if (gmtime_r(&t, &g) == NULL) return 0;
    g.tm_isdst = lt.tm_isdst;
    return (int64_t)difftime(t, mktime(&g));
#elif defined(__APPLE__) || defined(__USE_MISC) || defined(_DEFAULT_SOURCE)
    return (int64_t)lt.tm_gmtoff;
#else
    return 0;
#endif
}

void mlc_std_hash_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_encoding_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_binary_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_http_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_url_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_csv_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_fs_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_arena_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_json_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_hmac_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

void mlc_std_password_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

/* os.error_text: the text of a library error code. The codes
 * below 1000 are the system's (errno), with the most common ones written out;
 * those from 1000 up are MLC's and apply on every system. */
static const char *mlc_error_text(int64_t code) {
    switch (code) {
    case 0: return "no error";
    case 1000: return "doesn't fit in the buffer";
    case 1001: return "the directory isn't empty";
    case 1002: return "too long for the buffer";
    case 1003: return "end of the data";
    case 1004: return "directories nested too deep";
    case 2001: return "connection refused: nobody listens on that port";
    case 2002: return "the server's name wasn't found";
    case 2003: return "the port is already in use";
    case 2004: return "timed out";
    case 2005: return "the connection was closed by the other side";
    case 3001: return "invalid HTTP message";
    case 3002: return "HTTP header too large";
    case 3003: return "unsupported URL scheme (only http and https)";
    case 3101: return "invalid URL";
    case 3201: return "invalid configuration line (expected key=value)";
    case 3301: return "wrong quotes in a CSV field";
    case 4001: return "SQL error";
    case 4005: return "the database is locked by another process";
    case 4008: return "the database was opened read-only";
    case 4014: return "couldn't open the database file";
    case 4019: return "database constraint violated (UNIQUE, NOT NULL, FOREIGN KEY or CHECK)";
    case 4021: return "wrong use of the SQLite API";
    case 4900: return "the SQLite library wasn't found on this system";
    case 5001: return "the TLS handshake failed";
    case 5002: return "the server's certificate isn't valid (expired, for another name or from an unknown authority)";
    case 5003: return "the TLS library (OpenSSL) wasn't found on this system";
    case 5004: return "error in the TLS connection";
    case 5005: return "the server's certificate or key didn't load";
    case 6001: return "no Wi-Fi network with that name answered";
    case 6002: return "the Wi-Fi network refused the password";
    case 6003: return "the Wi-Fi network gave no address in time";
    case 6004: return "this system has no Wi-Fi to control (the system manages the network)";
    case 6005: return "couldn't start the Wi-Fi (ESP-IDF's log says why)";
    case 6006: return "the Wi-Fi network's name is empty or longer than 32 bytes, or the password longer than 64";
    case EPERM: return "operation not permitted";
    case ENOENT: return "doesn't exist";
    case EIO: return "input/output error";
    case EBADF: return "invalid file descriptor";
    case ENOMEM: return "out of memory";
    case EACCES: return "permission denied";
    case EBUSY: return "the resource is busy";
    case EEXIST: return "already exists";
    case EXDEV: return "can't move between different file systems";
    case ENOTDIR: return "not a directory";
    case EISDIR: return "is a directory";
    case EINVAL: return "invalid argument";
    case ENFILE:
    case EMFILE: return "too many open files";
    case EFBIG: return "file too large";
    case ENOSPC: return "no space left on the disk";
    case EROFS: return "read-only file system";
    case ENAMETOOLONG: return "name too long";
    case ELOOP: return "too many symbolic links (probably a cycle)";
    case EINTR: return "interrupted by a signal";
    case EAGAIN: return "resource temporarily unavailable";
    case EPIPE: return "the other end was closed";
    case ENETUNREACH: return "the network is unreachable";
    case EHOSTUNREACH: return "the server is unreachable";
    case ECONNABORTED: return "the connection was aborted";
    case ENOTCONN: return "not connected";
    case EADDRNOTAVAIL: return "address not available on this machine";
    }
    if (code > 4000 && code < 4900) return "SQLite error (the text comes from db.error)";
    return NULL;
}

int64_t mlc_std_os_error_text_raw(int64_t code, mlc_sl_byte *dst, const char *file, int line) {
    char buf[160];
    const char *t = mlc_error_text(code);
    if (t == NULL) {
        if (code > 0 && code < 1000) {
            snprintf(buf, sizeof buf, "%s (system error %" PRId64 ")", strerror((int)code), code);
        } else {
            snprintf(buf, sizeof buf, "unknown error (%" PRId64 ")", code);
        }
        t = buf;
    }
    return mlc_put_bytes(dst, t, (int64_t)strlen(t), "os.error_text", file, line);
}

#ifndef MLC_EMBEDDED
/* std/signal: the handler only marks the signal (got) and writes a byte into a pipe,
 * to wake whoever waits in signal.wait; what to do is up to the program.
 * The codes are MLC's (std/signal.mlc), translated here into the
 * system's (SIGUSR1 is 30 on macOS and 10 on Linux). */

static volatile sig_atomic_t mlc_sig_got[32];
static int mlc_sig_pipe[2] = {-1, -1};
static pthread_mutex_t mlc_sig_mu = PTHREAD_MUTEX_INITIALIZER;

static int mlc_sig_native(int64_t code) {
    switch (code) {
    case 1: return SIGHUP;
    case 2: return SIGINT;
    case 3: return SIGQUIT;
    case 10: return SIGUSR1;
    case 12: return SIGUSR2;
    case 15: return SIGTERM;
    }
    return 0;
}

static int mlc_sig_code(int sig) {
    for (int c = 1; c < 32; c++) {
        if (mlc_sig_native(c) == sig) return c;
    }
    return 0;
}

static void mlc_sig_handler(int sig) {
    int saved = errno;
    int c = mlc_sig_code(sig);
    if (c > 0) mlc_sig_got[c] = 1;
    if (mlc_sig_pipe[1] >= 0) {
        ssize_t r = write(mlc_sig_pipe[1], "s", 1);
        (void)r;
    }
    errno = saved;
}

static int mlc_sig_check(int64_t code, const char *file, int line) {
    int sig = mlc_sig_native(code);
    if (sig == 0) {
        char msg[96];
        snprintf(msg, sizeof msg, "signal: unknown signal code (%" PRId64 ")", code);
        mlc_panic(file, line, msg);
    }
    return sig;
}

static void mlc_sig_set(int sig, void (*handler)(int), const char *file, int line) {
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = handler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_RESTART;
    if (sigaction(sig, &sa, NULL) != 0) mlc_panic(file, line, "signal: the system refused the handler");
}

void mlc_std_signal_listen(int64_t code, const char *file, int line) {
    int sig = mlc_sig_check(code, file, line);
    pthread_mutex_lock(&mlc_sig_mu);
    if (mlc_sig_pipe[0] < 0) {
        if (pipe(mlc_sig_pipe) != 0) {
            pthread_mutex_unlock(&mlc_sig_mu);
            mlc_panic(file, line, "signal: couldn't create the signal pipe");
        }
        for (int i = 0; i < 2; i++) {
            fcntl(mlc_sig_pipe[i], F_SETFL, fcntl(mlc_sig_pipe[i], F_GETFL) | O_NONBLOCK);
            fcntl(mlc_sig_pipe[i], F_SETFD, FD_CLOEXEC);
        }
    }
    pthread_mutex_unlock(&mlc_sig_mu);
    mlc_sig_set(sig, mlc_sig_handler, file, line);
}

void mlc_std_signal_ignore(int64_t code, const char *file, int line) {
    mlc_sig_set(mlc_sig_check(code, file, line), SIG_IGN, file, line);
}

void mlc_std_signal_restore(int64_t code, const char *file, int line) {
    mlc_sig_set(mlc_sig_check(code, file, line), SIG_DFL, file, line);
}

bool mlc_std_signal_received(int64_t code, const char *file, int line) {
    mlc_sig_check(code, file, line);
    return mlc_sig_got[code] != 0;
}

void mlc_std_signal_clear(int64_t code, const char *file, int line) {
    mlc_sig_check(code, file, line);
    mlc_sig_got[code] = 0;
}

void mlc_std_signal_raise(int64_t code, const char *file, int line) {
    kill(getpid(), mlc_sig_check(code, file, line));
}

static int64_t mlc_sig_first(void) {
    for (int c = 1; c < 32; c++) {
        if (mlc_sig_got[c]) return c;
    }
    return 0;
}

int64_t mlc_std_signal_wait(int64_t ms, const char *file, int line) {
    (void)file;
    (void)line;
    struct timespec start;
    clock_gettime(CLOCK_MONOTONIC, &start);
    for (;;) {
        int64_t c = mlc_sig_first();
        if (c != 0) return c;
        int timeout = -1;
        if (ms >= 0) {
            struct timespec now;
            clock_gettime(CLOCK_MONOTONIC, &now);
            int64_t spent = (int64_t)(now.tv_sec - start.tv_sec) * 1000 + (now.tv_nsec - start.tv_nsec) / 1000000;
            if (spent >= ms) return 0;
            int64_t left = ms - spent;
            timeout = left > INT32_MAX ? INT32_MAX : (int)left;
        }
        if (mlc_sig_pipe[0] < 0) { /* nobody called listen: only waits for the time */
            if (ms < 0) mlc_panic(file, line, "signal.wait without a timeout and without signal.listen: it would wait forever");
            struct timespec ts = {(time_t)(timeout / 1000), (long)(timeout % 1000) * 1000000};
            nanosleep(&ts, NULL);
            continue;
        }
        struct pollfd p = {mlc_sig_pipe[0], POLLIN, 0};
        poll(&p, 1, timeout);
        char buf[64];
        while (read(mlc_sig_pipe[0], buf, sizeof buf) > 0) {
        }
    }
}

#endif /* !MLC_EMBEDDED */

/* std/log: the logger is a counted handle (clone for other threads, the
 * last close closes the file). Each line is assembled whole and written under
 * the lock, so lines from different threads don't mix. */

typedef struct mlc_log {
    pthread_mutex_t mu;
    int fd;
    bool owns_fd;
    _Atomic int64_t level;
    _Atomic bool time;
    _Atomic int64_t refs;
} mlc_log;

static mlc_log *mlc_log_make(int fd, bool owns, int64_t level, const char *file, int line) {
    mlc_log *l = mlc_alloc(sizeof *l, file, line);
    pthread_mutex_init(&l->mu, NULL);
    l->fd = fd;
    l->owns_fd = owns;
    l->level = level;
    l->time = true;
    l->refs = 1;
    return l;
}

mlc_log *mlc_std_log_new_raw(int64_t fd, int64_t level, const char *file, int line) {
    if (fd != 1 && fd != 2) mlc_panic(file, line, "log.new: the destination is log.STDOUT or log.STDERR (for a file, use log.open)");
    return mlc_log_make((int)fd, false, level, file, line);
}

mlc_log *mlc_std_log_open_raw(mlc_sl_byte *path, int64_t level, int64_t *err, const char *file, int line) {
    char p[4096];
    *err = 0;
    if (path->len >= (int64_t)sizeof p) {
        *err = ENAMETOOLONG;
        return NULL;
    }
    memcpy(p, path->ptr, (size_t)path->len);
    p[path->len] = '\0';
    int fd = open(p, O_WRONLY | O_CREAT | O_APPEND | O_CLOEXEC, 0644);
    if (fd < 0) {
        *err = errno;
        return NULL;
    }
    return mlc_log_make(fd, true, level, file, line);
}

mlc_log *mlc_std_log_clone_raw(mlc_log *l, const char *file, int line) {
    (void)file;
    (void)line;
    l->refs++;
    return l;
}

void mlc_std_log_close_raw(mlc_log *l, const char *file, int line) {
    (void)file;
    (void)line;
    if (--l->refs > 0) return;
    if (l->owns_fd) close(l->fd);
    pthread_mutex_destroy(&l->mu);
    mlc_free(l);
}

int64_t mlc_std_log_level_raw(mlc_log *l, const char *file, int line) {
    (void)file;
    (void)line;
    return l->level;
}

void mlc_std_log_set_level_raw(mlc_log *l, int64_t level, const char *file, int line) {
    (void)file;
    (void)line;
    l->level = level;
}

void mlc_std_log_set_time_raw(mlc_log *l, bool on, const char *file, int line) {
    (void)file;
    (void)line;
    l->time = on;
}

void mlc_std_log_emit_raw(mlc_log *l, int64_t level, mlc_sl_byte *msg, const char *file, int line) {
    (void)file;
    (void)line;
    static const char *names[] = {"DEBUG", "INFO ", "WARN ", "ERROR"};
    char buf[4096 + 128];
    size_t n = 0;
    if (l->time) {
        struct timespec ts;
        clock_gettime(CLOCK_REALTIME, &ts);
        time_t t = ts.tv_sec;
        struct tm lt;
        localtime_r(&t, &lt);
        n += strftime(buf, 32, "%Y-%m-%dT%H:%M:%S", &lt);
        long off = (long)(mlc_std_time_local_offset((int64_t)t * 1000, file, line) / 60);
        char sign = off < 0 ? '-' : '+';
        if (off < 0) off = -off;
        n += (size_t)snprintf(buf + n, 32, ".%03ld%c%02ld:%02ld ", ts.tv_nsec / 1000000, sign, off / 60, off % 60);
    }
    const char *name = level >= 0 && level <= 3 ? names[level] : "?    ";
    memcpy(buf + n, name, 5);
    n += 5;
    buf[n++] = ' ';
    size_t m = msg->len < 4096 ? (size_t)msg->len : 4096;
    memcpy(buf + n, msg->ptr, m);
    n += m;
    buf[n++] = '\n';
    pthread_mutex_lock(&l->mu);
    size_t done = 0;
    while (done < n) {
        ssize_t w = write(l->fd, buf + done, n - done);
        if (w < 0) {
            if (errno == EINTR) continue;
            break; /* nowhere to record the log's error */
        }
        done += (size_t)w;
    }
    pthread_mutex_unlock(&l->mu);
}

/* lg.info("...", x) and the other levels: the compiler writes the format into a
 * Writer whose destination is the thread's line (mlc_log_begin) and hands the line
 * to the logger (mlc_log_end). Two levels are enough for a write that logs
 * something while it's written; whatever goes past LINE bytes is left out. */

#define MLC_LOG_LINE 2048
static _Thread_local uint8_t *mlc_log_bufs = NULL; /* 2 lines, from the heap like mlc_sinks */
static _Thread_local int mlc_log_depth = 0;

/* mlc_thread_buffers_free gives back, when a thread ends, what its
 * str.format and log calls took from the heap. */
static void mlc_thread_buffers_free(void) {
    free(mlc_sinks);
    mlc_sinks = NULL;
    free(mlc_log_bufs);
    mlc_log_bufs = NULL;
}

/* C callbacks (see mlc_runtime.h). mlc_cb_depth counts the callbacks in
 * progress in this thread: only the outermost one, in a thread MLC didn't
 * create, prepares it and undoes it, so a callback that calls C that calls
 * back doesn't free buffers still in use. */
static _Thread_local int mlc_cb_depth;

void mlc_cb_enter(mlc_cb_frame *f, const char *file, int line) {
#ifdef MLC_EMBEDDED
    if (xPortInIsrContext()) {
        /* in an interrupt nothing of the runtime works (stdio, the stack
         * check, the panic): only the ROM's printf */
        esp_rom_printf("panic: %s:%d: an MLC function was called from an interrupt (ISR); handle the interrupt in C and pass the event on (see docs/ffi.md)\n",
                       file, line);
        abort();
    }
#else
    (void)file;
    (void)line;
#endif
    f->outer = !mlc_own_thread && mlc_cb_depth == 0;
    mlc_cb_depth++;
    if (f->outer) mlc_stack_init();
}

void mlc_cb_leave(mlc_cb_frame *f) {
    mlc_cb_depth--;
    if (f->outer) {
        mlc_thread_buffers_free();
        mlc_stack_low = 0;
    }
}

bool mlc_log_enabled(void *l, int64_t level) { return level >= ((mlc_log *)l)->level; }

int64_t mlc_log_begin(const char *file, int line) {
    if (mlc_log_depth == 2) mlc_panic(file, line, "log: logging inside logging inside logging");
    if (mlc_log_bufs == NULL && (mlc_log_bufs = malloc(2 * MLC_LOG_LINE)) == NULL) mlc_panic(file, line, "out of memory");
    mlc_sl_byte d = {mlc_log_bufs + mlc_log_depth++ * MLC_LOG_LINE, MLC_LOG_LINE};
    return mlc_sink_push(&d, file, line);
}

void mlc_log_end(void *l, int64_t level, int64_t sink, const char *file, int line) {
    int64_t n = mlc_sink_pop_trunc(sink, file, line);
    mlc_sl_byte m = {mlc_log_bufs + --mlc_log_depth * MLC_LOG_LINE, n};
    mlc_std_log_emit_raw((mlc_log *)l, level, &m, file, line);
}

/* std/sync: a group of threads (without a result), waited for together. The
 * capacity is chosen by whoever creates the group. */

typedef struct mlc_group {
    int64_t cap;
    int64_t len;
    mlc_thread *threads[];
} mlc_group;

mlc_group *mlc_std_sync_group(int64_t cap, const char *file, int line) {
    if (cap < 0) mlc_panic(file, line, "sync.group: negative capacity");
    mlc_group *g = mlc_alloc(sizeof *g + (size_t)cap * sizeof(mlc_thread *), file, line);
    g->cap = cap;
    g->len = 0;
    return g;
}

/* reap waits for the threads that already finished and takes them out of the group, keeping the
 * others' order. */
int64_t mlc_std_sync_reap_raw(mlc_group *g, const char *file, int line) {
    int64_t k = 0, freed = 0;
    for (int64_t i = 0; i < g->len; i++) {
        mlc_thread *t = g->threads[i];
        if (t->done) {
            mlc_thread_join(t, file, line);
            mlc_free(t);
            freed++;
        } else {
            g->threads[k++] = t;
        }
    }
    g->len = k;
    return freed;
}

void mlc_std_sync_add_raw(mlc_group *g, mlc_thread *t, const char *file, int line) {
    if (g->cap == 0) {
        mlc_thread_join(t, file, line);
        mlc_free(t);
        mlc_panic(file, line, "sync.Group.add: a group of capacity 0 takes no threads");
    }
    if (g->len == g->cap && mlc_std_sync_reap_raw(g, file, line) == 0) {
        /* full and nobody finished: waits for the oldest */
        mlc_thread_join(g->threads[0], file, line);
        mlc_free(g->threads[0]);
        memmove(&g->threads[0], &g->threads[1], (size_t)(g->len - 1) * sizeof g->threads[0]);
        g->len--;
    }
    g->threads[g->len++] = t;
}

int64_t mlc_std_sync_len_raw(mlc_group *g, const char *file, int line) {
    (void)file;
    (void)line;
    return g->len;
}

void mlc_std_sync_wait_raw(mlc_group *g, const char *file, int line) {
    for (int64_t i = 0; i < g->len; i++) {
        mlc_thread_join(g->threads[i], file, line);
        mlc_free(g->threads[i]);
    }
    mlc_free(g);
}

#ifndef MLC_EMBEDDED
/* std/sqlite. The SQLite library is loaded on the first call
 * (dlopen), so only whoever uses the module needs it, and nobody needs
 * link flags. MLC_SQLITE_LIB picks the file; without it, it tries the
 * macOS and Linux names. SQLite's errors become 4000 + its code
 * (4019 is SQLITE_CONSTRAINT), so they don't mix with the system's. The
 * text goes in with a copy (SQLITE_TRANSIENT): MLC only lends it during the
 * call. And the text that comes out is copied into the caller's buffer. */

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_stmt sqlite3_stmt;

static struct {
    int (*open_v2)(const char *, sqlite3 **, int, const char *);
    int (*close_v2)(sqlite3 *);
    int (*exec)(sqlite3 *, const char *, void *, void *, char **);
    void (*free)(void *);
    int (*prepare_v2)(sqlite3 *, const char *, int, sqlite3_stmt **, const char **);
    int (*finalize)(sqlite3_stmt *);
    int (*step)(sqlite3_stmt *);
    int (*reset)(sqlite3_stmt *);
    int (*clear_bindings)(sqlite3_stmt *);
    int (*bind_int64)(sqlite3_stmt *, int, int64_t);
    int (*bind_double)(sqlite3_stmt *, int, double);
    int (*bind_text)(sqlite3_stmt *, int, const char *, int, void (*)(void *));
    int (*bind_blob)(sqlite3_stmt *, int, const void *, int, void (*)(void *));
    int (*bind_null)(sqlite3_stmt *, int);
    int (*bind_parameter_count)(sqlite3_stmt *);
    int (*column_count)(sqlite3_stmt *);
    int (*column_type)(sqlite3_stmt *, int);
    int64_t (*column_int64)(sqlite3_stmt *, int);
    double (*column_double)(sqlite3_stmt *, int);
    const unsigned char *(*column_text)(sqlite3_stmt *, int);
    const void *(*column_blob)(sqlite3_stmt *, int);
    int (*column_bytes)(sqlite3_stmt *, int);
    const char *(*column_name)(sqlite3_stmt *, int);
    int64_t (*changes64)(sqlite3 *);
    int (*changes)(sqlite3 *);
    int64_t (*last_insert_rowid)(sqlite3 *);
    const char *(*errmsg)(sqlite3 *);
    int (*busy_timeout)(sqlite3 *, int);
    const char *(*libversion)(void);
    int loaded; /* 1: loaded; -1: not found */
} mlc_sq;

static pthread_once_t mlc_sq_once = PTHREAD_ONCE_INIT;

static void mlc_sq_load(void) {
    const char *names[] = {getenv("MLC_SQLITE_LIB"), "libsqlite3.dylib", "/usr/lib/libsqlite3.dylib",
                           "libsqlite3.so.0", "libsqlite3.so", NULL};
    void *h = NULL;
    for (int i = 0; i < (int)(sizeof names / sizeof names[0]) && h == NULL; i++) {
        if (names[i] != NULL && names[i][0] != 0) h = dlopen(names[i], RTLD_NOW | RTLD_LOCAL);
    }
    mlc_sq.loaded = -1;
    if (h == NULL) return;
#define MLC_SQ(field, sym)                         \
    *(void **)&mlc_sq.field = dlsym(h, sym);       \
    if (mlc_sq.field == NULL) return
    MLC_SQ(open_v2, "sqlite3_open_v2");
    MLC_SQ(close_v2, "sqlite3_close_v2");
    MLC_SQ(exec, "sqlite3_exec");
    MLC_SQ(free, "sqlite3_free");
    MLC_SQ(prepare_v2, "sqlite3_prepare_v2");
    MLC_SQ(finalize, "sqlite3_finalize");
    MLC_SQ(step, "sqlite3_step");
    MLC_SQ(reset, "sqlite3_reset");
    MLC_SQ(clear_bindings, "sqlite3_clear_bindings");
    MLC_SQ(bind_int64, "sqlite3_bind_int64");
    MLC_SQ(bind_double, "sqlite3_bind_double");
    MLC_SQ(bind_text, "sqlite3_bind_text");
    MLC_SQ(bind_blob, "sqlite3_bind_blob");
    MLC_SQ(bind_null, "sqlite3_bind_null");
    MLC_SQ(bind_parameter_count, "sqlite3_bind_parameter_count");
    MLC_SQ(column_count, "sqlite3_column_count");
    MLC_SQ(column_type, "sqlite3_column_type");
    MLC_SQ(column_int64, "sqlite3_column_int64");
    MLC_SQ(column_double, "sqlite3_column_double");
    MLC_SQ(column_text, "sqlite3_column_text");
    MLC_SQ(column_blob, "sqlite3_column_blob");
    MLC_SQ(column_bytes, "sqlite3_column_bytes");
    MLC_SQ(column_name, "sqlite3_column_name");
    MLC_SQ(changes, "sqlite3_changes");
    MLC_SQ(last_insert_rowid, "sqlite3_last_insert_rowid");
    MLC_SQ(errmsg, "sqlite3_errmsg");
    MLC_SQ(busy_timeout, "sqlite3_busy_timeout");
    MLC_SQ(libversion, "sqlite3_libversion");
#undef MLC_SQ
    *(void **)&mlc_sq.changes64 = dlsym(h, "sqlite3_changes64"); /* 3.37+: optional */
    mlc_sq.loaded = 1;
}

static int mlc_sq_ready(void) {
    pthread_once(&mlc_sq_once, mlc_sq_load);
    return mlc_sq.loaded == 1;
}

#define MLC_SQ_ERR(rc) (4000 + ((rc) & 0xFF)) /* the primary code, without the extended one */
#define MLC_SQ_TRANSIENT ((void (*)(void *))(intptr_t)-1)

/* copies the C text s into dst and returns the length (or -1000 - n if it didn't
 * fit: the caller decides) */
static int64_t mlc_sq_copy(mlc_sl_byte *dst, const void *s, int64_t n) {
    if (n > dst->len) return -1000 - n;
    if (n > 0) memcpy(dst->ptr, s, (size_t)n);
    return n;
}

bool mlc_std_sqlite_available(const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_sq_ready();
}

int64_t mlc_std_sqlite_version_raw(mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    if (!mlc_sq_ready()) return -1;
    const char *v = mlc_sq.libversion();
    return mlc_sq_copy(dst, v, (int64_t)strlen(v));
}

void *mlc_std_sqlite_open_raw(mlc_sl_byte *path, int64_t flags, int64_t *err, const char *file, int line) {
    if (!mlc_sq_ready()) {
        *err = 4900;
        return NULL;
    }
    char name[4096];
    if (path->len >= (int64_t)sizeof name) mlc_panic(file, line, "sqlite.open: path too long");
    memcpy(name, path->ptr, (size_t)path->len);
    name[path->len] = 0;
    sqlite3 *db = NULL;
    int rc = mlc_sq.open_v2(name, &db, (int)flags, NULL);
    if (rc != 0) {
        *err = MLC_SQ_ERR(rc);
        if (db != NULL) mlc_sq.close_v2(db);
        return NULL;
    }
    mlc_sq.busy_timeout(db, 5000); /* waits for a lock up to 5 s instead of failing right away */
    mlc_live_allocs++;             /* the handle counts as an allocation: close discounts it */
    *err = 0;
    return db;
}

void mlc_std_sqlite_close_raw(void *db, const char *file, int line) {
    (void)file;
    (void)line;
    mlc_sq.close_v2(db);
    mlc_live_allocs--;
}

int64_t mlc_std_sqlite_exec_raw(void *db, mlc_sl_byte *sql, mlc_sl_byte *msg, int64_t *msg_n, const char *file, int line) {
    char *text = malloc((size_t)sql->len + 1);
    if (text == NULL) mlc_panic(file, line, "sqlite.exec: out of memory");
    memcpy(text, sql->ptr, (size_t)sql->len);
    text[sql->len] = 0;
    char *err = NULL;
    int rc = mlc_sq.exec(db, text, NULL, NULL, &err);
    free(text);
    *msg_n = 0;
    if (err != NULL) {
        int64_t n = (int64_t)strlen(err);
        if (n > msg->len) n = msg->len;
        memcpy(msg->ptr, err, (size_t)n);
        *msg_n = n;
        mlc_sq.free(err);
    }
    return rc == 0 ? 0 : MLC_SQ_ERR(rc);
}

int64_t mlc_std_sqlite_errmsg_raw(void *db, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    const char *m = mlc_sq.errmsg(db);
    int64_t n = (int64_t)strlen(m);
    if (n > dst->len) n = dst->len;
    memcpy(dst->ptr, m, (size_t)n);
    return n;
}

void *mlc_std_sqlite_prepare_raw(void *db, mlc_sl_byte *sql, int64_t *err, const char *file, int line) {
    (void)file;
    (void)line;
    sqlite3_stmt *st = NULL;
    if (sql->len > 0x7FFFFFFF) mlc_panic(file, line, "sqlite.prepare: SQL too long");
    int rc = mlc_sq.prepare_v2(db, (const char *)sql->ptr, (int)sql->len, &st, NULL);
    if (rc != 0 || st == NULL) {
        *err = rc != 0 ? MLC_SQ_ERR(rc) : 4021; /* empty SQL: SQLITE_MISUSE */
        if (st != NULL) mlc_sq.finalize(st);
        return NULL;
    }
    mlc_live_allocs++;
    *err = 0;
    return st;
}

void mlc_std_sqlite_finalize_raw(void *st, const char *file, int line) {
    (void)file;
    (void)line;
    mlc_sq.finalize(st);
    mlc_live_allocs--;
}

int64_t mlc_std_sqlite_step_raw(void *st, const char *file, int line) {
    (void)file;
    (void)line;
    int rc = mlc_sq.step(st);
    if (rc == 100 || rc == 101) return rc; /* SQLITE_ROW, SQLITE_DONE */
    return MLC_SQ_ERR(rc);
}

int64_t mlc_std_sqlite_reset_raw(void *st, const char *file, int line) {
    (void)file;
    (void)line;
    int rc = mlc_sq.reset(st);
    mlc_sq.clear_bindings(st);
    return rc == 0 ? 0 : MLC_SQ_ERR(rc);
}

static int mlc_sq_index(void *st, int64_t i, const char *what, const char *file, int line) {
    int n = mlc_sq.bind_parameter_count(st);
    if (i < 1 || i > n) {
        char msg[128];
        snprintf(msg, sizeof msg, "sqlite.%s: parameter %" PRId64 " out of 1..%d", what, i, n);
        mlc_panic(file, line, msg);
    }
    return (int)i;
}

int64_t mlc_std_sqlite_bind_int_raw(void *st, int64_t i, int64_t v, const char *file, int line) {
    int rc = mlc_sq.bind_int64(st, mlc_sq_index(st, i, "bind_int", file, line), v);
    return rc == 0 ? 0 : MLC_SQ_ERR(rc);
}

int64_t mlc_std_sqlite_bind_float_raw(void *st, int64_t i, double v, const char *file, int line) {
    int rc = mlc_sq.bind_double(st, mlc_sq_index(st, i, "bind_float", file, line), v);
    return rc == 0 ? 0 : MLC_SQ_ERR(rc);
}

int64_t mlc_std_sqlite_bind_text_raw(void *st, int64_t i, mlc_sl_byte *s, const char *file, int line) {
    int rc = mlc_sq.bind_text(st, mlc_sq_index(st, i, "bind_text", file, line), (const char *)s->ptr, (int)s->len, MLC_SQ_TRANSIENT);
    return rc == 0 ? 0 : MLC_SQ_ERR(rc);
}

int64_t mlc_std_sqlite_bind_blob_raw(void *st, int64_t i, mlc_sl_byte *s, const char *file, int line) {
    int rc = mlc_sq.bind_blob(st, mlc_sq_index(st, i, "bind_blob", file, line), s->ptr, (int)s->len, MLC_SQ_TRANSIENT);
    return rc == 0 ? 0 : MLC_SQ_ERR(rc);
}

int64_t mlc_std_sqlite_bind_null_raw(void *st, int64_t i, const char *file, int line) {
    int rc = mlc_sq.bind_null(st, mlc_sq_index(st, i, "bind_null", file, line));
    return rc == 0 ? 0 : MLC_SQ_ERR(rc);
}

static int mlc_sq_col(void *st, int64_t i, const char *file, int line) {
    int n = mlc_sq.column_count(st);
    if (i < 0 || i >= n) {
        char msg[128];
        snprintf(msg, sizeof msg, "sqlite: column %" PRId64 " out of 0..%d", i, n - 1);
        mlc_panic(file, line, msg);
    }
    return (int)i;
}

int64_t mlc_std_sqlite_column_count_raw(void *st, const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_sq.column_count(st);
}

int64_t mlc_std_sqlite_column_type_raw(void *st, int64_t i, const char *file, int line) {
    return mlc_sq.column_type(st, mlc_sq_col(st, i, file, line));
}

int64_t mlc_std_sqlite_column_int_raw(void *st, int64_t i, const char *file, int line) {
    return mlc_sq.column_int64(st, mlc_sq_col(st, i, file, line));
}

double mlc_std_sqlite_column_float_raw(void *st, int64_t i, const char *file, int line) {
    return mlc_sq.column_double(st, mlc_sq_col(st, i, file, line));
}

int64_t mlc_std_sqlite_column_text_raw(void *st, int64_t i, mlc_sl_byte *dst, const char *file, int line) {
    int c = mlc_sq_col(st, i, file, line);
    const unsigned char *t = mlc_sq.column_text(st, c); /* before column_bytes, as SQLite asks */
    int n = mlc_sq.column_bytes(st, c);
    if (t == NULL) return 0;
    return mlc_sq_copy(dst, t, n);
}

int64_t mlc_std_sqlite_column_blob_raw(void *st, int64_t i, mlc_sl_byte *dst, const char *file, int line) {
    int c = mlc_sq_col(st, i, file, line);
    const void *b = mlc_sq.column_blob(st, c);
    int n = mlc_sq.column_bytes(st, c);
    if (b == NULL) return 0;
    return mlc_sq_copy(dst, b, n);
}

int64_t mlc_std_sqlite_column_name_raw(void *st, int64_t i, mlc_sl_byte *dst, const char *file, int line) {
    const char *name = mlc_sq.column_name(st, mlc_sq_col(st, i, file, line));
    if (name == NULL) return 0;
    int64_t n = (int64_t)strlen(name);
    return mlc_sq_copy(dst, name, n);
}

int64_t mlc_std_sqlite_changes_raw(void *db, const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_sq.changes64 != NULL ? mlc_sq.changes64(db) : mlc_sq.changes(db);
}

int64_t mlc_std_sqlite_last_insert_id_raw(void *db, const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_sq.last_insert_rowid(db);
}

void mlc_std_sqlite_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

/* std/tls. libssl (OpenSSL 3, or 1.1) is loaded on the first call,
 * like SQLite: only whoever uses it needs it, and nobody needs flags.
 * MLC_TLS_LIB picks the file; without it, it tries Homebrew's OpenSSL on
 * macOS (macOS's /usr/lib/libssl.dylib aborts the process on purpose,
 * and is never loaded) and the system's on Linux. The client checks the
 * certificate (chain, validity and host name) against the system's
 * authorities, or the MLC_TLS_CA file; TLS 1.2 at least. */

typedef struct ssl_st SSL;
typedef struct ssl_ctx_st SSL_CTX;
typedef struct ssl_method_st SSL_METHOD;

static struct {
    int (*init_ssl)(uint64_t, const void *);
    const SSL_METHOD *(*client_method)(void);
    const SSL_METHOD *(*server_method)(void);
    SSL_CTX *(*ctx_new)(const SSL_METHOD *);
    void (*ctx_free)(SSL_CTX *);
    long (*ctx_ctrl)(SSL_CTX *, int, long, void *);
    int (*ctx_default_paths)(SSL_CTX *);
    int (*ctx_load_locations)(SSL_CTX *, const char *, const char *);
    void (*ctx_set_verify)(SSL_CTX *, int, void *);
    int (*ctx_use_chain)(SSL_CTX *, const char *);
    int (*ctx_use_key)(SSL_CTX *, const char *, int);
    int (*ctx_check_key)(const SSL_CTX *);
    SSL *(*ssl_new)(SSL_CTX *);
    void (*ssl_free)(SSL *);
    int (*set_fd)(SSL *, int);
    long (*ctrl)(SSL *, int, long, void *);
    int (*set1_host)(SSL *, const char *);
    int (*connect)(SSL *);
    int (*accept)(SSL *);
    int (*read)(SSL *, void *, int);
    int (*write)(SSL *, const void *, int);
    int (*get_error)(const SSL *, int);
    long (*verify_result)(const SSL *);
    int (*shutdown)(SSL *);
    const char *(*verify_string)(long);
    unsigned long (*err_get)(void);
    void (*err_string)(unsigned long, char *, size_t);
    void (*err_clear)(void);
    int loaded;
    SSL_CTX *client; /* the client's context, a single one for the program */
} mlc_tl;

static pthread_once_t mlc_tl_once = PTHREAD_ONCE_INIT;
static _Thread_local char mlc_tl_msg[256]; /* the explanation of this thread's last error */

static void mlc_tl_load(void) {
    const char *names[] = {getenv("MLC_TLS_LIB"),
                           "/opt/homebrew/opt/openssl@3/lib/libssl.3.dylib",
                           "/usr/local/opt/openssl@3/lib/libssl.3.dylib",
                           "/opt/homebrew/opt/openssl@1.1/lib/libssl.1.1.dylib",
#ifndef __APPLE__
                           "libssl.so.3", "libssl.so.1.1", "libssl.so",
#endif
                           NULL};
    void *h = NULL;
    for (int i = 0; i < (int)(sizeof names / sizeof names[0]) && h == NULL; i++) {
        if (names[i] != NULL && names[i][0] != 0) h = dlopen(names[i], RTLD_NOW | RTLD_LOCAL);
    }
    mlc_tl.loaded = -1;
    if (h == NULL) return;
#define MLC_TL(field, sym)                    \
    *(void **)&mlc_tl.field = dlsym(h, sym);  \
    if (mlc_tl.field == NULL) return
    MLC_TL(init_ssl, "OPENSSL_init_ssl");
    MLC_TL(client_method, "TLS_client_method");
    MLC_TL(server_method, "TLS_server_method");
    MLC_TL(ctx_new, "SSL_CTX_new");
    MLC_TL(ctx_free, "SSL_CTX_free");
    MLC_TL(ctx_ctrl, "SSL_CTX_ctrl");
    MLC_TL(ctx_default_paths, "SSL_CTX_set_default_verify_paths");
    MLC_TL(ctx_load_locations, "SSL_CTX_load_verify_locations");
    MLC_TL(ctx_set_verify, "SSL_CTX_set_verify");
    MLC_TL(ctx_use_chain, "SSL_CTX_use_certificate_chain_file");
    MLC_TL(ctx_use_key, "SSL_CTX_use_PrivateKey_file");
    MLC_TL(ctx_check_key, "SSL_CTX_check_private_key");
    MLC_TL(ssl_new, "SSL_new");
    MLC_TL(ssl_free, "SSL_free");
    MLC_TL(set_fd, "SSL_set_fd");
    MLC_TL(ctrl, "SSL_ctrl");
    MLC_TL(set1_host, "SSL_set1_host");
    MLC_TL(connect, "SSL_connect");
    MLC_TL(accept, "SSL_accept");
    MLC_TL(read, "SSL_read");
    MLC_TL(write, "SSL_write");
    MLC_TL(get_error, "SSL_get_error");
    MLC_TL(verify_result, "SSL_get_verify_result");
    MLC_TL(shutdown, "SSL_shutdown");
    MLC_TL(verify_string, "X509_verify_cert_error_string"); /* from libcrypto, which libssl loads */
    MLC_TL(err_get, "ERR_get_error");
    MLC_TL(err_string, "ERR_error_string_n");
    MLC_TL(err_clear, "ERR_clear_error");
#undef MLC_TL
    mlc_tl.init_ssl(0, NULL);
    mlc_tl.loaded = 1;
}

static int mlc_tl_ready(void) {
    pthread_once(&mlc_tl_once, mlc_tl_load);
    return mlc_tl.loaded == 1;
}

/* the control codes of OpenSSL's macros */
#define MLC_TL_CTRL_SET_MIN_PROTO 123
#define MLC_TL_TLS1_2 0x0303
#define MLC_TL_CTRL_SNI 55

static void mlc_tl_note(const char *what) {
    unsigned long e = mlc_tl.err_get();
    if (e != 0) {
        char buf[200];
        mlc_tl.err_string(e, buf, sizeof buf);
        snprintf(mlc_tl_msg, sizeof mlc_tl_msg, "%s: %s", what, buf);
    } else {
        snprintf(mlc_tl_msg, sizeof mlc_tl_msg, "%s", what);
    }
    mlc_tl.err_clear();
}

static pthread_mutex_t mlc_tl_mu = PTHREAD_MUTEX_INITIALIZER;

/* the client's context: the system's authorities (or MLC_TLS_CA),
 * mandatory checking, TLS 1.2 at least */
static SSL_CTX *mlc_tl_client(void) {
    pthread_mutex_lock(&mlc_tl_mu);
    if (mlc_tl.client == NULL) {
        SSL_CTX *ctx = mlc_tl.ctx_new(mlc_tl.client_method());
        if (ctx != NULL) {
            mlc_tl.ctx_ctrl(ctx, MLC_TL_CTRL_SET_MIN_PROTO, MLC_TL_TLS1_2, NULL);
            const char *ca = getenv("MLC_TLS_CA");
            if (ca != NULL && ca[0] != 0) mlc_tl.ctx_load_locations(ctx, ca, NULL);
            else mlc_tl.ctx_default_paths(ctx);
            mlc_tl.ctx_set_verify(ctx, 1 /* SSL_VERIFY_PEER */, NULL);
            mlc_tl.client = ctx;
        }
    }
    pthread_mutex_unlock(&mlc_tl_mu);
    return mlc_tl.client;
}

typedef struct mlc_tls {
    int fd;
    SSL *ssl;
} mlc_tls;

/* the errors: 5001 the handshake failed, 5002 the certificate isn't valid,
 * 5003 no TLS library, 5004 error in the TLS connection, 5005 the
 * server's certificate or key didn't load */

mlc_tls *mlc_std_tls_connect(mlc_sl_byte *host, int64_t port, int64_t *err, const char *file, int line) {
    if (!mlc_tl_ready()) {
        snprintf(mlc_tl_msg, sizeof mlc_tl_msg, "libssl (OpenSSL) wasn't found; install OpenSSL or point MLC_TLS_LIB to it");
        *err = 5003;
        return NULL;
    }
    SSL_CTX *ctx = mlc_tl_client();
    if (ctx == NULL) {
        mlc_tl_note("couldn't create the TLS context");
        *err = 5001;
        return NULL;
    }
    char name[256];
    if (host->len >= (int64_t)sizeof name) mlc_panic(file, line, "tls.connect: host name too long");
    memcpy(name, host->ptr, (size_t)host->len);
    name[host->len] = 0;
    int fd = mlc_tcp_dial(host, port, err);
    if (fd < 0) {
        snprintf(mlc_tl_msg, sizeof mlc_tl_msg, "the TCP connection failed");
        return NULL;
    }
    SSL *ssl = mlc_tl.ssl_new(ctx);
    mlc_tl.set_fd(ssl, fd);
    mlc_tl.ctrl(ssl, MLC_TL_CTRL_SNI, 0 /* TLSEXT_NAMETYPE_host_name */, name);
    mlc_tl.set1_host(ssl, name); /* the certificate must be for this name */
    if (mlc_tl.connect(ssl) != 1) {
        long v = mlc_tl.verify_result(ssl);
        if (v != 0) {
            snprintf(mlc_tl_msg, sizeof mlc_tl_msg, "certificate refused: %s", mlc_tl.verify_string(v));
            mlc_tl.err_clear();
            *err = 5002;
        } else {
            mlc_tl_note("the TLS handshake failed");
            *err = 5001;
        }
        mlc_tl.ssl_free(ssl);
        close(fd);
        return NULL;
    }
    mlc_tls *t = mlc_alloc(sizeof *t, file, line);
    t->fd = fd;
    t->ssl = ssl;
    *err = 0;
    return t;
}

/* the server: a context with the certificate (the chain, in PEM) and the key */
void *mlc_std_tls_server_raw(mlc_sl_byte *cert, mlc_sl_byte *key, int64_t *err, const char *file, int line) {
    if (!mlc_tl_ready()) {
        snprintf(mlc_tl_msg, sizeof mlc_tl_msg, "libssl (OpenSSL) wasn't found; install OpenSSL or point MLC_TLS_LIB to it");
        *err = 5003;
        return NULL;
    }
    char c[4096], k[4096];
    if (!mlc_cpath(c, sizeof c, cert) || !mlc_cpath(k, sizeof k, key)) mlc_panic(file, line, "tls.new_server: path too long");
    SSL_CTX *ctx = mlc_tl.ctx_new(mlc_tl.server_method());
    if (ctx == NULL) {
        mlc_tl_note("couldn't create the TLS context");
        *err = 5005;
        return NULL;
    }
    mlc_tl.ctx_ctrl(ctx, MLC_TL_CTRL_SET_MIN_PROTO, MLC_TL_TLS1_2, NULL);
    if (mlc_tl.ctx_use_chain(ctx, c) != 1) {
        mlc_tl_note("the certificate didn't load");
        mlc_tl.ctx_free(ctx);
        *err = 5005;
        return NULL;
    }
    if (mlc_tl.ctx_use_key(ctx, k, 1 /* SSL_FILETYPE_PEM */) != 1 || mlc_tl.ctx_check_key(ctx) != 1) {
        mlc_tl_note("the key didn't load (or isn't the certificate's)");
        mlc_tl.ctx_free(ctx);
        *err = 5005;
        return NULL;
    }
    mlc_live_allocs++; /* the context counts as an allocation: close discounts it */
    *err = 0;
    return ctx;
}

void mlc_std_tls_server_close(void *ctx, const char *file, int line) {
    (void)file;
    (void)line;
    mlc_tl.ctx_free(ctx);
    mlc_live_allocs--;
}

/* accept does the server handshake on an accepted connection (which now
 * belongs to TLS: on error, it's closed here) */
mlc_tls *mlc_std_tls_accept_raw(mlc_sock *c, void *ctx, int64_t *err, const char *file, int line) {
    int fd = c->fd;
    mlc_free(c);
    SSL *ssl = mlc_tl.ssl_new(ctx);
    mlc_tl.set_fd(ssl, fd);
    if (mlc_tl.accept(ssl) != 1) {
        mlc_tl_note("the TLS handshake failed");
        mlc_tl.ssl_free(ssl);
        close(fd);
        *err = 5001;
        return NULL;
    }
    mlc_tls *t = mlc_alloc(sizeof *t, file, line);
    t->fd = fd;
    t->ssl = ssl;
    *err = 0;
    return t;
}

int64_t mlc_std_tls_read_raw(mlc_tls *t, mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    if (dst->len == 0) return 0;
    int n = dst->len > INT32_MAX ? INT32_MAX : (int)dst->len;
    for (;;) {
        int r = mlc_tl.read(t->ssl, dst->ptr, n);
        if (r > 0) return r;
        int e = mlc_tl.get_error(t->ssl, r);
        if (e == 6 /* SSL_ERROR_ZERO_RETURN: the other side closed */) return 0;
        if (e == 2 /* WANT_READ */ || e == 3 /* WANT_WRITE */) continue;
        if (e == 5 /* SSL_ERROR_SYSCALL */) {
            if (errno == EINTR) continue;
            if (errno == 0) return 0; /* closed without TLS's notice */
            return -mlc_net_err(errno);
        }
        mlc_tl_note("error in the TLS connection");
        return -5004;
    }
}

int64_t mlc_std_tls_write_raw(mlc_tls *t, mlc_sl_byte *data, const char *file, int line) {
    (void)file;
    (void)line;
    int64_t done = 0;
    while (done < data->len) {
        int64_t left = data->len - done;
        int n = left > INT32_MAX ? INT32_MAX : (int)left;
        int r = mlc_tl.write(t->ssl, data->ptr + done, n);
        if (r > 0) {
            done += r;
            continue;
        }
        int e = mlc_tl.get_error(t->ssl, r);
        if (e == 2 || e == 3) continue;
        if (e == 5 && errno == EINTR) continue;
        if (e == 5 && errno != 0) return -mlc_net_err(errno);
        mlc_tl_note("error in the TLS connection");
        return -5004;
    }
    return done;
}

int64_t mlc_std_tls_timeout_raw(mlc_tls *t, int64_t ms, const char *file, int line) {
    (void)file;
    (void)line;
    struct timeval tv = {(time_t)(ms / 1000), (suseconds_t)((ms % 1000) * 1000)};
    if (setsockopt(t->fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv) != 0) return -errno;
    if (setsockopt(t->fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv) != 0) return -errno;
    return 0;
}

void mlc_std_tls_close_raw(mlc_tls *t, const char *file, int line) {
    (void)file;
    (void)line;
    mlc_tl.shutdown(t->ssl); /* the end notice to the other side (without waiting for the answer) */
    mlc_tl.ssl_free(t->ssl);
    close(t->fd);
    mlc_free(t);
}

int64_t mlc_std_tls_last_error(mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    int64_t n = (int64_t)strlen(mlc_tl_msg);
    if (n > dst->len) n = dst->len;
    memcpy(dst->ptr, mlc_tl_msg, (size_t)n);
    return n;
}

bool mlc_std_tls_available(const char *file, int line) {
    (void)file;
    (void)line;
    return mlc_tl_ready();
}

void mlc_std_tls_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }

#endif /* !MLC_EMBEDDED */

#ifdef MLC_EMBEDDED
/* std/tls on the ESP32: there's no OpenSSL to load, so it behaves as on a
 * desktop without libssl (tls.available() is false and connect returns
 * tls.UNAVAILABLE). Only std/http reaches it, for HTTPS: a program's own
 * import "std/tls" is refused by mlc build -target esp32. The connections
 * and servers are never created, so the functions that take one aren't
 * called. */

static const char mlc_tl_none[] = "TLS isn't available with -target esp32";

void *mlc_std_tls_connect(mlc_sl_byte *host, int64_t port, int64_t *err, const char *file, int line) {
    (void)host;
    (void)port;
    (void)file;
    (void)line;
    *err = 5003; /* tls.UNAVAILABLE */
    return NULL;
}

void *mlc_std_tls_server_raw(mlc_sl_byte *cert, mlc_sl_byte *key, int64_t *err, const char *file, int line) {
    (void)cert;
    (void)key;
    (void)file;
    (void)line;
    *err = 5003;
    return NULL;
}

void *mlc_std_tls_accept_raw(mlc_sock *c, void *ctx, int64_t *err, const char *file, int line) {
    (void)ctx;
    mlc_std_net_close_raw(c, file, line); /* accept takes the connection */
    *err = 5003;
    return NULL;
}

static _Noreturn void mlc_tl_never(const char *file, int line) {
    mlc_panic(file, line, "internal error: a TLS connection with -target esp32");
}

void mlc_std_tls_server_close(void *ctx, const char *file, int line) {
    (void)ctx;
    mlc_tl_never(file, line);
}

int64_t mlc_std_tls_read_raw(void *t, mlc_sl_byte *dst, const char *file, int line) {
    (void)t;
    (void)dst;
    mlc_tl_never(file, line);
}

int64_t mlc_std_tls_write_raw(void *t, mlc_sl_byte *data, const char *file, int line) {
    (void)t;
    (void)data;
    mlc_tl_never(file, line);
}

int64_t mlc_std_tls_timeout_raw(void *t, int64_t ms, const char *file, int line) {
    (void)t;
    (void)ms;
    mlc_tl_never(file, line);
}

void mlc_std_tls_close_raw(void *t, const char *file, int line) {
    (void)t;
    mlc_tl_never(file, line);
}

int64_t mlc_std_tls_last_error(mlc_sl_byte *dst, const char *file, int line) {
    (void)file;
    (void)line;
    int64_t n = (int64_t)(sizeof mlc_tl_none - 1);
    if (n > dst->len) n = dst->len;
    memcpy(dst->ptr, mlc_tl_none, (size_t)n);
    return n;
}

bool mlc_std_tls_available(const char *file, int line) {
    (void)file;
    (void)line;
    return false;
}

void mlc_std_tls_fail(mlc_str msg, const char *file, int line) { mlc_std_process_fail(msg, file, line); }
#endif /* MLC_EMBEDDED */

#ifndef MLC_EMBEDDED
/* std/wifi on a desktop: the system manages the network, so there's nothing
 * to control (on the ESP32 it's rt/c/esp32/mlc_wifi.c). */
bool mlc_std_wifi_available(const char *file, int line) {
    (void)file;
    (void)line;
    return false;
}

bool mlc_std_wifi_connect(mlc_sl_byte *ssid, mlc_sl_byte *password, int64_t timeout_ms, int64_t *err, const char *file,
                          int line) {
    (void)ssid;
    (void)password;
    (void)timeout_ms;
    (void)file;
    (void)line;
    *err = 6004; /* wifi.UNAVAILABLE */
    return false;
}

bool mlc_std_wifi_connected(const char *file, int line) {
    (void)file;
    (void)line;
    return false;
}

int64_t mlc_std_wifi_ip(mlc_sl_byte *dst, const char *file, int line) {
    (void)dst;
    (void)file;
    (void)line;
    return 0;
}

int64_t mlc_std_wifi_rssi(const char *file, int line) {
    (void)file;
    (void)line;
    return 0;
}

void mlc_std_wifi_disconnect(const char *file, int line) {
    (void)file;
    (void)line;
}
#endif /* !MLC_EMBEDDED */
