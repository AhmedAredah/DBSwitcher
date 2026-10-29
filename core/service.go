package core

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ServiceInfo describes a Windows service that runs a database server.
type ServiceInfo struct {
	Name        string // the name the service control manager knows
	DisplayName string
	State       string // Running, Stopped, ...
	StartMode   string // Auto, Manual, Disabled
	PID         int
	BinaryPath  string
	ConfigFile  string // --defaults-file taken from BinaryPath
}

// IsRunning reports whether the service control manager considers it started.
func (s ServiceInfo) IsRunning() bool { return strings.EqualFold(s.State, "Running") }

// StartsAutomatically reports whether the service claims its port and data
// directory on its own after a reboot.
func (s ServiceInfo) StartsAutomatically() bool { return strings.EqualFold(s.StartMode, "Auto") }

// Describe renders the service for a status line.
func (s ServiceInfo) Describe() string {
	if s.StartMode == "" {
		return s.Name
	}
	return fmt.Sprintf("%s (%s start)", s.Name, strings.ToLower(s.StartMode))
}

const serviceCacheTTL = 30 * time.Second

var (
	serviceCacheMu   sync.Mutex
	serviceCache     []ServiceInfo
	serviceCacheTime time.Time
)

// FindServerServices returns the Windows services that run a MariaDB or MySQL
// server, identified by the binary they launch rather than by name.
//
// Guessing the name does not work: the installer names the service after the
// version - MariaDB11_4 on this machine - while the built-in default was
// "MariaDB". Every service query therefore failed, service control stayed
// switched off, and a service-managed server was stopped behind the service
// manager's back.
//
// Results are cached briefly: this is a PowerShell call, and the status view
// refreshes on a timer.
func FindServerServices() ([]ServiceInfo, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}

	serviceCacheMu.Lock()
	defer serviceCacheMu.Unlock()

	if serviceCache != nil && time.Since(serviceCacheTime) < serviceCacheTTL {
		return serviceCache, nil
	}

	const query = `Get-CimInstance Win32_Service | Where-Object { $_.PathName -match 'mysqld|mariadbd' } | ` +
		`Select-Object Name,DisplayName,State,StartMode,ProcessId,PathName | ConvertTo-Json -Compress`

	output, err := runPowerShell(query)
	if err != nil {
		return nil, fmt.Errorf("could not query services: %v", err)
	}

	services, err := parseServiceJSON(output)
	if err != nil {
		return nil, err
	}

	serviceCache = services
	serviceCacheTime = time.Now()
	return services, nil
}

// RefreshServerServices drops the cache, for use right after starting or
// stopping a service.
func RefreshServerServices() {
	serviceCacheMu.Lock()
	defer serviceCacheMu.Unlock()
	serviceCache = nil
}

// parseServiceJSON decodes ConvertTo-Json output, which is an object for a
// single service, an array for several, and empty for none.
func parseServiceJSON(output string) ([]ServiceInfo, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, nil
	}

	type jsonService struct {
		Name        string `json:"Name"`
		DisplayName string `json:"DisplayName"`
		State       string `json:"State"`
		StartMode   string `json:"StartMode"`
		ProcessID   int    `json:"ProcessId"`
		PathName    string `json:"PathName"`
	}

	var entries []jsonService
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, fmt.Errorf("cannot parse service list: %v", err)
		}
	} else {
		var single jsonService
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("cannot parse service entry: %v", err)
		}
		entries = append(entries, single)
	}

	services := make([]ServiceInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.Name == "" {
			continue
		}
		services = append(services, ServiceInfo{
			Name:        entry.Name,
			DisplayName: entry.DisplayName,
			State:       entry.State,
			StartMode:   entry.StartMode,
			PID:         entry.ProcessID,
			BinaryPath:  entry.PathName,
			ConfigFile:  extractConfigFromCmdLine(entry.PathName),
		})
	}

	return services, nil
}

// ServiceForProcess returns the service that owns a process, if any.
func ServiceForProcess(pid int) (ServiceInfo, bool) {
	if pid <= 0 {
		return ServiceInfo{}, false
	}

	services, err := FindServerServices()
	if err != nil {
		AppLogger.Debug("Could not match process %d to a service: %v", pid, err)
		return ServiceInfo{}, false
	}

	for _, service := range services {
		if service.PID == pid {
			return service, true
		}
	}
	return ServiceInfo{}, false
}

// ServiceForDataDir returns a service that serves a data directory.
//
// This is what reveals the overlap on this machine: the service's own my.ini
// and one of DBSwitcher's configurations point at the same directory, so they
// are two ways to run one database and cannot both hold the port.
func ServiceForDataDir(dataDir string) (ServiceInfo, bool) {
	if dataDir == "" {
		return ServiceInfo{}, false
	}

	services, err := FindServerServices()
	if err != nil {
		return ServiceInfo{}, false
	}

	for _, service := range services {
		if service.ConfigFile == "" || !PathExists(service.ConfigFile) {
			continue
		}
		if sameDirectory(ParseConfigFile(service.ConfigFile).DataDir, dataDir) {
			return service, true
		}
	}
	return ServiceInfo{}, false
}

// StopWindowsService stops a service through the service control manager.
//
// A service-managed server has to be stopped this way. Shutting it down with
// mariadb-admin works, but leaves the service manager believing its service
// terminated unexpectedly.
func StopWindowsService(name string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("not a Windows host")
	}
	if name == "" {
		return fmt.Errorf("no service name")
	}

	AppLogger.Log("Stopping Windows service %s through the service control manager...", name)

	command := fmt.Sprintf("Stop-Service -Name '%s' -Force -WarningAction SilentlyContinue -ErrorAction Stop",
		escapeSingleQuotes(name))
	if _, err := runPowerShell(command); err != nil {
		return fmt.Errorf("could not stop service %s: %v", name, err)
	}

	RefreshServerServices()
	return nil
}

// escapeSingleQuotes makes a value safe inside a PowerShell single-quoted
// string.
func escapeSingleQuotes(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// IsDisabled reports whether the service cannot be started at all.
func (s ServiceInfo) IsDisabled() bool { return strings.EqualFold(s.StartMode, "Disabled") }

// StartWindowsService starts a service through the service control manager.
func StartWindowsService(name string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("not a Windows host")
	}
	if name == "" {
		return fmt.Errorf("no service name")
	}

	AppLogger.Log("Starting Windows service %s through the service control manager...", name)

	command := fmt.Sprintf("Start-Service -Name '%s' -WarningAction SilentlyContinue -ErrorAction Stop",
		escapeSingleQuotes(name))
	if _, err := runPowerShell(command); err != nil {
		return fmt.Errorf("could not start service %s: %v", name, err)
	}

	RefreshServerServices()
	return nil
}

// ServiceToStartFor returns the service that would be used to start a
// configuration, so callers can say so before starting it.
func ServiceToStartFor(config MariaDBConfig, useService bool) (ServiceInfo, bool) {
	if !useService {
		return ServiceInfo{}, false
	}

	services, err := FindServerServices()
	if err != nil {
		AppLogger.Debug("Could not look for a service for '%s': %v", config.Name, err)
		return ServiceInfo{}, false
	}

	return serviceToStart(services, config)
}

// serviceToStart picks the service that serves a configuration's data
// directory and can actually be started.
func serviceToStart(services []ServiceInfo, config MariaDBConfig) (ServiceInfo, bool) {
	if config.DataDir == "" {
		return ServiceInfo{}, false
	}

	for _, service := range services {
		if service.IsDisabled() {
			continue
		}
		if service.ConfigFile == "" || !PathExists(service.ConfigFile) {
			continue
		}
		if sameDirectory(ParseConfigFile(service.ConfigFile).DataDir, config.DataDir) {
			return service, true
		}
	}

	return ServiceInfo{}, false
}

// StartMariaDBForConfig starts the server for a configuration.
//
// When a Windows service serves the same data directory, that service is
// started rather than a second server of our own. The two are one database
// reached two ways: they cannot both hold the port, and starting ours leaves
// the service manager believing its service is stopped while the data
// directory is in use.
//
// useService false forces a standalone server, for a configuration whose
// settings differ from the service's own.
func StartMariaDBForConfig(config MariaDBConfig, useService bool) error {
	service, viaService := ServiceToStartFor(config, useService)
	if !viaService {
		return StartMariaDBWithConfig(config.Path)
	}

	return startThroughService(service, config)
}

// startThroughService starts a service and waits until its server answers.
func startThroughService(service ServiceInfo, config MariaDBConfig) error {
	AppLogger.Log("========================================")
	AppLogger.Log("STARTING MARIADB SERVICE %s", service.Name)
	AppLogger.Log("========================================")

	// The service runs with its own options file, so its port is what matters.
	port := config.Port
	if service.ConfigFile != "" && PathExists(service.ConfigFile) {
		if servicePort := ParseConfigFile(service.ConfigFile).Port; servicePort != "" {
			port = servicePort
		}
	}

	// The same pre-flight a standalone start gets: nothing may be serving, and
	// the port has to be free.
	serving, isServing, err := FindServingServer()
	if err != nil {
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}
	if isServing {
		return fmt.Errorf("MariaDB is already running on port %s (PID %d) - please stop it first",
			serving.Port, serving.PID)
	}
	EndStaleServers()
	if !IsPortAvailable(port) {
		AppLogger.Log("Port %s is still in use", port)
		FindProcessUsingPort(port)
		return fmt.Errorf("cannot start - port %s is occupied by another process", port)
	}

	if err := StartWindowsService(service.Name); err != nil {
		return err
	}

	if err := WaitForMariaDBServing(0, port, StartupTimeout()); err != nil {
		return fmt.Errorf("service %s was started but %v", service.Name, err)
	}

	AppConfig.LastUsedConfig = config.Path
	SaveConfig()
	CurrentStatus = GetMariaDBStatus()

	AppLogger.Info("========================================")
	AppLogger.Info("MARIADB SERVICE STARTED SUCCESSFULLY")
	AppLogger.Info("========================================")

	NotifyMariaDBStarted(config.Name)
	return nil
}

// serviceControlRights are the rights service setup grants the user on a
// service: query its configuration and status, enumerate its dependents,
// start, stop, interrogate, send it a control, and read its permissions.
// Stop-Service needs the dependents as well as the stop right.
const serviceControlRights = "CCLCSWRPWPLOCRRC"

// ServiceSetup is what service setup changes on one service so that DBSwitcher
// can switch away from and back to it without administrator rights.
type ServiceSetup struct {
	Service ServiceInfo

	// SetManual is set when the service starts automatically, which claims
	// its port at every boot whichever configuration was in use.
	SetManual bool

	// GrantAccess is set when the user may not start and stop the service.
	// Without that right a stop fell back to a mariadb-admin shutdown, which
	// Windows records as the service terminating unexpectedly.
	GrantAccess bool

	security string // the service's permissions with the user's access added
}

// Needed reports whether anything has to change.
func (s ServiceSetup) Needed() bool { return s.SetManual || s.GrantAccess }

// ServicesForConfigs returns the services that serve the data directory of one
// of DBSwitcher's configurations and can be started.
func ServicesForConfigs() ([]ServiceInfo, error) {
	services, err := FindServerServices()
	if err != nil {
		return nil, err
	}

	var matched []ServiceInfo
	for _, service := range services {
		if service.IsDisabled() || service.ConfigFile == "" || !PathExists(service.ConfigFile) {
			continue
		}
		dataDir := ParseConfigFile(service.ConfigFile).DataDir
		for _, config := range AvailableConfigs {
			if config.DataDir != "" && sameDirectory(config.DataDir, dataDir) {
				matched = append(matched, service)
				break
			}
		}
	}
	return matched, nil
}

// PlanServiceSetup works out what service setup has to change on each service.
// It needs no administrator rights.
func PlanServiceSetup(services []ServiceInfo) ([]ServiceSetup, error) {
	sid, err := CurrentUserSID()
	if err != nil {
		return nil, err
	}

	plans := make([]ServiceSetup, 0, len(services))
	for _, service := range services {
		security, err := serviceSecurity(service.Name)
		if err != nil {
			return nil, err
		}

		updated, grant, err := grantServiceControl(security, sid)
		if err != nil {
			return nil, fmt.Errorf("cannot read the permissions of service %s: %v", service.Name, err)
		}

		plans = append(plans, ServiceSetup{
			Service:     service,
			SetManual:   service.StartsAutomatically(),
			GrantAccess: grant,
			security:    updated,
		})
	}
	return plans, nil
}

// ApplyServiceSetup makes the planned changes behind a single UAC prompt.
func ApplyServiceSetup(plans []ServiceSetup) error {
	var script strings.Builder
	script.WriteString("function Invoke-Sc { & sc.exe @args | Out-Null; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE } }\n")

	needed := false
	for _, plan := range plans {
		name := escapeSingleQuotes(plan.Service.Name)
		if plan.SetManual {
			fmt.Fprintf(&script, "Invoke-Sc config '%s' start= demand\n", name)
			needed = true
		}
		if plan.GrantAccess {
			fmt.Fprintf(&script, "Invoke-Sc sdset '%s' '%s'\n", name, escapeSingleQuotes(plan.security))
			needed = true
		}
	}
	if !needed {
		return nil
	}
	script.WriteString("exit 0\n")

	for _, plan := range plans {
		if plan.Needed() {
			AppLogger.Log("Setting up Windows service %s (manual start: %t, start/stop access: %t)",
				plan.Service.Name, plan.SetManual, plan.GrantAccess)
		}
	}

	if err := RunElevatedPowerShell(script.String()); err != nil {
		return fmt.Errorf("service setup failed: %v", err)
	}

	RefreshServerServices()
	return nil
}

// ServiceNeedsSetup reports whether service setup would change anything on a
// service. A failed check counts as not needed, since it only adds a hint.
func ServiceNeedsSetup(service ServiceInfo) bool {
	plans, err := PlanServiceSetup([]ServiceInfo{service})
	if err != nil || len(plans) == 0 {
		return false
	}
	return plans[0].Needed()
}

// CurrentUserSID returns the security identifier of the user running
// DBSwitcher. It is taken before elevating: the administrator who approves the
// prompt may be a different account.
func CurrentUserSID() (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("not a Windows host")
	}

	output, err := runPowerShell("[System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value")
	if err != nil {
		return "", fmt.Errorf("cannot determine the current user: %v", err)
	}

	sid := strings.TrimSpace(output)
	if !strings.HasPrefix(sid, "S-") {
		return "", fmt.Errorf("unexpected user identifier %q", sid)
	}
	return sid, nil
}

// serviceSecurity returns a service's permissions in SDDL form.
func serviceSecurity(name string) (string, error) {
	output, err := exec.Command("sc.exe", "sdshow", name).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return "", fmt.Errorf("cannot read the permissions of service %s: %s", name, text)
	}
	return text, nil
}

// grantServiceControl returns sddl with an entry allowing sid to start and stop
// the service, and whether one had to be added.
func grantServiceControl(sddl, sid string) (string, bool, error) {
	daclStart, daclEnd, err := daclBounds(sddl)
	if err != nil {
		return "", false, err
	}

	for _, ace := range sddlEntries(sddl[daclStart:daclEnd]) {
		fields := strings.Split(ace, ";")
		if len(fields) != 6 || fields[0] != "A" || !strings.EqualFold(fields[5], sid) {
			continue
		}
		if hasRight(fields[2], "RP") && hasRight(fields[2], "WP") {
			return sddl, false, nil
		}
	}

	entry := fmt.Sprintf("(A;;%s;;;%s)", serviceControlRights, sid)
	return sddl[:daclEnd] + entry + sddl[daclEnd:], true, nil
}

// daclBounds locates the access list - the "D:" part - of an SDDL string. The
// end is where the audit list ("S:") begins, or the end of the string.
func daclBounds(sddl string) (int, int, error) {
	start := -1
	depth := 0
	for i := 0; i+1 < len(sddl); i++ {
		switch sddl[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth != 0 || sddl[i+1] != ':' {
			continue
		}
		if sddl[i] == 'D' && start == -1 {
			start = i + 2
		} else if sddl[i] == 'S' && start != -1 {
			return start, i, nil
		}
	}

	if start == -1 {
		return 0, 0, fmt.Errorf("no access list in %q", sddl)
	}
	return start, len(sddl), nil
}

// sddlEntries returns the parenthesised entries of an access list, without the
// parentheses.
func sddlEntries(dacl string) []string {
	var entries []string
	for {
		open := strings.IndexByte(dacl, '(')
		if open == -1 {
			return entries
		}
		end := strings.IndexByte(dacl[open:], ')')
		if end == -1 {
			return entries
		}
		entries = append(entries, dacl[open+1:open+end])
		dacl = dacl[open+end+1:]
	}
}

// hasRight reports whether an SDDL rights string, made of two-letter codes,
// includes right.
func hasRight(rights, right string) bool {
	for i := 0; i+2 <= len(rights); i += 2 {
		if rights[i:i+2] == right {
			return true
		}
	}
	return false
}
