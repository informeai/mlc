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

/* ---------- esp_timer ---------- */

#include <unistd.h>

#include "driver/ledc.h"

struct esp_timer {
    esp_timer_create_args_t args;
    uint64_t period_us;
    pthread_t th;
    bool running;
    pthread_mutex_t mu;
    pthread_cond_t cv;
};

/* the timer's thread plays esp_timer's task: it calls the callback every
 * period until the timer stops */
static void *timer_run(void *p) {
    esp_timer_handle_t t = p;
    pthread_mutex_lock(&t->mu);
    while (t->running) {
        struct timespec until;
        clock_gettime(CLOCK_REALTIME, &until);
        until.tv_sec += (time_t)(t->period_us / 1000000);
        until.tv_nsec += (long)(t->period_us % 1000000) * 1000;
        if (until.tv_nsec >= 1000000000) {
            until.tv_sec++;
            until.tv_nsec -= 1000000000;
        }
        if (pthread_cond_timedwait(&t->cv, &t->mu, &until) == ETIMEDOUT && t->running) {
            pthread_mutex_unlock(&t->mu);
            t->args.callback(t->args.arg);
            pthread_mutex_lock(&t->mu);
        }
    }
    pthread_mutex_unlock(&t->mu);
    return NULL;
}

esp_err_t esp_timer_create(const esp_timer_create_args_t *args, esp_timer_handle_t *out) {
    if (args == NULL || args->callback == NULL || out == NULL) return ESP_ERR_INVALID_ARG;
    esp_timer_handle_t t = calloc(1, sizeof *t);
    t->args = *args; /* esp_timer copies the arguments */
    pthread_mutex_init(&t->mu, NULL);
    pthread_cond_init(&t->cv, NULL);
    *out = t;
    return ESP_OK;
}

esp_err_t esp_timer_start_periodic(esp_timer_handle_t t, uint64_t period_us) {
    if (t->running) return ESP_ERR_INVALID_STATE;
    t->period_us = period_us;
    t->running = true;
    pthread_create(&t->th, NULL, timer_run, t);
    return ESP_OK;
}

esp_err_t esp_timer_stop(esp_timer_handle_t t) {
    pthread_mutex_lock(&t->mu);
    bool was = t->running;
    t->running = false;
    pthread_cond_signal(&t->cv);
    pthread_mutex_unlock(&t->mu);
    if (!was) return ESP_ERR_INVALID_STATE;
    pthread_join(t->th, NULL);
    return ESP_OK;
}

esp_err_t esp_timer_delete(esp_timer_handle_t t) {
    if (t->running) {
        misuse("esp_timer_delete of a running timer");
        return ESP_ERR_INVALID_STATE;
    }
    pthread_mutex_destroy(&t->mu);
    pthread_cond_destroy(&t->cv);
    free(t);
    return ESP_OK;
}

/* ---------- LEDC ---------- */

static struct {
    bool configured;
    uint32_t freq, resolution;
} ledc_timers[LEDC_TIMER_MAX];
/* as on the chip, ledc_update_duty's duty takes effect at the start of the
 * next PWM period: updated is when, and ledc_get_duty gives the old duty
 * before the period ends */
static struct {
    bool configured;
    ledc_timer_t timer;
    uint32_t duty, pending, next;
    int64_t updated;
} ledc_channels[LEDC_CHANNEL_MAX];

static uint32_t ledc_duty_now(ledc_channel_t ch) {
    uint32_t freq = ledc_timers[ledc_channels[ch].timer].freq;
    int64_t period_us = freq > 0 ? 1000000 / freq : 0;
    if (ledc_channels[ch].updated != 0 && esp_timer_get_time() - ledc_channels[ch].updated >= period_us) {
        ledc_channels[ch].duty = ledc_channels[ch].next;
        ledc_channels[ch].updated = 0;
    }
    return ledc_channels[ch].duty;
}

esp_err_t ledc_timer_config(const ledc_timer_config_t *c) {
    if (c->timer_num >= LEDC_TIMER_MAX || c->freq_hz == 0 || c->duty_resolution < 1 || c->duty_resolution > 20) return ESP_ERR_INVALID_ARG;
    ledc_timers[c->timer_num].configured = true;
    ledc_timers[c->timer_num].freq = c->freq_hz;
    ledc_timers[c->timer_num].resolution = c->duty_resolution;
    return ESP_OK;
}

esp_err_t ledc_channel_config(const ledc_channel_config_t *c) {
    if (c->channel >= LEDC_CHANNEL_MAX || c->timer_sel >= LEDC_TIMER_MAX || !GPIO_IS_VALID_OUTPUT_GPIO(c->gpio_num)) return ESP_ERR_INVALID_ARG;
    if (!ledc_timers[c->timer_sel].configured) misuse("ledc_channel_config before ledc_timer_config of its timer");
    ledc_channels[c->channel].configured = true;
    ledc_channels[c->channel].timer = c->timer_sel;
    ledc_channels[c->channel].duty = ledc_channels[c->channel].pending = ledc_channels[c->channel].next = c->duty;
    ledc_channels[c->channel].updated = 0;
    return ESP_OK;
}

esp_err_t ledc_set_duty(ledc_mode_t mode, ledc_channel_t ch, uint32_t duty) {
    (void)mode;
    if (ch >= LEDC_CHANNEL_MAX || !ledc_channels[ch].configured) return ESP_ERR_INVALID_STATE;
    if (duty > (1u << ledc_timers[ledc_channels[ch].timer].resolution)) return ESP_ERR_INVALID_ARG;
    ledc_channels[ch].pending = duty; /* takes effect at ledc_update_duty, as on the chip */
    return ESP_OK;
}

esp_err_t ledc_update_duty(ledc_mode_t mode, ledc_channel_t ch) {
    (void)mode;
    if (ch >= LEDC_CHANNEL_MAX || !ledc_channels[ch].configured) return ESP_ERR_INVALID_STATE;
    ledc_duty_now(ch);
    ledc_channels[ch].next = ledc_channels[ch].pending;
    ledc_channels[ch].updated = esp_timer_get_time();
    return ESP_OK;
}

uint32_t ledc_get_duty(ledc_mode_t mode, ledc_channel_t ch) {
    (void)mode;
    return ch < LEDC_CHANNEL_MAX ? ledc_duty_now(ch) : 0;
}

uint32_t ledc_get_freq(ledc_mode_t mode, ledc_timer_t timer) {
    (void)mode;
    return timer < LEDC_TIMER_MAX ? ledc_timers[timer].freq : 0;
}
