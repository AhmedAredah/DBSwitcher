package core

import (
	"net"
	"strings"
	"testing"
)

// fakeServer listens on a loopback port and sends reply to the first client,
// standing in for a database in a particular state.
func fakeServer(t *testing.T, reply []byte) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if len(reply) > 0 {
			conn.Write(reply)
		}
	}()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("cannot read port: %v", err)
	}
	return port
}

// greeting builds the packet a healthy server sends before authentication.
func greeting(version string) []byte {
	payload := append([]byte{0x0A}, []byte(version)...)
	payload = append(payload, 0x00)
	payload = append(payload, 0x01, 0x02, 0x03, 0x04) // connection id, truncated
	return append([]byte{byte(len(payload)), 0x00, 0x00, 0x00}, payload...)
}

func TestProbeHealthyServer(t *testing.T) {
	AppLogger = &Logger{}

	port := fakeServer(t, greeting("11.4.8-MariaDB"))

	probe := ProbeServerPort(port)
	if !probe.Healthy() {
		t.Fatalf("expected a healthy probe, got %+v", probe)
	}
	if probe.Version != "11.4.8-MariaDB" {
		t.Errorf("expected the running server's version, got %q", probe.Version)
	}
}

func TestProbePortOpenButSilent(t *testing.T) {
	AppLogger = &Logger{}

	// The case that made a broken database look fine: something holds the
	// port, but no database answers.
	port := fakeServer(t, nil)

	probe := ProbeServerPort(port)
	if probe.Healthy() {
		t.Fatal("a silent port must not be reported as healthy")
	}
	if !probe.Reachable {
		t.Error("the port did accept a connection")
	}
	if !strings.Contains(probe.Message, "no greeting") {
		t.Errorf("expected the message to explain the silence, got %q", probe.Message)
	}
}

func TestProbeServerRefusing(t *testing.T) {
	AppLogger = &Logger{}

	// A server that is up but will not serve says so in an error packet.
	message := "Too many connections"
	payload := append([]byte{0xFF, 0x10, 0x04}, []byte(message)...)
	packet := append([]byte{byte(len(payload)), 0x00, 0x00, 0x00}, payload...)

	probe := ProbeServerPort(fakeServer(t, packet))
	if probe.Healthy() {
		t.Fatal("a refusing server must not be reported as healthy")
	}
	if !probe.Responded {
		t.Error("the server did respond, in protocol")
	}
	if probe.Message != message {
		t.Errorf("expected the server's own words, got %q", probe.Message)
	}
}

func TestProbeNotADatabase(t *testing.T) {
	AppLogger = &Logger{}

	probe := ProbeServerPort(fakeServer(t, []byte("HTTP/1.1 200 OK\r\n\r\n")))
	if probe.Healthy() {
		t.Fatal("an unrelated service must not be reported as healthy")
	}
	if !strings.Contains(probe.Message, "did not answer as a database server") {
		t.Errorf("unexpected message: %q", probe.Message)
	}
}

func TestProbeNothingListening(t *testing.T) {
	AppLogger = &Logger{}

	// Take a port and release it, so nothing is listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()

	probe := ProbeServerPort(port)
	if probe.Healthy() || probe.Reachable {
		t.Fatalf("expected an unreachable probe, got %+v", probe)
	}
}

func TestErrorPacketStripsSQLState(t *testing.T) {
	payload := append([]byte{0xFF, 0x10, 0x04}, []byte("#HY000Host 'x' is blocked")...)
	if got := errorPacketMessage(payload); got != "Host 'x' is blocked" {
		t.Errorf("expected the SQL state to be stripped, got %q", got)
	}
}
