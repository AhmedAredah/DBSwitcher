package core

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"time"
)

// probeTimeout bounds a health check so status stays quick.
const probeTimeout = 2 * time.Second

// ServerProbe is what a server answered when asked for nothing.
//
// Every MySQL-protocol server greets a new connection before any credentials
// are exchanged, so a database can be checked for free: no password, no
// privileges. An open port proves only that something is bound to it, which is
// why a server that was no longer serving anything could still be reported as
// running.
type ServerProbe struct {
	// Reachable reports whether the port accepted a connection.
	Reachable bool

	// Responded reports whether a MySQL-protocol server greeted us.
	Responded bool

	// Version is the running server's version, which is not necessarily the
	// version of the installed binaries.
	Version string

	// Message explains a refusal, in the server's own words ("Too many
	// connections", "Host ... is blocked"), or why no greeting arrived.
	Message string
}

// Healthy reports whether the server is usable.
func (p ServerProbe) Healthy() bool { return p.Responded && p.Message == "" }

// ProbeServerPort asks whether a usable database is listening on a port.
func ProbeServerPort(port string) ServerProbe {
	if port == "" {
		return ServerProbe{Message: "no port to check"}
	}

	// Try both loopback families: a server may be bound to IPv4 only.
	var probe ServerProbe
	for _, host := range []string{"127.0.0.1", "::1"} {
		probe = probeServerAddress(net.JoinHostPort(host, port))
		if probe.Reachable {
			return probe
		}
	}
	return probe
}

func probeServerAddress(address string) ServerProbe {
	var probe ServerProbe

	conn, err := net.DialTimeout("tcp", address, probeTimeout)
	if err != nil {
		probe.Message = "nothing is accepting connections on that port"
		return probe
	}
	defer conn.Close()

	probe.Reachable = true

	if err := conn.SetReadDeadline(time.Now().Add(probeTimeout)); err != nil {
		probe.Message = fmt.Sprintf("cannot read from the server: %v", err)
		return probe
	}

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if n == 0 {
		// The port accepts connections but nothing greets us: a server that is
		// still starting, one that is wedged, or not a database at all.
		probe.Message = "the port is open but the server sent no greeting"
		if err != nil {
			probe.Message = fmt.Sprintf("the port is open but the server sent no greeting (%v)", err)
		}
		return probe
	}

	return parseHandshake(buf[:n])
}

// parseHandshake reads a MySQL protocol greeting, or the error packet a server
// sends when it is up but refusing connections.
func parseHandshake(packet []byte) ServerProbe {
	probe := ServerProbe{Reachable: true}

	// Three bytes of payload length, one sequence number, then the payload.
	if len(packet) < 5 {
		probe.Message = "the server sent a truncated greeting"
		return probe
	}

	payload := packet[4:]

	switch payload[0] {
	case 0xFF:
		// Up, and telling us why it will not serve.
		probe.Responded = true
		probe.Message = errorPacketMessage(payload)
	case 0x0A, 0x09:
		probe.Responded = true
		if idx := bytes.IndexByte(payload[1:], 0x00); idx > 0 {
			probe.Version = printableASCII(payload[1 : 1+idx])
		}
	default:
		probe.Message = "the port is open but did not answer as a database server"
	}

	return probe
}

// errorPacketMessage extracts the text of a pre-authentication error packet.
func errorPacketMessage(payload []byte) string {
	const refused = "the server refused the connection"

	if len(payload) < 4 {
		return refused
	}

	// 0xFF, then a two byte error code, then the human readable message.
	message := printableASCII(payload[3:])

	// Some servers prefix a SQL state as "#HY000".
	if strings.HasPrefix(message, "#") && len(message) > 6 {
		message = message[6:]
	}

	if message = strings.TrimSpace(message); message == "" {
		return refused
	}
	return message
}

// printableASCII keeps the readable part of a protocol field, so a binary
// packet can never garble a status line.
func printableASCII(raw []byte) string {
	var out strings.Builder
	for _, b := range raw {
		if b >= 0x20 && b < 0x7F {
			out.WriteByte(b)
		}
	}
	return out.String()
}
