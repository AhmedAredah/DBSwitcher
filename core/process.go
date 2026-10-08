package core

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// IsServing reports whether the process is accepting connections.
//
// Presence in the process list is not enough to call a server running: a
// server that has completed its shutdown but never exited still appears
// there, holding no port and no data directory. Treating such a process as
// running made DBSwitcher wait for something that would never go away, and
// report the wrong configuration while a different server was the live one.
func (p ProcessInfo) IsServing() bool { return p.Port != "" }

// ServingServers returns the processes that are accepting connections.
func ServingServers(procs []ProcessInfo) []ProcessInfo {
	var serving []ProcessInfo
	for _, proc := range procs {
		if proc.IsServing() {
			serving = append(serving, proc)
		}
	}
	return serving
}

// StaleServers returns server processes that are not accepting connections:
// servers that stopped without exiting.
func StaleServers(procs []ProcessInfo) []ProcessInfo {
	var stale []ProcessInfo
	for _, proc := range procs {
		if !proc.IsServing() {
			stale = append(stale, proc)
		}
	}
	return stale
}

// ServerProcessAlive reports whether a specific server process is still present.
func ServerProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	procs, err := FindServerProcesses()
	if err != nil {
		// The state is unknown; claiming the process is gone would be worse
		// than waiting for it.
		AppLogger.Debug("Could not check whether process %d is alive: %v", pid, err)
		return true
	}

	for _, proc := range procs {
		if proc.PID == pid {
			return true
		}
	}
	return false
}

// reportStaleServers logs every server process that is no longer serving, so a
// process that will never exit on its own is visible rather than silently
// breaking the next switch.
func reportStaleServers() {
	procs, err := FindServerProcesses()
	if err != nil {
		return
	}

	for _, stale := range StaleServers(procs) {
		AppLogger.Warn("Server process %d is present but not accepting connections; it stopped without exiting and can be ended safely", stale.PID)
	}
}

// annotateListeningPorts records which of the processes hold a listening
// socket, in a single pass over the system's connection table.
func annotateListeningPorts(procs []ProcessInfo) {
	if len(procs) == 0 {
		return
	}

	listeners := listeningPorts()

	// netstat can fail, and on some Windows installs it prints state names
	// this parser does not know. Rather than declare a live server stale,
	// ask the port each server was told to use whether anything answers.
	if len(listeners) == 0 {
		for i := range procs {
			if port := configuredPort(procs[i]); port != "" && IsPortListening(port) {
				procs[i].Port = port
			}
		}
		return
	}

	for i := range procs {
		procs[i].Port = listeners[procs[i].PID]
	}
}

// configuredPort works out which port a server was told to use, from its own
// command line or from the configuration file named on it.
func configuredPort(proc ProcessInfo) string {
	if port := extractPortFromArgs(proc.CommandLine); port != "" {
		return port
	}

	configFile := extractConfigFromCmdLine(proc.CommandLine)
	if configFile == "" || !PathExists(configFile) {
		return ""
	}

	return ParseConfigFile(configFile).Port
}

// listeningPorts maps process IDs to a TCP port they are listening on.
func listeningPorts() map[int]string {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("netstat", "-ano", "-p", "TCP")
	default:
		cmd = exec.Command("netstat", "-tlnp")
	}

	output, err := cmd.Output()
	if err != nil {
		AppLogger.Debug("Could not list listening ports: %v", err)
		return nil
	}

	return parseListeningPorts(string(output))
}

// parseListeningPorts reads netstat output into a process to port mapping.
func parseListeningPorts(output string) map[int]string {
	listeners := map[int]string{}

	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(strings.ToUpper(line), "LISTEN") {
			continue
		}

		fields := strings.Fields(line)

		// Windows: TCP 0.0.0.0:3306 0.0.0.0:0 LISTENING 1234
		// Unix:    tcp 0 0 0.0.0.0:3306 0.0.0.0:* LISTEN 1234/mysqld
		var localAddr, pidField string
		if runtime.GOOS == "windows" {
			if len(fields) < 5 {
				continue
			}
			localAddr, pidField = fields[1], fields[4]
		} else {
			if len(fields) < 7 {
				continue
			}
			localAddr, pidField = fields[3], strings.SplitN(fields[6], "/", 2)[0]
		}

		port := portFromAddress(localAddr)
		if port == "" {
			continue
		}

		pid, err := strconv.Atoi(pidField)
		if err != nil || pid <= 0 {
			continue
		}

		if _, seen := listeners[pid]; !seen {
			listeners[pid] = port
		}
	}

	return listeners
}

// portFromAddress returns the port of an "address:port" pair, including the
// IPv6 form [::]:3306.
func portFromAddress(address string) string {
	idx := strings.LastIndex(address, ":")
	if idx == -1 || idx == len(address)-1 {
		return ""
	}

	port := address[idx+1:]
	if port == "0" || port == "*" {
		return ""
	}
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return port
}
