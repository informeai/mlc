#ifndef MLC_SIM_ESP_NETIF_H
#define MLC_SIM_ESP_NETIF_H
/* ESP-IDF's esp_netif (the TCP/IP interfaces), as std/net and std/wifi use
 * it. On the host the sockets are the system's: esp_netif_init only
 * records that it was called. */
#include <stdbool.h>
#include <stdint.h>

#include "esp_err.h"
#include "esp_event.h"

typedef struct esp_netif_obj esp_netif_t;

typedef struct {
    uint32_t addr; /* as lwIP keeps it: the first byte in the low bits */
} esp_ip4_addr_t;

typedef struct {
    esp_ip4_addr_t ip, netmask, gw;
} esp_netif_ip_info_t;

extern esp_event_base_t const IP_EVENT;

typedef enum { IP_EVENT_STA_GOT_IP, IP_EVENT_STA_LOST_IP } ip_event_t;

typedef struct {
    esp_netif_t *esp_netif;
    esp_netif_ip_info_t ip_info;
    bool ip_changed;
} ip_event_got_ip_t;

esp_err_t esp_netif_init(void);
#endif
