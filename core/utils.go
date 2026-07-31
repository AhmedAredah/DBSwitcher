package core

import (
	"bytes"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

// PathExists checks if a path exists
func PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// GetExecutableName returns the platform-specific executable name
func GetExecutableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// IsDirEmpty checks if a directory is empty
func IsDirEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == os.ErrNotExist || err == nil {
		return err != nil, nil
	}
	return false, err
}

// IsPortAvailable checks if a port is free on every wildcard address family
// the host supports.
//
// A single net.Listen("tcp", ":port") is not enough on Windows: Go resolves
// that to a dual-stack [::] socket, and Windows grants it even when another
// process already owns 0.0.0.0:port. That false "available" answer is what let
// a second mysqld be launched on top of a live server, which then aborted with
// "Bind on TCP/IP port. Got error: 10048".
func IsPortAvailable(port string) bool {
	if port == "" {
		return false
	}

	// Anything actively accepting connections settles the question.
	if IsPortListening(port) {
		return false
	}

	if !canListen("tcp4", port) {
		return false
	}

	// Only hold IPv6 against the port when the host actually has IPv6.
	if ipv6Supported() && !canListen("tcp6", port) {
		return false
	}

	return true
}

// canListen reports whether port can be bound on the given network.
func canListen(network, port string) bool {
	ln, err := net.Listen(network, net.JoinHostPort("", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

var (
	ipv6Once      sync.Once
	ipv6Available bool
)

// ipv6Supported reports whether IPv6 sockets can be bound at all, so a host
// without IPv6 is not mistaken for a busy port.
func ipv6Supported() bool {
	ipv6Once.Do(func() {
		ln, err := net.Listen("tcp6", "[::]:0")
		if err != nil {
			return
		}
		ln.Close()
		ipv6Available = true
	})
	return ipv6Available
}

// IsPortListening checks if a port is actively listening. Both loopback
// families are probed because a server may be bound to IPv4 only.
func IsPortListening(port string) bool {
	if port == "" {
		return false
	}

	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// syncBuffer is a bytes.Buffer that stays safe to read while the copy
// goroutines started by os/exec are still writing into it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
