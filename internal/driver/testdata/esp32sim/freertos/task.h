#ifndef MLC_SIM_TASK_H
#define MLC_SIM_TASK_H
#include "freertos/FreeRTOS.h"

BaseType_t xTaskCreate(TaskFunction_t fn, const char *name, uint32_t stack_bytes, void *arg, UBaseType_t prio, TaskHandle_t *out);
void vTaskDelete(TaskHandle_t t);
uint8_t *pxTaskGetStackStart(TaskHandle_t t);
#endif
