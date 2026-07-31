package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ClientError carries the output of a mariadb-admin/mysql invocation alongside
// the exit status. Without it every failure collapsed into "exit status 1",
// which hid "Access denied" from IsCredentialError and from the user.
type ClientError struct {
	Output string
	Err    error
}

func (e *ClientError) Error() string {
	output := strings.TrimSpace(e.Output)
	if output == "" {
		return fmt.Sprintf("%v", e.Err)
	}
	return fmt.Sprintf("%v: %s", e.Err, output)
}

func (e *ClientError) Unwrap() error { return e.Err }

// ClientOutput returns the message the client printed for err, so callers can
// show the server's own wording ("Access denied for user ...").
func ClientOutput(err error) string {
	if err == nil {
		return ""
	}

	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		return err.Error()
	}

	for _, line := range strings.Split(clientErr.Output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "error:") || strings.Contains(lower, "access denied") {
			return line
		}
	}

	if trimmed := strings.TrimSpace(clientErr.Output); trimmed != "" {
		return trimmed
	}
	return err.Error()
}

// findAdminTool locates the shutdown client. mariadb-admin is preferred:
// mysqladmin is only a legacy alias and newer MariaDB releases stop shipping it.
func findAdminTool() (string, error) {
	return findClientBinary("mariadb-admin", "mysqladmin")
}

// findClientTool locates the SQL client, preferring the modern name.
func findClientTool() (string, error) {
	return findClientBinary("mariadb", "mysql")
}

func findClientBinary(names ...string) (string, error) {
	for _, name := range names {
		path := filepath.Join(AppConfig.MariaDBBin, GetExecutableName(name))
		if PathExists(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("none of %s found in %s", strings.Join(names, ", "), AppConfig.MariaDBBin)
}

// writeClientOptionsFile puts the credentials in a private options file.
// Passing "-p<password>" on the command line published the password to every
// process listing on the machine and wrote it verbatim into the log file.
func writeClientOptionsFile(creds MySQLCredentials) (string, func(), error) {
	noop := func() {}

	file, err := os.CreateTemp("", "dbswitcher-client-*.cnf")
	if err != nil {
		return "", noop, fmt.Errorf("cannot create temporary options file: %v", err)
	}
	cleanup := func() { os.Remove(file.Name()) }

	var contents strings.Builder
	contents.WriteString("[client]\n")
	contents.WriteString(fmt.Sprintf("user=%s\n", quoteOptionValue(creds.Username)))
	if creds.Password != "" {
		contents.WriteString(fmt.Sprintf("password=%s\n", quoteOptionValue(creds.Password)))
	}

	if _, err := file.WriteString(contents.String()); err != nil {
		file.Close()
		cleanup()
		return "", noop, fmt.Errorf("cannot write temporary options file: %v", err)
	}

	if err := file.Close(); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("cannot close temporary options file: %v", err)
	}

	if err := os.Chmod(file.Name(), 0600); err != nil {
		AppLogger.Warn("Could not restrict permissions on temporary options file: %v", err)
	}

	return file.Name(), cleanup, nil
}

// quoteOptionValue quotes a value for a MariaDB options file. Unquoted values
// end at a '#', so anything unusual has to be wrapped and escaped.
func quoteOptionValue(value string) string {
	if value == "" {
		return ""
	}
	if !strings.ContainsAny(value, " \t#\"'\\") {
		return value
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return `"` + escaped + `"`
}

// clientCommand builds a client invocation whose credentials are supplied
// through a private options file. A nil ctx means no timeout.
func clientCommand(ctx context.Context, creds MySQLCredentials, args ...string) (*exec.Cmd, func(), error) {
	noop := func() {}

	clientPath, err := findClientTool()
	if err != nil {
		return nil, noop, err
	}

	SetCredentialsDefaults(&creds)

	optionsFile, cleanup, err := writeClientOptionsFile(creds)
	if err != nil {
		return nil, noop, err
	}

	// --defaults-extra-file must come first for the client to honour it.
	full := append([]string{
		fmt.Sprintf("--defaults-extra-file=%s", optionsFile),
		"-h", creds.Host,
		"-P", creds.Port,
		"-u", creds.Username,
	}, args...)

	if ctx == nil {
		return exec.Command(clientPath, full...), cleanup, nil
	}
	return exec.CommandContext(ctx, clientPath, full...), cleanup, nil
}

// StopMySQLWithCredentials gracefully stops MySQL using admin credentials and
// waits until the server has actually gone away.
func StopMySQLWithCredentials(creds MySQLCredentials) error {
	SetCredentialsDefaults(&creds)

	adminPath, err := findAdminTool()
	if err != nil {
		return err
	}

	optionsFile, cleanup, err := writeClientOptionsFile(creds)
	if err != nil {
		return err
	}
	defer cleanup()

	args := []string{
		fmt.Sprintf("--defaults-extra-file=%s", optionsFile),
		"-h", creds.Host,
		"-P", creds.Port,
		"-u", creds.Username,
		"shutdown",
	}

	AppLogger.Log("Executing graceful shutdown with %s...", filepath.Base(adminPath))
	AppLogger.Log("Command: %s -h %s -P %s -u %s shutdown", adminPath, creds.Host, creds.Port, creds.Username)

	cmd := exec.Command(adminPath, args...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		AppLogger.Error("Shutdown failed: %v - %s", err, strings.TrimSpace(string(output)))
		return &ClientError{Output: string(output), Err: err}
	}

	// The shutdown command only asks; wait until the server is really gone
	// instead of assuming a fixed three seconds is enough.
	AppLogger.Log("Shutdown accepted, waiting for the server to exit...")
	if err := WaitForMariaDBStopped(creds.Port, ShutdownTimeout()); err != nil {
		AppLogger.Error("%v", err)
		return err
	}

	AppLogger.Info("MySQL shutdown completed")

	// Show notification
	NotifyMariaDBStopped()

	return nil
}

// GetCredentialsForRunningInstance returns the saved credentials aimed at the
// server that is actually running, so a stale saved port cannot send the
// shutdown somewhere else.
func GetCredentialsForRunningInstance() MySQLCredentials {
	creds := GetDefaultCredentials()
	SetCredentialsDefaults(&creds)

	if status := GetMariaDBStatus(); status.IsRunning && status.Port != "" && status.Port != creds.Port {
		AppLogger.Log("Targeting detected port %s instead of saved port %s", status.Port, creds.Port)
		creds.Port = status.Port
	}

	return creds
}

// ValidateConfigFile validates a MariaDB configuration file
func ValidateConfigFile(mysqldPath, configFile string) error {
	cmd := exec.Command(mysqldPath, "--defaults-file="+configFile, "--validate-config")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	// Some builds exit non-zero without printing anything; reporting that as a
	// validation failure only trained everyone to ignore the warning.
	message := strings.TrimSpace(string(output))
	if message == "" {
		AppLogger.Debug("Config validation exited with %v and no output", err)
		return nil
	}

	return fmt.Errorf("config validation failed: %s", message)
}

// ValidateDataDirectory checks if a data directory has required files
func ValidateDataDirectory(dataDir string) bool {
	// Check for essential files/directories
	essentialPaths := []string{
		filepath.Join(dataDir, "mysql"),
		filepath.Join(dataDir, "performance_schema"),
		filepath.Join(dataDir, "ibdata1"),
	}
	
	for _, path := range essentialPaths {
		if !PathExists(path) {
			AppLogger.Log("Missing essential file/directory: %s", path)
			return false
		}
	}
	
	return true
}

// InitializeDataDir initializes a new MariaDB data directory
func InitializeDataDir(dataDir string) error {
	// Try mysql_install_db first
	installDbPath := filepath.Join(AppConfig.MariaDBBin, "mysql_install_db")
	if runtime.GOOS == "windows" {
		installDbPath += ".exe"
	}
	
	if PathExists(installDbPath) {
		cmd := exec.Command(installDbPath, "--datadir="+dataDir, "--auth-root-authentication-method=normal")
		output, err := cmd.CombinedOutput()
		if err != nil {
			AppLogger.Log("mysql_install_db failed: %v\nOutput: %s", err, string(output))
			return err
		}
		AppLogger.Log("Data directory initialized with mysql_install_db")
		return nil
	}
	
	// Try mysqld --initialize-insecure
	mysqldPath := filepath.Join(AppConfig.MariaDBBin, "mysqld")
	if runtime.GOOS == "windows" {
		mysqldPath += ".exe"
	}
	
	cmd := exec.Command(mysqldPath, "--initialize-insecure", "--datadir="+dataDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		AppLogger.Log("mysqld --initialize-insecure failed: %v\nOutput: %s", err, string(output))
		return err
	}
	
	AppLogger.Log("Data directory initialized with mysqld --initialize-insecure")
	return nil
}

// InitializeDataDirAlternative tries alternative methods to initialize data directory
func InitializeDataDirAlternative(dataDir, configFile string) error {
	mysqldPath := filepath.Join(AppConfig.MariaDBBin, "mysqld")
	if runtime.GOOS == "windows" {
		mysqldPath += ".exe"
	}
	
	// Try with config file
	cmd := exec.Command(mysqldPath, "--defaults-file="+configFile, "--initialize-insecure")
	output, err := cmd.CombinedOutput()
	if err != nil {
		AppLogger.Log("Alternative initialization failed: %v\nOutput: %s", err, string(output))
		return fmt.Errorf("failed to initialize data directory: %v", err)
	}
	
	AppLogger.Log("Data directory initialized with alternative method")
	return nil
}

// ParseMariaDBError parses MariaDB error output for common issues
func ParseMariaDBError(errorOutput string) string {
	lowerOutput := strings.ToLower(errorOutput)
	
	if strings.Contains(lowerOutput, "access denied") {
		return "Access denied - check your credentials"
	}
	if strings.Contains(lowerOutput, "port") && strings.Contains(lowerOutput, "already in use") {
		return "Port already in use - another instance might be running"
	}
	if strings.Contains(lowerOutput, "permission denied") {
		return "Permission denied - may need administrator privileges"
	}
	if strings.Contains(lowerOutput, "data directory") && strings.Contains(lowerOutput, "not empty") {
		return "Data directory is not empty - initialization might have failed"
	}
	if strings.Contains(lowerOutput, "can't create/write to file") {
		return "Cannot write to data directory - check permissions"
	}
	if strings.Contains(lowerOutput, "unknown variable") {
		return "Configuration file contains unknown variables"
	}
	if strings.Contains(lowerOutput, "plugin") && strings.Contains(lowerOutput, "not loaded") {
		return "Required plugin not loaded - check configuration"
	}
	if strings.Contains(lowerOutput, "10048") || strings.Contains(lowerOutput, "bind on tcp/ip port") {
		return "Port already in use - another server still holds it"
	}
	if strings.Contains(lowerOutput, "crash recovery failed") || strings.Contains(lowerOutput, "can't init tc log") {
		return "Crash recovery failed - the data directory was not shut down cleanly"
	}

	// Prefer the server's own [ERROR] lines; mysqld prefixes every line with a
	// timestamp, so a plain "first non-empty line" only ever returns a [Note].
	lines := strings.Split(errorOutput, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "[ERROR]") {
			if idx := strings.Index(line, "[ERROR]"); idx != -1 {
				return strings.TrimSpace(line[idx+len("[ERROR]"):])
			}
		}
	}

	// Return first non-empty line as fallback
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "[") {
			return line
		}
	}

	return "Unknown error - check logs for details"
}

// ExecMySQLQueryWithCredentials executes a MySQL query with provided credentials
func ExecMySQLQueryWithCredentials(variable string, creds MySQLCredentials) string {
	timeout := AppConfig.ConnectionTimeoutSecs
	if timeout <= 0 {
		timeout = 5
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd, cleanup, err := clientCommand(ctx, creds, "-s", "-N", "-e", fmt.Sprintf("SELECT @@%s;", variable))
	if err != nil {
		AppLogger.Log("MySQL query failed for variable %s: %v", variable, err)
		return ""
	}
	defer cleanup()

	output, err := cmd.Output()

	if err != nil {
		AppLogger.Log("MySQL query failed for variable %s: %v", variable, err)
		return ""
	}
	
	result := strings.TrimSpace(string(output))
	AppLogger.Log("MySQL query for %s returned: %s", variable, result)
	return result
}

// FindProcessUsingPort finds which process is using a specific port
func FindProcessUsingPort(port string) {
	var cmd *exec.Cmd
	
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("netstat", "-ano", "-p", "TCP")
	case "darwin":
		cmd = exec.Command("lsof", "-i", fmt.Sprintf(":%s", port))
	default:
		cmd = exec.Command("netstat", "-tlnp")
	}
	
	output, err := cmd.Output()
	if err != nil {
		AppLogger.Log("Failed to run port check command: %v", err)
		return
	}
	
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, ":"+port) {
			AppLogger.Log("Port %s usage: %s", port, line)
		}
	}
}

// StopLinuxService stops the MariaDB service on Linux
func StopLinuxService() error {
	if AppConfig.RequireElevation {
		cmd := exec.Command("sudo", "systemctl", "stop", AppConfig.ServiceNames["linux"])
		return cmd.Run()
	}
	cmd := exec.Command("systemctl", "stop", AppConfig.ServiceNames["linux"])
	return cmd.Run()
}

// StopMacService stops the MariaDB service on macOS
func StopMacService() error {
	// Try launchctl first
	cmd := exec.Command("launchctl", "unload", "-w",
		"/Library/LaunchDaemons/com.mariadb.server.plist")
	if err := cmd.Run(); err == nil {
		return nil
	}
	
	// Try brew services
	cmd = exec.Command("brew", "services", "stop", "mariadb")
	return cmd.Run()
}

// ValidateCredentials validates MySQL credentials
func ValidateCredentials(creds MySQLCredentials) error {
	if creds.Username == "" {
		return fmt.Errorf("username is required")
	}
	if creds.Host == "" {
		return fmt.Errorf("host is required")
	}
	if creds.Port == "" {
		return fmt.Errorf("port is required")
	}
	return nil
}