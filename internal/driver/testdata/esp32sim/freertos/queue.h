#ifndef MLC_SIM_FREERTOS_QUEUE_H
#define MLC_SIM_FREERTOS_QUEUE_H
/* FreeRTOS queues, as std/gpio uses them: sent from an interrupt
 * (xQueueSendFromISR), received in a task (xQueueReceive). In the
 * simulation a tick is a millisecond. */
#include <stdint.h>

#include "freertos/FreeRTOS.h"

typedef struct sim_queue *QueueHandle_t;

QueueHandle_t xQueueCreate(UBaseType_t len, UBaseType_t item);
void vQueueDelete(QueueHandle_t q);
BaseType_t xQueueSendFromISR(QueueHandle_t q, const void *item, BaseType_t *woken);
BaseType_t xQueueReceive(QueueHandle_t q, void *item, TickType_t ticks);
#endif
