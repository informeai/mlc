#ifndef MLC_SIM_NVS_FLASH_H
#define MLC_SIM_NVS_FLASH_H
#include "esp_err.h"

#define ESP_ERR_NVS_NO_FREE_PAGES 0x110d
#define ESP_ERR_NVS_NEW_VERSION_FOUND 0x1110

esp_err_t nvs_flash_init(void);
esp_err_t nvs_flash_erase(void);
#endif
