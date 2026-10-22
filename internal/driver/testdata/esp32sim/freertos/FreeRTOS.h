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

/* whether the code runs in an interrupt; in the simulation, inside
 * sim_isr_call (sim.c), which plays one */
BaseType_t xPortInIsrContext(void);
#endif
