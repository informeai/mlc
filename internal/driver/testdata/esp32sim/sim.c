/* ESP-IDF simulation for the tests: each FreeRTOS task is a pthread with the
 * asked stack, and main plays the role of the ESP-IDF startup (calls app_main;
 * the process ends when the last task does). */
#define _DARWIN_C_SOURCE
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>

#include "esp_random.h"
#include "esp_rom_sys.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

void app_main(void);

struct start {
    TaskFunction_t fn;
    void *arg;
};

static void *run(void *p) {
    struct start s = *(struct start *)p;
    free(p);
    s.fn(s.arg);
    return NULL;
}

BaseType_t xTaskCreate(TaskFunction_t fn, const char *name, uint32_t stack_bytes, void *arg, UBaseType_t prio, TaskHandle_t *out) {
    (void)name;
    (void)prio;
    (void)out;
    struct start *s = malloc(sizeof *s);
    s->fn = fn;
    s->arg = arg;
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    pthread_attr_setstacksize(&attr, (stack_bytes + 16383) / 16384 * 16384);
    pthread_t th;
    int err = pthread_create(&th, &attr, run, s);
    pthread_attr_destroy(&attr);
    return err == 0 ? pdPASS : 0;
}

void vTaskDelete(TaskHandle_t t) {
    (void)t;
    pthread_exit(NULL);
}

uint8_t *pxTaskGetStackStart(TaskHandle_t t) {
    (void)t;
    pthread_t self = pthread_self();
#if defined(__APPLE__)
    return (uint8_t *)pthread_get_stackaddr_np(self) - pthread_get_stacksize_np(self);
#else
    pthread_attr_t attr;
    void *addr = NULL;
    size_t n = 0;
    pthread_getattr_np(self, &attr);
    pthread_attr_getstack(&attr, &addr, &n);
    pthread_attr_destroy(&attr);
    return addr;
#endif
}

void esp_fill_random(void *buf, size_t len) {
    arc4random_buf(buf, len);
}

static _Thread_local BaseType_t in_isr;

BaseType_t xPortInIsrContext(void) { return in_isr; }

/* sim_isr_call calls fn as an interrupt handler would (TestESP32Callbacks) */
void sim_isr_call(void (*fn)(void)) {
    in_isr = 1;
    fn();
    in_isr = 0;
}

int esp_rom_printf(const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    int n = vfprintf(stderr, fmt, ap);
    va_end(ap);
    return n;
}

int main(void) {
    app_main();
    pthread_exit(NULL); /* like ESP-IDF's startup: the program goes on in its tasks */
}
