package main

// Tests run against an in-process fake Waveshare relay that implements the same Modbus subset
// as the real device (including its exception behavior), over both RTU-over-TCP and Modbus TCP framing.

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRelay struct {
	t        *testing.T
	listener net.Listener
	tcp      bool // Modbus TCP framing instead of RTU
	mutex    sync.Mutex
	coils    []bool
}

func newFakeRelay(t *testing.T, relays int, tcp bool) *fakeRelay {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRelay{t: t, listener: listener, tcp: tcp, coils: make([]bool, relays)}
	t.Cleanup(func() { listener.Close() })
	go f.serve()
	if tcp {
		modbusProtocol = "tcp"
	} else {
		modbusProtocol = "rtu"
	}
	return f
}

func (f *fakeRelay) socketKey() string {
	return ":@" + f.listener.Addr().String()
}

func (f *fakeRelay) coil(relay int) bool {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.coils[relay-1]
}

func (f *fakeRelay) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeRelay) handle(conn net.Conn) {
	defer conn.Close()
	for {
		var header, pdu []byte
		if f.tcp {
			header = make([]byte, 7)
			if _, err := io.ReadFull(conn, header); err != nil {
				return
			}
			pdu = make([]byte, binary.BigEndian.Uint16(header[4:])-1)
			if _, err := io.ReadFull(conn, pdu); err != nil {
				return
			}
		} else {
			frame := make([]byte, 8) // every request we send is unit + 5 byte PDU + CRC
			if _, err := io.ReadFull(conn, frame); err != nil {
				return
			}
			if !bytes.Equal(crc16(frame[:6]), frame[6:]) {
				f.t.Errorf("bad CRC in request % X", frame)
				return
			}
			pdu = frame[1:6]
		}

		resp := f.process(pdu)
		if f.tcp {
			binary.BigEndian.PutUint16(header[4:], uint16(len(resp)+1))
			conn.Write(append(header, resp...))
		} else {
			frame := append([]byte{modbusUnitID}, resp...)
			conn.Write(append(frame, crc16(frame)...))
		}
	}
}

func (f *fakeRelay) process(pdu []byte) []byte {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	fc := pdu[0]
	address := int(binary.BigEndian.Uint16(pdu[1:]))
	value := int(binary.BigEndian.Uint16(pdu[3:]))
	exception := func(code byte) []byte { return []byte{fc | 0x80, code} }
	n := len(f.coils)

	switch fc {
	case fcReadCoils:
		if address >= n {
			return exception(0x02)
		}
		if address+value > n {
			return exception(0x03)
		}
		resp := []byte{fc, byte((value + 7) / 8)}
		resp = append(resp, make([]byte, resp[1])...)
		for i := 0; i < value; i++ {
			if f.coils[address+i] {
				resp[2+i/8] |= 1 << (i % 8)
			}
		}
		return resp
	case fcWriteSingleCoil:
		switch {
		case address < n:
			f.coils[address] = value == coilValueOn
		case address == coilAllRelays:
			for i := range f.coils {
				f.coils[i] = value == coilValueOn
			}
		case address >= coilFlashOnBase && address < coilFlashOnBase+n:
			relay := address - coilFlashOnBase
			f.coils[relay] = true
			time.AfterFunc(time.Duration(value)*flashOnUnit, func() {
				f.mutex.Lock()
				f.coils[relay] = false
				f.mutex.Unlock()
			})
		default:
			return exception(0x02)
		}
		return pdu
	case fcReadHoldingRegisters:
		switch address {
		case registerFirmware:
			return []byte{fc, 2, 0x01, 0x2C} // V3.00, same as the test unit
		case 0x4000:
			return []byte{fc, 2, 0x00, 0x01}
		}
		return exception(0x02)
	}
	return exception(0x01)
}

func TestCRC16MatchesWaveshareDocs(t *testing.T) {
	examples := map[string]string{
		"01 05 00 00 FF 00": "8C 3A",
		"01 05 00 00 00 00": "CD CA",
		"01 01 00 00 00 08": "3D CC",
		"01 05 02 00 00 07": "8D B0",
		"01 03 80 00 00 01": "AD CA",
	}
	for frame, want := range examples {
		if got := bytesToHex(crc16(hexToBytes(t, frame))); got != want {
			t.Errorf("crc16(%s) = %s, want %s", frame, got, want)
		}
	}
}

func hexToBytes(t *testing.T, s string) []byte {
	var out []byte
	for _, field := range strings.Fields(s) {
		var b byte
		for _, c := range field {
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= byte(c - '0')
			case c >= 'A' && c <= 'F':
				b |= byte(c-'A') + 10
			default:
				t.Fatalf("bad hex %q", s)
			}
		}
		out = append(out, b)
	}
	return out
}

func bytesToHex(b []byte) string {
	const digits = "0123456789ABCDEF"
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = string([]byte{digits[x>>4], digits[x&0xF]})
	}
	return strings.Join(parts, " ")
}

func TestRelayCountDetection(t *testing.T) {
	for _, relays := range []int{1, 4, 8, 16, 32} {
		f := newFakeRelay(t, relays, false)
		got, err := getRelayCount(f.socketKey())
		if err != nil || got != relays {
			t.Errorf("getRelayCount = %d, %v; want %d", got, err, relays)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	for _, tcp := range []bool{false, true} {
		f := newFakeRelay(t, 8, tcp)
		key := f.socketKey()

		if _, err := setState(key, "3", `"on"`); err != nil {
			t.Fatalf("tcp=%v setState: %v", tcp, err)
		}
		if !f.coil(3) || f.coil(2) || f.coil(4) {
			t.Errorf("tcp=%v relay 3 should be the only one on", tcp)
		}
		if got, err := getState(key, "3"); got != `"on"` || err != nil {
			t.Errorf("tcp=%v getState(3) = %s, %v", tcp, got, err)
		}
		if got, _ := getStates(key); got != `{"1":"off","2":"off","3":"on","4":"off","5":"off","6":"off","7":"off","8":"off"}` {
			t.Errorf("tcp=%v getStates = %s", tcp, got)
		}
		if _, err := setState(key, "3", `"off"`); err != nil || f.coil(3) {
			t.Errorf("tcp=%v relay 3 should be off: %v", tcp, err)
		}
		if got, _ := getState(key, "3"); got != `"off"` {
			t.Errorf("tcp=%v getState(3) = %s", tcp, got)
		}
	}
}

func TestAllStates(t *testing.T) {
	f := newFakeRelay(t, 4, false)
	if _, err := setAllStates(f.socketKey(), `"on"`); err != nil {
		t.Fatal(err)
	}
	if got, _ := getStates(f.socketKey()); got != `{"1":"on","2":"on","3":"on","4":"on"}` {
		t.Errorf("getStates = %s", got)
	}
	setAllStates(f.socketKey(), `"off"`)
	if got, _ := getStates(f.socketKey()); got != `{"1":"off","2":"off","3":"off","4":"off"}` {
		t.Errorf("getStates = %s", got)
	}
}

func TestBadInput(t *testing.T) {
	f := newFakeRelay(t, 8, false)
	key := f.socketKey()
	for _, relay := range []string{"0", "9", "-1", "abc", ""} {
		if _, err := setState(key, relay, `"on"`); err == nil {
			t.Errorf("setState(%q) should fail", relay)
		}
	}
	if _, err := setState(key, "1", `"maybe"`); err == nil {
		t.Error("setState with bad value should fail")
	}
	if _, err := setTrigger(key, "1", `"soon"`); err == nil {
		t.Error("setTrigger with bad seconds should fail")
	}
	for i := 1; i <= 8; i++ {
		if f.coil(i) {
			t.Errorf("relay %d changed on bad input", i)
		}
	}
}

func TestTrigger(t *testing.T) {
	f := newFakeRelay(t, 8, false)
	done := make(chan error)
	start := time.Now()
	go func() {
		_, err := setTrigger(f.socketKey(), "2", `"0.5"`)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if !f.coil(2) {
		t.Error("relay 2 should be on during the pulse")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.coil(2) {
		t.Error("relay 2 should be off after the pulse")
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Errorf("setTrigger returned after %v, should block for the pulse", elapsed)
	}
}

func TestTimedTrigger(t *testing.T) {
	f := newFakeRelay(t, 8, false)
	done := make(chan error)
	start := time.Now()
	go func() {
		_, err := setTimedTrigger(f.socketKey(), "1", "2", `"2"`)
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // relay 1 pulsing ("down")
	if !f.coil(1) || f.coil(2) {
		t.Error("only relay 1 should be on at the start")
	}
	time.Sleep(1200 * time.Millisecond) // gap before the stop
	if f.coil(1) || f.coil(2) {
		t.Error("both relays should be off while the screen moves")
	}
	time.Sleep(800 * time.Millisecond) // stop: both pulsing
	if !f.coil(1) || !f.coil(2) {
		t.Error("both relays should be on for the stop")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.coil(1) || f.coil(2) {
		t.Error("both relays should be off at the end")
	}
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Errorf("setTimedTrigger returned after %v", elapsed)
	}
}

func TestHealthCheck(t *testing.T) {
	f := newFakeRelay(t, 8, false)
	if got, _ := healthCheck(f.socketKey()); got != `"true firmware: V3.00 relays: 8"` {
		t.Errorf("healthCheck = %s", got)
	}

	// a port nothing is listening on
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	closedKey := ":@" + listener.Addr().String()
	listener.Close()
	if got, _ := healthCheck(closedKey); got != `"no connection"` {
		t.Errorf("healthCheck on closed port = %s", got)
	}
}

func TestExceptionDecoding(t *testing.T) {
	f := newFakeRelay(t, 8, false)
	_, err := readCoils(f.socketKey(), 8, 1)
	if !isModbusException(err, 0x02) {
		t.Errorf("expected illegal data address exception, got %v", err)
	}
	_, err = readCoils(f.socketKey(), 0, 9)
	if !isModbusException(err, 0x03) {
		t.Errorf("expected illegal data value exception, got %v", err)
	}
}

func TestProtocolAutoSelect(t *testing.T) {
	modbusProtocol = ""
	if !useModbusTCP("10.0.0.1:502") || useModbusTCP("10.0.0.1:4196") {
		t.Error("port 502 should select Modbus TCP, anything else RTU over TCP")
	}
}
