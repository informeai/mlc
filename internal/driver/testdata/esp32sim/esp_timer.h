#ifndef MLC_SIM_ESP_TIMER_H
#define MLC_SIM_ESP_TIMER_H
#include <stdint.h>

/* microseconds since the boot (in the simulation, since the process started) */
int64_t esp_timer_get_time(void);
#endif
