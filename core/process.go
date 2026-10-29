package core

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// IsServing reports whether the process is accepting connections.
//
// Presence in the process list is not enough to call a server running: a
// server whose shutdown has closed its port but which never exited still
// appears there. It may yet hold its data directory open - see
// EndStaleServers - but it serves nobody. Treating such a process as
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

// staleServerIdleWindow is how long a server that accepts no connections must
// use no CPU time at all before it counts as hung rather than busy - still
// flushing its buffer pool during a shutdown, say.
const staleServerIdleWindow = 3 * time.Second

// processActivity is what can be seen of a process from outside it.
type processActivity struct {
	Age     time.Duration // time since it started
	CPUTime time.Duration // processor time used so far
}

// EndStaleServers ends DBSwitcher's own servers that accept no connections and
// will never exit on their own, and returns the PIDs it ended.
//
// Such servers used to be logged and left alone. One here had been sitting
// for two days after its shutdown hung on the exFAT data drive, still holding
// that data directory open and its buffer pool allocated, and every status
// check reported it.
//
// A process is ended only when all of these hold, so that a server still
// starting up or still working through a shutdown is never killed:
//   - it is listening on no port;
//   - it runs one of DBSwitcher's configurations and no service owns it;
//   - it has been running longer than a server is given to start;
//   - it used no CPU time across staleServerIdleWindow.
//
// Where its shutdown had not finished, the next start of that data directory
// runs crash recovery, as after a power cut.
func EndStaleServers() []int {
	procs, err := FindServerProcesses()
	if err != nil {
		return nil
	}

	stale := StaleServers(procs)
	if len(stale) == 0 {
		return nil
	}

	services, err := FindServerServices()
	if err != nil {
		AppLogger.Debug("Could not look up services, leaving stale servers alone: %v", err)
		reportStaleServers(stale)
		return nil
	}

	candidates := ownStaleServers(stale, services)
	if len(candidates) == 0 {
		reportStaleServers(stale)
		return nil
	}

	pids := make([]int, 0, len(candidates))
	for _, proc := range candidates {
		pids = append(pids, proc.PID)
	}

	before, err := sampleProcessActivity(pids)
	if err != nil {
		AppLogger.Debug("Cannot tell whether stale servers are idle: %v", err)
		reportStaleServers(stale)
		return nil
	}
	time.Sleep(staleServerIdleWindow)
	after, err := sampleProcessActivity(pids)
	if err != nil {
		AppLogger.Debug("Cannot tell whether stale servers are idle: %v", err)
		reportStaleServers(stale)
		return nil
	}

	var ended []int
	for _, pid := range pids {
		first, seenBefore := before[pid]
		second, seenAfter := after[pid]
		if !seenBefore || !seenAfter {
			continue // it exited on its own
		}
		if !isHungServer(first, second, StartupTimeout()) {
			AppLogger.Log("Server process %d accepts no connections but is still busy; leaving it to finish", pid)
			continue
		}

		AppLogger.Warn("Ending server process %d: it accepts no connections and has done nothing for %s", pid, staleServerIdleWindow)
		if err := endProcess(pid); err != nil {
			AppLogger.Error("Could not end server process %d: %v", pid, err)
			continue
		}
		ended = append(ended, pid)
	}

	return ended
}

// ownStaleServers picks out the stale servers DBSwitcher may end: those it
// started, from one of its configurations. A service's server is the service
// manager's to deal with, and anything else is not DBSwitcher's at all.
func ownStaleServers(stale []ProcessInfo, services []ServiceInfo) []ProcessInfo {
	var own []ProcessInfo

	for _, proc := range stale {
		if proc.IsServing() {
			continue
		}

		ownedByService := false
		for _, service := range services {
			if service.PID == proc.PID {
				ownedByService = true
				break
			}
		}
		if ownedByService {
			continue
		}

		if FindConfigByPath(extractConfigFromCmdLine(proc.CommandLine)) == nil {
			continue
		}
		own = append(own, proc)
	}

	return own
}

// isHungServer decides from two samples taken staleServerIdleWindow apart
// whether a server that accepts no connections is hung. A server younger than
// startupGrace may still be recovering before it opens its port.
func isHungServer(before, after processActivity, startupGrace time.Duration) bool {
	return after.Age > startupGrace && after.CPUTime == before.CPUTime
}

// reportStaleServers logs servers that accept no connections but are left
// running, so they are visible rather than silently breaking the next switch.
func reportStaleServers(stale []ProcessInfo) {
	for _, proc := range stale {
		AppLogger.Warn("Server process %d is present but not accepting connections", proc.PID)
	}
}

// sampleProcessActivity returns the age and CPU time of each process that is
// still present.
func sampleProcessActivity(pids []int) (map[int]processActivity, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("not supported on %s", runtime.GOOS)
	}

	ids := make([]string, 0, len(pids))
	for _, pid := range pids {
		ids = append(ids, strconv.Itoa(pid))
	}

	script := fmt.Sprintf(`Get-Process -Id %s -ErrorAction SilentlyContinue | ForEach-Object { '{0} {1} {2}' -f $_.Id, [long]((Get-Date) - $_.StartTime).TotalSeconds, $_.TotalProcessorTime.Ticks }`,
		strings.Join(ids, ","))

	output, err := runPowerShell(script)
	if err != nil {
		return nil, err
	}
	return parseProcessActivity(output), nil
}

// parseProcessActivity reads "<pid> <age seconds> <CPU ticks>" lines, where a
// tick is 100 nanoseconds.
func parseProcessActivity(output string) map[int]processActivity {
	activity := make(map[int]processActivity)

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}

		pid, err1 := strconv.Atoi(fields[0])
		age, err2 := strconv.ParseInt(fields[1], 10, 64)
		ticks, err3 := strconv.ParseInt(fields[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		activity[pid] = processActivity{
			Age:     time.Duration(age) * time.Second,
			CPUTime: time.Duration(ticks) * 100 * time.Nanosecond,
		}
	}

	return activity
}

// endProcess terminates a process and waits for it to be gone.
func endProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Kill(); err != nil {
		return err
	}

	deadline := time.Now().Add(10 * time.Second)
	for ServerProcessAlive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("process %d is still present after being ended", pid)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
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
