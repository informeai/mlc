/* ESP-IDF's Wi-Fi, NVS, default event loop and esp_netif in the simulation
 * (TestESP32WiFi). The networks around:
 *
 *   mlc-sim, password "secret123": connects, 192.168.4.2, -48 dBm
 *   mlc-open, without a password:  connects, 192.168.4.3, -71 dBm
 *   mlc-silent:                    never answers (the connect times out)
 *   any other name:                WIFI_REASON_NO_AP_FOUND
 *   a wrong password:              WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT (what ESP-IDF 5 reports)
 *
 * As in ESP-IDF, the events come in order from one task (here a thread that
 * lives while there are events), and an attempt answers 10 ms after
 * esp_wifi_connect. A disconnect or stop cancels the attempts on the way. A
 * misuse of the API (out of ESP-IDF's order, or a handler still registered
 * at deinit) is written to stderr with "sim:", which fails the test. */
#define _DARWIN_C_SOURCE
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include <pthread.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "esp_event.h"
#include "esp_netif.h"
#include "esp_wifi.h"
#include "nvs_flash.h"

esp_event_base_t const WIFI_EVENT = "WIFI_EVENT";
esp_event_base_t const IP_EVENT = "IP_EVENT";

#define SIM_HANDLERS 8

typedef struct sim_ev {
    struct sim_ev *next;
    int64_t due_ms;
    bool attempt; /* an attempt to connect: decided when it's due */
    int64_t gen;  /* the attempt's generation; an older one is dropped */
    esp_event_base_t base;
    int32_t id;
    union {
        wifi_event_sta_disconnected_t disc;
        ip_event_got_ip_t got;
    } data;
} sim_ev;

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static struct {
    bool nvs, netif, loop, driver, started, connected;
    wifi_mode_t mode;
    esp_netif_t *sta;
    wifi_config_t conf;
    int64_t gen;
    int8_t rssi;
    struct {
        bool used;
        esp_event_base_t base;
        int32_t id;
        esp_event_handler_t h;
        void *arg;
    } handlers[SIM_HANDLERS];
    sim_ev *head, *tail;
    bool worker;
} sim;

static struct esp_netif_obj {
    int unused;
} sta_netif;

static void misuse(const char *what) { fprintf(stderr, "sim: %s\n", what); }

static int64_t now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

/* deliver calls the handlers registered for the event, outside the lock
 * (a handler may call esp_wifi_connect) */
static void deliver(esp_event_base_t base, int32_t id, void *data) {
    struct {
        esp_event_handler_t h;
        void *arg;
    } hs[SIM_HANDLERS];
    int n = 0;
    pthread_mutex_lock(&mu);
    for (int i = 0; i < SIM_HANDLERS; i++) {
        if (sim.handlers[i].used && sim.handlers[i].base == base && (sim.handlers[i].id == ESP_EVENT_ANY_ID || sim.handlers[i].id == id)) {
            hs[n].h = sim.handlers[i].h;
            hs[n].arg = sim.handlers[i].arg;
            n++;
        }
    }
    pthread_mutex_unlock(&mu);
    for (int i = 0; i < n; i++) hs[i].h(hs[i].arg, base, id, data);
}

/* decide turns a due attempt into the event the network answers with
 * (false: nothing, the silent network or a cancelled attempt). Called with
 * mu held. */
static bool decide(sim_ev *e) {
    if (e->gen != sim.gen || !sim.started) return false;
    const char *ssid = (const char *)sim.conf.sta.ssid;
    const char *pass = (const char *)sim.conf.sta.password;
    uint32_t last = 0;
    const char *want = NULL;
    if (strncmp(ssid, "mlc-sim", 32) == 0) {
        want = "secret123";
        last = 2;
        sim.rssi = -48;
    } else if (strncmp(ssid, "mlc-open", 32) == 0) {
        want = "";
        last = 3;
        sim.rssi = -71;
    } else if (strncmp(ssid, "mlc-silent", 32) == 0) {
        return false;
    }
    if (want == NULL || strncmp(pass, want, 64) != 0) {
        e->base = WIFI_EVENT;
        e->id = WIFI_EVENT_STA_DISCONNECTED;
        memset(&e->data.disc, 0, sizeof e->data.disc);
        e->data.disc.reason = want == NULL ? WIFI_REASON_NO_AP_FOUND : WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT;
        return true;
    }
    sim.connected = true;
    e->base = IP_EVENT;
    e->id = IP_EVENT_STA_GOT_IP;
    memset(&e->data.got, 0, sizeof e->data.got);
    e->data.got.esp_netif = sim.sta;
    e->data.got.ip_info.ip.addr = 192u | 168u << 8 | 4u << 16 | last << 24;
    return true;
}

static void *worker(void *p) {
    (void)p;
    for (;;) {
        pthread_mutex_lock(&mu);
        sim_ev *e = sim.head;
        if (e == NULL) {
            sim.worker = false; /* no events: the thread ends, so the process can end */
            pthread_mutex_unlock(&mu);
            return NULL;
        }
        int64_t wait = e->due_ms - now_ms();
        pthread_mutex_unlock(&mu);
        if (wait > 0) usleep((useconds_t)(wait * 1000));
        pthread_mutex_lock(&mu);
        sim.head = e->next;
        if (sim.head == NULL) sim.tail = NULL;
        bool go = e->attempt ? decide(e) : (e->gen < 0 || e->gen == sim.gen);
        pthread_mutex_unlock(&mu);
        if (go) deliver(e->base, e->id, &e->data);
        free(e);
    }
}

/* post queues an event (with mu held) and starts the event thread if needed */
static void post(sim_ev *e, int64_t delay_ms) {
    e->next = NULL;
    e->due_ms = now_ms() + delay_ms;
    if (sim.tail != NULL) {
        sim.tail->next = e;
    } else {
        sim.head = e;
    }
    sim.tail = e;
    if (!sim.worker) {
        sim.worker = true;
        pthread_t th;
        pthread_create(&th, NULL, worker, NULL);
        pthread_detach(th);
    }
}

/* leave_event queues the disconnection that follows a disconnect or stop */
static void leave_event(void) {
    if (!sim.connected) return;
    sim.connected = false;
    sim_ev *e = calloc(1, sizeof *e);
    e->gen = -1;
    e->base = WIFI_EVENT;
    e->id = WIFI_EVENT_STA_DISCONNECTED;
    e->data.disc.reason = WIFI_REASON_ASSOC_LEAVE;
    post(e, 0);
}

esp_err_t nvs_flash_init(void) {
    sim.nvs = true;
    return ESP_OK;
}

esp_err_t nvs_flash_erase(void) { return ESP_OK; }

esp_err_t esp_netif_init(void) {
    sim.netif = true;
    return ESP_OK;
}

esp_err_t esp_event_loop_create_default(void) {
    pthread_mutex_lock(&mu);
    bool had = sim.loop;
    sim.loop = true;
    pthread_mutex_unlock(&mu);
    return had ? ESP_ERR_INVALID_STATE : ESP_OK;
}

esp_err_t esp_event_handler_instance_register(esp_event_base_t base, int32_t id, esp_event_handler_t h, void *arg,
                                              esp_event_handler_instance_t *out) {
    pthread_mutex_lock(&mu);
    if (!sim.loop) misuse("handler registered before esp_event_loop_create_default");
    for (int i = 0; i < SIM_HANDLERS; i++) {
        if (!sim.handlers[i].used) {
            sim.handlers[i].used = true;
            sim.handlers[i].base = base;
            sim.handlers[i].id = id;
            sim.handlers[i].h = h;
            sim.handlers[i].arg = arg;
            *out = &sim.handlers[i];
            pthread_mutex_unlock(&mu);
            return ESP_OK;
        }
    }
    pthread_mutex_unlock(&mu);
    misuse("too many handlers");
    return ESP_FAIL;
}

esp_err_t esp_event_handler_instance_unregister(esp_event_base_t base, int32_t id, esp_event_handler_instance_t inst) {
    pthread_mutex_lock(&mu);
    for (int i = 0; i < SIM_HANDLERS; i++) {
        if (inst == &sim.handlers[i] && sim.handlers[i].used && sim.handlers[i].base == base && sim.handlers[i].id == id) {
            sim.handlers[i].used = false;
            pthread_mutex_unlock(&mu);
            return ESP_OK;
        }
    }
    pthread_mutex_unlock(&mu);
    misuse("unregister of a handler that isn't registered (or with another base or id)");
    return ESP_ERR_INVALID_ARG;
}

esp_netif_t *esp_netif_create_default_wifi_sta(void) {
    pthread_mutex_lock(&mu);
    if (!sim.netif || !sim.loop) misuse("esp_netif_create_default_wifi_sta before esp_netif_init and the event loop");
    if (sim.sta != NULL) misuse("the station interface created twice");
    sim.sta = &sta_netif;
    pthread_mutex_unlock(&mu);
    return &sta_netif;
}

void esp_netif_destroy_default_wifi(void *netif) {
    pthread_mutex_lock(&mu);
    if (netif != sim.sta || netif == NULL) misuse("esp_netif_destroy_default_wifi of an interface that doesn't exist");
    if (sim.driver) misuse("the interface destroyed before esp_wifi_deinit");
    sim.sta = NULL;
    pthread_mutex_unlock(&mu);
}

esp_err_t esp_wifi_init(const wifi_init_config_t *cfg) {
    pthread_mutex_lock(&mu);
    if (cfg->magic != 0x1F2F3F4F) misuse("esp_wifi_init without WIFI_INIT_CONFIG_DEFAULT");
    if (!sim.nvs) misuse("esp_wifi_init before nvs_flash_init");
    if (sim.driver) misuse("esp_wifi_init twice");
    sim.driver = true;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_deinit(void) {
    pthread_mutex_lock(&mu);
    if (sim.started) misuse("esp_wifi_deinit before esp_wifi_stop");
    for (int i = 0; i < SIM_HANDLERS; i++) {
        if (sim.handlers[i].used) misuse("esp_wifi_deinit with a handler still registered");
    }
    sim.driver = false;
    sim.mode = WIFI_MODE_NULL;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_set_mode(wifi_mode_t mode) {
    pthread_mutex_lock(&mu);
    if (!sim.driver) misuse("esp_wifi_set_mode before esp_wifi_init");
    sim.mode = mode;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_start(void) {
    pthread_mutex_lock(&mu);
    if (!sim.driver || sim.mode != WIFI_MODE_STA) misuse("esp_wifi_start without the driver in station mode");
    sim.started = true;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_stop(void) {
    pthread_mutex_lock(&mu);
    sim.gen++;
    leave_event();
    sim.started = false;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_set_config(wifi_interface_t iface, wifi_config_t *conf) {
    (void)iface;
    pthread_mutex_lock(&mu);
    if (!sim.driver) misuse("esp_wifi_set_config before esp_wifi_init");
    sim.conf = *conf;
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_connect(void) {
    pthread_mutex_lock(&mu);
    if (!sim.started) {
        pthread_mutex_unlock(&mu);
        misuse("esp_wifi_connect before esp_wifi_start");
        return ESP_FAIL;
    }
    sim_ev *e = calloc(1, sizeof *e);
    e->attempt = true;
    e->gen = ++sim.gen;
    post(e, 10);
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_disconnect(void) {
    pthread_mutex_lock(&mu);
    sim.gen++;
    leave_event();
    pthread_mutex_unlock(&mu);
    return ESP_OK;
}

esp_err_t esp_wifi_sta_get_ap_info(wifi_ap_record_t *ap) {
    pthread_mutex_lock(&mu);
    bool c = sim.connected;
    ap->rssi = sim.rssi;
    pthread_mutex_unlock(&mu);
    return c ? ESP_OK : ESP_FAIL;
}
