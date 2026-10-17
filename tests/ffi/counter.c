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
