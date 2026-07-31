package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"mariadb-monitor/core"
	"golang.org/x/term"
)

// CLI represents the command-line interface
type CLI struct{}

// NewCLI creates a new CLI instance
func NewCLI() *CLI {
	return &CLI{}
}

// List displays all available configurations
func (c *CLI) List() error {
	fmt.Println("Available MariaDB Configurations:")
	fmt.Println("=================================")
	
	if len(core.AvailableConfigs) == 0 {
		fmt.Println("No configurations found.")
		fmt.Printf("Configuration directory: %s\n", core.AppConfig.ConfigPath)
		fmt.Println("Add .ini or .cnf files to this directory to create configurations.")
		return nil
	}
	
	// Get current status to mark active config
	status := core.GetMariaDBStatus()
	
	for i, config := range core.AvailableConfigs {
		fmt.Printf("%d. %s", i+1, config.Name)
		
		if config.Description != "" {
			fmt.Printf(" (%s)", config.Description)
		}
		
		fmt.Printf("\n   Port: %s", config.Port)
		
		if config.DataDir != "" {
			fmt.Printf("\n   Data: %s", config.DataDir)
		}
		
		fmt.Printf("\n   File: %s", config.Path)
		
		// Mark active configuration (normalize paths for comparison)
		if status.IsRunning && filepath.Clean(config.Path) == filepath.Clean(status.ConfigFile) {
			fmt.Printf("\n   Status: ✓ ACTIVE (PID: %d)", status.ProcessID)
		} else {
			fmt.Printf("\n   Status: Available")
		}
		
		fmt.Println()
	}
	
	return nil
}

// Status shows the current MariaDB status
func (c *CLI) Status() error {
	fmt.Println("MariaDB Status:")
	fmt.Println("===============")
	
	status := core.GetMariaDBStatus()
	
	if status.IsRunning {
		fmt.Printf("Status: ✓ RUNNING\n")
		fmt.Printf("Process ID: %d\n", status.ProcessID)
		fmt.Printf("Configuration: %s\n", status.ConfigName)
		fmt.Printf("Port: %s\n", status.Port)
		if status.DataPath != "" {
			fmt.Printf("Data Directory: %s\n", status.DataPath)
		}
		if status.Version != "" {
			fmt.Printf("Version: %s\n", status.Version)
		}
	} else {
		fmt.Printf("Status: ✗ STOPPED\n")
	}
	
	return nil
}

// Switch switches to a different configuration
func (c *CLI) Switch(configName string) error {
	fmt.Printf("Switching to configuration: %s\n", configName)
	
	// Find the configuration
	var targetConfig *core.MariaDBConfig
	for _, config := range core.AvailableConfigs {
		if strings.EqualFold(config.Name, configName) {
			targetConfig = &config
			break
		}
	}
	
	if targetConfig == nil {
		return fmt.Errorf("configuration '%s' not found", configName)
	}
	
	// Check if MariaDB is currently running
	running, err := core.IsMariaDBRunningE()
	if err != nil {
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}

	if running {
		status := core.GetMariaDBStatus()
		if status.ConfigName != "" {
			fmt.Printf("MariaDB is currently running with '%s'. Stopping it first...\n", status.ConfigName)
		} else {
			fmt.Println("MariaDB is currently running. Stopping it first...")
		}

		if err := c.stopRunningInstance(); err != nil {
			return fmt.Errorf("failed to stop current MariaDB instance: %v", err)
		}

		// Wait for the port the new instance needs, not just for the process:
		// the old server releases its port a moment after it exits.
		fmt.Println("Waiting for shutdown to complete...")
		core.AppLogger.Log("Waiting for complete shutdown before switching...")
		if err := core.WaitForMariaDBStopped(targetConfig.Port, core.ShutdownTimeout()); err != nil {
			return err
		}
	}

	// Start with new configuration
	fmt.Printf("Starting MariaDB with %s configuration...\n", targetConfig.Name)

	if err := core.StartMariaDBWithConfig(targetConfig.Path); err != nil {
		return fmt.Errorf("failed to start MariaDB: %v", err)
	}
	
	fmt.Printf("✓ Successfully switched to %s configuration\n", targetConfig.Name)
	fmt.Printf("  Port: %s\n", targetConfig.Port)
	if targetConfig.DataDir != "" {
		fmt.Printf("  Data Directory: %s\n", targetConfig.DataDir)
	}
	
	return nil
}

// Start starts MariaDB with a specific configuration
func (c *CLI) Start(configName string) error {
	if configName == "" {
		return fmt.Errorf("configuration name is required")
	}
	
	// Find the configuration
	var targetConfig *core.MariaDBConfig
	for _, config := range core.AvailableConfigs {
		if strings.EqualFold(config.Name, configName) {
			targetConfig = &config
			break
		}
	}
	
	if targetConfig == nil {
		return fmt.Errorf("configuration '%s' not found", configName)
	}
	
	// Check if already running
	running, err := core.IsMariaDBRunningE()
	if err != nil {
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}
	if running {
		status := core.GetMariaDBStatus()
		if status.ConfigName == "" {
			return fmt.Errorf("MariaDB is already running (PID %d)", status.ProcessID)
		}
		return fmt.Errorf("MariaDB is already running with configuration '%s'", status.ConfigName)
	}

	fmt.Printf("Starting MariaDB with %s configuration...\n", targetConfig.Name)

	if err := core.StartMariaDBWithConfig(targetConfig.Path); err != nil {
		return fmt.Errorf("failed to start MariaDB: %v", err)
	}
	
	fmt.Printf("✓ MariaDB started successfully\n")
	fmt.Printf("  Configuration: %s\n", targetConfig.Name)
	fmt.Printf("  Port: %s\n", targetConfig.Port)
	
	return nil
}

// maxCredentialAttempts limits how often the user is re-prompted after the
// server rejects the credentials.
const maxCredentialAttempts = 3

// Stop stops the running MariaDB instance
func (c *CLI) Stop() error {
	running, err := core.IsMariaDBRunningE()
	if err != nil {
		return fmt.Errorf("cannot determine whether MariaDB is running: %v", err)
	}

	if !running {
		fmt.Println("MariaDB is not currently running.")
		return nil
	}

	return c.stopRunningInstance()
}

// stopRunningInstance shuts the server down, re-prompting when the stored
// credentials are rejected instead of failing the whole command. A stale
// keyring entry used to abort every switch with "shutdown failed: exit status 1".
func (c *CLI) stopRunningInstance() error {
	fmt.Println("Stopping MariaDB...")

	// Target the instance that is actually running rather than whatever port
	// happens to be stored with the credentials.
	status := core.GetMariaDBStatus()
	allowSaved := core.SavedCredentials != nil

	for attempt := 1; attempt <= maxCredentialAttempts; attempt++ {
		creds, usedSaved, err := c.promptForCredentials(allowSaved, status.Port)
		if err != nil {
			return fmt.Errorf("failed to get credentials: %v", err)
		}

		err = core.StopMySQLWithCredentials(creds)
		if err == nil {
			fmt.Println("✓ MariaDB stopped successfully")
			if !usedSaved {
				// Only ever store credentials that have just worked.
				c.offerToSaveCredentials(creds)
			}
			return nil
		}

		if !core.IsCredentialError(err) {
			return fmt.Errorf("failed to stop MariaDB gracefully: %v", err)
		}

		fmt.Printf("\nMariaDB rejected those credentials:\n  %s\n", core.ClientOutput(err))
		if usedSaved {
			fmt.Println("The credentials saved in your keyring are no longer valid.")
		}
		if attempt < maxCredentialAttempts {
			fmt.Println("Please enter the current MySQL admin credentials.")
		}

		// Never retry with the entry that was just rejected.
		allowSaved = false
	}

	return fmt.Errorf("failed to stop MariaDB: credentials rejected %d times", maxCredentialAttempts)
}

// promptForCredentials prompts the user for MySQL credentials. It reports
// whether the saved credentials were reused, so only freshly entered ones are
// offered for saving. detectedPort, when known, is the port of the running
// server and is preferred over the stored one.
func (c *CLI) promptForCredentials(allowSaved bool, detectedPort string) (core.MySQLCredentials, bool, error) {
	reader := bufio.NewReader(os.Stdin)

	// Try to use saved credentials first
	if allowSaved && core.SavedCredentials != nil {
		fmt.Printf("Use saved credentials (user: %s, host: %s)? [Y/n]: ",
			core.SavedCredentials.Username, core.SavedCredentials.Host)

		response, _ := reader.ReadString('\n')
		response = strings.ToLower(strings.TrimSpace(response))

		if response == "" || response == "y" || response == "yes" {
			creds := *core.SavedCredentials
			if detectedPort != "" && detectedPort != creds.Port {
				fmt.Printf("Using detected port %s (saved credentials say %s).\n", detectedPort, creds.Port)
				creds.Port = detectedPort
			}
			return creds, true, nil
		}
	}

	// Prompt for new credentials
	creds := core.MySQLCredentials{}

	defaultUsername := "root"
	defaultHost := "localhost"
	defaultPort := "3306"
	if core.SavedCredentials != nil {
		if core.SavedCredentials.Username != "" {
			defaultUsername = core.SavedCredentials.Username
		}
		if core.SavedCredentials.Host != "" {
			defaultHost = core.SavedCredentials.Host
		}
	}
	if detectedPort != "" {
		defaultPort = detectedPort
	}

	fmt.Printf("MySQL Username [%s]: ", defaultUsername)
	username, _ := reader.ReadString('\n')
	username = strings.TrimSpace(username)
	if username == "" {
		username = defaultUsername
	}
	creds.Username = username

	fmt.Printf("MySQL Host [%s]: ", defaultHost)
	host, _ := reader.ReadString('\n')
	host = strings.TrimSpace(host)
	if host == "" {
		host = defaultHost
	}
	creds.Host = host

	fmt.Printf("MySQL Port [%s]: ", defaultPort)
	port, _ := reader.ReadString('\n')
	port = strings.TrimSpace(port)
	if port == "" {
		port = defaultPort
	}
	creds.Port = port

	fmt.Print("MySQL Password (leave empty if none): ")

	// Hide password input
	passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return creds, false, fmt.Errorf("failed to read password: %v", err)
	}
	fmt.Println() // New line after password input

	creds.Password = string(passwordBytes)

	return creds, false, nil
}

// offerToSaveCredentials stores credentials once they are known to work. The
// old flow saved them before the first connection attempt, which is how a
// wrong password ended up in the keyring and broke every later switch.
func (c *CLI) offerToSaveCredentials(creds core.MySQLCredentials) {
	reader := bufio.NewReader(os.Stdin)

	prompt := "Save these credentials for future use? [Y/n]: "
	if core.SavedCredentials != nil {
		prompt = "Update the saved credentials with these? [Y/n]: "
	}
	fmt.Print(prompt)

	response, _ := reader.ReadString('\n')
	response = strings.ToLower(strings.TrimSpace(response))
	if response != "" && response != "y" && response != "yes" {
		return
	}

	if err := core.SaveCredentialsToKeyring(creds); err != nil {
		fmt.Printf("Warning: Failed to save credentials: %v\n", err)
		return
	}

	saved := creds
	core.SavedCredentials = &saved
	fmt.Println("Credentials saved securely.")
}

// ShowHelp displays CLI help information
func (c *CLI) ShowHelp() {
	fmt.Println(`DBSwitcher CLI - MariaDB Configuration Manager

USAGE:
    dbswitcher <command> [arguments]

COMMANDS:
    list                    List all available configurations
    status                  Show current MariaDB status
    start <config>          Start MariaDB with specified configuration
    switch <config>         Switch to a different configuration (stops current, starts new)
    stop                    Stop the running MariaDB instance
    gui                     Launch the GUI interface
    tray                    Run in system tray mode
    help                    Show this help message

EXAMPLES:
    dbswitcher list                    # List all configurations
    dbswitcher status                  # Show current status
    dbswitcher start production        # Start with production config
    dbswitcher switch development      # Switch to development config
    dbswitcher stop                    # Stop MariaDB
    dbswitcher gui                     # Launch GUI

CONFIGURATION:
    Configuration files (.ini or .cnf) should be placed in:
    Windows: %APPDATA%\DBSwitcher\configs
    Linux/macOS: ~/.config/DBSwitcher

    Each configuration file should contain a [mysqld] section with
    settings like datadir, port, and an optional description.`)
}