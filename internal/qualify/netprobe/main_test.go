package main

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every test talks to loopback only; no external connection is made.

func TestLoopbackIPv4(t *testing.T) {
	c := loopback("tcp4", "127.0.0.1:0")
	if c.Outcome != "connected" || c.OK == nil || !*c.OK {
		t.Fatalf("loopback = %+v", c)
	}
}

func TestLoopbackIPv6IsConnectedOrNoPath(t *testing.T) {
	c := loopback("tcp6", "[::1]:0")
	if c.Outcome != "connected" && c.Outcome != "no_path" {
		t.Fatalf("loopback ::1 = %+v", c)
	}
}

func TestTCPCheckConnectsAndFails(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	if c := tcpCheck("x", "tcp4", addr); c.Outcome != "connected" {
		t.Fatalf("open port: %+v", c)
	}
	ln.Close()
	c := expect(tcpCheck("x", "tcp4", addr), 0, false)
	if c.Outcome != "failed" || c.Error == "" || !*c.OK {
		t.Fatalf("closed port, expected denied: %+v", c)
	}
	if c := expect(tcpCheck("x", "tcp4", addr), 1, true); *c.OK {
		t.Fatalf("closed port must not satisfy a control: %+v", c)
	}
}

func TestUDPCheckNeedsAMatchingAnswer(t *testing.T) {
	old := timeout
	timeout = 300 * time.Millisecond
	defer func() { timeout = old }()

	answer := func(reply func(q []byte) []byte) string {
		pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		go func() {
			buf := make([]byte, 512)
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := reply(buf[:n]); r != nil {
				pc.WriteTo(r, from)
			}
		}()
		return udpCheck("udp", pc.LocalAddr().String()).Outcome
	}
	valid := func(q []byte) []byte {
		r := append([]byte(nil), q...)
		r[2] |= 0x80 // QR: response
		return r
	}
	if got := answer(valid); got != "answered" {
		t.Fatalf("valid reply: %s", got)
	}
	if got := answer(func([]byte) []byte { return nil }); got != "no_answer" {
		t.Fatalf("silent server: %s", got)
	}
	wrongID := func(q []byte) []byte {
		r := valid(q)
		binary.BigEndian.PutUint16(r, binary.BigEndian.Uint16(q)+1)
		return r
	}
	if got := answer(wrongID); got != "no_answer" {
		t.Fatalf("wrong id: %s", got)
	}
}

func TestRootNSQuery(t *testing.T) {
	q := rootNSQuery(0xabcd)
	want := []byte{0xab, 0xcd, 0x01, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 1}
	if string(q) != string(want) {
		t.Fatalf("query = % x", q)
	}
}

func TestReadFileReportsOutcomeOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(p, []byte("not reported"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := readFile(p); c.Outcome != "read" || c.Detail != "" {
		t.Fatalf("read = %+v", c)
	}
	if c := readFile(p + ".missing"); c.Outcome != "failed" {
		t.Fatalf("missing = %+v", c)
	}
}
