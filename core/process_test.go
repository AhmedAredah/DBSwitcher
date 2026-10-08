package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPortFromAddress(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:3306":   "3306",
		"[::]:3306":      "3306",
		"127.0.0.1:3307": "3307",
		"0.0.0.0:0":      "", // not a listening port
		"0.0.0.0:*":      "",
		"[::1]:":         "",
		"nonsense":       "",
		"1.2.3.4:abc":    "",
	}

	for address, want := range cases {
		if got := portFromAddress(address); got != want {
			t.Errorf("portFromAddress(%q) = %q, want %q", address, got, want)
		}
	}
}

func TestParseListeningPorts(t *testing.T) {
	var output string
	if runtime.GOOS == "windows" {
		output = `
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1484
  TCP    0.0.0.0:3306           0.0.0.0:0              LISTENING       33856
  TCP    127.0.0.1:51234        127.0.0.1:3306         ESTABLISHED     9000
  TCP    [::]:3306              [::]:0                 LISTENING       33856
`
	} else {
		output = `
Active Internet connections (only servers)
tcp        0      0 0.0.0.0:135             0.0.0.0:*               LISTEN      1484/rpcbind
tcp        0      0 0.0.0.0:3306            0.0.0.0:*               LISTEN      33856/mysqld
`
	}

	listeners := parseListeningPorts(output)

	if got := listeners[33856]; got != "3306" {
		t.Errorf("expected PID 33856 on port 3306, got %q", got)
	}
	if got := listeners[1484]; got != "135" {
		t.Errorf("expected PID 1484 on port 135, got %q", got)
	}

	// An established connection is not a listener, and a process absent from
	// the table must not acquire a port.
	if _, ok := listeners[9000]; ok {
		t.Error("an ESTABLISHED connection must not count as listening")
	}
	if _, ok := listeners[369188]; ok {
		t.Error("a process with no socket must not appear")
	}
}

func TestServingAndStalePartition(t *testing.T) {
	// The situation that broke every switch: a server that stopped without
	// exiting (no port) alongside the one that is really serving.
	procs := []ProcessInfo{
		{PID: 369188, CommandLine: "mysqld.exe --defaults-file=external.ini --console"},
		{PID: 4242, CommandLine: "mysqld.exe --defaults-file=my.ini MariaDB11_4", Port: "3306"},
	}

	serving := ServingServers(procs)
	if len(serving) != 1 || serving[0].PID != 4242 {
		t.Fatalf("expected only PID 4242 to be serving, got %+v", serving)
	}

	stale := StaleServers(procs)
	if len(stale) != 1 || stale[0].PID != 369188 {
		t.Fatalf("expected only PID 369188 to be stale, got %+v", stale)
	}

	if procs[0].IsServing() {
		t.Error("a process with no listening port must not count as serving")
	}
	if !procs[1].IsServing() {
		t.Error("a process with a listening port must count as serving")
	}
}

func TestReadServerLogFromOffset(t *testing.T) {
	AppLogger = &Logger{}

	path := filepath.Join(t.TempDir(), "mysqld-test.log")
	if err := os.WriteFile(path, []byte("older run output\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	offset := info.Size()

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if _, err := file.WriteString("[ERROR] Can't start server: Bind on TCP/IP port\n"); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	file.Close()

	got := readServerLogFrom(path, offset)
	if got != "[ERROR] Can't start server: Bind on TCP/IP port" {
		t.Errorf("expected only this run's output, got %q", got)
	}

	// An offset past the end means the file was rotated; read what is there
	// instead of returning nothing.
	if got := readServerLogFrom(path, 1<<20); got == "" {
		t.Error("expected a rotated file to fall back to the whole content")
	}
}

func TestServerLogPath(t *testing.T) {
	named := serverLogPath("external")
	if filepath.Base(named) != "mysqld-external.log" {
		t.Errorf("unexpected log name: %s", named)
	}

	unnamed := serverLogPath("")
	if filepath.Base(unnamed) != "mysqld.log" {
		t.Errorf("unexpected log name: %s", unnamed)
	}
}
