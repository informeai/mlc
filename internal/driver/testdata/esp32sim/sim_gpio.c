/* The ESP32's pins, FreeRTOS's queues and esp_timer in the simulation
 * (TestESP32Callbacks, tests/esp32/gpio.mlc). A pin in INPUT_OUTPUT mode with
 * an interrupt and a handler fires it when gpio_set_level changes its level:
 * the handler runs right there, in a simulated interrupt (sim_isr_run), as
 * on the chip, where the interrupt preempts the task that wrote the pin. A
 * misuse (a queue received from in an interrupt or sent to from a task, a
 * handler without the ISR service) is written to stderr with "sim:", which
 * fails the test. */
#define _DARWIN_C_SOURCE
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "driver/gpio.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"

void sim_isr_run(void (*fn)(void *), void *arg);

static void misuse(const char *what) { fprintf(stderr, "sim: %s\n", what); }

int64_t esp_timer_get_time(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000 + ts.tv_nsec / 1000;
}

/* ---------- queues ---------- */

struct sim_queue {
    pthread_mutex_t mu;
    pthread_cond_t cv;
    size_t len, item, head, count;
    unsigned char *buf;
};

QueueHandle_t xQueueCreate(UBaseType_t len, UBaseType_t item) {
    QueueHandle_t q = calloc(1, sizeof *q);
    pthread_mutex_init(&q->mu, NULL);
    pthread_cond_init(&q->cv, NULL);
    q->len = len;
    q->item = item;
    q->buf = calloc(len, item);
    return q;
}

void vQueueDelete(QueueHandle_t q) {
    pthread_mutex_destroy(&q->mu);
    pthread_cond_destroy(&q->cv);
    free(q->buf);
    free(q);
}

BaseType_t xQueueSendFromISR(QueueHandle_t q, const void *item, BaseType_t *woken) {
    if (!xPortInIsrContext()) misuse("xQueueSendFromISR outside an interrupt");
    pthread_mutex_lock(&q->mu);
    BaseType_t ok = q->count < q->len;
    if (ok) {
        memcpy(q->buf + ((q->head + q->count) % q->len) * q->item, item, q->item);
        q->count++;
        pthread_cond_signal(&q->cv);
        *woken = pdTRUE;
    }
    pthread_mutex_unlock(&q->mu);
    return ok ? pdTRUE : pdFALSE;
}

BaseType_t xQueueReceive(QueueHandle_t q, void *item, TickType_t ticks) {
    if (xPortInIsrContext()) misuse("xQueueReceive in an interrupt");
    struct timespec until;
    clock_gettime(CLOCK_REALTIME, &until);
    if (ticks != portMAX_DELAY) {
        until.tv_sec += ticks / 1000;
        until.tv_nsec += (long)(ticks % 1000) * 1000000;
        if (until.tv_nsec >= 1000000000) {
            until.tv_sec++;
            until.tv_nsec -= 1000000000;
        }
    }
    pthread_mutex_lock(&q->mu);
    while (q->count == 0) {
        if (ticks == portMAX_DELAY) {
            pthread_cond_wait(&q->cv, &q->mu);
        } else if (pthread_cond_timedwait(&q->cv, &q->mu, &until) == ETIMEDOUT) {
            break;
        }
    }
    BaseType_t ok = q->count > 0;
    if (ok) {
        memcpy(item, q->buf + q->head * q->item, q->item);
        q->head = (q->head + 1) % q->len;
        q->count--;
    }
    pthread_mutex_unlock(&q->mu);
    return ok ? pdTRUE : pdFALSE;
}

/* ---------- pins ---------- */

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static struct {
    gpio_mode_t mode;
    gpio_int_type_t intr;
    int level;
    gpio_isr_t h;
    void *arg;
} pins[40];
static bool service;

esp_err_t gpio_config(const gpio_config_t *c) {
    pthread_mutex_lock(&mu);
    for (int n = 0; n < 40; n++) {
        if (!(c->pin_bit_mask & (1ULL << n))) continue;
        if (!GPIO_IS_VALID_GPIO(n) || ((c->mode & GPIO_MODE_OUTPUT) && !GPIO_IS_VALID_OUTPUT_GPIO(n))) {
            pthread_mutex_unlock(&mu);
            return ESP_ERR_INVALID_ARG;
        }
        pins[n].mode = c->mode;
        pins[n].intr = c->intr_type;
        if (!(c->mode & GPIO_MODE_OUTPUT)) pins[n].level = c->pull_up_en ? 1 : 0; /* an input rests at its pull */
    }
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t gpio_set_level(gpio_num_t pin, uint32_t level) {
    if (!GPIO_IS_VALID_GPIO(pin)) return ESP_ERR_INVALID_ARG;
    pthread_mutex_lock(&mu);
    if (!(pins[pin].mode & GPIO_MODE_OUTPUT)) misuse("gpio_set_level on a pin that isn't an output");
    int old = pins[pin].level, now = level != 0;
    pins[pin].level = now;
    gpio_int_type_t intr = (pins[pin].mode & GPIO_MODE_INPUT) ? pins[pin].intr : GPIO_INTR_DISABLE;
    gpio_isr_t h = pins[pin].h;
    void *arg = pins[pin].arg;
    pthread_mutex_unlock(&mu);
    bool fire = old != now && h != NULL &&
                (intr == GPIO_INTR_ANYEDGE || (intr == GPIO_INTR_POSEDGE && now) || (intr == GPIO_INTR_NEGEDGE && !now));
    if (fire) sim_isr_run(h, arg);
    return ESP_OK;
}

int gpio_get_level(gpio_num_t pin) {
    if (!GPIO_IS_VALID_GPIO(pin)) return 0;
    pthread_mutex_lock(&mu);
    int l = pins[pin].level;
    pthread_mutex_unlock(&mu);
    return l;
}

esp_err_t gpio_intr_disable(gpio_num_t pin) {
    pthread_mutex_lock(&mu);
    pins[pin].intr = GPIO_INTR_DISABLE;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t gpio_install_isr_service(int flags) {
    (void)flags;
    pthread_mutex_lock(&mu);
    bool had = service;
    service = true;
    pthread_mutex_unlock(&mu);
    return had ? ESP_ERR_INVALID_STATE : ESP_OK;
}

esp_err_t gpio_isr_handler_add(gpio_num_t pin, gpio_isr_t h, void *arg) {
    pthread_mutex_lock(&mu);
    if (!service) misuse("gpio_isr_handler_add without gpio_install_isr_service");
    if (pins[pin].h != NULL) misuse("gpio_isr_handler_add on a pin that has a handler");
    pins[pin].h = h;
    pins[pin].arg = arg;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t gpio_isr_handler_remove(gpio_num_t pin) {
    pthread_mutex_lock(&mu);
    if (pins[pin].h == NULL) misuse("gpio_isr_handler_remove on a pin without a handler");
    pins[pin].h = NULL;
    pins[pin].arg = NULL;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}
