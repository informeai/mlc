"""Runs tests/esp32ble/battery.mlc on an ESP32 and talks to it over Bluetooth
LE from this machine, as a phone would: the check of the BLE peripheral that
the simulation can't do (it has no Bluetooth stack). See docs/esp32.md.

The program is built in an ESP-IDF project with Bluetooth on (the script
writes its sdkconfig.defaults) and flashed whole, since the partition table
is the large one. Then the script finds the device by its name, connects,
reads the battery level, gets three notifications (the level drops one point
a second), disconnects, and checks the chip's console too (advertising,
connected, disconnected, advertising again).

Run it in ESP-IDF's environment, with its Python (which has pyserial) and
bleak (pip install bleak); on macOS the terminal needs Bluetooth permission:

    . ~/esp/esp-idf/export.sh
    python tools/esp32_ble.py --port /dev/cu.usbserial-0001
"""
import argparse
import asyncio
import os
import re
import subprocess
import sys
import tempfile
import threading
import time

import serial

try:
    from bleak import BleakClient, BleakScanner
    from bleak.exc import BleakError
except ImportError:
    sys.exit("bleak not found: pip install bleak (in ESP-IDF's Python: python -m pip install bleak)")

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PROGRAM = os.path.join(REPO, "tests", "esp32ble", "battery.mlc")
NAME = "mlc-battery"
LEVEL = "00002a19-0000-1000-8000-00805f9b34fb"
SDKCONFIG = """CONFIG_BT_ENABLED=y
CONFIG_BT_NIMBLE_ENABLED=y
CONFIG_PARTITION_TABLE_SINGLE_APP_LARGE=y
CONFIG_ESPTOOLPY_FLASHSIZE_4MB=y
"""


def run(args, **kw):
    return subprocess.run(args, capture_output=True, text=True, **kw)


def build_and_flash(port, proj, mlc):
    if not os.path.exists(os.path.join(proj, "CMakeLists.txt")):
        r = run(["idf.py", "create-project", "-p", proj, os.path.basename(proj.rstrip("/"))])
        if r.returncode != 0:
            sys.exit("idf.py create-project failed:\n" + r.stdout + r.stderr)
        main = os.path.join(proj, "main")
        for f in os.listdir(main):
            os.remove(os.path.join(main, f))
        with open(os.path.join(proj, "sdkconfig.defaults"), "w") as f:
            f.write(SDKCONFIG)
    r = run([mlc, "build", "-target", "esp32", "-o", os.path.join(proj, "main"), PROGRAM])
    if r.returncode != 0:
        sys.exit("mlc build failed:\n" + r.stderr)
    if not os.path.exists(os.path.join(proj, "sdkconfig")):
        r = run(["idf.py", "-C", proj, "set-target", "esp32"])
        if r.returncode != 0:
            sys.exit("idf.py set-target failed:\n" + r.stdout[-2000:] + r.stderr)
    r = run(["idf.py", "-C", proj, "build"])
    if r.returncode != 0:
        sys.exit("ESP-IDF build failed:\n" + "\n".join(l for l in (r.stdout + r.stderr).splitlines() if "error" in l)[:3000])
    for baud in ("921600", "460800"):
        r = run(["idf.py", "-C", proj, "-p", port, "-b", baud, "flash"])
        if r.returncode == 0:
            return
    sys.exit("flash failed:\n" + r.stdout[-1000:] + r.stderr[-1000:])


class Console:
    """the chip's console, read in a thread from the reset on"""

    def __init__(self, port):
        self.lines = []
        self.stop = False
        self.s = serial.Serial(port, 115200, timeout=0.1)
        self.s.dtr = False
        self.s.rts = True
        time.sleep(0.1)
        self.s.rts = False
        self.t = threading.Thread(target=self.read, daemon=True)
        self.t.start()

    def read(self):
        buf = b""
        while not self.stop:
            buf += self.s.read(4096)
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                self.lines.append(line.decode("utf-8", "replace").rstrip("\r"))

    def wait(self, pattern, timeout):
        end = time.time() + timeout
        while time.time() < end:
            if any(re.search(pattern, l) for l in self.lines):
                return True
            time.sleep(0.1)
        return False

    def close(self):
        self.stop = True
        self.t.join()
        self.s.close()


async def central(results):
    device = await BleakScanner.find_device_by_name(NAME, timeout=20.0)
    if device is None:
        results.append(("scan", False, f"{NAME} wasn't found"))
        return
    results.append(("scan", True, device.address))
    async with BleakClient(device) as client:
        value = await client.read_gatt_char(LEVEL)
        results.append(("read", len(value) == 1 and 0 < value[0] <= 100, f"level {list(value)}"))
        got = []
        await client.start_notify(LEVEL, lambda _, data: got.append(data[0]))
        end = time.time() + 10
        while len(got) < 3 and time.time() < end:
            await asyncio.sleep(0.2)
        await client.stop_notify(LEVEL)
        steady = len(got) >= 3 and all(b == a - 1 or (a == 1 and b == 100) for a, b in zip(got, got[1:]))
        results.append(("notify", steady, f"levels {got}"))


def main():
    ap = argparse.ArgumentParser(description="runs the BLE peripheral on an ESP32 and talks to it from this machine")
    ap.add_argument("--port", required=True, help="the board's serial port, e.g. /dev/cu.usbserial-0001")
    ap.add_argument("--project", help="the ESP-IDF project to build in (default: a temporary one)")
    args = ap.parse_args()

    mlc = os.path.join(REPO, "bin", "mlc")
    r = run(["go", "build", "-o", mlc, "./cmd/mlc"], cwd=REPO)
    if r.returncode != 0:
        sys.exit("go build failed:\n" + r.stderr)
    proj = os.path.abspath(args.project) if args.project else os.path.join(tempfile.mkdtemp(prefix="mlc-esp32-ble-"), "ble")
    build_and_flash(args.port, proj, mlc)

    console = Console(args.port)
    results = []
    try:
        results.append(("advertising", console.wait(r"^advertising: 0 0$", 20), "the chip advertises"))
        try:
            asyncio.run(central(results))
        except BleakError as e:  # this machine's Bluetooth: off, or not allowed for the terminal
            results.append(("bluetooth", False, f"{e} (turn this machine's Bluetooth on; on macOS, allow it for the terminal)"))
        results.append(("connected", console.wait(r"^connected: 0$", 5), "the chip saw the connection"))
        results.append(("disconnected", console.wait(r"^disconnected$", 10), "the chip saw the disconnection"))
        again = lambda: sum(1 for l in console.lines if l == "advertising: 0 0") >= 2
        end = time.time() + 5
        while not again() and time.time() < end:
            time.sleep(0.1)
        results.append(("advertising again", again(), "after the disconnection"))
    finally:
        console.close()

    failed = [r for r in results if not r[1]]
    for name, ok, detail in results:
        print("ok  " if ok else "FAIL", name, "-", detail)
    if failed:
        print("\nconsole:\n" + "\n".join(console.lines[-40:]))
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
