/* std/wifi on the ESP32 (station mode), written by mlc build -target esp32.
 * It's a file of its own so that only a program that imports std/wifi
 * links ESP-IDF's Wi-Fi driver (some 500 KB of flash): the linker takes
 * this object from the component's library only when mlc_std_wifi_* is
 * called.
 *
 * The driver works through events, delivered in ESP-IDF's event task: the
 * handlers below only record what happened (address, last disconnection
 * reason) and wake whoever waits in connect. Once connected, a dropped
 * connection is retried in the handler. connect and disconnect are
 * serialized by mlc_wf_op; the state is protected by mlc_wf_mu. */
#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "esp_event.h"
#include "esp_netif.h"
#include "esp_wifi.h"
#include "mlc_runtime.h"
#include "nvs_flash.h"

#define MLC_WF_NOT_FOUND 6001
#define MLC_WF_AUTH 6002
#define MLC_WF_TIMEOUT 6003
#define MLC_WF_FAILED 6005
#define MLC_WF_BAD_NAME 6006

static pthread_mutex_t mlc_wf_op = PTHREAD_MUTEX_INITIALIZER;
static pthread_mutex_t mlc_wf_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t mlc_wf_cv = PTHREAD_COND_INITIALIZER;

static struct {
    bool started;   /* driver initialized and the radio on */
    bool want;      /* a connection was asked for: retry when it drops */
    bool connected; /* has an address */
    uint32_t ip;    /* the address, as lwIP keeps it (first byte in the low bits) */
    int reason;     /* the last disconnection's reason (WIFI_REASON_*), 0: none */
    int auth_fails; /* refusals of the password since the connect */
    esp_netif_t *netif;
    esp_event_handler_instance_t on_wifi, on_ip;
} mlc_wf;

static bool mlc_wf_is_auth(int reason) {
    return reason == WIFI_REASON_AUTH_FAIL || reason == WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT ||
           reason == WIFI_REASON_HANDSHAKE_TIMEOUT;
}

static void mlc_wf_event(void *arg, esp_event_base_t base, int32_t id, void *data) {
    (void)arg;
    bool retry = false;
    pthread_mutex_lock(&mlc_wf_mu);
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        wifi_event_sta_disconnected_t *d = data;
        mlc_wf.connected = false;
        mlc_wf.ip = 0;
        mlc_wf.reason = d->reason;
        if (mlc_wf_is_auth(d->reason)) mlc_wf.auth_fails++;
        retry = mlc_wf.want;
    } else if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        ip_event_got_ip_t *g = data;
        mlc_wf.connected = true;
        mlc_wf.ip = g->ip_info.ip.addr;
    } else if (base == IP_EVENT && id == IP_EVENT_STA_LOST_IP) {
        mlc_wf.connected = false;
        mlc_wf.ip = 0;
    }
    pthread_cond_broadcast(&mlc_wf_cv);
    pthread_mutex_unlock(&mlc_wf_mu);
    if (retry) esp_wifi_connect();
}

/* mlc_wf_start brings up NVS (the driver keeps its calibration there), the
 * TCP/IP stack, the default event loop and the driver, and turns the radio
 * on. What the program already started through the FFI (ESP_ERR_INVALID_STATE)
 * is kept. */
static bool mlc_wf_start(void) {
    esp_err_t e = nvs_flash_init();
    if (e == ESP_ERR_NVS_NO_FREE_PAGES || e == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        if (nvs_flash_erase() != ESP_OK) return false;
        e = nvs_flash_init();
    }
    if (e != ESP_OK) return false;
    if (esp_netif_init() != ESP_OK) return false;
    e = esp_event_loop_create_default();
    if (e != ESP_OK && e != ESP_ERR_INVALID_STATE) return false;
    mlc_wf.netif = esp_netif_create_default_wifi_sta();
    if (mlc_wf.netif == NULL) return false;
    wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    if (esp_wifi_init(&cfg) != ESP_OK) goto no_driver;
    if (esp_event_handler_instance_register(WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, mlc_wf_event, NULL, &mlc_wf.on_wifi) != ESP_OK)
        goto no_handlers;
    if (esp_event_handler_instance_register(IP_EVENT, ESP_EVENT_ANY_ID, mlc_wf_event, NULL, &mlc_wf.on_ip) != ESP_OK) {
        esp_event_handler_instance_unregister(WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, mlc_wf.on_wifi);
        goto no_handlers;
    }
    if (esp_wifi_set_mode(WIFI_MODE_STA) != ESP_OK || esp_wifi_start() != ESP_OK) {
        esp_event_handler_instance_unregister(IP_EVENT, ESP_EVENT_ANY_ID, mlc_wf.on_ip);
        esp_event_handler_instance_unregister(WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, mlc_wf.on_wifi);
        goto no_handlers;
    }
    mlc_wf.started = true;
    return true;
no_handlers:
    esp_wifi_deinit();
no_driver:
    esp_netif_destroy_default_wifi(mlc_wf.netif);
    mlc_wf.netif = NULL;
    return false;
}

/* mlc_wf_leave drops the connection without trying again and waits (up to
 * a second) for the driver to confirm. Called with mlc_wf_op held. */
static void mlc_wf_leave(void) {
    pthread_mutex_lock(&mlc_wf_mu);
    bool was = mlc_wf.want || mlc_wf.connected;
    mlc_wf.want = false;
    pthread_mutex_unlock(&mlc_wf_mu);
    if (!was) return;
    esp_wifi_disconnect();
    struct timespec until;
    clock_gettime(CLOCK_REALTIME, &until);
    until.tv_sec += 1;
    pthread_mutex_lock(&mlc_wf_mu);
    while (mlc_wf.connected && pthread_cond_timedwait(&mlc_wf_cv, &mlc_wf_mu, &until) == 0) {
    }
    mlc_wf.connected = false;
    mlc_wf.ip = 0;
    pthread_mutex_unlock(&mlc_wf_mu);
}

bool mlc_std_wifi_available(const char *file, int line) {
    (void)file;
    (void)line;
    return true;
}

bool mlc_std_wifi_connect(mlc_sl_byte *ssid, mlc_sl_byte *password, int64_t timeout_ms, int64_t *err, const char *file,
                          int line) {
    (void)file;
    (void)line;
    *err = 0;
    wifi_config_t conf;
    memset(&conf, 0, sizeof conf);
    if (ssid->len < 1 || ssid->len > (int64_t)sizeof conf.sta.ssid || password->len > (int64_t)sizeof conf.sta.password) {
        *err = MLC_WF_BAD_NAME;
        return false;
    }
    memcpy(conf.sta.ssid, ssid->ptr, (size_t)ssid->len);
    if (password->len > 0) memcpy(conf.sta.password, password->ptr, (size_t)password->len);

    pthread_mutex_lock(&mlc_wf_op);
    if (!mlc_wf.started && !mlc_wf_start()) {
        pthread_mutex_unlock(&mlc_wf_op);
        *err = MLC_WF_FAILED;
        return false;
    }
    mlc_wf_leave();
    if (esp_wifi_set_config(WIFI_IF_STA, &conf) != ESP_OK) {
        pthread_mutex_unlock(&mlc_wf_op);
        *err = MLC_WF_FAILED;
        return false;
    }
    pthread_mutex_lock(&mlc_wf_mu);
    mlc_wf.reason = 0;
    mlc_wf.auth_fails = 0;
    mlc_wf.want = true;
    pthread_mutex_unlock(&mlc_wf_mu);
    esp_wifi_connect();

    /* waits for the address; the handler retries each failure, and two
     * refusals of the password end it early */
    struct timespec until;
    clock_gettime(CLOCK_REALTIME, &until);
    if (timeout_ms >= 0) {
        until.tv_sec += (time_t)(timeout_ms / 1000);
        until.tv_nsec += (long)(timeout_ms % 1000) * 1000000;
        if (until.tv_nsec >= 1000000000) {
            until.tv_sec++;
            until.tv_nsec -= 1000000000;
        }
    }
    pthread_mutex_lock(&mlc_wf_mu);
    bool timed_out = false;
    while (!mlc_wf.connected && mlc_wf.auth_fails < 2 && !timed_out) {
        if (timeout_ms < 0) {
            pthread_cond_wait(&mlc_wf_cv, &mlc_wf_mu);
        } else {
            timed_out = pthread_cond_timedwait(&mlc_wf_cv, &mlc_wf_mu, &until) == ETIMEDOUT;
        }
    }
    bool ok = mlc_wf.connected;
    int reason = mlc_wf.reason;
    pthread_mutex_unlock(&mlc_wf_mu);
    if (!ok) {
        mlc_wf_leave();
        if (mlc_wf_is_auth(reason)) {
            *err = MLC_WF_AUTH;
        } else if (reason == WIFI_REASON_NO_AP_FOUND || reason == WIFI_REASON_NO_AP_FOUND_W_COMPATIBLE_SECURITY ||
                   reason == WIFI_REASON_NO_AP_FOUND_IN_AUTHMODE_THRESHOLD ||
                   reason == WIFI_REASON_NO_AP_FOUND_IN_RSSI_THRESHOLD) {
            *err = MLC_WF_NOT_FOUND;
        } else {
            *err = MLC_WF_TIMEOUT;
        }
    }
    pthread_mutex_unlock(&mlc_wf_op);
    return ok;
}

bool mlc_std_wifi_connected(const char *file, int line) {
    (void)file;
    (void)line;
    pthread_mutex_lock(&mlc_wf_mu);
    bool c = mlc_wf.connected;
    pthread_mutex_unlock(&mlc_wf_mu);
    return c;
}

int64_t mlc_std_wifi_ip(mlc_sl_byte *dst, const char *file, int line) {
    pthread_mutex_lock(&mlc_wf_mu);
    uint32_t a = mlc_wf.connected ? mlc_wf.ip : 0;
    pthread_mutex_unlock(&mlc_wf_mu);
    if (a == 0) return 0;
    char buf[16];
    int n = snprintf(buf, sizeof buf, "%u.%u.%u.%u", (unsigned)(a & 0xff), (unsigned)((a >> 8) & 0xff),
                     (unsigned)((a >> 16) & 0xff), (unsigned)(a >> 24));
    if (n > dst->len) {
        char msg[128];
        snprintf(msg, sizeof msg, "wifi.ip: the address (%d bytes) doesn't fit in the destination (%lld bytes)", n,
                 (long long)dst->len);
        mlc_panic(file, line, msg);
    }
    memcpy(dst->ptr, buf, (size_t)n);
    return n;
}

int64_t mlc_std_wifi_rssi(const char *file, int line) {
    (void)file;
    (void)line;
    if (!mlc_std_wifi_connected(file, line)) return 0;
    wifi_ap_record_t ap;
    if (esp_wifi_sta_get_ap_info(&ap) != ESP_OK) return 0;
    return ap.rssi;
}

void mlc_std_wifi_disconnect(const char *file, int line) {
    (void)file;
    (void)line;
    pthread_mutex_lock(&mlc_wf_op);
    if (mlc_wf.started) {
        mlc_wf_leave();
        esp_wifi_stop();
        esp_event_handler_instance_unregister(IP_EVENT, ESP_EVENT_ANY_ID, mlc_wf.on_ip);
        esp_event_handler_instance_unregister(WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, mlc_wf.on_wifi);
        esp_wifi_deinit();
        esp_netif_destroy_default_wifi(mlc_wf.netif);
        mlc_wf.netif = NULL;
        mlc_wf.started = false;
    }
    pthread_mutex_unlock(&mlc_wf_op);
}
