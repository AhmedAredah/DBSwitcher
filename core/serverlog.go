package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// serverLogMaxBytes caps a server log before it is rotated.
const serverLogMaxBytes = 8 * 1024 * 1024

// serverLogPath is the file a server started for a configuration writes its
// own output to.
func serverLogPath(configName string) string {
	name := "mysqld"
	if configName != "" {
		name = "mysqld-" + configName
	}
	return filepath.Join(GetAppDataDir(), name+".log")
}

// openServerLog opens that file for the server to inherit and returns the
// offset where this instance's output begins.
//
// The server's output has to land in a file rather than in a pipe held by this
// process. The server is detached and outlives the process that starts it, so
// os/exec would hand it a pipe whose reader disappears moments later; the
// server can then block on its own log writes and never finish shutting down.
// That is how servers were left alive with no listener, refusing to exit, and
// blocking every later switch.
func openServerLog(configName string) (*os.File, int64, error) {
	path := serverLogPath(configName)

	// Keep one previous generation instead of growing without bound.
	if info, err := os.Stat(path); err == nil && info.Size() > serverLogMaxBytes {
		if err := os.Rename(path, path+".old"); err != nil {
			AppLogger.Debug("Could not rotate %s: %v", path, err)
		}
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot open server log %s: %v", path, err)
	}

	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		offset = 0
	}

	return file, offset, nil
}

// readServerLogFrom returns what the server appended to its log after offset.
func readServerLogFrom(path string, offset int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ""
	}

	if offset > info.Size() {
		offset = 0 // rotated or truncated underneath us
	}

	// A server that fails to start prints little; cap the read so a chatty one
	// cannot flood the error message.
	const maxRead = 16 * 1024
	if info.Size()-offset > maxRead {
		offset = info.Size() - maxRead
	}

	size := info.Size() - offset
	if size <= 0 {
		return ""
	}

	buf := make([]byte, size)
	n, err := file.ReadAt(buf, offset)
	if n == 0 {
		if err != nil {
			AppLogger.Debug("Could not read %s: %v", path, err)
		}
		return ""
	}

	return strings.TrimSpace(string(buf[:n]))
}
