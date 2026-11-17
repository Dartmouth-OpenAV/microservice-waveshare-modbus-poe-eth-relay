#!/usr/bin/env python3
"""
Live test loop for the Waveshare relay microservice against real hardware.

Drives the microservice's REST API and independently verifies every result by reading the relay
coils straight from the device over Modbus (RTU over TCP), so we're checking what the hardware
actually did rather than the microservice's cache.

WARNING: this switches every relay on the device. Disconnect anything (like a screen motor) first.

Usage:
    python3 tests/live_test_loop.py --device DEVICE_IP [--microservice localhost:8080] [--port 4196] [--loops 1]

--device is required and has no default, so the test can't switch a production relay by accident.

Only uses the Python standard library.
"""

import argparse
import json
import socket
import sys
import time
import urllib.error
import urllib.request


# ---------- direct Modbus access (independent of the microservice) ----------

def crc16(data):
    crc = 0xFFFF
    for b in data:
        crc ^= b
        for _ in range(8):
            crc = (crc >> 1) ^ 0xA001 if crc & 1 else crc >> 1
    return bytes([crc & 0xFF, crc >> 8])


def read_coils(device, port, count):
    request = bytes([0x01, 0x01, 0x00, 0x00, 0x00, count])
    with socket.create_connection((device, port), timeout=3) as sock:
        sock.sendall(request + crc16(request))
        expected = 3 + (count + 7) // 8 + 2
        response = b""
        while len(response) < expected:
            chunk = sock.recv(256)
            if not chunk:
                break
            response += chunk
            if len(response) >= 5 and response[1] & 0x80:
                break
    if len(response) < expected or response[1] != 0x01 or crc16(response[:-2]) != response[-2:]:
        raise RuntimeError("bad read coils response: " + response.hex(" "))
    bits = int.from_bytes(response[3:-2], "little")
    return [bool(bits >> i & 1) for i in range(count)]


# ---------- microservice REST helpers ----------

class Tester:
    def __init__(self, args):
        self.base = "http://%s/%s" % (args.microservice, args.device)
        self.device = args.device
        self.port = args.port
        self.relays = 0
        self.failures = 0
        self.passes = 0

    def request(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                text = resp.read().decode()
                return resp.status, json.loads(text) if text.strip() else None
        except urllib.error.HTTPError as e:
            text = e.read().decode()
            return e.code, json.loads(text) if text.strip() else None

    def put(self, path, body):
        status, value = self.request("PUT", path, body)
        self.check(status == 200 and value == "ok", "PUT %s %s -> %s %s" % (path, json.dumps(body), status, value))

    def get_fresh(self, path, timeout=10):
        """GET an endpoint, waiting until the framework has a real value from the device.

        The first GET returns 204. While the framework's first device read is in flight it
        caches "unknown" as a placeholder and returns that with a 200, so skip it too."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            status, value = self.request("GET", path)
            if status == 200 and value != "unknown":
                return value
            time.sleep(0.25)
        raise RuntimeError("GET %s never returned 200" % path)

    def coils(self):
        return read_coils(self.device, self.port, self.relays)

    def wait_for_coils(self, expected, timeout=3):
        deadline = time.time() + timeout
        actual = None
        while time.time() < deadline:
            actual = self.coils()
            if actual == expected:
                return True, actual
            time.sleep(0.1)
        return False, actual

    def check(self, ok, description):
        if ok:
            self.passes += 1
            print("  PASS  " + description)
        else:
            self.failures += 1
            print("  FAIL  " + description)

    def expect_coils(self, expected, description):
        ok, actual = self.wait_for_coils(expected)
        self.check(ok, "%s (device: %s)" % (description, fmt(actual)))

    def all(self, on):
        return [on] * self.relays

    def only(self, *relays):
        return [i + 1 in relays for i in range(self.relays)]

    def sample_coils(self, seconds, interval=0.1):
        """Reads the coils for a while and returns [(elapsed, coils)]"""
        samples = []
        start = time.time()
        while time.time() - start < seconds:
            samples.append((time.time() - start, self.coils()))
            time.sleep(interval)
        return samples


def fmt(coils):
    return "".join("1" if c else "0" for c in coils) if coils is not None else "?"


def on_window(samples, relay):
    """Returns (first, last) elapsed time the relay was seen on, or None"""
    times = [t for t, coils in samples if coils[relay - 1]]
    return (times[0], times[-1]) if times else None


# ---------- the tests ----------

def run(t):
    print("\nhealthcheck")
    health = t.get_fresh("/healthcheck")
    t.check(isinstance(health, str) and health.startswith("true"), "healthcheck: %s" % health)
    t.relays = int(health.rsplit("relays:", 1)[1])
    t.request("GET", "/errors")  # clear any errors from earlier runs

    print("\nall off / all on")
    t.put("/allstates", "off")
    t.expect_coils(t.all(False), "all relays off")
    t.put("/allstates", "on")
    t.expect_coils(t.all(True), "all relays on")
    t.put("/allstates", "off")
    t.expect_coils(t.all(False), "all relays off again")

    print("\nindividual relays")
    for relay in range(1, t.relays + 1):
        t.put("/state/%d" % relay, "on")
        t.expect_coils(t.only(relay), "relay %d on, others off" % relay)
        t.put("/state/%d" % relay, "off")
        t.expect_coils(t.all(False), "relay %d off" % relay)

    print("\nreading state back through the microservice")
    t.put("/state/2", "on")
    t.expect_coils(t.only(2), "relay 2 on")
    # /states isn't touched by PUTs, so its value comes from the device. Once cached, the framework only
    # re-reads it every RefreshDataPointsEvery (50s), so on later loops we wait for that refresh.
    expected = {str(i): "on" if i == 2 else "off" for i in range(1, t.relays + 1)}
    started = time.time()
    states = t.get_fresh("/states")
    while states != expected and time.time() - started < 65:
        time.sleep(1)
        states = t.get_fresh("/states")
    t.check(states == expected, "GET states matches device after %.0fs: %s" % (time.time() - started, json.dumps(states)))
    t.put("/state/2", "off")
    t.expect_coils(t.all(False), "relay 2 off")

    print("\ntrigger (momentary pulse)")
    t.put("/trigger/3", "1.5")
    samples = t.sample_coils(3)
    window = on_window(samples, 3)
    others = any(c for _, coils in samples for i, c in enumerate(coils) if i != 2)
    t.check(window is not None and 1.0 <= window[1] - window[0] <= 1.8,
            "relay 3 pulsed ~1.5s (seen on from %s)" % (window and "%.2fs to %.2fs" % window))
    t.check(not others, "no other relays switched during trigger")
    t.check(not samples[-1][1][2], "relay 3 off after trigger")

    print("\ntimedtrigger (screen down for 3s then stop)")
    t.put("/timedtrigger/1/2", "3")
    samples = t.sample_coils(6)
    r1 = [t_ for t_, coils in samples if coils[0]]
    both = [t_ for t_, coils in samples if coils[0] and coils[1]]
    t.check(bool(r1), "relay 1 pulsed")
    start = r1[0] if r1 else 0
    t.check(bool(both) and 2.6 <= both[0] - start <= 3.4,
            "relays 1+2 pulsed together ~3s after start (at %s)" % (both and "%.2fs" % (both[0] - start)))
    # relay 1's flash timer starts ~20ms before relay 2's, so relay 2 is briefly alone at the very end of the stop
    r2_early = [t_ for t_, coils in samples if coils[1] and (not both or t_ < both[0])]
    t.check(not r2_early, "relay 2 not on before the stop")
    t.check(samples[-1][1] == t.all(False), "all off after timedtrigger (device: %s)" % fmt(samples[-1][1]))

    print("\nbad input")
    t.put("/state/%d" % (t.relays + 1), "on")  # PUTs are async, so the error shows up in /errors
    time.sleep(1)
    status, errs = t.request("GET", "/errors")
    t.check(status == 500 and any("out of range" in e for e in (errs or {}).values()),
            "out of range relay reported in /errors")
    t.expect_coils(t.all(False), "bad input didn't switch anything")

    print("\nunexpected errors")
    status, errs = t.request("GET", "/errors")
    t.check(not errs, "no errors reported by microservice: %s" % errs)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--microservice", default="localhost:8080")
    parser.add_argument("--device", required=True,
                        help="address of a test relay (never a production device: every relay gets switched)")
    parser.add_argument("--port", type=int, default=4196, help="device Modbus RTU over TCP port")
    parser.add_argument("--loops", type=int, default=1)
    args = parser.parse_args()

    t = Tester(args)
    for loop in range(1, args.loops + 1):
        print("=== loop %d of %d ===" % (loop, args.loops))
        try:
            run(t)
        except Exception as e:  # keep looping, but count it
            t.check(False, "exception: %r" % e)

    print("\n%d passed, %d failed" % (t.passes, t.failures))
    sys.exit(1 if t.failures else 0)


if __name__ == "__main__":
    main()
