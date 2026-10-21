/* ESP-IDF simulation for the tests (TestESP32Sim): the few FreeRTOS
 * declarations the ESP32 runtime and mlc_main.c use, on top of pthread. */
#ifndef MLC_SIM_FREERTOS_H
#define MLC_SIM_FREERTOS_H
#include <stdint.h>

typedef int BaseType_t;
typedef unsigned int UBaseType_t;
typedef void *TaskHandle_t;
typedef void (*TaskFunction_t)(void *);
#define pdPASS 1
#define portNUM_PROCESSORS 2
#endif
