/* Biblioteca de C para TestFFIOutHandle: devolve handles por ponteiro
 * (counter **out), como sqlite3_open, e conta os handles abertos. */
#include <stdlib.h>

typedef struct counter {
    int n;
} counter;

static int live = 0;

int counter_open(int start, counter **out) {
    if (start < 0) {
        *out = NULL;
        return -1;
    }
    *out = malloc(sizeof **out);
    (*out)->n = start;
    live++;
    return 0;
}

/* Abre dois de uma vez. */
int counter_pair(counter **a, counter **b) {
    return counter_open(10, a) + counter_open(20, b);
}

int counter_next(counter *c) { return ++c->n; }

void counter_close(counter *c) {
    free(c);
    live--;
}

int counter_live(void) { return live; }

/* Parâmetros que aceitam NULL (?&T no MLC). */

/* Escreve o valor em out, se out não for NULL; diz se escreveu. */
int counter_peek(counter *c, int *out) {
    if (out == NULL) return 0;
    *out = c->n;
    return 1;
}

/* Soma n inteiros; com data NULL, a soma é 0. */
long counter_sum(const long *data, long n) {
    if (data == NULL) return 0;
    long s = 0;
    for (long i = 0; i < n; i++) s += data[i];
    return s;
}

/* Abre um contador em *out, se out não for NULL (senão só devolve -2). */
int counter_maybe_open(int start, counter **out) {
    if (out == NULL) return -2;
    return counter_open(start, out);
}
