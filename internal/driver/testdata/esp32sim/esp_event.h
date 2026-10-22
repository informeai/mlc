#ifndef MLC_SIM_ESP_EVENT_H
#define MLC_SIM_ESP_EVENT_H
/* ESP-IDF's default event loop: in the simulation, the events are delivered
 * by threads of sim_wifi.c, as ESP-IDF delivers them in its event task. */
#include <stdint.h>

#include "esp_err.h"

typedef const char *esp_event_base_t;
typedef void *esp_event_handler_instance_t;
typedef void (*esp_event_handler_t)(void *arg, esp_event_base_t base, int32_t id, void *data);

#define ESP_EVENT_ANY_ID (-1)

esp_err_t esp_event_loop_create_default(void);
esp_err_t esp_event_handler_instance_register(esp_event_base_t base, int32_t id, esp_event_handler_t h, void *arg,
                                              esp_event_handler_instance_t *out);
esp_err_t esp_event_handler_instance_unregister(esp_event_base_t base, int32_t id, esp_event_handler_instance_t inst);
#endif
