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
}

// GetMariaDBStatus returns the current MariaDB status
func GetMariaDBStatus() MariaDBStatus {
	status := MariaDBStatus{
		IsRunning: false,
	}

	proc, found := FindServerProcess()
	status.IsRunning = found

	if !status.IsRunning {
		return status
	}

	status.ProcessID = proc.PID

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
			AppLogger.Debug("No matching config found for file: %s", configFile)
			AppLogger.Debug(" Available configs:")
			for _, cfg := range AvailableConfigs {
				AppLogger.Debug("   - %s: %s", cfg.Name, filepath.Clean(cfg.Path))
			}
		}
	}

	// Always confirm the port against the live server; a config file can be
	// edited after the server was started.
	if status.Port == "" || !IsPortListening(status.Port) {
		if detected := getCurrentPort(); detected != "" {
			status.Port = detected
		}
	}

	// Try to get version
	status.Version = GetMariaDBVersion()

	return status
}

// IsMariaDBRunningE reports whether a server process exists.
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
	return len(procs) > 0, nil
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

// FindServerProcess returns the first running server process, if any.
func FindServerProcess() (ProcessInfo, bool) {
	procs, err := FindServerProcesses()
	if err != nil {
		AppLogger.Warn("Process detection failed: %v", err)
		return ProcessInfo{}, false
	}
	if len(procs) == 0 {
		return ProcessInfo{}, false
	}
	return procs[0], true
}

// FindServerProcesses returns every running MariaDB/MySQL server process.
func FindServerProcesses() ([]ProcessInfo, error) {
	switch runtime.GOOS {
	case "windows":
		return findWindowsProcesses(serverProcessNames())
	default:
		return findUnixProcesses(serverProcessNames())
	}
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

// ShutdownTimeout returns how long to wait for a server to stop. Flushing a
// large InnoDB buffer pool routinely takes longer than the configured process
// timeout, so a one minute floor applies.
func ShutdownTimeout() time.Duration {
	seconds := AppConfig.ProcessTimeoutSecs
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// WaitForMariaDBStopped blocks until no server process remains and port is
// free again. The old code assumed a fixed three second sleep was enough,
// which it is not for a multi-gigabyte buffer pool.
func WaitForMariaDBStopped(port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastState := "unknown"

	for {
		running, err := IsMariaDBRunningE()
		switch {
		case err != nil:
			lastState = fmt.Sprintf("process state unknown: %v", err)
		case running:
			lastState = "server process is still running"
		case port != "" && !IsPortAvailable(port):
			lastState = fmt.Sprintf("port %s is still in use", port)
		default:
			AppLogger.Log("MariaDB has stopped and port %s is free", port)
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
	// Look for --defaults-file= parameter
	if idx := strings.Index(cmdLine, "--defaults-file="); idx != -1 {
		start := idx + len("--defaults-file=")
		end := strings.IndexAny(cmdLine[start:], " \t\n")
		if end == -1 {
			return strings.Trim(cmdLine[start:], "\"'")
		}
		return strings.Trim(cmdLine[start:start+end], "\"'")
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
	cmdLine := proc.CommandLine

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
	
	// Check if MariaDB is already running. A failed lookup aborts the start:
	// launching on top of a live server corrupts nothing but does leave the
	// user with a confusing "process not found" after the port bind fails.
	running, err := IsMariaDBRunningE()
	if err != nil {
		AppLogger.Error(" Cannot determine whether MariaDB is running: %v", err)
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}
	if running {
		AppLogger.Log("MariaDB is already running")
		return fmt.Errorf("MariaDB is already running - please stop it first")
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

	// Check if MySQL/MariaDB is still running - no force stop
	AppLogger.Log("Checking if all MySQL/MariaDB processes are stopped...")
	running, err = IsMariaDBRunningE()
	if err != nil {
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}
	if running {
		return fmt.Errorf("MySQL/MariaDB is still running - please stop it gracefully with credentials before starting a new instance")
	}

	// Double-check the port is free
	if !IsPortAvailable(configData.Port) {
		AppLogger.Log("Port %s is still in use", configData.Port)
		FindProcessUsingPort(configData.Port)
		return fmt.Errorf("cannot start - port %s is occupied by another process", configData.Port)
	}

	AppLogger.Log("Port %s is confirmed available", configData.Port)

	// First, try to validate the config file syntax
	AppLogger.Log("Validating configuration file syntax...")
	if err := ValidateConfigFile(mysqldPath, absConfigFile); err != nil {
		AppLogger.Warn(" Config file validation failed: %v", err)
		// Continue anyway, as some versions don't support --validate-config
	}

	// Start the MariaDB process with better error capture
	AppLogger.Log("Starting MariaDB with configuration...")
	
	// Create command with proper arguments
	args := []string{
		fmt.Sprintf("--defaults-file=%s", absConfigFile),
		"--console", // Add console output for debugging
	}
	
	cmd := exec.Command(mysqldPath, args...)

	// Capture both stdout and stderr. The buffers are read below while the
	// copy goroutines started by os/exec may still be writing, so they have to
	// be synchronised.
	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Set working directory to bin directory
	cmd.Dir = AppConfig.MariaDBBin
	
	// Platform-specific configuration
	if runtime.GOOS == "windows" {
		// Use CREATE_NEW_PROCESS_GROUP to detach process from parent
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true, // Hide console window
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
	
	AppLogger.Log("Process started with PID: %d", cmd.Process.Pid)
	
	// Release the process so it's detached from parent and can survive parent termination
	err = cmd.Process.Release()
	if err != nil {
		AppLogger.Warn(" Could not release process: %v", err)
		// Continue anyway - the process group flag should still work
	} else {
		AppLogger.Log("Process successfully detached from parent")
	}
	
	// Brief wait to allow process to initialize before verification
	initWaitTime := 3
	if AppConfig.ProcessTimeoutSecs > 10 {
		initWaitTime = AppConfig.ProcessTimeoutSecs / 10 // Use 10% of process timeout for init wait
	}
	AppLogger.Info("Waiting %d seconds for MariaDB to initialize...", initWaitTime)
	time.Sleep(time.Duration(initWaitTime) * time.Second)
	
	// Additional verification - try to connect
	AppLogger.Info("Verifying MariaDB is accessible...")
	maxRetries := AppConfig.MaxRetryAttempts
	if maxRetries <= 0 {
		maxRetries = 3 // fallback
	}
	for i := 0; i < maxRetries; i++ {
		if IsMariaDBRunning() && IsPortListening(configData.Port) {
			AppLogger.Log("MariaDB is running and accepting connections")
			break
		}
		AppLogger.Log("Waiting for MariaDB to be ready... (%d/%d)", i+1, maxRetries)
		time.Sleep(1 * time.Second)
	}

	// Final verification
	if !IsMariaDBRunning() {
		stdoutStr := stdout.String()
		stderrStr := stderr.String()
		AppLogger.Error(" MariaDB process not found after startup")
		if stdoutStr != "" {
			AppLogger.Log("Final stdout: %s", stdoutStr)
		}
		if stderrStr != "" {
			AppLogger.Log("Final stderr: %s", stderrStr)
		}

		// Report why the server gave up instead of the unhelpful
		// "process not found".
		serverOutput := stderrStr
		if serverOutput == "" {
			serverOutput = stdoutStr
		}
		if serverOutput != "" {
			return fmt.Errorf("MariaDB failed to start: %s", ParseMariaDBError(serverOutput))
		}
		return fmt.Errorf("MariaDB failed to start - process not found. Check logs for details")
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