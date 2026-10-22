/* C library for TestFFIRegistry (see registry.h). */
#include "registry.h"

#include <pthread.h>
#include <string.h>

struct reg_cfg reg_cfg;

static int32_t calls;
int32_t reg_next(void) { return ++calls; }
int32_t reg_calls(void) { return calls; }

static int32_t noted;
void reg_note(int32_t v) { noted = v; }
int32_t reg_noted(void) { return noted; }

struct reg_buf {
    uint8_t data[64];
    uint16_t len;
};

int reg_buf_append(struct reg_buf *b, const void *data, uint16_t len) {
    if (b->len + len > sizeof b->data) return -1;
    memcpy(b->data + b->len, data, len);
    b->len += len;
    return 0;
}

uint16_t reg_buf_len(const struct reg_buf *b) { return b->len; }

static const struct reg_svc_def *table; /* kept, as NimBLE keeps it */

static uint16_t uuid16(const struct reg_uuid *u) { return u->type == REG_UUID_16 ? ((const reg_uuid16_t *)u)->value : 0; }

int reg_add_svcs(const struct reg_svc_def *svcs) {
    table = svcs;
    int n = 0;
    for (const struct reg_svc_def *s = svcs; s->type != 0; s++) n++;
    return n;
}

int reg_find(uint16_t svc, uint16_t chr, uint16_t *out_handle) {
    uint16_t h = 1;
    for (const struct reg_svc_def *s = table; s != NULL && s->type != 0; s++, h++) {
        for (const struct reg_chr_def *c = s->characteristics; c->uuid != NULL; c++, h++) {
            if (uuid16(s->uuid) == svc && uuid16(c->uuid) == chr) {
                *out_handle = h;
                return 0;
            }
        }
    }
    return -1;
}

static int32_t total;

static void *run(void *p) {
    (void)p;
    if (reg_cfg.sync_cb != NULL) reg_cfg.sync_cb();
    uint16_t h = 1;
    for (const struct reg_svc_def *s = table; s->type != 0; s++, h++) {
        for (const struct reg_chr_def *c = s->characteristics; c->uuid != NULL; c++, h++) {
            struct reg_buf b = {{0}, 0};
            struct reg_ctxt ctxt = {1, &b};
            if (c->access_cb(h, &ctxt, c->arg) == 0) {
                for (uint16_t i = 0; i < b.len; i++) total += b.data[i];
            }
        }
    }
    return NULL;
}

int32_t reg_run(void) {
    pthread_t th;
    if (pthread_create(&th, NULL, run, NULL) != 0) return -1;
    pthread_join(th, NULL);
    return total;
}
