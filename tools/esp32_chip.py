"""Runs tests/run and tests/panic on an ESP32 and compares its console with
the expected output: the check of the ESP32 target on a real chip, which
`make check` can't do (it uses the simulation of TestESP32Sim). See
docs/esp32.md.

Each program is built with `mlc build -target esp32`, compiled by ESP-IDF,
flashed (only the app partition) and run; about 25 s each. Run it in
ESP-IDF's environment, with its Python (which has pyserial):

    . ~/esp/esp-idf/export.sh
    python tools/esp32_chip.py --port /dev/cu.usbserial-0001

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


def run_on_chip(port, timeout=30.0, idle=3.0):
    """resets the board and returns the program's console output"""
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
            if started is not None and END.search(text.split("Calling app_main()", 1)[-1]):
                break
        elif started is not None and time.time() - last > idle:
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


def main():
    ap = argparse.ArgumentParser(description="runs tests/run and tests/panic on an ESP32")
    ap.add_argument("--port", required=True, help="the board's serial port, e.g. /dev/cu.usbserial-0001")
    ap.add_argument("--project", help="the ESP-IDF project to build in (default: a temporary one; reusing one saves the first full build)")
    ap.add_argument("--target", default="esp32", help="ESP-IDF's chip (default esp32)")
    ap.add_argument("--only", default="", help="comma-separated parts of program names to run")
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
    for kind in ("run", "panic"):
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
            r = run([mlc, "build", "-target", "esp32", "-o", os.path.join(proj, "main"), name], cwd=src_dir)
            if r.returncode != 0:
                if "not available with -target esp32" in r.stderr or "link " in r.stderr:
                    skipped.append(f"{test} (not available on the ESP32)")
                else:
                    failed.append((test, "mlc build failed:\n" + r.stderr))
                    print("FAIL", test, flush=True)
                continue
            set_target(proj, args.target)
            r = run(["idf.py", "-C", proj, "build"])
            if r.returncode != 0:
                errs = [l for l in (r.stdout + r.stderr).splitlines() if "error" in l]
                failed.append((test, "ESP-IDF build failed:\n" + "\n".join(errs[:10])))
                print("FAIL", test, flush=True)
                continue
            # 921600 baud is fast but sometimes too much for the cable or the
            # USB-serial chip ("serial noise"): one more try at half
            for baud in ("921600", "460800"):
                r = run(["idf.py", "-C", proj, "-p", args.port, "-b", baud, "app-flash"])
                if r.returncode == 0:
                    break
            if r.returncode != 0:
                failed.append((test, "flash failed:\n" + r.stdout[-500:] + r.stderr[-500:]))
                print("FAIL", test, flush=True)
                continue
            got = run_on_chip(args.port)
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
