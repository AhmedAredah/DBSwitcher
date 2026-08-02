package main

import (
	"fmt"
	"os"
	"strings"

	"mariadb-monitor/cli"
	"mariadb-monitor/core"
	"mariadb-monitor/gui"
)

// Version information - these can be set at build time
var (
	Version     = "0.0.1"                                    // Set via -ldflags at build time
	BuildDate   = "unknown"                                  // Set via -ldflags at build time  
	Description = "MariaDB Configuration Manager"
)

// displayVersion returns the version with exactly one leading "v". The build
// passes the git tag, which already carries one, so a plain "v%s" printed
// "vv0.0.1".
func displayVersion() string {
	return "v" + strings.TrimPrefix(Version, "v")
}

func main() {
	// Initialize core subsystems
	if err := initializeApplication(); err != nil {
		fmt.Printf("Failed to initialize application: %v\n", err)
		os.Exit(1)
	}

	// Parse command line arguments
	if len(os.Args) < 2 {
		// Default to GUI mode
		startMinimized := core.AppConfig.StartMinimized
		core.AppLogger.Log("Starting application in GUI mode (no arguments provided), minimized: %t", startMinimized)
		if err := gui.RunWithOptions(startMinimized); err != nil {
			fmt.Printf("Error running GUI: %v\n", err)
			fmt.Println("Use 'help' for CLI usage information")
			os.Exit(1)
		}
		return
	}

	command := os.Args[1]
	
	// Check for --minimized flag
	if command == "--minimized" {
		core.AppLogger.Log("Starting application in GUI mode (minimized)")
		if err := gui.RunWithOptions(true); err != nil {
			fmt.Printf("Error running GUI: %v\n", err)
			os.Exit(1)
		}
		return
	}
	
	core.AppLogger.Log("Executing command: %s", command)
	
	cli := cli.NewCLI()

	switch command {
	case "list":
		if err := cli.List(); err != nil {
			core.AppLogger.Log("List command failed: %v", err)
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	case "status":
		if err := cli.Status(); err != nil {
			core.AppLogger.Log("Status command failed: %v", err)
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	case "start":
		if len(os.Args) < 3 {
			fmt.Println("Error: Configuration name required")
			fmt.Println("Usage: dbswitcher start <config-name>")
			os.Exit(1)
		}
		configName := os.Args[2]
		core.AppLogger.Log("Starting MariaDB with configuration: %s", configName)
		if err := cli.Start(configName); err != nil {
			core.AppLogger.Log("Start command failed: %v", err)
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	case "switch":
		if len(os.Args) < 3 {
			fmt.Println("Error: Configuration name required")
			fmt.Println("Usage: dbswitcher switch <config-name>")
			os.Exit(1)
		}
		configName := os.Args[2]
		core.AppLogger.Log("Switching to configuration: %s", configName)
		if err := cli.Switch(configName); err != nil {
			core.AppLogger.Log("Switch command failed: %v", err)
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	case "stop":
		core.AppLogger.Log("Stopping MariaDB")
		if err := cli.Stop(); err != nil {
			core.AppLogger.Log("Stop command failed: %v", err)
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

	case "gui":
		core.AppLogger.Log("Starting application in GUI mode")
		if err := gui.Run(); err != nil {
			core.AppLogger.Log("GUI mode failed: %v", err)
			fmt.Printf("Error running GUI: %v\n", err)
			os.Exit(1)
		}

	case "tray":
		core.AppLogger.Log("Starting application in system tray mode")
		if err := gui.RunTray(); err != nil {
			core.AppLogger.Log("Tray mode failed: %v", err)
			fmt.Printf("Error running tray: %v\n", err)
			os.Exit(1)
		}

	case "help", "--help", "-h":
		cli.ShowHelp()

	case "version", "--version", "-v":
		fmt.Printf("DBSwitcher %s - %s\n", displayVersion(), Description)
		if BuildDate != "unknown" {
			fmt.Printf("Build Date: %s\n", BuildDate)
		}

	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Println("Use 'help' for usage information")
		os.Exit(1)
	}
}

// initializeApplication initializes all core subsystems
func initializeApplication() error {
	// Initialize core configuration and logging
	core.Init()
	
	// Initialize credential management
	core.InitCredentials()
	
	// Log application startup
	core.AppLogger.Log("DBSwitcher %s started", displayVersion())
	core.AppLogger.Log("Configuration directory: %s", core.AppConfig.ConfigPath)
	core.AppLogger.Log("MariaDB binary directory: %s", core.AppConfig.MariaDBBin)
	
	return nil
}