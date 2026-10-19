package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dartmouth-OpenAV/microservice-framework/framework"
)

// Waveshare Modbus POE ETH Relay coil and register map
// https://www.waveshare.com/wiki/Modbus_POE_ETH_Relay
const (
	coilRelayBase    = 0x0000 // relay N is coil N-1
	coilAllRelays    = 0x00FF
	coilFlashOnBase  = 0x0200 // relay N closes, then opens by itself after value * 100ms
	coilValueOn      = 0xFF00
	coilValueOff     = 0x0000
	flashOnUnit      = 100 * time.Millisecond
	flashOnMaxUnits  = 0x7FFF
	registerFirmware = 0x8000 // firmware version * 100, e.g. 300 = V3.00

	maxProbeRelays = 64 // upper bound when detecting the channel count
	pulseMargin    = 200 * time.Millisecond

	triggerMinSeconds      = 0.1
	triggerMaxSeconds      = 5
	timedTriggerMinSeconds = 1
	timedTriggerMaxSeconds = 30
)

// Number of relays on each device, detected once per socketKey (the board comes in 4, 8, 16 and 32 channel versions)
var relayCounts = make(map[string]int)
var relayCountsMutex sync.Mutex

func reportError(socketKey string, function string, errMsg string) error {
	errMsg = function + " - " + errMsg
	framework.AddToErrors(socketKey, errMsg)
	return errors.New(errMsg)
}

// Finds the highest relay number the device accepts by reading single coils:
// nonexistent relays come back with Modbus exception 0x02 (illegal data address).
func getRelayCount(socketKey string) (int, error) {
	relayCountsMutex.Lock()
	count, ok := relayCounts[socketKey]
	relayCountsMutex.Unlock()
	if ok {
		return count, nil
	}

	relayExists := func(relay int) (bool, error) {
		_, err := readCoils(socketKey, uint16(coilRelayBase+relay-1), 1)
		if isModbusException(err, 0x02) {
			return false, nil
		}
		return err == nil, err
	}

	// binary search for the largest existing relay in [1, maxProbeRelays]
	low, high := 0, maxProbeRelays+1
	for high-low > 1 {
		mid := (low + high) / 2
		exists, err := relayExists(mid)
		if err != nil {
			return 0, err
		}
		if exists {
			low = mid
		} else {
			high = mid
		}
	}
	if low == 0 {
		return 0, errors.New("device doesn't report any relays")
	}

	framework.Log(fmt.Sprintf("getRelayCount - %s has %d relays", socketKey, low))
	relayCountsMutex.Lock()
	relayCounts[socketKey] = low
	relayCountsMutex.Unlock()
	return low, nil
}

func forgetRelayCount(socketKey string) {
	relayCountsMutex.Lock()
	delete(relayCounts, socketKey)
	relayCountsMutex.Unlock()
}

// Validates a relay number from the URI (1-based) against the device's channel count
func parseRelay(socketKey string, function string, relay string) (int, error) {
	relayNum, err := strconv.Atoi(strings.Trim(relay, `"`))
	if err != nil || relayNum < 1 {
		return 0, reportError(socketKey, function, "unrecognized relay value: "+relay)
	}
	count, err := getRelayCount(socketKey)
	if err != nil {
		return 0, reportError(socketKey, function, "couldn't determine relay count: "+err.Error())
	}
	if relayNum > count {
		return 0, reportError(socketKey, function, fmt.Sprintf("relay %d out of range, device has %d relays", relayNum, count))
	}
	return relayNum, nil
}

func parseOnOff(socketKey string, function string, state string) (uint16, error) {
	switch strings.ToLower(strings.Trim(state, `"`)) {
	case "on":
		return coilValueOn, nil
	case "off":
		return coilValueOff, nil
	}
	return 0, reportError(socketKey, function, "unrecognized state value: "+state)
}

// Parses a JSON body number of seconds (e.g. "2" or "1.5") and clamps it to [min, max] like the Global Cache microservice does
func parseSeconds(socketKey string, function string, seconds string, min float64, max float64) (time.Duration, error) {
	value, err := strconv.ParseFloat(strings.Trim(seconds, `"`), 64)
	if err != nil || math.IsNaN(value) {
		return 0, reportError(socketKey, function, "can't parse seconds: "+seconds)
	}
	if value > max {
		reportError(socketKey, function, fmt.Sprintf("seconds can't be greater than %v", max))
		value = max
	}
	if value < min {
		reportError(socketKey, function, fmt.Sprintf("seconds can't be less than %v", min))
		value = min
	}
	return time.Duration(value * float64(time.Second)), nil
}

func setState(socketKey string, relay string, state string) (string, error) {
	function := "setState"

	relayNum, err := parseRelay(socketKey, function, relay)
	if err != nil {
		return relay, err
	}
	value, err := parseOnOff(socketKey, function, state)
	if err != nil {
		return state, err
	}

	if err := writeSingleCoil(socketKey, uint16(coilRelayBase+relayNum-1), value); err != nil {
		return "", reportError(socketKey, function, "error sending command: "+err.Error())
	}
	return "ok", nil
}

func getState(socketKey string, relay string) (string, error) {
	function := "getState"

	relayNum, err := parseRelay(socketKey, function, relay)
	if err != nil {
		return relay, err
	}
	coils, err := readCoils(socketKey, uint16(coilRelayBase+relayNum-1), 1)
	if err != nil {
		return "", reportError(socketKey, function, "error reading state: "+err.Error())
	}
	return onOffJSON(coils[0]), nil
}

// Returns all relay states as a JSON object, e.g. {"1":"off","2":"on",...}
func getStates(socketKey string) (string, error) {
	function := "getStates"

	count, err := getRelayCount(socketKey)
	if err != nil {
		return "", reportError(socketKey, function, "couldn't determine relay count: "+err.Error())
	}
	coils, err := readCoils(socketKey, coilRelayBase, uint16(count))
	if err != nil {
		return "", reportError(socketKey, function, "error reading states: "+err.Error())
	}
	pairs := make([]string, count)
	for i, on := range coils {
		pairs[i] = fmt.Sprintf(`"%d":%s`, i+1, onOffJSON(on))
	}
	return "{" + strings.Join(pairs, ",") + "}", nil
}

func setAllStates(socketKey string, state string) (string, error) {
	function := "setAllStates"

	value, err := parseOnOff(socketKey, function, state)
	if err != nil {
		return state, err
	}
	if err := writeSingleCoil(socketKey, coilAllRelays, value); err != nil {
		return "", reportError(socketKey, function, "error sending command: "+err.Error())
	}
	return "ok", nil
}

func onOffJSON(on bool) string {
	if on {
		return `"on"`
	}
	return `"off"`
}

// Closes the relay for the given duration using the device's hardware "flash on" timer,
// so the relay still opens on time even if we lose the network connection mid-pulse.
func pulseRelay(socketKey string, relayNum int, duration time.Duration) error {
	units := int(math.Round(float64(duration) / float64(flashOnUnit)))
	if units < 1 {
		units = 1
	} else if units > flashOnMaxUnits {
		units = flashOnMaxUnits
	}
	return writeSingleCoil(socketKey, uint16(coilFlashOnBase+relayNum-1), uint16(units))
}

// Called after a pulse should have finished: makes sure the relays really are open, forcing them off if not.
func ensureRelaysOff(socketKey string, function string, relayNums ...int) error {
	for _, relayNum := range relayNums {
		coils, err := readCoils(socketKey, uint16(coilRelayBase+relayNum-1), 1)
		if err != nil {
			return reportError(socketKey, function, "error confirming relay is off: "+err.Error())
		}
		if coils[0] {
			reportError(socketKey, function, fmt.Sprintf("relay %d still on after pulse, turning it off", relayNum))
			if err := writeSingleCoil(socketKey, uint16(coilRelayBase+relayNum-1), coilValueOff); err != nil {
				return reportError(socketKey, function, "error turning relay off: "+err.Error())
			}
		}
	}
	return nil
}

// Momentarily closes a relay: "on" for the number of seconds in the body (0.1 - 5), then back to "off".
// Blocks until the pulse is done, like the Global Cache microservice, so queued commands run afterwards.
func setTrigger(socketKey string, relay string, seconds string) (string, error) {
	function := "setTrigger"

	relayNum, err := parseRelay(socketKey, function, relay)
	if err != nil {
		return relay, err
	}
	duration, err := parseSeconds(socketKey, function, seconds, triggerMinSeconds, triggerMaxSeconds)
	if err != nil {
		return seconds, err
	}

	if err := pulseRelay(socketKey, relayNum, duration); err != nil {
		return "", reportError(socketKey, function, "error sending command: "+err.Error())
	}
	time.Sleep(duration + pulseMargin)
	if err := ensureRelaysOff(socketKey, function, relayNum); err != nil {
		return "", err
	}
	return "ok", nil
}

// Moves a motorized screen for a set time and then stops it, matching the Global Cache timedtrigger:
//  1. pulse relay1 for 1 second (e.g. "down")
//  2. wait until the requested number of seconds (1 - 30) has passed since the start
//  3. pulse relay1 and relay2 together for 1 second (most screen controllers treat up+down as "stop")
func setTimedTrigger(socketKey string, relay1 string, relay2 string, seconds string) (string, error) {
	function := "setTimedTrigger"

	relayNum1, err := parseRelay(socketKey, function, relay1)
	if err != nil {
		return relay1, err
	}
	relayNum2, err := parseRelay(socketKey, function, relay2)
	if err != nil {
		return relay2, err
	}
	duration, err := parseSeconds(socketKey, function, seconds, timedTriggerMinSeconds, timedTriggerMaxSeconds)
	if err != nil {
		return seconds, err
	}

	start := time.Now()
	if err := pulseRelay(socketKey, relayNum1, time.Second); err != nil {
		return "", reportError(socketKey, function, "error sending command: "+err.Error())
	}
	time.Sleep(time.Until(start.Add(duration)))

	if err := pulseRelay(socketKey, relayNum1, time.Second); err != nil {
		return "", reportError(socketKey, function, "error sending stop command: "+err.Error())
	}
	if err := pulseRelay(socketKey, relayNum2, time.Second); err != nil {
		return "", reportError(socketKey, function, "error sending stop command: "+err.Error())
	}
	time.Sleep(time.Second + pulseMargin)
	if err := ensureRelaysOff(socketKey, function, relayNum1, relayNum2); err != nil {
		return "", err
	}
	return "ok", nil
}

func healthCheck(socketKey string) (string, error) {
	function := "healthCheck"

	// re-detect the channel count in case the device behind this address was swapped
	forgetRelayCount(socketKey)

	version, err := readHoldingRegister(socketKey, registerFirmware)
	if err != nil {
		reportError(socketKey, function, err.Error())
		if errors.Is(err, errNoConnection) {
			return `"no connection"`, nil
		}
		return `"false"`, nil
	}
	count, err := getRelayCount(socketKey)
	if err != nil {
		reportError(socketKey, function, "couldn't determine relay count: "+err.Error())
		return `"false"`, nil
	}
	return fmt.Sprintf(`"true firmware: V%d.%02d relays: %d"`, version/100, version%100, count), nil
}
