package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"
	"mariadb-monitor/core"
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

		// Mark the active configuration. Matching the name as well as the path
		// recognises a server started outside DBSwitcher: the Windows service
		// runs from its own my.ini but serves a known data directory.
		active := status.IsRunning &&
			(filepath.Clean(config.Path) == filepath.Clean(status.ConfigFile) ||
				(status.ConfigName != "" && strings.EqualFold(status.ConfigName, config.Name)))

		if active {
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

	if status.IsRunning && !status.Responding {
		// The process is there and the port is open, but the server will not
		// serve. Saying "RUNNING" here is how a broken database looked fine.
		fmt.Printf("Status: ⚠ RUNNING BUT NOT USABLE\n")
		if status.StatusMessage != "" {
			fmt.Printf("Problem: %s\n", status.StatusMessage)
		}
		fmt.Printf("Process ID: %d\n", status.ProcessID)
		fmt.Printf("Port: %s\n", status.Port)
		if status.ConfigName != "" {
			fmt.Printf("Configuration: %s\n", status.ConfigName)
		}
		printServiceLine(status)
	} else if status.IsRunning {
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
		printServiceLine(status)
	} else {
		fmt.Printf("Status: ✗ STOPPED\n")
	}

	// Report a server that stopped without exiting: it is invisible otherwise,
	// and it used to make every later switch time out.
	for _, pid := range status.StaleProcessIDs {
		fmt.Printf("\n⚠ Server process %d is present but accepting no connections.\n", pid)
		fmt.Println("  It stopped without exiting; ending it is safe once it holds no data directory.")
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

		// Wait for the server that was stopped, and for the port the new
		// instance needs, which is released a moment after it exits.
		fmt.Println("Waiting for shutdown to complete...")
		core.AppLogger.Log("Waiting for complete shutdown before switching...")
		if err := core.WaitForMariaDBStopped(status.ProcessID, targetConfig.Port, core.ShutdownTimeout()); err != nil {
			return err
		}
	}

	// Start with new configuration
	c.warnAboutServiceOverlap(targetConfig)
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

	c.warnAboutServiceOverlap(targetConfig)
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

	// A server run by a Windows service has to be stopped through the service
	// manager. A mariadb-admin shutdown does stop it, but the manager is left
	// believing its service terminated unexpectedly.
	if handled, err := c.stopServiceManagedServer(); handled {
		return err
	}

	// Target the instance that is actually running rather than whatever port
	// happens to be stored with the credentials.
	status := core.GetMariaDBStatus()
	configName := status.ConfigName

	// Each configuration has its own data directory and therefore its own
	// password for the same account.
	saved, ownEntry, err := core.LoadCredentialsForConfig(configName)
	if err != nil {
		fmt.Printf("Warning: could not read stored credentials: %v\n", err)
		saved = nil
	}

	for attempt := 1; attempt <= maxCredentialAttempts; attempt++ {
		creds, usedSaved, err := c.promptForCredentials(saved, configName, status.Port)
		if err != nil {
			return fmt.Errorf("failed to get credentials: %v", err)
		}

		err = core.StopMySQLWithCredentials(creds)
		if err == nil {
			fmt.Println("✓ MariaDB stopped successfully")

			if usedSaved {
				// The shared entry turned out to be right for this
				// configuration; file it under the configuration so the next
				// switch does not depend on the fallback.
				if core.RecordWorkingCredentials(configName, creds) {
					fmt.Printf("Recorded these credentials for the '%s' configuration.\n", configName)
				}
			} else {
				// Only ever store credentials that have just worked.
				c.offerToSaveCredentials(configName, creds)
			}
			return nil
		}

		if !core.IsCredentialError(err) {
			return fmt.Errorf("failed to stop MariaDB gracefully: %v", err)
		}

		fmt.Printf("\nMariaDB rejected those credentials:\n  %s\n", core.ClientOutput(err))
		if usedSaved {
			if ownEntry {
				fmt.Printf("The credentials stored for '%s' are no longer valid.\n", configName)
			} else {
				fmt.Printf("Those are the shared credentials; '%s' uses a different data directory and may have its own password.\n", configName)
			}
		}
		if attempt < maxCredentialAttempts {
			fmt.Println("Please enter the current MySQL admin credentials.")
		}

		// Never retry with the entry that was just rejected.
		saved = nil
	}

	return fmt.Errorf("failed to stop MariaDB: credentials rejected %d times", maxCredentialAttempts)
}

// stopServiceManagedServer stops the running server through the service
// control manager when a service owns it. handled reports whether the stop
// was carried out; false means fall back to a graceful shutdown with
// credentials.
func (c *CLI) stopServiceManagedServer() (handled bool, err error) {
	status := core.GetMariaDBStatus()
	if status.ServiceName == "" {
		return false, nil
	}

	fmt.Printf("This server is run by the Windows service %s; stopping it through the service manager...\n",
		status.ServiceName)

	if err := core.StopWindowsService(status.ServiceName); err != nil {
		// Stopping a service needs elevation, which the tool may not have.
		fmt.Printf("Could not stop the service: %v\n", err)
		fmt.Println("Falling back to a graceful shutdown with credentials. Windows will report that the")
		fmt.Println("service stopped unexpectedly; run DBSwitcher as administrator to avoid that.")
		return false, nil
	}

	if err := core.WaitForMariaDBStopped(status.ProcessID, status.Port, core.ShutdownTimeout()); err != nil {
		return true, err
	}

	fmt.Printf("✓ Service %s stopped\n", status.ServiceName)
	return true, nil
}

// warnAboutServiceOverlap points out a Windows service that serves the same
// data directory as the configuration being started. The two are one database
// reached two ways and cannot both hold the port, and an automatic service
// takes it back after every reboot.
func (c *CLI) warnAboutServiceOverlap(config *core.MariaDBConfig) {
	service, ok := core.ServiceForDataDir(config.DataDir)
	if !ok || !service.StartsAutomatically() {
		return
	}

	fmt.Printf("Note: Windows service %s serves the same data directory and starts automatically,\n", service.Name)
	fmt.Printf("      so it will claim port %s again after a reboot. Set it to manual start to avoid that:\n", config.Port)
	fmt.Printf("      Set-Service -Name %s -StartupType Manual   (as administrator)\n", service.Name)
}

// promptForCredentials prompts the user for MySQL credentials. saved is the
// entry to offer first, or nil to prompt from scratch; the second result
// reports whether it was reused, so only freshly entered credentials are
// offered for saving. detectedPort, when known, is the port of the running
// server and is preferred over the stored one.
func (c *CLI) promptForCredentials(saved *core.MySQLCredentials, configName, detectedPort string) (core.MySQLCredentials, bool, error) {
	reader := bufio.NewReader(os.Stdin)

	// Try to use saved credentials first
	if saved != nil {
		scope := "saved credentials"
		if configName != "" {
			scope = fmt.Sprintf("saved credentials for '%s'", configName)
		}
		fmt.Printf("Use %s (user: %s, host: %s)? [Y/n]: ", scope, saved.Username, saved.Host)

		response, _ := reader.ReadString('\n')
		response = strings.ToLower(strings.TrimSpace(response))

		if response == "" || response == "y" || response == "yes" {
			creds := *saved
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
	if saved != nil {
		if saved.Username != "" {
			defaultUsername = saved.Username
		}
		if saved.Host != "" {
			defaultHost = saved.Host
		}
	} else if core.SavedCredentials != nil {
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

// offerToSaveCredentials stores credentials once they are known to work, under
// the configuration they were proven against. The old flow saved them before
// the first connection attempt and under a single shared key, which is how a
// wrong password ended up in the keyring and broke every later switch.
func (c *CLI) offerToSaveCredentials(configName string, creds core.MySQLCredentials) {
	reader := bufio.NewReader(os.Stdin)

	existing, _, _ := core.LoadCredentialsForConfig(configName)

	target := "future use"
	if configName != "" {
		target = fmt.Sprintf("the '%s' configuration", configName)
	}

	prompt := fmt.Sprintf("Save these credentials for %s? [Y/n]: ", target)
	if existing != nil {
		prompt = fmt.Sprintf("Update the saved credentials for %s? [Y/n]: ", target)
	}
	fmt.Print(prompt)

	response, _ := reader.ReadString('\n')
	response = strings.ToLower(strings.TrimSpace(response))
	if response != "" && response != "y" && response != "yes" {
		return
	}

	if err := core.SaveCredentialsForConfig(configName, creds); err != nil {
		fmt.Printf("Warning: Failed to save credentials: %v\n", err)
		return
	}

	fmt.Println("Credentials saved securely.")
}

// printServiceLine reports the Windows service that owns the running server.
func printServiceLine(status core.MariaDBStatus) {
	if status.ServiceName == "" {
		return
	}

	fmt.Printf("Managed by: Windows service %s", status.ServiceName)
	if status.ServiceStartMode != "" {
		fmt.Printf(" (%s start)", strings.ToLower(status.ServiceStartMode))
	}
	fmt.Println()
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
