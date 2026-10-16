#include "mlc_runtime.h"

#include <inttypes.h>
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
