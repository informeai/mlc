/* ESP-IDF entry, written by mlc build -target esp32. app_main starts a
 * FreeRTOS task with MLC_MAIN_STACK bytes of stack (32 KB by default; the
 * ESP-IDF main task has only 3.5 KB) and runs the MLC program's main in it.
 * When main returns, the task ends; a panic calls exit, which in ESP-IDF
 * aborts and restarts the chip, after the message on the console. */
#include <stdlib.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#ifndef MLC_MAIN_STACK
#define MLC_MAIN_STACK 32768
#endif

int mlc_main(int argc, char **argv);

static void mlc_main_task(void *arg) {
    (void)arg;
    static char name[] = "mlc";
    static char *argv[] = {name, NULL};
    mlc_main(1, argv);
    vTaskDelete(NULL);
}

void app_main(void) {
    /* in ESP-IDF, the stack size of xTaskCreate is in bytes */
    if (xTaskCreate(mlc_main_task, "mlc_main", MLC_MAIN_STACK, NULL, 1, NULL) != pdPASS) abort();
}
