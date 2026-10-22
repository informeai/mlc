#ifndef MLC_SIM_DRIVER_LEDC_H
#define MLC_SIM_DRIVER_LEDC_H
/* ESP-IDF's LEDC (PWM) driver, with the names and fields of ESP-IDF 5.5 that
 * the tests use; sim_gpio.c keeps the timers and channels' configuration. */
#include <stdbool.h>
#include <stdint.h>

#include "esp_err.h"

typedef enum { LEDC_HIGH_SPEED_MODE, LEDC_LOW_SPEED_MODE, LEDC_SPEED_MODE_MAX } ledc_mode_t;
typedef enum { LEDC_TIMER_0 = 0, LEDC_TIMER_1, LEDC_TIMER_2, LEDC_TIMER_3, LEDC_TIMER_MAX } ledc_timer_t;
typedef enum { LEDC_CHANNEL_0 = 0, LEDC_CHANNEL_1, LEDC_CHANNEL_MAX = 8 } ledc_channel_t;
typedef enum { LEDC_TIMER_1_BIT = 1, LEDC_TIMER_8_BIT = 8, LEDC_TIMER_10_BIT = 10, LEDC_TIMER_13_BIT = 13 } ledc_timer_bit_t;
typedef enum { LEDC_AUTO_CLK = 0 } ledc_clk_cfg_t;
typedef enum { LEDC_INTR_DISABLE = 0 } ledc_intr_type_t;

typedef struct {
    ledc_mode_t speed_mode;
    ledc_timer_bit_t duty_resolution;
    ledc_timer_t timer_num;
    uint32_t freq_hz;
    ledc_clk_cfg_t clk_cfg;
    bool deconfigure;
} ledc_timer_config_t;

typedef struct {
    int gpio_num;
    ledc_mode_t speed_mode;
    ledc_channel_t channel;
    ledc_intr_type_t intr_type;
    ledc_timer_t timer_sel;
    uint32_t duty;
    int hpoint;
    struct {
        unsigned int output_invert : 1;
    } flags;
} ledc_channel_config_t;

esp_err_t ledc_timer_config(const ledc_timer_config_t *c);
esp_err_t ledc_channel_config(const ledc_channel_config_t *c);
esp_err_t ledc_set_duty(ledc_mode_t mode, ledc_channel_t ch, uint32_t duty);
esp_err_t ledc_update_duty(ledc_mode_t mode, ledc_channel_t ch);
uint32_t ledc_get_duty(ledc_mode_t mode, ledc_channel_t ch);
uint32_t ledc_get_freq(ledc_mode_t mode, ledc_timer_t timer);
#endif
