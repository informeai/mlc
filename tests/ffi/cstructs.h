/* Header for TestFFICStructs: structs with what makes real drivers' ones
 * hard to copy (an anonymous union, bitfields in a nested anonymous struct,
 * a field MLC doesn't declare, a size_t, a const char *), a callback with
 * its context in a struct, a struct by value and constants. */
#ifndef MLC_TEST_CSTRUCTS_H
#define MLC_TEST_CSTRUCTS_H
#include <stddef.h>
#include <stdint.h>

#define CS_MODE_FAST 7
enum cs_color { CS_RED = 1, CS_GREEN = 2, CS_BLUE = 4 };

typedef struct {
    int port;
    union {
        int clock;
        float clock_f;
    };
    uint8_t glitch;
    size_t depth;
    struct {
        uint32_t pullup : 1;
        uint32_t sleep : 1;
    } flags;
    int hidden; /* not declared in MLC: it must stay zero */
    const char *name;
} cs_config_t;

typedef struct {
    int32_t x, y;
} cs_point_t;

typedef struct {
    void (*callback)(void *arg);
    void *arg;
    const char *name;
    int32_t times;
} cs_timer_args_t;

/* 0 if the configuration is the one the test writes, or the number of the
 * first check that failed */
int32_t cs_check(const cs_config_t *c);
cs_point_t cs_add(cs_point_t a, cs_point_t b);
/* copies the arguments (as esp_timer does) and calls the callback times
 * times from a thread of its own, after the call */
int32_t cs_timer_start(const cs_timer_args_t *a);
void cs_timer_wait(void);
int32_t cs_timer_name_is(const char *expected);
/* calls f(ctx, &p) for three points */
void cs_each_point(void (*f)(void *ctx, const cs_point_t *p), void *ctx);
#endif
