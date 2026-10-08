package core

import (
	"encoding/json"
	"fmt"
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
