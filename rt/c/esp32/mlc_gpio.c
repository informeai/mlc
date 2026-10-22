/* std/gpio on the ESP32, written by mlc build -target esp32. A file of its
 * own, like mlc_wifi.c: only a program that calls std/gpio links it.
 *
 * A watch is a FreeRTOS queue of edges and the pin's interrupt handler,
 * registered in ESP-IDF's GPIO ISR service. The handler runs in an
 * interrupt, so it only reads the level and the time, applies the
 * debounce and puts the edge in the queue (xQueueSendFromISR); with the
 * queue full, it counts the edge as missed. next reads the queue in the
 * program's task. No MLC code runs in the interrupt. */
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>

#include "driver/gpio.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "mlc_runtime.h"

#define MLC_GP_BAD_PIN 6102
#define MLC_GP_FAILED 6103
#define MLC_GP_IN_USE 6104
#define MLC_GP_QUEUE 16 /* gpio.QUEUE */

typedef struct {
    int64_t at_us;
    bool high;
} mlc_gp_edge;

typedef struct mlc_gp_watch {
    QueueHandle_t q;
    int pin;
    int edge; /* gpio.RISING, FALLING or ANY */
    int64_t debounce_us;
    int64_t last_us;          /* the last edge delivered (the debounce counts from it) */
    volatile int64_t missed;  /* written by the handler, read by missed() */
} mlc_gp_watch;

/* the pins made outputs by gpio.output, and the ones with a watch; written
 * only by the program's tasks, under mlc_gp_mu */
static uint64_t mlc_gp_outputs, mlc_gp_watched;
static pthread_mutex_t mlc_gp_mu = PTHREAD_MUTEX_INITIALIZER;
static bool mlc_gp_service;

static void mlc_gp_isr(void *arg) {
    mlc_gp_watch *w = arg;
    /* RISING and FALLING say their own level: edges closer than the
     * interrupt's latency are merged by the hardware, and by then the pin may
     * already be at the next one's level; ANY reads it */
    bool high = w->edge == 1 ? true : w->edge == 2 ? false : gpio_get_level((gpio_num_t)w->pin) != 0;
    mlc_gp_edge e = {esp_timer_get_time(), high};
    if (w->debounce_us > 0 && w->last_us != 0 && e.at_us - w->last_us < w->debounce_us) return;
    BaseType_t woken = pdFALSE;
    if (xQueueSendFromISR(w->q, &e, &woken) == pdTRUE) {
        w->last_us = e.at_us;
    } else {
        w->missed++;
    }
    if (woken) portYIELD_FROM_ISR();
}

bool mlc_std_gpio_available(const char *file, int line) {
    (void)file;
    (void)line;
    return true;
}

static bool mlc_gp_configure(int64_t pin, gpio_mode_t mode, int64_t pull, gpio_int_type_t intr, int64_t *err) {
    gpio_config_t c = {
        .pin_bit_mask = 1ULL << pin,
        .mode = mode,
        .pull_up_en = pull == 1 ? GPIO_PULLUP_ENABLE : GPIO_PULLUP_DISABLE,
        .pull_down_en = pull == 2 ? GPIO_PULLDOWN_ENABLE : GPIO_PULLDOWN_DISABLE,
        .intr_type = intr,
    };
    if (gpio_config(&c) != ESP_OK) {
        *err = MLC_GP_FAILED;
        return false;
    }
    return true;
}

bool mlc_std_gpio_output(int64_t pin, int64_t *err, const char *file, int line) {
    (void)file;
    (void)line;
    *err = 0;
    if (pin < 0 || pin > 63 || !GPIO_IS_VALID_OUTPUT_GPIO(pin)) {
        *err = MLC_GP_BAD_PIN;
        return false;
    }
    /* INPUT_OUTPUT: read gives back the level written, and a watch on the
     * pin sees the edges the program writes */
    if (!mlc_gp_configure(pin, GPIO_MODE_INPUT_OUTPUT, 0, GPIO_INTR_DISABLE, err)) return false;
    gpio_set_level((gpio_num_t)pin, 0);
    pthread_mutex_lock(&mlc_gp_mu);
    mlc_gp_outputs |= 1ULL << pin;
    pthread_mutex_unlock(&mlc_gp_mu);
    return true;
}

bool mlc_std_gpio_input(int64_t pin, int64_t pull, int64_t *err, const char *file, int line) {
    (void)file;
    (void)line;
    *err = 0;
    if (pin < 0 || pin > 63 || !GPIO_IS_VALID_GPIO(pin) || pull < 0 || pull > 2) {
        *err = MLC_GP_BAD_PIN;
        return false;
    }
    if (!mlc_gp_configure(pin, GPIO_MODE_INPUT, pull, GPIO_INTR_DISABLE, err)) return false;
    pthread_mutex_lock(&mlc_gp_mu);
    mlc_gp_outputs &= ~(1ULL << pin);
    pthread_mutex_unlock(&mlc_gp_mu);
    return true;
}

void mlc_std_gpio_write(int64_t pin, bool high, const char *file, int line) {
    if (pin < 0 || pin > 63 || !GPIO_IS_VALID_OUTPUT_GPIO(pin)) mlc_panic(file, line, "gpio.write: that pin can't be an output (34 to 39 are input only)");
    gpio_set_level((gpio_num_t)pin, high ? 1 : 0);
}

bool mlc_std_gpio_read(int64_t pin, const char *file, int line) {
    if (pin < 0 || pin > 63 || !GPIO_IS_VALID_GPIO(pin)) mlc_panic(file, line, "gpio.read: the pin doesn't exist");
    return gpio_get_level((gpio_num_t)pin) != 0;
}

mlc_gp_watch *mlc_std_gpio_watch(int64_t pin, int64_t edge, int64_t pull, int64_t debounce_ms, int64_t *err,
                                 const char *file, int line) {
    *err = 0;
    if (pin < 0 || pin > 63 || !GPIO_IS_VALID_GPIO(pin) || edge < 1 || edge > 3 || pull < 0 || pull > 2 || debounce_ms < 0) {
        *err = MLC_GP_BAD_PIN;
        return NULL;
    }
    pthread_mutex_lock(&mlc_gp_mu);
    if (mlc_gp_watched & (1ULL << pin)) {
        pthread_mutex_unlock(&mlc_gp_mu);
        *err = MLC_GP_IN_USE;
        return NULL;
    }
    gpio_int_type_t intr = edge == 1 ? GPIO_INTR_POSEDGE : edge == 2 ? GPIO_INTR_NEGEDGE : GPIO_INTR_ANYEDGE;
    gpio_mode_t mode = (mlc_gp_outputs & (1ULL << pin)) ? GPIO_MODE_INPUT_OUTPUT : GPIO_MODE_INPUT;
    if (!mlc_gp_service) {
        esp_err_t e = gpio_install_isr_service(0); /* a program's own FFI may have installed it */
        if (e != ESP_OK && e != ESP_ERR_INVALID_STATE) {
            pthread_mutex_unlock(&mlc_gp_mu);
            *err = MLC_GP_FAILED;
            return NULL;
        }
        mlc_gp_service = true;
    }
    mlc_gp_watch *w = mlc_alloc(sizeof *w, file, line);
    w->pin = (int)pin;
    w->edge = (int)edge;
    w->debounce_us = debounce_ms * 1000;
    w->q = xQueueCreate(MLC_GP_QUEUE, sizeof(mlc_gp_edge));
    if (w->q == NULL) {
        pthread_mutex_unlock(&mlc_gp_mu);
        mlc_free(w);
        *err = MLC_GP_FAILED;
        return NULL;
    }
    if (!mlc_gp_configure(pin, mode, pull, intr, err) || gpio_isr_handler_add((gpio_num_t)pin, mlc_gp_isr, w) != ESP_OK) {
        pthread_mutex_unlock(&mlc_gp_mu);
        gpio_intr_disable((gpio_num_t)pin);
        vQueueDelete(w->q);
        mlc_free(w);
        *err = MLC_GP_FAILED;
        return NULL;
    }
    mlc_gp_watched |= 1ULL << pin;
    pthread_mutex_unlock(&mlc_gp_mu);
    return w;
}

bool mlc_std_gpio_next_raw(mlc_gp_watch *w, int64_t timeout_ms, bool *high, int64_t *at_us, const char *file, int line) {
    (void)file;
    (void)line;
    TickType_t ticks = timeout_ms < 0 ? portMAX_DELAY : pdMS_TO_TICKS(timeout_ms);
    mlc_gp_edge e;
    if (xQueueReceive(w->q, &e, ticks) != pdTRUE) return false;
    *high = e.high;
    *at_us = e.at_us;
    return true;
}

int64_t mlc_std_gpio_missed_raw(mlc_gp_watch *w, const char *file, int line) {
    (void)file;
    (void)line;
    return w->missed;
}

void mlc_std_gpio_close_raw(mlc_gp_watch *w, const char *file, int line) {
    (void)file;
    (void)line;
    pthread_mutex_lock(&mlc_gp_mu);
    gpio_intr_disable((gpio_num_t)w->pin);
    gpio_isr_handler_remove((gpio_num_t)w->pin); /* after it, the handler doesn't run anymore */
    mlc_gp_watched &= ~(1ULL << w->pin);
    pthread_mutex_unlock(&mlc_gp_mu);
    vQueueDelete(w->q);
    mlc_free(w);
}
