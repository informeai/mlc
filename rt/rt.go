// Package rt embeds the C runtime in the mlc binary, so the compiler
// works without depending on external files.
package rt

import _ "embed"

//go:embed c/mlc_runtime.h
var Header string

//go:embed c/mlc_runtime.c
var Source string

// The ESP-IDF component of mlc build -target esp32: the entry (app_main) and the
// CMakeLists.txt, written next to the generated C and the runtime.
//
//go:embed c/esp32/mlc_main.c
var ESP32Main string

//go:embed c/esp32/CMakeLists.txt
var ESP32CMake string

// ESP32WiFi is std/wifi on the chip, a file of its own so that only a
// program that calls it links ESP-IDF's Wi-Fi driver.
//
//go:embed c/esp32/mlc_wifi.c
var ESP32WiFi string
