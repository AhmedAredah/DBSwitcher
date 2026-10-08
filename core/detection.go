package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DetectMariaDBBin detects the MariaDB binary directory
func DetectMariaDBBin() string {
	// Use 'which' or 'where' command first
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("where", "mysqld.exe")
	} else {
		cmd = exec.Command("which", "mysqld")
	}

	if output, err := cmd.Output(); err == nil {
		path := strings.TrimSpace(string(output))
		if path != "" {
			return filepath.Dir(path)
		}
	}

	// Fall back to common paths
	switch runtime.GOOS {
	case "windows":
		return detectWindowsMariaDB()
	case "linux":
		return detectLinuxMariaDB()
	case "darwin":
		return detectMacMariaDB()
	case "freebsd":
		return detectFreeBSDMariaDB()
	default:
		return "/usr/local/bin"
	}
}

func detectWindowsMariaDB() string {
	// Check PATH first
	pathEnv := os.Getenv("PATH")
	paths := strings.Split(pathEnv, ";")
	for _, path := range paths {
		if strings.Contains(strings.ToLower(path), "mariadb") || strings.Contains(strings.ToLower(path), "mysql") {
			if _, err := os.Stat(filepath.Join(path, "mysqld.exe")); err == nil {
				return path
			}
		}
	}

	// Common installation paths
	commonPaths := []string{
		`C:\Program Files\MariaDB*\bin`,
		`C:\Program Files (x86)\MariaDB*\bin`,
		`C:\MariaDB*\bin`,
		`C:\xampp\mysql\bin`,
		`C:\wamp64\bin\mariadb\mariadb*\bin`,
		`C:\wamp\bin\mysql\mysql*\bin`,
	}

	for _, pattern := range commonPaths {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if _, err := os.Stat(filepath.Join(match, "mysqld.exe")); err == nil {
				return match
			}
		}
	}

	return ""
}

func detectLinuxMariaDB() string {
	paths := []string{
		"/usr/sbin",
		"/usr/bin",
		"/usr/local/bin",
		"/usr/local/mysql/bin",
		"/opt/mariadb/bin",
		"/opt/mysql/bin",
	}

	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(path, "mysqld")); err == nil {
			return path
		}
	}
	return "/usr/bin"
}

func detectMacMariaDB() string {
	paths := []string{
		"/usr/local/mysql/bin",
		"/opt/homebrew/bin",
		"/opt/homebrew/opt/mariadb/bin",
		"/usr/local/bin",
		"/opt/local/bin",
		"/Applications/MAMP/Library/bin",
	}

	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(path, "mysqld")); err == nil {
			return path
		}
	}
	return "/usr/local/bin"
}

func detectFreeBSDMariaDB() string {
	paths := []string{
		"/usr/local/libexec",
		"/usr/local/bin",
		"/usr/local/mysql/bin",
	}

	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(path, "mysqld")); err == nil {
			return path
		}
	}
	return "/usr/local/bin"
}

// IsDriveRemovable checks if a Windows drive is removable
func IsDriveRemovable(path string) bool {
	if runtime.GOOS != "windows" {
		return false
	}

	if len(path) < 2 || path[1] != ':' {
		return false
	}

	// Use PowerShell to get drive type
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-WmiObject Win32_LogicalDisk -Filter \"Name='%s'\").DriveType", path[:2]))
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	driveType := strings.TrimSpace(string(output))
	if driveType == "2" { // Removable Disk
		return true
	}

	return false
}

// DetectExternalDrive detects external drives with MariaDB data
func DetectExternalDrive() string {
	switch runtime.GOOS {
	case "windows":
		// Check for removable drives
		for _, drive := range "DEFGHIJKLMNOPQRSTUVWXYZ" {
			drivePath := string(drive) + ":\\"
			if IsDriveRemovable(drivePath[:2]) {
				mariadbPath := filepath.Join(drivePath, "MariaDB", "data")
				if PathExists(mariadbPath) {
					return drivePath[:2]
				}
			}
		}
	case "darwin":
		// Check /Volumes for external drives
		volumes, _ := os.ReadDir("/Volumes")
		for _, vol := range volumes {
			volPath := filepath.Join("/Volumes", vol.Name())
			mariadbPath := filepath.Join(volPath, "MariaDB", "data")
			if PathExists(mariadbPath) {
				return volPath
			}
		}
	case "linux", "freebsd":
		// Check common mount points
		mountPoints := []string{"/mnt", "/media", "/run/media"}
		for _, mp := range mountPoints {
			if dirs, err := os.ReadDir(mp); err == nil {
				for _, dir := range dirs {
					dirPath := filepath.Join(mp, dir.Name())
					mariadbPath := filepath.Join(dirPath, "MariaDB", "data")
					if PathExists(mariadbPath) {
						return dirPath
					}
				}
			}
		}
	}
	return ""
}

// CheckElevationRequired checks if elevation is required
func CheckElevationRequired() bool {
	switch runtime.GOOS {
	case "windows":
		// Check if we can access service control
		cmd := exec.Command("sc", "query")
		return cmd.Run() != nil
	case "linux", "darwin", "freebsd":
		// Check if we're root
		return os.Geteuid() != 0
	}
	return false
}

// CheckServiceControlAvailable checks if service control is available
func CheckServiceControlAvailable() bool {
	switch runtime.GOOS {
	case "windows":
		// Find the service by the binary it runs; its name follows the version
		// ("MariaDB11_4"), so the "MariaDB" guessed here matched nothing and
		// service control was always reported unavailable.
		services, err := FindServerServices()
		if err != nil || len(services) == 0 {
			return false
		}

		if AppConfig.ServiceNames == nil {
			AppConfig.ServiceNames = map[string]string{}
		}
		AppConfig.ServiceNames["windows"] = services[0].Name
		return true
	case "linux":
		cmd := exec.Command("systemctl", "status", AppConfig.ServiceNames["linux"])
		return cmd.Run() == nil
	case "darwin":
		cmd := exec.Command("launchctl", "list")
		output, _ := cmd.Output()
		return strings.Contains(string(output), "mariadb") || strings.Contains(string(output), "mysql")
	case "freebsd":
		cmd := exec.Command("service", AppConfig.ServiceNames["freebsd"], "status")
		return cmd.Run() == nil
	}
	return false
}
