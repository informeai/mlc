#ifndef MLC_SIM_ESP_ROM_SYS_H
#define MLC_SIM_ESP_ROM_SYS_H
/* the ROM's printf, which ESP-IDF allows in an interrupt; in the
 * simulation it writes to stderr */
int esp_rom_printf(const char *fmt, ...);
#endif
