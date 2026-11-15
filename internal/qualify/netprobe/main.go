// netprobe is the M27 network-isolation probe (tasks/M27/FEASIBILITY-CHECKLIST.md, F-0).
// It makes only the connections the owner approved (OD-5): a TCP handshake to 9.9.9.9:53 and
// [2620:fe::fe]:53, one DNS query for the root zone NS to 9.9.9.9:53 over UDP, and one
// system-resolver lookup of registry.terraform.io. It sends nothing else.
//
//	netprobe control                   outside the isolation: 3 attempts per endpoint
//	netprobe isolated [-read <file>]   inside it: loopback, outbound checks, then the same
//	                                   outbound checks again from a grandchild (re-executed self)
//
// It prints one JSON document to stdout and exits 0 when it ran; judging the isolation level
// from the results (with the control) is left to the operator, as F-0 describes.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// OD-5 endpoints (approved 2026-11-14). Not configurable on purpose.
const (
	ipv4TCP  = "9.9.9.9:53"
	ipv6TCP  = "[2620:fe::fe]:53"
	udpDNS   = "9.9.9.9:53"
	hostname = "registry.terraform.io"
)

// timeout per attempt; a var so tests can shorten it.
var timeout = 5 * time.Second

type check struct {
	Name     string `json:"name"`
	Target   string `json:"target,omitempty"`
	Proto    string `json:"proto,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Outcome  string `json:"outcome"` // connected|failed, answered|no_answer, resolved|failed, read|failed, no_path
	Error    string `json:"error,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Millis   int64  `json:"ms"`
	Expected string `json:"expected,omitempty"`
	OK       *bool  `json:"ok,omitempty"` // nil: informational
}

type report struct {
	Mode       string    `json:"mode"`
	Depth      int       `json:"depth,omitempty"`
	GOOS       string    `json:"goos"`
	GOARCH     string    `json:"goarch"`
	PID        int       `json:"pid"`
	PPID       int       `json:"ppid"`
	Started    time.Time `json:"started"`
	Resolver   string    `json:"resolver"`
	Checks     []check   `json:"checks"`
	Grandchild *report   `json:"grandchild,omitempty"`
	ChildError string    `json:"childError,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: netprobe control | netprobe isolated [-read <file>]")
		os.Exit(2)
	}
	mode := os.Args[1]
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	read := fs.String("read", "", "isolated: file whose read is attempted (F-0 check 8)")
	depth := fs.Int("depth", 0, "internal: re-execution depth")
	_ = fs.Parse(os.Args[2:])

	r := report{Mode: mode, Depth: *depth, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		PID: os.Getpid(), PPID: os.Getppid(), Started: time.Now().UTC(),
		Resolver: "net.DefaultResolver (PreferGo=false: libSystem on darwin, cgo/getaddrinfo or Go on others)"}
	switch {
	case mode == "control":
		for i := 1; i <= 3; i++ {
			r.Checks = append(r.Checks, outbound(i, true)...)
		}
		r.Checks = append(r.Checks, resolve())
	case mode == "isolated" && *depth == 0:
		r.Checks = append(r.Checks, loopback("tcp4", "127.0.0.1:0"), loopback("tcp6", "[::1]:0"))
		r.Checks = append(r.Checks, outbound(0, false)...)
		r.Checks = append(r.Checks, resolve())
		if *read != "" {
			r.Checks = append(r.Checks, readFile(*read))
		}
		r.Grandchild, r.ChildError = reexec(1)
	case mode == "isolated" && *depth == 1:
		// The child only starts the grandchild and passes its report through.
		gc, errText := reexec(2)
		if gc == nil {
			fmt.Fprintln(os.Stderr, errText)
			os.Exit(1)
		}
		emit(*gc)
		return
	case mode == "isolated" && *depth == 2:
		r.Checks = outbound(0, false)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", mode)
		os.Exit(2)
	}
	emit(r)
}

func emit(r report) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// reexec runs this executable one level deeper and decodes its report.
func reexec(depth int) (*report, string) {
	self, err := os.Executable()
	if err != nil {
		return nil, "executable: " + err.Error()
	}
	cmd := exec.Command(self, "isolated", "-depth", fmt.Sprint(depth))
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Sprintf("depth %d: %v: %s", depth, err, stderr.String())
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return nil, fmt.Sprintf("depth %d: decode: %v", depth, err)
	}
	return &r, ""
}

// outbound runs checks 2–4. wantReach is true outside the isolation (control).
func outbound(attempt int, wantReach bool) []check {
	return []check{
		expect(tcpCheck("ipv4_tcp", "tcp4", ipv4TCP), attempt, wantReach),
		expect(tcpCheck("ipv6_tcp", "tcp6", ipv6TCP), attempt, wantReach),
		expect(udpCheck("udp_dns", udpDNS), attempt, wantReach),
	}
}

func expect(c check, attempt int, wantReach bool) check {
	c.Attempt = attempt
	reached := c.Outcome == "connected" || c.Outcome == "answered"
	if wantReach {
		c.Expected = "reached"
	} else {
		c.Expected = "denied"
	}
	ok := reached == wantReach
	c.OK = &ok
	return c
}

func tcpCheck(name, network, addr string) check {
	c := check{Name: name, Target: addr, Proto: network}
	start := time.Now()
	conn, err := net.DialTimeout(network, addr, timeout)
	c.Millis = time.Since(start).Milliseconds()
	if err != nil {
		c.Outcome, c.Error = "failed", err.Error()
		if noRoute(err) {
			c.Detail = "no route or address family unavailable"
		}
		return c
	}
	conn.Close()
	c.Outcome = "connected"
	return c
}

func noRoute(err error) bool {
	var op *net.OpError
	msg := err.Error()
	return errors.As(err, &op) && !op.Timeout() && (strings.Contains(msg, "unreachable") || strings.Contains(msg, "no route"))
}

// rootNSQuery is a DNS query for ". IN NS" with the given ID and recursion desired.
func rootNSQuery(id uint16) []byte {
	q := make([]byte, 12, 17)
	binary.BigEndian.PutUint16(q[0:], id)
	q[2] = 0x01                                    // RD
	binary.BigEndian.PutUint16(q[4:], 1)           // QDCOUNT
	return append(q, 0x00, 0x00, 0x02, 0x00, 0x01) // root name, QTYPE NS, QCLASS IN
}

func udpCheck(name, addr string) check {
	c := check{Name: name, Target: addr, Proto: "udp4"}
	start := time.Now()
	conn, err := net.DialTimeout("udp4", addr, timeout)
	if err != nil {
		c.Outcome, c.Error, c.Millis = "failed", err.Error(), time.Since(start).Milliseconds()
		return c
	}
	defer conn.Close()
	id := uint16(time.Now().UnixNano())
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(rootNSQuery(id)); err != nil {
		c.Outcome, c.Error, c.Millis = "no_answer", "write: "+err.Error(), time.Since(start).Milliseconds()
		return c
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	c.Millis = time.Since(start).Milliseconds()
	switch {
	case err != nil:
		c.Outcome, c.Error = "no_answer", "read: "+err.Error()
	case n < 12 || binary.BigEndian.Uint16(buf) != id || buf[2]&0x80 == 0:
		c.Outcome, c.Error = "no_answer", fmt.Sprintf("unexpected %d-byte reply", n)
	default:
		c.Outcome = "answered"
	}
	return c
}

// loopback (check 1) dials a listener this process opens; no listener means no path.
func loopback(network, addr string) check {
	c := check{Name: "loopback_" + network, Proto: network, Expected: "reached"}
	start := time.Now()
	ln, err := net.Listen(network, addr)
	if err != nil {
		c.Outcome, c.Error, c.Millis = "no_path", "listen: "+err.Error(), time.Since(start).Milliseconds()
		return c
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	c.Target = ln.Addr().String()
	// macOS sandbox-exec denies loopback for ~0.1–1 s after a cold start (F-0, 2026-11-14), so
	// retry for up to `timeout` and report the attempts and the first error.
	var first string
	for {
		c.Attempt++
		r := tcpCheck(c.Name, network, c.Target)
		c.Outcome, c.Error = r.Outcome, r.Error
		if first == "" {
			first = r.Error
		}
		if c.Outcome == "connected" || time.Since(start) >= timeout {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if c.Attempt > 1 {
		c.Detail = fmt.Sprintf("%d attempts; first error: %s", c.Attempt, first)
	}
	c.Millis = time.Since(start).Milliseconds()
	ok := c.Outcome == "connected"
	c.OK = &ok
	return c
}

// resolve (check 5) is informational: resolving inside means DNS leaks through a system daemon.
func resolve() check {
	c := check{Name: "system_resolver", Target: hostname}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, hostname)
	c.Millis = time.Since(start).Milliseconds()
	if err != nil {
		c.Outcome, c.Error = "failed", err.Error()
		return c
	}
	c.Outcome, c.Detail = "resolved", fmt.Sprint(len(addrs), " addresses")
	return c
}

// readFile (check 8) is informational for OD-14; only the outcome is reported, never content.
func readFile(path string) check {
	c := check{Name: "home_read", Target: path}
	start := time.Now()
	_, err := os.ReadFile(path)
	c.Millis = time.Since(start).Milliseconds()
	if err != nil {
		c.Outcome, c.Error = "failed", err.Error()
		return c
	}
	c.Outcome = "read"
	return c
}
