package core

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Global configuration instance
var (
	AppConfig        Config
	AvailableConfigs []MariaDBConfig
	CurrentStatus    MariaDBStatus
	AppLogger        *Logger
)

// Initialize the core package
func Init() {
	AppLogger = NewLogger()
	LoadConfig()
	ScanForConfigs()
}

// LoadConfig loads the application configuration
func LoadConfig() {
	// Default configuration
	AppConfig = Config{
		ProcessNames: map[string]string{
			"windows": "mysqld.exe",
			"linux":   "mysqld",
			"darwin":  "mysqld",
			"freebsd": "mysqld",
		},
		ServiceNames: map[string]string{
			"windows": "MariaDB",
			"linux":   "mariadb",
			"darwin":  "mariadb",
			"freebsd": "mysql-server",
		},
		UseServiceControl: false,
		RequireElevation:  false,

		// Default UI/Application Settings
		AutoRefreshEnabled:   true,
		RefreshIntervalSecs:  5,
		NotificationsEnabled: true,
		StartMinimized:       false,
		AutoStartWithSystem:  false,
		LogLevel:             "INFO",

		// Default Advanced Settings
		ProcessTimeoutSecs:    30,
		MaxRetryAttempts:      3,
		ConnectionTimeoutSecs: 5,
		DebugMode:             false,
		VerboseLogging:        false,
		BackgroundProcessing:  true,
	}

	// Set user config directory
	AppConfig.ConfigPath = GetUserConfigDir()

	// Try to load existing config
	configFile := GetConfigPath()
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &AppConfig); err != nil {
			AppLogger.Error("Error parsing config: %v", err)
		}
	} else {
		// Auto-detect and create config
		AutoDetectConfig()
		AppConfig.AutoDetected = true
		SaveConfig()
	}

	// Ensure config directory exists and is set correctly
	if AppConfig.ConfigPath == "" || !PathExists(AppConfig.ConfigPath) {
		AppConfig.ConfigPath = GetUserConfigDir()
		SaveConfig()
	}
}

// GetConfigPath returns the path to the settings file
func GetConfigPath() string {
	return filepath.Join(GetAppDataDir(), "settings.json")
}

// SaveConfig saves the current configuration
func SaveConfig() error {
	data, err := json.MarshalIndent(AppConfig, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(GetConfigPath(), data, 0644)
}

// AutoDetectConfig auto-detects the MariaDB configuration
func AutoDetectConfig() {
	AppLogger.Log("Auto-detecting configuration for %s", runtime.GOOS)

	// Detect MariaDB installation
	AppConfig.MariaDBBin = DetectMariaDBBin()

	// Check if we need elevation
	AppConfig.RequireElevation = CheckElevationRequired()
	AppConfig.UseServiceControl = CheckServiceControlAvailable()

	AppLogger.Log("Auto-detection complete: bin=%s", AppConfig.MariaDBBin)
}

// ScanForConfigs scans for all available MariaDB configuration files
func ScanForConfigs() {
	AvailableConfigs = []MariaDBConfig{}

	// Ensure config directory exists
	EnsureConfigDirectory()
	configDir := AppConfig.ConfigPath

	// Find all .ini and .cnf files
	patterns := []string{"*.ini", "*.cnf"}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(configDir, pattern))
		for _, match := range matches {
			configName := strings.TrimSuffix(filepath.Base(match), filepath.Ext(match))

			// Parse the config file to get details
			parsedConfig := ParseConfigFile(match)
			parsedConfig.Name = configName
			parsedConfig.Path = match
			parsedConfig.Exists = true

			// Check if this is the active config
			if CurrentStatus.IsRunning && CurrentStatus.ConfigFile == match {
				parsedConfig.IsActive = true
			}

			AvailableConfigs = append(AvailableConfigs, parsedConfig)
		}
	}

	// Sort configs by name
	sort.Slice(AvailableConfigs, func(i, j int) bool {
		return AvailableConfigs[i].Name < AvailableConfigs[j].Name
	})

	AppLogger.Log("Found %d configuration files in %s", len(AvailableConfigs), configDir)
}

// ParseConfigFile parses a MariaDB config file to extract key information
func ParseConfigFile(configPath string) MariaDBConfig {
	config := MariaDBConfig{
		Path:   configPath,
		Exists: PathExists(configPath),
	}

	file, err := os.Open(configPath)
	if err != nil {
		return config
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inMysqldSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Check for [mysqld] section
		if strings.HasPrefix(line, "[") {
			inMysqldSection = strings.ToLower(line) == "[mysqld]"
			continue
		}

		if inMysqldSection {
			// Parse key=value pairs
			if strings.Contains(line, "=") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(strings.ToLower(parts[0]))
					value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")

					switch key {
					case "datadir", "data_dir":
						config.DataDir = value
					case "port":
						config.Port = value
					case "description", "comment":
						config.Description = value
					}
				}
			}
		}
	}

	// Set default port if not specified
	if config.Port == "" {
		config.Port = "3306"
	}

	return config
}

// EnsureConfigDirectory ensures the config directory exists and creates README
func EnsureConfigDirectory() {
	configDir := AppConfig.ConfigPath
	os.MkdirAll(configDir, 0755)

	// Create a README file with instructions
	readmePath := filepath.Join(configDir, "README.txt")
	if !PathExists(readmePath) {
		readme := `MariaDB Configuration Files
============================

This directory is for your MariaDB configuration files.
The DBSwitcher will automatically detect any .ini or .cnf files you place here.

File Format:
- Use .ini or .cnf extension
- Files must contain a [mysqld] section
- Key settings: datadir, port, description (optional)

To add a new configuration:
1. Create a new .ini or .cnf file in this directory
2. Add your MariaDB settings (copy from your existing MariaDB installation)
3. Modify the datadir and port as needed
4. The DBSwitcher will automatically detect it

Tips:
- Use descriptive filenames (e.g., production.ini, testing.ini)
- Add a 'description' field in [mysqld] section for clarity in the UI
- Different configs can use different ports to run simultaneously
- Use forward slashes (/) in paths for better compatibility
- Backup your configurations regularly

Configuration Directory Location:
` + configDir

		os.WriteFile(readmePath, []byte(readme), 0644)
		AppLogger.Log("Created README file for config directory: %s", readmePath)
	}
}

// FindConfigByPath finds a configuration by its file path
func FindConfigByPath(path string) *MariaDBConfig {
	normalizedPath := filepath.Clean(path)
	for _, config := range AvailableConfigs {
		if filepath.Clean(config.Path) == normalizedPath {
			return &config
		}
	}
	return nil
}

// findConfigByDataDir matches a running server's own configuration file to a
// known configuration by the data directory it serves.
//
// Identifying a server only by the path of its --defaults-file misses any
// server DBSwitcher did not start - the Windows service runs from its own
// my.ini. The data directory is what actually says which database is being
// served, and therefore which credentials apply to it.
func findConfigByDataDir(configFile string) *MariaDBConfig {
	if configFile == "" || !PathExists(configFile) {
		return nil
	}

	running := ParseConfigFile(configFile)
	if running.DataDir == "" {
		return nil
	}

	for _, cfg := range AvailableConfigs {
		if cfg.DataDir != "" && sameDirectory(cfg.DataDir, running.DataDir) {
			match := cfg
			return &match
		}
	}

	return nil
}

// sameDirectory compares two directory paths, tolerating separator and case
// differences on Windows.
func sameDirectory(a, b string) bool {
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)

	if runtime.GOOS == "windows" {
		return strings.EqualFold(cleanA, cleanB)
	}
	return cleanA == cleanB
}
