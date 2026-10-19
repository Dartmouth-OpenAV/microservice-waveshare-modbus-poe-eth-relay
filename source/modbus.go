package main

// Minimal Modbus client for the Waveshare Modbus POE ETH Relay.
//
// The relay board speaks Modbus RTU internally and sits behind a serial-to-Ethernet gateway.
// The gateway supports two framings on the network side:
//   - "Modbus RTU over TCP" (the factory default, "Transfer Protocol: None" in the web UI, port 4196):
//     the raw RTU frame (unit id + PDU + CRC16) is sent over the TCP socket unchanged.
//   - "Modbus TCP" ("Transfer Protocol: Modbus TCP Protocol" in the web UI, port 502):
//     the CRC is dropped and an MBAP header is prepended.
//
// The framework's socket helpers are line oriented (they read up to a delimiter), which doesn't
// work for binary Modbus frames, so this file opens its own short-lived TCP connection per transaction.
// All calls into here come through doDeviceSpecificGet/Set, which the framework already serializes.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Dartmouth-OpenAV/microservice-framework/framework"
)

const (
	modbusUnitID = 0x01 // Waveshare docs: the slave address is always 1 since devices are identified by IP

	fcReadCoils            = 0x01
	fcReadHoldingRegisters = 0x03
	fcWriteSingleCoil      = 0x05

	modbusTCPPort = "502"
)

// "rtu", "tcp", or "" (auto: Modbus TCP when the device port is 502, RTU over TCP otherwise).
// Can be overridden with the MODBUS_PROTOCOL environment variable.
var modbusProtocol = strings.ToLower(os.Getenv("MODBUS_PROTOCOL"))

// Returned (wrapped) when we can't reach the device at all
var errNoConnection = errors.New("no connection")

var modbusExceptionNames = map[byte]string{
	0x01: "illegal function",
	0x02: "illegal data address",
	0x03: "illegal data value",
	0x04: "server device failure",
	0x05: "acknowledge",
	0x06: "server device busy",
}

type modbusException struct {
	function byte
	code     byte
}

func (e *modbusException) Error() string {
	name, ok := modbusExceptionNames[e.code]
	if !ok {
		name = "unknown exception"
	}
	return fmt.Sprintf("modbus exception 0x%02X (%s) for function 0x%02X", e.code, name, e.function)
}

func isModbusException(err error, code byte) bool {
	var exception *modbusException
	return errors.As(err, &exception) && exception.code == code
}

// The framework socketKey looks like "user:password@host:port"
func deviceAddress(socketKey string) string {
	if i := strings.LastIndex(socketKey, "@"); i >= 0 {
		return socketKey[i+1:]
	}
	return socketKey
}

func useModbusTCP(address string) bool {
	switch modbusProtocol {
	case "tcp":
		return true
	case "rtu":
		return false
	}
	_, port, err := net.SplitHostPort(address)
	return err == nil && port == modbusTCPPort
}

// Sends one request PDU (function code + data) and returns the response PDU.
func modbusTransact(socketKey string, pdu []byte) ([]byte, error) {
	address := deviceAddress(socketKey)
	conn, err := net.DialTimeout("tcp", address, time.Duration(framework.ConnectTimeout)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoConnection, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Duration(framework.ReadTimeout) * time.Second))

	var resp []byte
	if useModbusTCP(address) {
		resp, err = transactTCP(conn, pdu)
	} else {
		resp, err = transactRTU(conn, pdu)
	}
	if err != nil {
		return nil, err
	}

	if len(resp) == 0 {
		return nil, errors.New("empty modbus response")
	}
	if resp[0] == pdu[0]|0x80 {
		if len(resp) < 2 {
			return nil, errors.New("truncated modbus exception response")
		}
		return nil, &modbusException{function: pdu[0], code: resp[1]}
	}
	if resp[0] != pdu[0] {
		return nil, fmt.Errorf("modbus response function 0x%02X doesn't match request 0x%02X", resp[0], pdu[0])
	}
	return resp, nil
}

func transactRTU(conn net.Conn, pdu []byte) ([]byte, error) {
	frame := append([]byte{modbusUnitID}, pdu...)
	frame = append(frame, crc16(frame)...)
	framework.Log(fmt.Sprintf("transactRTU - sending: % X", frame))
	if _, err := conn.Write(frame); err != nil {
		return nil, fmt.Errorf("%w: write failed: %v", errNoConnection, err)
	}

	// RTU frames have no length header, so work out how much to read from the function code
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, fmt.Errorf("reading modbus RTU header: %v", err)
	}
	var remaining int
	switch fc := resp[1]; {
	case fc&0x80 != 0: // exception: code + CRC
		remaining = 1 + 2
	case fc == fcReadCoils || fc == fcReadHoldingRegisters: // byte count + data + CRC
		count := make([]byte, 1)
		if _, err := io.ReadFull(conn, count); err != nil {
			return nil, fmt.Errorf("reading modbus RTU byte count: %v", err)
		}
		resp = append(resp, count[0])
		remaining = int(count[0]) + 2
	case fc == fcWriteSingleCoil: // address + value + CRC
		remaining = 4 + 2
	default:
		return nil, fmt.Errorf("unexpected modbus RTU function code in response: 0x%02X", fc)
	}
	rest := make([]byte, remaining)
	if _, err := io.ReadFull(conn, rest); err != nil {
		return nil, fmt.Errorf("reading modbus RTU body: %v", err)
	}
	resp = append(resp, rest...)
	framework.Log(fmt.Sprintf("transactRTU - received: % X", resp))

	body := resp[:len(resp)-2]
	if !bytes.Equal(crc16(body), resp[len(resp)-2:]) {
		return nil, fmt.Errorf("modbus RTU CRC mismatch in response: % X", resp)
	}
	if body[0] != modbusUnitID {
		return nil, fmt.Errorf("modbus RTU response from unexpected unit id %d", body[0])
	}
	return body[1:], nil
}

var modbusTransactionID uint16

func transactTCP(conn net.Conn, pdu []byte) ([]byte, error) {
	modbusTransactionID++
	frame := make([]byte, 7, 7+len(pdu))
	binary.BigEndian.PutUint16(frame[0:], modbusTransactionID)
	binary.BigEndian.PutUint16(frame[2:], 0) // protocol id
	binary.BigEndian.PutUint16(frame[4:], uint16(len(pdu)+1))
	frame[6] = modbusUnitID
	frame = append(frame, pdu...)
	framework.Log(fmt.Sprintf("transactTCP - sending: % X", frame))
	if _, err := conn.Write(frame); err != nil {
		return nil, fmt.Errorf("%w: write failed: %v", errNoConnection, err)
	}

	header := make([]byte, 7)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("reading modbus TCP header: %v", err)
	}
	length := int(binary.BigEndian.Uint16(header[4:]))
	if binary.BigEndian.Uint16(header[0:]) != modbusTransactionID || binary.BigEndian.Uint16(header[2:]) != 0 ||
		length < 2 || length > 254 {
		return nil, fmt.Errorf("bad modbus TCP header in response: % X", header)
	}
	resp := make([]byte, length-1)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, fmt.Errorf("reading modbus TCP body: %v", err)
	}
	framework.Log(fmt.Sprintf("transactTCP - received: % X % X", header, resp))
	return resp, nil
}

// Modbus CRC16 (poly 0xA001, init 0xFFFF), returned low byte first as it goes on the wire
func crc16(data []byte) []byte {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return []byte{byte(crc), byte(crc >> 8)}
}

func readCoils(socketKey string, start uint16, count uint16) ([]bool, error) {
	pdu := []byte{fcReadCoils, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(pdu[1:], start)
	binary.BigEndian.PutUint16(pdu[3:], count)
	resp, err := modbusTransact(socketKey, pdu)
	if err != nil {
		return nil, err
	}
	byteCount := (int(count) + 7) / 8
	if len(resp) < 2 || int(resp[1]) != byteCount || len(resp) != 2+byteCount {
		return nil, fmt.Errorf("unexpected read coils response: % X", resp)
	}
	coils := make([]bool, count)
	for i := range coils {
		coils[i] = resp[2+i/8]&(1<<(i%8)) != 0
	}
	return coils, nil
}

func writeSingleCoil(socketKey string, address uint16, value uint16) error {
	pdu := []byte{fcWriteSingleCoil, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(pdu[1:], address)
	binary.BigEndian.PutUint16(pdu[3:], value)
	resp, err := modbusTransact(socketKey, pdu)
	if err != nil {
		return err
	}
	// The device echoes the request back on success
	if !bytes.Equal(resp, pdu) {
		return fmt.Errorf("unexpected write coil response: % X", resp)
	}
	return nil
}

func readHoldingRegister(socketKey string, address uint16) (uint16, error) {
	pdu := []byte{fcReadHoldingRegisters, 0, 0, 0, 1}
	binary.BigEndian.PutUint16(pdu[1:], address)
	resp, err := modbusTransact(socketKey, pdu)
	if err != nil {
		return 0, err
	}
	if len(resp) != 4 || resp[1] != 2 {
		return 0, fmt.Errorf("unexpected read holding register response: % X", resp)
	}
	return binary.BigEndian.Uint16(resp[2:]), nil
}
