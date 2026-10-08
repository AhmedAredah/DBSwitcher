package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcessInfo describes a running database server process.
type ProcessInfo struct {
	PID         int
	CommandLine string

	// Port is a TCP port the process listens on, empty when it accepts no
	// connections.
	Port string
}

// GetMariaDBStatus returns the current MariaDB status
func GetMariaDBStatus() MariaDBStatus {
	status := MariaDBStatus{
		IsRunning: false,
	}

	procs, err := FindServerProcesses()
	if err != nil {
		AppLogger.Warn("Process detection failed: %v", err)
		return status
	}

	// Surface a server that stopped without exiting rather than letting it
	// silently block the next switch.
	for _, stale := range StaleServers(procs) {
		status.StaleProcessIDs = append(status.StaleProcessIDs, stale.PID)
		AppLogger.Warn("Server process %d is present but not accepting connections", stale.PID)
	}

	serving := ServingServers(procs)
	if len(serving) == 0 {
		return status
	}

	proc := serving[0]
	status.IsRunning = true
	status.ProcessID = proc.PID

	// Note when the server belongs to a Windows service: it has to be stopped
	// through the service manager, not behind its back.
	if service, ok := ServiceForProcess(proc.PID); ok {
		status.ServiceName = service.Name
		status.ServiceStartMode = service.StartMode
	}

	// Log the command line for debugging
	AppLogger.Debug("Found MariaDB process with command line: %s", proc.CommandLine)

	// Extract config file from command line
	configFile := extractConfigFromCmdLine(proc.CommandLine)
	AppLogger.Debug(" Extracted config file: '%s'", configFile)

	if configFile != "" {
		status.ConfigFile = configFile

		// Normalize the config file path for comparison
		normalizedConfigFile := filepath.Clean(configFile)

		// Find matching config in our list
		for _, cfg := range AvailableConfigs {
			normalizedCfgPath := filepath.Clean(cfg.Path)
			AppLogger.Debug(" Comparing '%s' with '%s'", normalizedConfigFile, normalizedCfgPath)

			if normalizedCfgPath == normalizedConfigFile {
				status.ConfigName = cfg.Name
				status.Port = cfg.Port
				status.DataPath = cfg.DataDir
				AppLogger.Debug(" Matched config: %s, Port: %s", cfg.Name, cfg.Port)
				break
			}
		}

		if status.ConfigName == "" {
			// The server may have been started from a configuration file
			// DBSwitcher does not manage - the Windows service runs from its
			// own my.ini. Match it by data directory instead, so the server is
			// named correctly and the credentials for that database are the
			// ones used to stop it.
			if cfg := findConfigByDataDir(configFile); cfg != nil {
				status.ConfigName = cfg.Name
				status.DataPath = cfg.DataDir
				if status.Port == "" {
					status.Port = cfg.Port
				}
				AppLogger.Log("Running server serves the same data directory as configuration '%s'", cfg.Name)
			}
		}

		if status.ConfigName == "" {
			AppLogger.Debug("No matching config found for file: %s", configFile)
			AppLogger.Debug(" Available configs:")
			for _, cfg := range AvailableConfigs {
				AppLogger.Debug("   - %s: %s", cfg.Name, filepath.Clean(cfg.Path))
			}
		}
	}

	// The port the server actually listens on wins over the one in the config
	// file, which can be edited after the server was started.
	if proc.Port != "" {
		status.Port = proc.Port
	} else if status.Port == "" {
		status.Port = getCurrentPort()
	}

	// Ask the server whether it is usable, and take its version from the
	// answer: GetMariaDBVersion reports the version of the installed binaries,
	// which is printed just as happily when the server is unusable.
	probe := ProbeServerPort(status.Port)
	status.Responding = probe.Healthy()
	status.StatusMessage = probe.Message

	if probe.Version != "" {
		status.Version = probe.Version
	} else {
		status.Version = GetMariaDBVersion()
	}

	if !status.Responding {
		AppLogger.Warn("Server process %d holds port %s but is not usable: %s",
			status.ProcessID, status.Port, probe.Message)
	}

	return status
}

// IsMariaDBRunningE reports whether a server is accepting connections.
//
// A non-nil error means the lookup itself failed and the state is unknown.
// Callers that are about to start or stop a server must use this form: the
// boolean-only helper reports a failed lookup as "not running", which once let
// DBSwitcher start a second server on top of a live one.
func IsMariaDBRunningE() (bool, error) {
	procs, err := FindServerProcesses()
	if err != nil {
		return false, err
	}
	// Presence in the process list is not enough: a server that finished
	// shutting down but never exited is still listed. See ProcessInfo.IsServing.
	return len(ServingServers(procs)) > 0, nil
}

// IsMariaDBRunning is the best-effort form of IsMariaDBRunningE, for status
// displays where an unknown state is acceptable.
func IsMariaDBRunning() bool {
	running, err := IsMariaDBRunningE()
	if err != nil {
		AppLogger.Warn("Process detection failed, assuming MariaDB is not running: %v", err)
		return false
	}
	return running
}

// FindServingServer returns the server process that is accepting connections.
func FindServingServer() (ProcessInfo, bool, error) {
	procs, err := FindServerProcesses()
	if err != nil {
		return ProcessInfo{}, false, err
	}

	serving := ServingServers(procs)
	if len(serving) == 0 {
		return ProcessInfo{}, false, nil
	}
	if len(serving) > 1 {
		AppLogger.Warn("%d servers are accepting connections; acting on PID %d (port %s)",
			len(serving), serving[0].PID, serving[0].Port)
	}

	return serving[0], true, nil
}

// FindServerProcess returns the server that is accepting connections, falling
// back to any server process when none is.
//
// The fallback is for diagnostics only. Taking the first process in the list
// meant DBSwitcher could report a dead server's configuration - and load its
// credentials - while a different server was the live one.
func FindServerProcess() (ProcessInfo, bool) {
	procs, err := FindServerProcesses()
	if err != nil {
		AppLogger.Warn("Process detection failed: %v", err)
		return ProcessInfo{}, false
	}
	if len(procs) == 0 {
		return ProcessInfo{}, false
	}

	if serving := ServingServers(procs); len(serving) > 0 {
		return serving[0], true
	}
	return procs[0], true
}

// FindServerProcesses returns every running MariaDB/MySQL server process,
// annotated with the port each one listens on.
func FindServerProcesses() ([]ProcessInfo, error) {
	var (
		procs []ProcessInfo
		err   error
	)

	switch runtime.GOOS {
	case "windows":
		procs, err = findWindowsProcesses(serverProcessNames())
	default:
		procs, err = findUnixProcesses(serverProcessNames())
	}
	if err != nil {
		return nil, err
	}

	annotateListeningPorts(procs)
	return procs, nil
}

// serverProcessNames lists the executable names a server may run under.
// MariaDB 11 ships both mysqld and mariadbd, so neither name alone is enough.
func serverProcessNames() []string {
	names := []string{}

	appendName := func(name string) {
		if name == "" {
			return
		}
		for _, existing := range names {
			if strings.EqualFold(existing, name) {
				return
			}
		}
		names = append(names, name)
	}

	appendName(AppConfig.ProcessNames[runtime.GOOS])
	appendName(GetExecutableName("mysqld"))
	appendName(GetExecutableName("mariadbd"))

	return names
}

func findWindowsProcesses(names []string) ([]ProcessInfo, error) {
	filters := make([]string, 0, len(names))
	for _, name := range names {
		filters = append(filters, fmt.Sprintf("Name='%s'", name))
	}
	query := fmt.Sprintf(`Win32_Process -Filter "%s" | Select-Object ProcessId,CommandLine | ConvertTo-Json -Compress`,
		strings.Join(filters, " or "))

	// Get-CimInstance is the supported cmdlet; Get-WmiObject is kept as a
	// fallback for older PowerShell hosts where CIM is unavailable.
	var lastErr error
	for _, cmdlet := range []string{"Get-CimInstance", "Get-WmiObject"} {
		output, err := runPowerShell(cmdlet + " " + query)
		if err != nil {
			lastErr = err
			continue
		}

		procs, err := parseProcessJSON(output)
		if err != nil {
			lastErr = err
			continue
		}
		return procs, nil
	}

	return nil, fmt.Errorf("could not query running processes: %v", lastErr)
}

// runPowerShell runs a PowerShell snippet and treats anything on stderr as a
// failure, so a broken WMI query can never be read as an empty process list.
func runPowerShell(script string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("powershell: %s", detail)
	}

	if detail := strings.TrimSpace(stderr.String()); detail != "" {
		return "", fmt.Errorf("powershell: %s", detail)
	}

	return stdout.String(), nil
}

// parseProcessJSON decodes ConvertTo-Json output, which is an object for a
// single match, an array for several, and empty for none.
func parseProcessJSON(output string) ([]ProcessInfo, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, nil
	}

	type jsonProcess struct {
		ProcessID   int    `json:"ProcessId"`
		CommandLine string `json:"CommandLine"`
	}

	var entries []jsonProcess
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, fmt.Errorf("cannot parse process list: %v", err)
		}
	} else {
		var single jsonProcess
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("cannot parse process entry: %v", err)
		}
		entries = append(entries, single)
	}

	procs := make([]ProcessInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.ProcessID > 0 {
			procs = append(procs, ProcessInfo{PID: entry.ProcessID, CommandLine: entry.CommandLine})
		}
	}
	return procs, nil
}

func findUnixProcesses(names []string) ([]ProcessInfo, error) {
	cmd := exec.Command("ps", "-eo", "pid=,args=")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps failed: %v", err)
	}

	var procs []ProcessInfo
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		cmdLine := strings.Join(fields[1:], " ")
		if !matchesProcessName(cmdLine, names) {
			continue
		}

		procs = append(procs, ProcessInfo{PID: pid, CommandLine: cmdLine})
	}

	return procs, nil
}

// matchesProcessName compares the executable of a command line against the
// known server names, so unrelated processes that merely mention mysqld (a
// client, an editor, this program) are not counted as a running server.
func matchesProcessName(cmdLine string, names []string) bool {
	executable := strings.TrimSpace(cmdLine)

	// A quoted executable may contain spaces ("C:\Program Files\...\mysqld.exe").
	if strings.HasPrefix(executable, `"`) {
		if end := strings.Index(executable[1:], `"`); end != -1 {
			executable = executable[1 : end+1]
		}
	} else if idx := strings.IndexAny(executable, " \t"); idx != -1 {
		executable = executable[:idx]
	}

	executable = filepath.Base(strings.Trim(executable, `"'`))

	for _, name := range names {
		if strings.EqualFold(executable, name) {
			return true
		}
	}
	return false
}

// ShutdownTimeout returns how long to wait for a server to stop.
func ShutdownTimeout() time.Duration { return serverWaitTimeout() }

// StartupTimeout returns how long to wait for a server to start answering.
func StartupTimeout() time.Duration { return serverWaitTimeout() }

// serverWaitTimeout bounds waiting on a server. Flushing or recovering a large
// InnoDB buffer pool routinely takes longer than the configured process
// timeout, so a one minute floor applies.
func serverWaitTimeout() time.Duration {
	seconds := AppConfig.ProcessTimeoutSecs
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// WaitForMariaDBServing blocks until a usable server answers on port.
//
// When pid is given it gives up as soon as that process is gone, so a server
// that aborts is reported at once while one doing crash recovery is given the
// full timeout.
func WaitForMariaDBServing(pid int, port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastState := "no answer yet"

	for {
		probe := ProbeServerPort(port)
		if probe.Healthy() {
			AppLogger.Log("MariaDB is accepting connections on port %s", port)
			return nil
		}
		if probe.Message != "" {
			lastState = probe.Message
		}

		if pid > 0 && !ServerProcessAlive(pid) {
			return fmt.Errorf("the server exited before accepting connections on port %s (%s)", port, lastState)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for MariaDB to accept connections on port %s (%s)",
				timeout, port, lastState)
		}

		AppLogger.Debug("Waiting for the server to answer: %s", lastState)
		time.Sleep(1 * time.Second)
	}
}

// WaitForMariaDBStopped blocks until no server process remains and port is
// free again. The old code assumed a fixed three second sleep was enough,
// which it is not for a multi-gigabyte buffer pool.
func WaitForMariaDBStopped(pid int, port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastState := "unknown"

	for {
		switch {
		case pid > 0 && ServerProcessAlive(pid):
			lastState = fmt.Sprintf("server process %d has not exited", pid)
		case port != "" && !IsPortAvailable(port):
			lastState = fmt.Sprintf("port %s is still in use", port)
		default:
			AppLogger.Log("MariaDB has stopped and port %s is free", port)
			reportStaleServers()
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for MariaDB to stop (%s)", timeout, lastState)
		}

		AppLogger.Debug("Waiting for shutdown: %s", lastState)
		time.Sleep(1 * time.Second)
	}
}

func extractConfigFromCmdLine(cmdLine string) string {
	const flag = "--defaults-file="

	if idx := strings.Index(cmdLine, flag); idx != -1 {
		start := idx + len(flag)

		// The service control manager quotes each argument whole, as
		// "--defaults-file=C:\...\my.ini", and such a path contains spaces
		// here ("MariaDB 11.4"), so the value cannot end at the first space.
		if idx > 0 && (cmdLine[idx-1] == '"' || cmdLine[idx-1] == '\'') {
			if end := strings.IndexByte(cmdLine[start:], cmdLine[idx-1]); end != -1 {
				return cmdLine[start : start+end]
			}
			return cmdLine[start:]
		}

		// Or just the value is quoted: --defaults-file="C:\...\my.ini".
		if start < len(cmdLine) && (cmdLine[start] == '"' || cmdLine[start] == '\'') {
			quote := cmdLine[start]
			if end := strings.IndexByte(cmdLine[start+1:], quote); end != -1 {
				return cmdLine[start+1 : start+1+end]
			}
			return cmdLine[start+1:]
		}

		if end := strings.IndexAny(cmdLine[start:], " \t\n"); end != -1 {
			return strings.Trim(cmdLine[start:start+end], "\"'")
		}
		return strings.Trim(cmdLine[start:], "\"'")
	}

	// Look for --defaults-file parameter with space
	parts := strings.Fields(cmdLine)
	for i, part := range parts {
		if part == "--defaults-file" && i+1 < len(parts) {
			return strings.Trim(parts[i+1], "\"'")
		}
	}

	return ""
}

// getCurrentPort attempts to determine the port MariaDB is running on
func getCurrentPort() string {
	// Method 1: Try to extract port from process command line arguments
	if port := extractPortFromCmdLine(); port != "" {
		AppLogger.Debug(" Found port %s from command line arguments", port)
		return port
	}

	// Method 2: Try to query the database directly
	if port := queryDatabasePort(); port != "" {
		AppLogger.Debug(" Found port %s from database query", port)
		return port
	}

	// Method 3: Check netstat output for MariaDB/MySQL processes
	if port := getPortFromNetstat(); port != "" {
		AppLogger.Debug(" Found port %s from netstat", port)
		return port
	}

	// Method 4: Check common ports in order of likelihood
	commonPorts := []string{"3306", "3307", "3308", "3309", "3310"}

	for _, port := range commonPorts {
		if IsPortListening(port) {
			AppLogger.Debug(" Found service listening on port %s", port)
			return port
		}
	}

	AppLogger.Debug(" Could not determine port, defaulting to 3306")
	return "3306" // Default fallback
}

// extractPortFromCmdLine attempts to extract port from command line arguments
func extractPortFromCmdLine() string {
	proc, found := FindServerProcess()
	if !found {
		return ""
	}
	return extractPortFromArgs(proc.CommandLine)
}

// extractPortFromArgs reads a --port argument out of a command line.
func extractPortFromArgs(cmdLine string) string {

	// Look for --port= parameter
	if idx := strings.Index(cmdLine, "--port="); idx != -1 {
		start := idx + len("--port=")
		end := strings.IndexAny(cmdLine[start:], " \t\n")
		if end == -1 {
			return strings.Trim(cmdLine[start:], "\"'")
		}
		return strings.Trim(cmdLine[start:start+end], "\"'")
	}

	// Look for --port parameter with space
	parts := strings.Fields(cmdLine)
	for i, part := range parts {
		if part == "--port" && i+1 < len(parts) {
			return strings.Trim(parts[i+1], "\"'")
		}
	}

	return ""
}

// queryDatabasePort attempts to query the database for its port
func queryDatabasePort() string {
	// Try to connect with default credentials and query the port
	creds := GetDefaultCredentials()

	cmd, cleanup, err := clientCommand(nil, creds,
		"-e", "SHOW VARIABLES LIKE 'port';",
		"--silent",
		"--skip-column-names")
	if err != nil {
		AppLogger.Debug("Cannot query port from server: %v", err)
		return ""
	}
	defer cleanup()

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Parse output: should be "port\t3306"
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 && parts[0] == "port" {
			return strings.TrimSpace(parts[1])
		}
	}

	return ""
}

// getPortFromNetstat attempts to find MariaDB port from netstat output
func getPortFromNetstat() string {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("netstat", "-ano")
	default:
		cmd = exec.Command("netstat", "-tlnp")
	}

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Get the PID of MariaDB process
	proc, found := FindServerProcess()
	if !found {
		return ""
	}
	pidStr := strconv.Itoa(proc.PID)

	// Parse netstat output to find ports used by this PID
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "LISTENING") || strings.Contains(line, "LISTEN") {
			fields := strings.Fields(line)

			// Windows format: TCP 0.0.0.0:3306 0.0.0.0:0 LISTENING 1234
			// Unix format: tcp 0 0 0.0.0.0:3306 0.0.0.0:* LISTEN 1234/mysqld

			var localAddr, processInfo string
			if runtime.GOOS == "windows" {
				if len(fields) >= 5 {
					localAddr = fields[1]
					processInfo = fields[4]
				}
			} else {
				if len(fields) >= 4 {
					localAddr = fields[3]
					if len(fields) >= 7 {
						processInfo = fields[6]
					}
				}
			}

			// Check if this line matches our PID
			if strings.Contains(processInfo, pidStr) {
				// Extract port from address (format: ip:port)
				if colonIdx := strings.LastIndex(localAddr, ":"); colonIdx != -1 {
					port := localAddr[colonIdx+1:]
					if port != "0" && len(port) > 0 {
						return port
					}
				}
			}
		}
	}

	return ""
}

// GetMariaDBVersion returns the MariaDB version
func GetMariaDBVersion() string {
	mysqldPath := filepath.Join(AppConfig.MariaDBBin, "mysqld")
	if runtime.GOOS == "windows" {
		mysqldPath += ".exe"
	}

	cmd := exec.Command(mysqldPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		return "Unknown"
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		parts := strings.Fields(lines[0])
		for i, part := range parts {
			if strings.Contains(part, "Ver") && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return "Unknown"
}

// GetDefaultDataDir returns the default data directory for MariaDB
func GetDefaultDataDir() string {
	switch runtime.GOOS {
	case "windows":
		if AppConfig.MariaDBBin != "" {
			return filepath.Join(filepath.Dir(AppConfig.MariaDBBin), "data")
		}
		return `C:\Program Files\MariaDB\data`
	case "linux":
		return "/var/lib/mysql"
	case "darwin":
		return "/usr/local/var/mysql"
	case "freebsd":
		return "/var/db/mysql"
	}
	return ""
}

// StartMariaDBWithConfig starts MariaDB with the specified configuration file
func StartMariaDBWithConfig(configFile string) error {
	AppLogger.Log("========================================")
	AppLogger.Log("STARTING MARIADB")
	AppLogger.Log("========================================")

	// Refuse to start on top of a server that is actually serving. A failed
	// lookup aborts too: acting on an unknown state is what left users with a
	// confusing "process not found" after the port bind failed.
	serving, isServing, err := FindServingServer()
	if err != nil {
		AppLogger.Error(" Cannot determine whether MariaDB is running: %v", err)
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}
	if isServing {
		AppLogger.Log("MariaDB is already running (PID %d, port %s)", serving.PID, serving.Port)
		return fmt.Errorf("MariaDB is already running on port %s (PID %d) - please stop it first",
			serving.Port, serving.PID)
	}

	// Validate config.MariaDBBin
	if AppConfig.MariaDBBin == "" {
		AppLogger.Error(" MariaDB binary path is empty!")
		return fmt.Errorf("MariaDB binary path not configured")
	}

	// Check if binary directory exists
	if !PathExists(AppConfig.MariaDBBin) {
		AppLogger.Error(" MariaDB binary directory does not exist: %s", AppConfig.MariaDBBin)
		return fmt.Errorf("MariaDB binary directory not found: %s", AppConfig.MariaDBBin)
	}

	// Build full mysqld path
	mysqldPath := filepath.Join(AppConfig.MariaDBBin, GetExecutableName("mysqld"))
	AppLogger.Log("Full mysqld path: %s", mysqldPath)

	// Check if mysqld exists
	if !PathExists(mysqldPath) {
		AppLogger.Error(" mysqld not found at: %s", mysqldPath)

		// Try mariadbd as alternative
		mariadbdPath := filepath.Join(AppConfig.MariaDBBin, GetExecutableName("mariadbd"))
		if PathExists(mariadbdPath) {
			AppLogger.Log("Found mariadbd instead of mysqld at: %s", mariadbdPath)
			mysqldPath = mariadbdPath
		} else {
			// Try to find mysqld using which/where
			var findCmd *exec.Cmd
			if runtime.GOOS == "windows" {
				findCmd = exec.Command("where", "mysqld.exe")
			} else {
				findCmd = exec.Command("which", "mysqld")
			}

			if output, err := findCmd.Output(); err == nil {
				foundPath := strings.TrimSpace(string(output))
				AppLogger.Log("Found mysqld at: %s", foundPath)
				mysqldPath = foundPath
			} else {
				AppLogger.Log("Could not find mysqld in system PATH")
				return fmt.Errorf("mysqld not found at: %s", mysqldPath)
			}
		}
	}

	// Check if config file exists
	if !PathExists(configFile) {
		AppLogger.Error(" Configuration file not found: %s", configFile)
		return fmt.Errorf("configuration file not found: %s", configFile)
	}

	// Get absolute path for config file
	absConfigFile, err := filepath.Abs(configFile)
	if err != nil {
		AppLogger.Error(" Cannot get absolute path for config: %v", err)
		return fmt.Errorf("cannot get absolute path for config: %v", err)
	}
	AppLogger.Log("Absolute config file path: %s", absConfigFile)

	// The server's own log file is named after the configuration it serves.
	configName := ""
	if cfg := FindConfigByPath(absConfigFile); cfg != nil {
		configName = cfg.Name
	}

	// Parse config first to validate it
	AppLogger.Log("Parsing configuration file...")
	configData := ParseConfigFile(configFile)
	AppLogger.Log("Config parsed - DataDir: %s, Port: %s", configData.DataDir, configData.Port)

	// Validate and prepare data directory
	if configData.DataDir != "" {
		// Convert to absolute path if relative
		if !filepath.IsAbs(configData.DataDir) {
			configData.DataDir = filepath.Join(filepath.Dir(absConfigFile), configData.DataDir)
			AppLogger.Log("Converted relative datadir to absolute: %s", configData.DataDir)
		}

		if !PathExists(configData.DataDir) {
			AppLogger.Log("Data directory does not exist, creating: %s", configData.DataDir)
			if err := os.MkdirAll(configData.DataDir, 0755); err != nil {
				AppLogger.Error(" Failed to create data directory: %v", err)
				return fmt.Errorf("failed to create data directory: %v", err)
			}
		}

		// Check if data directory is empty and needs initialization
		if isEmpty, _ := IsDirEmpty(configData.DataDir); isEmpty {
			AppLogger.Log("Data directory is empty, needs initialization")
			if err := InitializeDataDir(configData.DataDir); err != nil {
				AppLogger.Error(" Failed to initialize data directory: %v", err)
				// Try alternative initialization
				if err := InitializeDataDirAlternative(configData.DataDir, absConfigFile); err != nil {
					return fmt.Errorf("failed to initialize data directory: %v", err)
				}
			}
		} else {
			// Check for critical files in data directory
			if !ValidateDataDirectory(configData.DataDir) {
				AppLogger.Warn(" Data directory may be corrupted or incomplete")
			}
		}
	}

	// Re-check now that the data directory work is done: nothing may be
	// serving. A process that stopped without exiting is reported but does not
	// block the start - it holds no port and no data directory, and demanding
	// an empty process list made such a leftover break every switch.
	AppLogger.Log("Checking that no MySQL/MariaDB server is serving...")
	procs, err := FindServerProcesses()
	if err != nil {
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}
	if live := ServingServers(procs); len(live) > 0 {
		return fmt.Errorf("MySQL/MariaDB is still running on port %s (PID %d) - stop it gracefully with credentials before starting a new instance",
			live[0].Port, live[0].PID)
	}
	for _, stale := range StaleServers(procs) {
		AppLogger.Warn("Ignoring server process %d, which is not accepting connections", stale.PID)
	}

	// Double-check the port is free
	if !IsPortAvailable(configData.Port) {
		AppLogger.Log("Port %s is still in use", configData.Port)
		FindProcessUsingPort(configData.Port)
		return fmt.Errorf("cannot start - port %s is occupied by another process", configData.Port)
	}

	AppLogger.Log("Port %s is confirmed available", configData.Port)

	// No --validate-config probe: MariaDB has no such option (it is a MySQL 8
	// flag), and running it started a whole server that initialised InnoDB and
	// rewrote ibtmp1 inside the data directory before aborting with "unknown
	// option". The server's own log, read below, is the better signal.

	// Start the MariaDB process with better error capture
	AppLogger.Log("Starting MariaDB with configuration...")

	// Create command with proper arguments
	args := []string{
		fmt.Sprintf("--defaults-file=%s", absConfigFile),
		"--console", // Add console output for debugging
	}

	cmd := exec.Command(mysqldPath, args...)

	// Send the server's output to a file, never to a pipe owned by this
	// process. The server is detached and outlives this one, so os/exec would
	// hand it a pipe whose reader disappears when this process exits; the
	// server can then block on its own log writes and never finish shutting
	// down. That is how servers were left alive with no listener, refusing to
	// exit, and blocking every later switch.
	serverLog, logOffset, err := openServerLog(configName)
	if err != nil {
		return err
	}
	defer serverLog.Close() // the child keeps its own handle
	cmd.Stdout = serverLog
	cmd.Stderr = serverLog
	AppLogger.Log("Server output: %s", serverLog.Name())

	// Set working directory to bin directory
	cmd.Dir = AppConfig.MariaDBBin

	// Platform-specific configuration
	if runtime.GOOS == "windows" {
		// Use CREATE_NEW_PROCESS_GROUP to detach process from parent
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,       // Hide console window
			CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP - allows process to survive parent termination
		}
	}

	AppLogger.Log("Executing command: %s %s", mysqldPath, strings.Join(args, " "))

	// Start the process
	err = cmd.Start()
	if err != nil {
		AppLogger.Error(" Failed to start process: %v", err)
		return fmt.Errorf("failed to start MariaDB: %v", err)
	}

	startedPID := cmd.Process.Pid
	AppLogger.Log("Process started with PID: %d", startedPID)

	// Release the process so it's detached from parent and can survive parent termination
	err = cmd.Process.Release()
	if err != nil {
		AppLogger.Warn(" Could not release process: %v", err)
		// Continue anyway - the process group flag should still work
	} else {
		AppLogger.Log("Process successfully detached from parent")
	}

	// Wait for the server to answer, rather than sleeping a fixed three
	// seconds and then retrying a fixed number of times: a data directory
	// needing recovery takes longer, and a server that aborts is noticed as
	// soon as its process is gone.
	AppLogger.Info("Waiting for MariaDB to accept connections...")
	if err := WaitForMariaDBServing(startedPID, configData.Port, StartupTimeout()); err != nil {
		serverOutput := readServerLogFrom(serverLog.Name(), logOffset)
		if serverOutput != "" {
			AppLogger.Error(" Server output: %s", serverOutput)
			return fmt.Errorf("MariaDB failed to start: %s - see %s",
				ParseMariaDBError(serverOutput), serverLog.Name())
		}
		return fmt.Errorf("MariaDB failed to start: %v - see %s", err, serverLog.Name())
	}

	// Save the last used config
	AppConfig.LastUsedConfig = absConfigFile
	SaveConfig()

	// Update global status
	CurrentStatus = GetMariaDBStatus()

	AppLogger.Info("========================================")
	AppLogger.Info("MARIADB STARTED SUCCESSFULLY")
	AppLogger.Info("========================================")

	// Show success notification
	if config := FindConfigByPath(absConfigFile); config != nil {
		NotifyMariaDBStarted(config.Name)
	} else {
		NotifyMariaDBStarted("Unknown")
	}

	return nil
}
