"""Runs tests/run, tests/panic and tests/esp32 (the programs that only run on
the chip, like a C callback from ESP-IDF's event loop) on an ESP32 and
compares its console with the expected output: the check of the ESP32 target on a real chip, which
`make check` can't do (it uses the simulation of TestESP32Sim). See
docs/esp32.md.

Each program is built with `mlc build -target esp32`, compiled by ESP-IDF,
flashed (only the app partition) and run; about 25 s each. Run it in
ESP-IDF's environment, with its Python (which has pyserial):

    . ~/esp/esp-idf/export.sh
    python tools/esp32_chip.py --port /dev/cu.usbserial-0001
    python tools/esp32_chip.py --port /dev/cu.usbserial-0001 --wifi

--wifi runs std/wifi's checks instead of the tests: a network that doesn't
exist (NOT_FOUND) always, and, when MLC_WIFI_SSID and MLC_WIFI_PASSWORD are
in the environment, a real connection and an HTTP GET to example.com. The
password goes only into the temporary program flashed to the chip; it isn't
printed.

What differs from the desktop is handled here:
  - stdout, stderr and the panic message come through the same UART, so the
    lines the program writes to stderr on the desktop may be among the output;
  - a panic aborts and reboots the chip: the output ends there;
  - programs with .args or .in are skipped (argv is only the name, no stdin),
    and so are the ones whose imports the target refuses;
  - the thread and server tests start several 64 KB threads at once, which don't fit
    in the ESP32's RAM: they run with 16 KB stacks (SMALL_STACKS);
  - stack_recursion.mlc panics at its first recursion in the 32 KB task, as
    TestESP32Sim expects too.
"""
import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

import serial

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# what ESP-IDF and the ROM print around the program
NOISE = re.compile(r"^([IWE] \(\d+\) |ets |rst:|configsip:|clk_drv:|mode:|load:|entry )")
# what ends a run: the panic's exit aborts and reboots the chip
END = re.compile(r"abort\(\) was called|Rebooting\.\.\.|Guru Meditation|Backtrace:")
# the stack_recursion difference that TestESP32Sim expects too
EXPECTED_STDOUT = {"stack_recursion.mlc": ""}
# the programs that run with their thread stacks changed to 16 KB
SMALL_STACKS = {"threads.mlc", "channels.mlc", "mutex.mlc", "server.mlc"}


def run(args, **kw):
    return subprocess.run(args, capture_output=True, text=True, **kw)


def make_project(proj):
    """an ESP-IDF project whose main component is the one mlc writes"""
    if os.path.exists(os.path.join(proj, "CMakeLists.txt")):
        return
    r = run(["idf.py", "create-project", "-p", proj, os.path.basename(proj.rstrip("/"))])
    if r.returncode != 0:
        sys.exit("idf.py create-project failed:\n" + r.stdout + r.stderr)
    main = os.path.join(proj, "main")
    for f in os.listdir(main):
        os.remove(os.path.join(main, f))


def set_target(proj, target):
    """configures the project for the chip, once, after mlc wrote the main
    component: configured without main's CMakeLists.txt, ESP-IDF leaves the
    component out, and app_main doesn't link"""
    if os.path.exists(os.path.join(proj, "sdkconfig")):
        return
    r = run(["idf.py", "-C", proj, "set-target", target])
    if r.returncode != 0:
        sys.exit("idf.py set-target failed:\n" + r.stdout[-2000:] + r.stderr)


def run_on_chip(port, timeout=30.0, idle=3.0, until=None):
    """resets the board and returns the program's console output; with
    until (a regex), waits for it instead of for silence"""
    s = serial.Serial(port, 115200, timeout=0.1)
    s.dtr = False
    s.rts = True
    time.sleep(0.1)
    s.rts = False
    buf = b""
    started = None
    last = time.time()
    t0 = time.time()
    while time.time() - t0 < timeout:
        data = s.read(4096)
        if data:
            buf += data
            last = time.time()
            text = buf.decode("utf-8", "replace")
            if started is None and "Calling app_main()" in text:
                started = time.time()
            after = text.split("Calling app_main()", 1)[-1]
            if started is not None and (END.search(after) or (until is not None and re.search(until, after))):
                break
        elif started is not None and until is None and time.time() - last > idle:
            break
    s.close()
    text = buf.decode("utf-8", "replace").replace("\r\n", "\n")
    text = text.split("Calling app_main()\n", 1)[-1]
    out = []
    for line in text.split("\n"):
        if END.search(line):
            break
        if line.startswith("assert failed") or NOISE.match(line):
            continue
        out.append(line)
    return "\n".join(out).rstrip("\n")


def desktop_stderr(mlc, d, name, tmp):
    """the lines the program writes to stderr on this machine"""
    exe = os.path.join(tmp, "desktop")
    if run([mlc, "build", "-o", exe, name], cwd=d).returncode != 0:
        return []
    return run([exe], cwd=d).stderr.splitlines()


def matches(got, want, err_lines):
    if got == want:
        return True
    g = got.split("\n")
    for line in err_lines:
        if line in g:
            g.remove(line)
    return "\n".join(g).rstrip("\n") == want


def read(path):
    try:
        with open(path) as f:
            return f.read()
    except FileNotFoundError:
        return ""


def flash_and_run(proj, port, target, mlc, src_dir, name, until=None):
    """builds the program into the project, flashes it and returns
    ("ok", console), ("skip", why) or ("fail", why)"""
    r = run([mlc, "build", "-target", "esp32", "-o", os.path.join(proj, "main"), name], cwd=src_dir)
    if r.returncode != 0:
        if "not available with -target esp32" in r.stderr or "link " in r.stderr:
            return "skip", "not available on the ESP32"
        return "fail", "mlc build failed:\n" + r.stderr
    set_target(proj, target)
    r = run(["idf.py", "-C", proj, "build"])
    if r.returncode != 0:
        errs = [l for l in (r.stdout + r.stderr).splitlines() if "error" in l]
        return "fail", "ESP-IDF build failed:\n" + "\n".join(errs[:10])
    # 921600 baud is fast but sometimes too much for the cable or the
    # USB-serial chip ("serial noise"): one more try at half
    for baud in ("921600", "460800"):
        r = run(["idf.py", "-C", proj, "-p", port, "-b", baud, "app-flash"])
        if r.returncode == 0:
            break
    if r.returncode != 0:
        return "fail", "flash failed:\n" + r.stdout[-500:] + r.stderr[-500:]
    return "ok", run_on_chip(port, timeout=60.0, until=until) if until else run_on_chip(port)


def mlc_literal(text):
    return '"' + text.replace("\\", "\\\\").replace('"', '\\"') + '"'


WIFI_NOT_FOUND = """import "std/io"
import "std/wifi"

fn main() {
    err := 0
    ok := wifi.connect_str("mlc-no-such-network-7f3a", "", 10000, &err)
    io.print("connect:", ok, err == wifi.NOT_FOUND)
    wifi.disconnect()
    io.print("connected:", wifi.connected())
    io.print("done")
}
"""

WIFI_REAL = """import "std/http"
import "std/io"
import "std/net"
import "std/str"
import "std/wifi"

fn main() {
    err := 0
    ok := wifi.connect_str(SSID, PASSWORD, 20000, &err)
    io.print("connect:", ok, err)
    if ok {
        var ip [16]byte
        io.print("address:", wifi.ip(&ip) > 0, "signal:", wifi.rssi() < 0)
        var host [16]byte
        hn := str.copy(&host, "example.com")
        c := net.connect(&host[:hn], 80, &err)
        if c != nil {
            var b http.Builder
            b.request("GET", "/")
            b.header("Host", "example.com")
            b.header("Connection", "close")
            b.send_str(&c, "")
            var m http.Message
            match m.read_response(&c) {
                Ok(_) => io.print("http:", m.status())
                Err(code) => io.print("http error:", code)
            }
            c.close()
        } else {
            io.print("net.connect:", err)
        }
        wifi.disconnect()
        io.print("connected:", wifi.connected())
    }
    io.print("done")
}
"""


def wifi_checks(args, mlc, proj, tmp):
    """std/wifi on the chip: [(name, source, expected console)]"""
    checks = [("wifi_not_found.mlc", WIFI_NOT_FOUND, "connect: false true\nconnected: false\ndone")]
    ssid, password = os.environ.get("MLC_WIFI_SSID"), os.environ.get("MLC_WIFI_PASSWORD")
    if ssid:
        src = WIFI_REAL.replace("SSID", mlc_literal(ssid), 1).replace("PASSWORD", mlc_literal(password or ""), 1)
        checks.append(("wifi_real.mlc", src, "connect: true 0\naddress: true signal: true\nhttp: 200\nconnected: false\ndone"))
    else:
        print("(MLC_WIFI_SSID isn't set: only the network that doesn't exist)", flush=True)
    d = os.path.join(tmp, "wifi")
    os.makedirs(d, exist_ok=True)
    passed, failed = [], []
    for name, src, want in checks:
        with open(os.path.join(d, name), "w") as f:
            f.write(src)
        status, got = flash_and_run(proj, args.port, args.target, mlc, d, name, until=r"(?m)^done$")
        os.remove(os.path.join(d, name)) # the password doesn't stay on disk
        if status == "ok" and got == want:
            passed.append(name)
            print("ok  ", name, flush=True)
        else:
            failed.append((name, got if status != "ok" else f"got:\n{got}\nexpected:\n{want}"))
            print("FAIL", name, flush=True)
    return passed, failed


def main():
    ap = argparse.ArgumentParser(description="runs tests/run and tests/panic on an ESP32")
    ap.add_argument("--port", required=True, help="the board's serial port, e.g. /dev/cu.usbserial-0001")
    ap.add_argument("--project", help="the ESP-IDF project to build in (default: a temporary one; reusing one saves the first full build)")
    ap.add_argument("--target", default="esp32", help="ESP-IDF's chip (default esp32)")
    ap.add_argument("--only", default="", help="comma-separated parts of program names to run")
    ap.add_argument("--wifi", action="store_true", help="run std/wifi's checks instead of the tests (see above)")
    args = ap.parse_args()

    mlc = os.path.join(REPO, "bin", "mlc")
    r = run(["go", "build", "-o", mlc, "./cmd/mlc"], cwd=REPO)
    if r.returncode != 0:
        sys.exit("go build failed:\n" + r.stderr)
    if shutil.which("idf.py") is None:
        sys.exit("idf.py not found: run . ~/esp/esp-idf/export.sh first")

    tmp = tempfile.mkdtemp(prefix="mlc-esp32-chip-")
    proj = os.path.abspath(args.project) if args.project else os.path.join(tmp, "chip")
    make_project(proj)
    only = args.only.split(",")

    passed, failed, skipped = [], [], []
    if args.wifi:
        passed, failed = wifi_checks(args, mlc, proj, tmp)
    for kind in ("run", "panic", "esp32") if not args.wifi else ():
        d = os.path.join(REPO, "tests", kind)
        for name in sorted(os.listdir(d)):
            if not name.endswith(".mlc") or not any(o in name for o in only):
                continue
            test = f"{kind}/{name}"
            base = os.path.join(d, name[:-4])
            if os.path.exists(base + ".args") or os.path.exists(base + ".in"):
                skipped.append(f"{test} (arguments or stdin)")
                continue
            src_dir = d
            if name in SMALL_STACKS:
                src_dir = os.path.join(tmp, "small")
                os.makedirs(src_dir, exist_ok=True)
                with open(os.path.join(d, name)) as f:
                    src = f.read()
                with open(os.path.join(src_dir, name), "w") as f:
                    f.write(re.sub(r"thread\.STACK_(64KB|256KB)", "thread.STACK_16KB", src))
            status, got = flash_and_run(proj, args.port, args.target, mlc, src_dir, name)
            if status == "skip":
                skipped.append(f"{test} ({got})")
                continue
            if status == "fail":
                failed.append((test, got))
                print("FAIL", test, flush=True)
                continue
            want = EXPECTED_STDOUT.get(name, read(base + ".out"))
            if kind == "panic":
                want += read(base + ".err")
            want = want.rstrip("\n")
            err_lines = [] if kind == "panic" else desktop_stderr(mlc, src_dir, name, tmp)
            if matches(got, want, err_lines):
                passed.append(test)
                print("ok  ", test, flush=True)
            else:
                failed.append((test, f"got:\n{got}\nexpected:\n{want}"))
                print("FAIL", test, flush=True)

    print(f"\n{len(passed)} passed, {len(failed)} failed, {len(skipped)} skipped")
    for test, why in failed:
        print(f"\n=== {test}\n{why}")
    if skipped:
        print("\nskipped:\n  " + "\n  ".join(skipped))
    shutil.rmtree(tmp, ignore_errors=True)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
