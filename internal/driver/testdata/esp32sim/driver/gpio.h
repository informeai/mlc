#ifndef MLC_SIM_DRIVER_GPIO_H
#define MLC_SIM_DRIVER_GPIO_H
/* ESP-IDF's GPIO driver, with the names of ESP-IDF 5.5 that std/gpio uses;
 * sim_gpio.c plays the ESP32's pins and their interrupts. */
#include <stdint.h>

#include "esp_err.h"

typedef int gpio_num_t;
typedef enum { GPIO_MODE_DISABLE = 0, GPIO_MODE_INPUT = 1, GPIO_MODE_OUTPUT = 2, GPIO_MODE_INPUT_OUTPUT = 3 } gpio_mode_t;
typedef enum { GPIO_PULLUP_DISABLE = 0, GPIO_PULLUP_ENABLE = 1 } gpio_pullup_t;
typedef enum { GPIO_PULLDOWN_DISABLE = 0, GPIO_PULLDOWN_ENABLE = 1 } gpio_pulldown_t;
typedef enum { GPIO_INTR_DISABLE = 0, GPIO_INTR_POSEDGE = 1, GPIO_INTR_NEGEDGE = 2, GPIO_INTR_ANYEDGE = 3 } gpio_int_type_t;

typedef struct {
    uint64_t pin_bit_mask;
    gpio_mode_t mode;
    gpio_pullup_t pull_up_en;
    gpio_pulldown_t pull_down_en;
    gpio_int_type_t intr_type;
} gpio_config_t;

typedef void (*gpio_isr_t)(void *arg);

/* the ESP32's pins: 0 to 39 without 20, 24 and 28 to 31; 34 to 39 input only */
#define GPIO_IS_VALID_GPIO(n) ((n) >= 0 && (n) <= 39 && (n) != 20 && (n) != 24 && !((n) >= 28 && (n) <= 31))
#define GPIO_IS_VALID_OUTPUT_GPIO(n) (GPIO_IS_VALID_GPIO(n) && (n) < 34)

esp_err_t gpio_config(const gpio_config_t *c);
esp_err_t gpio_set_level(gpio_num_t pin, uint32_t level);
int gpio_get_level(gpio_num_t pin);
esp_err_t gpio_intr_disable(gpio_num_t pin);
esp_err_t gpio_install_isr_service(int flags);
esp_err_t gpio_isr_handler_add(gpio_num_t pin, gpio_isr_t h, void *arg);
esp_err_t gpio_isr_handler_remove(gpio_num_t pin);
#endif
