#ifndef MLC_SIM_ESP_NETIF_H
#define MLC_SIM_ESP_NETIF_H
/* ESP-IDF's esp_netif_init starts lwIP's thread; on the host, the sockets
 * are the system's and there's nothing to start. */
typedef int esp_err_t;
esp_err_t esp_netif_init(void);
#endif
