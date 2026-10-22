/* ESP-IDF simulation for the tests (TestESP32Sim): the few FreeRTOS
 * declarations the ESP32 runtime and mlc_main.c use, on top of pthread. */
#ifndef MLC_SIM_FREERTOS_H
#define MLC_SIM_FREERTOS_H
#include <stdint.h>
#include <time.h>

/* newlib's struct tm (ESP-IDF's C library) has no tm_gmtoff */
#define tm_gmtoff tm_gmtoff_is_not_in_newlib

typedef int BaseType_t;
typedef unsigned int UBaseType_t;
typedef void *TaskHandle_t;
typedef void (*TaskFunction_t)(void *);
#define pdPASS 1
#define portNUM_PROCESSORS 2
#define pdTRUE 1
#define pdFALSE 0

typedef uint32_t TickType_t;
#define portMAX_DELAY ((TickType_t)0xffffffffu)
#define pdMS_TO_TICKS(ms) ((TickType_t)(ms)) /* a tick is a millisecond here */
#define portYIELD_FROM_ISR(...) ((void)0)

/* whether the code runs in an interrupt; in the simulation, inside
 * sim_isr_call (sim.c), which plays one */
BaseType_t xPortInIsrContext(void);
#endif
