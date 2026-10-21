/* C library for TestFFIOutHandle: returns handles through a pointer
 * (counter **out), like sqlite3_open, and counts the open handles. */
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

/* Opens two at once. */
int counter_pair(counter **a, counter **b) {
    return counter_open(10, a) + counter_open(20, b);
}

int counter_next(counter *c) { return ++c->n; }

void counter_close(counter *c) {
    free(c);
    live--;
}

int counter_live(void) { return live; }

/* Parameters that accept NULL (?&T in MLC). */

/* Writes the value into out, if out isn't NULL; says whether it wrote. */
int counter_peek(counter *c, int *out) {
    if (out == NULL) return 0;
    *out = c->n;
    return 1;
}

/* Adds n integers; with a NULL data, the sum is 0. */
long counter_sum(const long *data, long n) {
    if (data == NULL) return 0;
    long s = 0;
    for (long i = 0; i < n; i++) s += data[i];
    return s;
}

/* Opens a counter in *out, if out isn't NULL (otherwise it only returns -2). */
int counter_maybe_open(int start, counter **out) {
    if (out == NULL) return -2;
    return counter_open(start, out);
}
