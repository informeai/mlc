#ifndef MLC_SIM_ESP_ERR_H
#define MLC_SIM_ESP_ERR_H
/* the few ESP-IDF error codes the runtime and the simulation use */
typedef int esp_err_t;
#define ESP_OK 0
#define ESP_FAIL (-1)
#define ESP_ERR_INVALID_ARG 0x102
#define ESP_ERR_INVALID_STATE 0x103
#endif
