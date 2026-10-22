/* C library for TestFFICStructs (see cstructs.h). */
#include "cstructs.h"

#include <pthread.h>
#include <string.h>

int32_t cs_check(const cs_config_t *c) {
    if (c->port != 3) return 1;
    if (c->clock != 400000) return 2;
    if (c->glitch != 7) return 3;
    if (c->depth != 16) return 4;
    if (c->flags.pullup != 1 || c->flags.sleep != 0) return 5;
    if (c->hidden != 0) return 6;
    if (c->name == NULL || strcmp(c->name, "bus0") != 0) return 7;
    return 0;
}

cs_point_t cs_add(cs_point_t a, cs_point_t b) {
    cs_point_t r = {a.x + b.x, a.y + b.y};
    return r;
}

static cs_timer_args_t timer; /* the copy, as esp_timer keeps it */
static pthread_t timer_thread;

static void *run(void *p) {
    (void)p;
    for (int32_t i = 0; i < timer.times; i++) timer.callback(timer.arg);
    return NULL;
}

int32_t cs_timer_start(const cs_timer_args_t *a) {
    timer = *a;
    return pthread_create(&timer_thread, NULL, run, NULL) == 0 ? 0 : -1;
}

void cs_timer_wait(void) { pthread_join(timer_thread, NULL); }

int32_t cs_timer_name_is(const char *expected) { return timer.name != NULL && strcmp(timer.name, expected) == 0; }

void cs_each_point(void (*f)(void *ctx, const cs_point_t *p), void *ctx) {
    cs_point_t pts[3] = {{1, 2}, {3, 4}, {5, 6}};
    for (int i = 0; i < 3; i++) f(ctx, &pts[i]);
}
