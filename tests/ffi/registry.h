/* Header for TestFFIRegistry: a library shaped like NimBLE. Structs named by
 * tag (no typedef), a function-like macro that uses its argument twice, a
 * static inline function, a global configuration struct, a table of
 * services that the library keeps and walks later from a thread of its own,
 * and an opaque buffer handed to the callbacks inside a struct. */
#ifndef MLC_TEST_REGISTRY_H
#define MLC_TEST_REGISTRY_H
#include <stdint.h>

#define REG_UUID_16 16
#define REG_SVC_PRIMARY 1
#define REG_F_READ 0x02

struct reg_uuid {
    uint8_t type;
};

typedef struct {
    struct reg_uuid u;
    uint16_t value;
} reg_uuid16_t;

struct reg_buf; /* opaque: only the library's functions look inside */

struct reg_ctxt {
    uint8_t op;
    struct reg_buf *buf;
};

typedef int (*reg_access_fn)(uint16_t handle, struct reg_ctxt *ctxt, void *arg);

struct reg_chr_def {
    const struct reg_uuid *uuid; /* NULL: the end of the array */
    reg_access_fn access_cb;
    void *arg;
    uint16_t flags;
};

struct reg_svc_def {
    uint8_t type; /* 0: the end of the array */
    const struct reg_uuid *uuid;
    const struct reg_chr_def *characteristics;
};

struct reg_cfg {
    void (*sync_cb)(void);
    int32_t level;
};
extern struct reg_cfg reg_cfg;

int32_t reg_next(void); /* counts its calls */
void reg_note(int32_t v); /* a function without a result */
int32_t reg_noted(void);
int32_t reg_calls(void);
#define REG_TWICE(x) ((x) + (x))
static inline int32_t reg_square(int32_t x) { return x * x; }

int reg_add_svcs(const struct reg_svc_def *svcs); /* keeps the table */
/* calls reg_cfg.sync_cb, then each characteristic's access_cb with a
 * buffer, from a thread of its own; returns the bytes the callbacks
 * appended, as a sum */
int32_t reg_run(void);
int reg_find(uint16_t svc, uint16_t chr, uint16_t *out_handle);
int reg_buf_append(struct reg_buf *b, const void *data, uint16_t len);
#define REG_BUF_LEN(b) (reg_buf_len(b))
uint16_t reg_buf_len(const struct reg_buf *b);
#endif
