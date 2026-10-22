#ifndef MLC_SIM_ESP_WIFI_H
#define MLC_SIM_ESP_WIFI_H
/* ESP-IDF's Wi-Fi driver (station mode), with the names and layouts of
 * ESP-IDF 5.5 that std/wifi uses; sim_wifi.c plays the networks. */
#include <stdint.h>

#include "esp_err.h"
#include "esp_event.h"
#include "esp_netif.h"

extern esp_event_base_t const WIFI_EVENT;

typedef enum { WIFI_EVENT_STA_START = 2, WIFI_EVENT_STA_STOP, WIFI_EVENT_STA_CONNECTED, WIFI_EVENT_STA_DISCONNECTED } wifi_event_t;

typedef enum {
    WIFI_REASON_ASSOC_LEAVE = 8,
    WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT = 15,
    WIFI_REASON_NO_AP_FOUND = 201,
    WIFI_REASON_AUTH_FAIL = 202,
    WIFI_REASON_HANDSHAKE_TIMEOUT = 204,
    WIFI_REASON_NO_AP_FOUND_W_COMPATIBLE_SECURITY = 210,
    WIFI_REASON_NO_AP_FOUND_IN_AUTHMODE_THRESHOLD = 211,
    WIFI_REASON_NO_AP_FOUND_IN_RSSI_THRESHOLD = 212,
} wifi_err_reason_t;

typedef struct {
    uint8_t ssid[32];
    uint8_t ssid_len;
    uint8_t bssid[6];
    uint8_t reason;
    int8_t rssi;
} wifi_event_sta_disconnected_t;

typedef struct {
    int magic;
} wifi_init_config_t;
#define WIFI_INIT_CONFIG_DEFAULT() {.magic = 0x1F2F3F4F}

typedef enum { WIFI_MODE_NULL, WIFI_MODE_STA } wifi_mode_t;
typedef enum { WIFI_IF_STA } wifi_interface_t;

typedef union {
    struct {
        uint8_t ssid[32];
        uint8_t password[64];
    } sta;
} wifi_config_t;

typedef struct {
    int8_t rssi;
} wifi_ap_record_t;

esp_netif_t *esp_netif_create_default_wifi_sta(void);
void esp_netif_destroy_default_wifi(void *netif);
esp_err_t esp_wifi_init(const wifi_init_config_t *cfg);
esp_err_t esp_wifi_deinit(void);
esp_err_t esp_wifi_set_mode(wifi_mode_t mode);
esp_err_t esp_wifi_start(void);
esp_err_t esp_wifi_stop(void);
esp_err_t esp_wifi_set_config(wifi_interface_t iface, wifi_config_t *conf);
esp_err_t esp_wifi_connect(void);
esp_err_t esp_wifi_disconnect(void);
esp_err_t esp_wifi_sta_get_ap_info(wifi_ap_record_t *ap);
#endif
