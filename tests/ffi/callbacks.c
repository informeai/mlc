/* C library for TestFFICallbacks: calls MLC back during a call, keeps a
 * callback and calls it later from threads of its own (with the stack it
 * chooses), and adds up what the callbacks returned. */
#include <limits.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>

/* synchronous: calls f(ctx, i) for i from 0 to n-1, during the call */
int32_t cb_each(int32_t n, void (*f)(void *, int32_t), void *ctx) {
    for (int32_t i = 0; i < n; i++) f(ctx, i);
    return n;
}

/* kept: the callback (with its context, or without one) stays here */
static int32_t (*kept_f)(void *, int32_t);
static int32_t (*kept_plain)(int32_t);
static void *kept_ctx;
static int64_t returned;

void cb_keep(int32_t (*f)(void *, int32_t), void *ctx) {
    kept_f = f;
    kept_ctx = ctx;
}

void cb_keep_plain(int32_t (*f)(int32_t)) { kept_plain = f; }

struct job {
    int32_t from, n;
};

static void *run(void *p) {
    struct job *j = p;
    for (int32_t i = 0; i < j->n; i++) {
        returned += kept_f != NULL ? kept_f(kept_ctx, j->from + i) : kept_plain(j->from + i);
    }
    return NULL;
}

static void in_thread(int32_t from, int32_t n, int32_t stack_kb) {
    struct job j = {from, n};
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    size_t stack = (size_t)stack_kb * 1024;
    if (stack < PTHREAD_STACK_MIN) stack = PTHREAD_STACK_MIN;
    pthread_attr_setstacksize(&attr, stack);
    pthread_t th;
    if (pthread_create(&th, &attr, run, &j) != 0) abort();
    pthread_attr_destroy(&attr);
    pthread_join(th, NULL);
}

/* calls the kept callback n times (with from, from+1...) from a new thread
 * with stack_kb KB of stack, and waits for it */
void cb_fire(int32_t from, int32_t n, int32_t stack_kb) { in_thread(from, n, stack_kb); }

/* the same from k new threads in a row, each calling once (from+i) */
void cb_fire_threads(int32_t from, int32_t k) {
    for (int32_t i = 0; i < k; i++) in_thread(from + i, 1, 256);
}

int32_t cb_returned(void) { return (int32_t)returned; }
