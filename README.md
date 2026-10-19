# openav microservice waveshare relay

OpenAV microservice for the Waveshare Modbus POE ETH Relay. Uses the microservice framework and Modbus to control the relays.

The Waveshare Modbus POE ETH Relay is a PoE network relay board, controlled over Modbus, that provides contact closures for devices with relay-based inputs (e.g., projector screens, window shades). It's used like the Global Caché IP2CC-P, and this microservice offers the same API as [microservice-global-cache](https://github.com/Dartmouth-OpenAV/microservice-global-cache), plus a few extra endpoints.

[Waveshare Modbus POE ETH Relay wiki](https://www.waveshare.com/wiki/Modbus_POE_ETH_Relay)

[curl test documentation](https://github.com/Dartmouth-OpenAV/documentation/blob/main/curl_test_readme.md)

## Endpoints

Relays are numbered from 1. The microservice detects how many relays the device has, so the 4, 8, 16 and 32 channel versions all work.

| Method | URI | Body | Description |
|---|---|---|---|
| GET | `/:address/state/:relay` | | `"on"` or `"off"` |
| GET | `/:address/states` | | all relays, e.g. `{"1":"off","2":"on",...}` |
| GET | `/:address/healthcheck` | | `"true firmware: V3.00 relays: 8"`, `"false"`, or `"no connection"` |
| GET | `/:address/errors` | | errors logged for this device (framework) |
| PUT | `/:address/state/:relay` | `"on"` / `"off"` | latch a relay on or off |
| PUT | `/:address/allstates` | `"on"` / `"off"` | all relays at once |
| PUT | `/:address/trigger/:relay` | `"1"` (0.1 – 5 seconds) | momentary contact: on, wait, off |
| PUT | `/:address/timedtrigger/:relay1/:relay2` | `"15"` (1 – 30 seconds) | trigger relay1, wait, then trigger relay1 + relay2 together (move a screen, then stop it) |

As in every OpenAV microservice, the first GET of an endpoint returns HTTP 204 until the microservice has polled the device. After that it serves the cached value and keeps it refreshed.

### Projector screen example

With "down" wired to relay 1 and "up" wired to relay 2. Every PUT takes its value as a JSON request body. For `trigger` and `timedtrigger` the body is a number of seconds, sent as a JSON string like the other OpenAV microservices do (a bare number such as `1` works too).

Screen down: close relay 1 for 1 second. The screen's controller takes the momentary contact and runs the motor down to its limit.
```
curl -X PUT http://localhost:8080/10.8.80.12/trigger/1 -H "Content-Type: application/json" -d '"1"'
```

Screen up: close relay 2 for 1 second.
```
curl -X PUT http://localhost:8080/10.8.80.12/trigger/2 -H "Content-Type: application/json" -d '"1"'
```

Screen down for 12 seconds, then stop. This pulses relay 1 ("down") for 1 second at the start, then 12 seconds after the start pulses relays 1 and 2 together for 1 second, which most screen controllers treat as "stop". In the path, the first relay moves the screen and the second is pulsed along with it to stop it. The body gives the seconds.
```
curl -X PUT http://localhost:8080/10.8.80.12/timedtrigger/1/2 -H "Content-Type: application/json" -d '"12"'
```

```
time:    0s        1s                      12s       13s
relay 1: [on------]off.....................[on------]off
relay 2: off...............................[on------]off
screen:  starts down ------ moving ------> stops
```

`trigger` and `timedtrigger` use the device's hardware "flash on" timer for each pulse. If the network or the microservice drops mid-pulse, the relay still opens on time and a motor won't be left latched. After each pulse the microservice reads the relays back and forces them off if any are still closed.

A trigger or timedtrigger blocks the framework until it finishes, as it does in the Global Cache microservice. Commands queued behind it run once the pulse is done.

## Device setup

- Factory IP is 192.168.1.254. Set the IP in the device's web page (`http://<device ip>`). If no password has been set, any password is accepted.
- **Default protocol (recommended):** Transfer Protocol "None" (Modbus RTU over TCP) on port **4196**. This is the microservice's default port, so `/:address` can just be the IP.
- **Modbus TCP:** if the device is set to Transfer Protocol "Modbus TCP Protocol", it listens on port 502. Use `/:address:502/...`; the microservice switches to Modbus TCP framing automatically when the port is 502. To force a framing on any port, set the `MODBUS_PROTOCOL` environment variable to `rtu` or `tcp`.
- Waveshare recommends the gateway's non-storage (multi-host) mode.

## Running

```
docker build -t microservice-waveshare-relay .
docker run -d -p 8080:80 microservice-waveshare-relay
curl localhost:8080/10.8.80.12/healthcheck
```

The Docker build runs the unit tests (`source/driver_test.go`). They use a simulated Waveshare relay that speaks both RTU and Modbus TCP framing, so no hardware is needed at build time.

The Dockerfile is a multistage build. The `build` stage uses `golang:latest` to vet, test and compile. The final image is `scratch` with only the static binary in it, about 10 MB instead of 1.6 GB. The binary is built with `-tags timetzdata` because the framework timestamps its logs in America/New_York and `scratch` has no time zone database. Since there's no shell in the image, use `docker logs` to see output; `docker exec` into it won't work.

## Testing against hardware

> These tests switch every relay on the device. Disconnect anything connected, such as a screen motor, before running them.

`tests/live_test_loop.py` (Python standard library only) drives the microservice's REST API and checks every result with Modbus reads made directly against the device. It doesn't rely on the microservice's own cache. It covers all-on/off, each relay on its own, state readback, trigger pulse timing, timedtrigger sequencing, bad input, and `/errors`.

```
python3 tests/live_test_loop.py --microservice localhost:8080 --device 10.8.80.12 --loops 5
```

`waveshare_relay_curl_tests.sh` and `Waveshare_Relay.postman_collection.json` exercise every endpoint by hand, the same way as the Global Cache equivalents.

## Framework

`source/microservice-framework` is the [microservice-framework](https://github.com/Dartmouth-OpenAV/microservice-framework) git submodule (see `.gitmodules`). The framework's socket helpers read text lines up to a delimiter, which doesn't work for binary Modbus frames. So `source/modbus.go` opens its own short-lived TCP connection for each Modbus transaction. Calls still go through the framework's function stack and device lock.
