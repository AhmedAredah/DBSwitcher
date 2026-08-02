package gui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/cmd/fyne_settings/settings"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"mariadb-monitor/core"
)

// ShowCredentialsDialog shows the MySQL credentials dialog. Credentials belong
// to the running configuration, because every configuration has its own data
// directory and therefore its own accounts.
func ShowCredentialsDialog(parent fyne.Window, onSuccess func(core.MySQLCredentials), onCancel func()) {
	configName := core.CurrentStatus.ConfigName
	prefill, _, _ := core.LoadCredentialsForConfig(configName)
	if prefill == nil {
		prefill = core.SavedCredentials
	}

	// Create form fields
	usernameEntry := widget.NewEntry()
	usernameEntry.SetPlaceHolder("root")
	if prefill != nil && prefill.Username != "" {
		usernameEntry.SetText(prefill.Username)
	} else {
		usernameEntry.SetText("root") // Default suggestion
	}

	passwordEntry := widget.NewPasswordEntry()
	passwordEntry.SetPlaceHolder("Enter password (leave empty if none)")
	if prefill != nil {
		passwordEntry.SetText(prefill.Password)
	}

	hostEntry := widget.NewEntry()
	hostEntry.SetPlaceHolder("localhost")
	if prefill != nil && prefill.Host != "" {
		hostEntry.SetText(prefill.Host)
	} else {
		hostEntry.SetText("localhost")
	}

	portEntry := widget.NewEntry()
	portEntry.SetPlaceHolder("3306")
	switch {
	case core.CurrentStatus.IsRunning && core.CurrentStatus.Port != "":
		// The running server settles the port, whatever was stored.
		portEntry.SetText(core.CurrentStatus.Port)
	case prefill != nil && prefill.Port != "":
		portEntry.SetText(prefill.Port)
	default:
		portEntry.SetText("3306")
	}

	// Remember credentials checkbox options
	rememberSessionCheck := widget.NewCheck("Remember for this session", nil)
	rememberSessionCheck.SetChecked(true)

	saveLabel := "Save credentials permanently (secure storage)"
	if configName != "" {
		saveLabel = fmt.Sprintf("Save credentials for '%s' permanently (secure storage)", configName)
	}
	rememberPermanentCheck := widget.NewCheck(saveLabel, nil)
	rememberPermanentCheck.SetChecked(prefill != nil)

	// Create form
	items := []*widget.FormItem{
		widget.NewFormItem("Username", usernameEntry),
		widget.NewFormItem("Password", passwordEntry),
		widget.NewFormItem("Host", hostEntry),
		widget.NewFormItem("Port", portEntry),
		widget.NewFormItem("", rememberSessionCheck),
		widget.NewFormItem("", rememberPermanentCheck),
	}

	title := "MySQL Admin Credentials"
	if configName != "" {
		title = fmt.Sprintf("MySQL Admin Credentials - %s", configName)
	}

	// Create dialog with custom buttons
	d := dialog.NewForm(title, "Connect", "Cancel", items,
		func(confirmed bool) {
			if confirmed {
				creds := core.MySQLCredentials{
					Username: usernameEntry.Text,
					Password: passwordEntry.Text,
					Host:     hostEntry.Text,
					Port:     portEntry.Text,
				}
				
				// Set defaults if empty
				if creds.Username == "" {
					creds.Username = "root"
				}
				if creds.Host == "" {
					creds.Host = "localhost"
				}
				if creds.Port == "" {
					creds.Port = "3306"
				}
				
				// Save credentials for session if requested
				if rememberSessionCheck.Checked {
					core.SavedCredentials = &creds
				}
				
				// Save credentials permanently if requested, under the
				// configuration they belong to.
				if rememberPermanentCheck.Checked {
					if err := core.SaveCredentialsForConfig(configName, creds); err != nil {
						core.AppLogger.Log("Failed to save credentials to keyring: %v", err)
						dialog.ShowError(fmt.Errorf("Failed to save credentials: %v", err), parent)
					} else {
						core.SavedCredentials = &creds
					}
				} else if prefill != nil {
					// If unchecked, remove what was stored for this configuration
					if err := core.DeleteCredentialsForConfig(configName); err != nil {
						core.AppLogger.Log("Failed to delete credentials from keyring: %v", err)
					}
				}
				
				onSuccess(creds)
			} else {
				onCancel()
			}
		}, parent)
	
	d.Resize(fyne.NewSize(400, 300))
	d.Show()
}

// ShowSettings shows the settings dialog with configuration options
func ShowSettings() {
	fyne.Do(func() {
		// Create settings window
		settingsWindow := FyneApp.NewWindow("Settings - DBSwitcher")
		settingsWindow.Resize(fyne.NewSize(600, 500))
		settingsWindow.CenterOnScreen()
		
		// Create form entries that need to be accessed by save button
		var (
			refreshIntervalEntry    *widget.Entry
			processTimeoutEntry     *widget.Entry
			maxRetriesEntry         *widget.Entry
			connectionTimeoutEntry  *widget.Entry
		)
		
		// Create tabs for different settings categories
		tabs := container.NewAppTabs()
		
		// General Settings Tab
		generalTab, refreshEntry := createGeneralSettingsTabWithEntry()
		refreshIntervalEntry = refreshEntry
		tabs.Append(container.NewTabItem("General", generalTab))
		
		// Paths Settings Tab
		pathsTab := createPathsSettingsTab()
		tabs.Append(container.NewTabItem("Paths", pathsTab))
		
		// Advanced Settings Tab
		advancedTab, processEntry, retriesEntry, connEntry := createAdvancedSettingsTabWithEntries()
		processTimeoutEntry = processEntry
		maxRetriesEntry = retriesEntry
		connectionTimeoutEntry = connEntry
		tabs.Append(container.NewTabItem("Advanced", advancedTab))
		
		// About Tab
		aboutTab := createAboutSettingsTab()
		tabs.Append(container.NewTabItem("About", aboutTab))
		
		// Buttons
		saveBtn := widget.NewButton("Save Settings", func() {
			// Parse and validate numeric entries
			refreshSettingsChanged := false
			if refreshInterval, err := strconv.Atoi(refreshIntervalEntry.Text); err == nil && refreshInterval > 0 {
				if core.AppConfig.RefreshIntervalSecs != refreshInterval {
					core.AppConfig.RefreshIntervalSecs = refreshInterval
					refreshSettingsChanged = true
				}
			}
			if processTimeout, err := strconv.Atoi(processTimeoutEntry.Text); err == nil && processTimeout > 0 {
				core.AppConfig.ProcessTimeoutSecs = processTimeout
			}
			if maxRetries, err := strconv.Atoi(maxRetriesEntry.Text); err == nil && maxRetries >= 0 {
				core.AppConfig.MaxRetryAttempts = maxRetries
			}
			if connectionTimeout, err := strconv.Atoi(connectionTimeoutEntry.Text); err == nil && connectionTimeout > 0 {
				core.AppConfig.ConnectionTimeoutSecs = connectionTimeout
			}
			
			if err := core.SaveConfig(); err != nil {
				dialog.ShowError(fmt.Errorf("Failed to save settings: %v", err), settingsWindow)
			} else {
				// Restart auto-refresh if refresh settings changed
				if refreshSettingsChanged || core.AppConfig.AutoRefreshEnabled {
					RestartAutoRefresh()
				}
				
				// Update auto-start setting
				if err := core.UpdateAutoStartSetting(); err != nil {
					core.AppLogger.Error("Failed to update auto-start setting: %v", err)
					dialog.ShowError(fmt.Errorf("Settings saved but failed to update auto-start: %v", err), settingsWindow)
				} else {
					dialog.ShowInformation("Settings Saved", "Settings have been saved successfully.", settingsWindow)
				}
			}
		})
		
		cancelBtn := widget.NewButton("Cancel", func() {
			settingsWindow.Close()
		})
		
		resetBtn := widget.NewButton("Reset to Defaults", func() {
			dialog.ShowConfirm("Reset Settings", 
				"Are you sure you want to reset all settings to their default values?", 
				func(confirmed bool) {
					if confirmed {
						resetToDefaultSettings()
						settingsWindow.Close()
						ShowSettings() // Reopen with default values
					}
				}, settingsWindow)
		})
		
		buttonContainer := container.NewHBox(
			resetBtn,
			widget.NewSeparator(),
			saveBtn,
			cancelBtn,
		)
		
		// Main layout
		content := container.NewBorder(nil, buttonContainer, nil, nil, tabs)
		settingsWindow.SetContent(content)
		settingsWindow.Show()
	})
}

// createGeneralSettingsTabWithEntry creates the general settings tab and returns the refresh interval entry
func createGeneralSettingsTabWithEntry() (fyne.CanvasObject, *widget.Entry) {
	// Auto-refresh settings
	refreshIntervalEntry := widget.NewEntry()
	refreshIntervalEntry.SetText(fmt.Sprintf("%d", core.AppConfig.RefreshIntervalSecs))
	refreshIntervalEntry.SetPlaceHolder("5")
	
	autoRefreshCheck := widget.NewCheck("Enable auto-refresh", func(checked bool) {
		core.AppConfig.AutoRefreshEnabled = checked
		refreshIntervalEntry.Enable()
		if !checked {
			refreshIntervalEntry.Disable()
		}
	})
	autoRefreshCheck.SetChecked(core.AppConfig.AutoRefreshEnabled)
	
	// Notification settings
	notificationsCheck := widget.NewCheck("Enable notifications", func(checked bool) {
		core.AppConfig.NotificationsEnabled = checked
	})
	notificationsCheck.SetChecked(core.AppConfig.NotificationsEnabled)
	
	// Startup settings
	startMinimizedCheck := widget.NewCheck("Start minimized to system tray", func(checked bool) {
		core.AppConfig.StartMinimized = checked
	})
	startMinimizedCheck.SetChecked(core.AppConfig.StartMinimized)
	
	autoStartCheck := widget.NewCheck("Start with Windows (requires restart)", func(checked bool) {
		core.AppConfig.AutoStartWithSystem = checked
	})
	autoStartCheck.SetChecked(core.AppConfig.AutoStartWithSystem)
	
	// Log level settings
	logLevelSelect := widget.NewSelect([]string{"DEBUG", "INFO", "WARN", "ERROR"}, func(selected string) {
		core.AppConfig.LogLevel = selected
	})
	logLevelSelect.SetSelected(core.AppConfig.LogLevel)
	
	generalForm := &widget.Form{
		Items: []*widget.FormItem{
			widget.NewFormItem("Auto-refresh Status", autoRefreshCheck),
			widget.NewFormItem("Refresh Interval (seconds)", refreshIntervalEntry),
			widget.NewFormItem("", widget.NewSeparator()),
			widget.NewFormItem("Show Notifications", notificationsCheck),
			widget.NewFormItem("", widget.NewSeparator()),
			widget.NewFormItem("Start Minimized", startMinimizedCheck),
			widget.NewFormItem("Auto-start with System", autoStartCheck),
			widget.NewFormItem("", widget.NewSeparator()),
			widget.NewFormItem("Log Level", logLevelSelect),
		},
	}
	
	return container.NewScroll(generalForm), refreshIntervalEntry
}

// createGeneralSettingsTab creates the general settings tab (legacy wrapper)
func createGeneralSettingsTab() fyne.CanvasObject {
	tab, _ := createGeneralSettingsTabWithEntry()
	return tab
}

// createPathsSettingsTab creates the paths configuration tab
func createPathsSettingsTab() fyne.CanvasObject {
	// MariaDB binary path
	mariadbPathEntry := widget.NewEntry()
	mariadbPathEntry.SetText(core.AppConfig.MariaDBBin)
	mariadbPathEntry.MultiLine = false
	
	// Path validation status
	mariadbPathStatus := widget.NewLabel("")
	updateMariaDBPathStatus := func(path string) {
		if path == "" {
			mariadbPathStatus.SetText("⚠️ Path is empty")
			mariadbPathStatus.Importance = widget.MediumImportance
		} else if !core.PathExists(path) {
			mariadbPathStatus.SetText("❌ Path does not exist")
			mariadbPathStatus.Importance = widget.HighImportance
		} else {
			// Check if mysqld exists in the path
			mysqldPath := filepath.Join(path, core.GetExecutableName("mysqld"))
			if core.PathExists(mysqldPath) {
				mariadbPathStatus.SetText("✅ Valid MariaDB binary directory")
				mariadbPathStatus.Importance = widget.SuccessImportance
			} else {
				mariadbPathStatus.SetText("⚠️ mysqld not found in directory")
				mariadbPathStatus.Importance = widget.MediumImportance
			}
		}
	}
	
	// Initial validation
	updateMariaDBPathStatus(mariadbPathEntry.Text)
	
	// Real-time validation on text change
	mariadbPathEntry.OnChanged = func(text string) {
		updateMariaDBPathStatus(text)
		core.AppConfig.MariaDBBin = text
	}
	
	mariadbBrowseBtn := widget.NewButton("Browse", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			mariadbPathEntry.SetText(uri.Path())
		}, FyneApp.Driver().AllWindows()[0])
	})
	
	// Configuration directory path
	configPathEntry := widget.NewEntry()
	configPathEntry.SetText(core.AppConfig.ConfigPath)
	configPathEntry.MultiLine = false
	
	// Config path validation status
	configPathStatus := widget.NewLabel("")
	updateConfigPathStatus := func(path string) {
		if path == "" {
			configPathStatus.SetText("⚠️ Path is empty")
			configPathStatus.Importance = widget.MediumImportance
		} else if !core.PathExists(path) {
			configPathStatus.SetText("❌ Directory does not exist")
			configPathStatus.Importance = widget.HighImportance
		} else {
			configPathStatus.SetText("✅ Valid configuration directory")
			configPathStatus.Importance = widget.SuccessImportance
		}
	}
	
	// Initial validation
	updateConfigPathStatus(configPathEntry.Text)
	
	// Real-time validation on text change
	configPathEntry.OnChanged = func(text string) {
		updateConfigPathStatus(text)
		core.AppConfig.ConfigPath = text
	}
	
	configBrowseBtn := widget.NewButton("Browse", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			configPathEntry.SetText(uri.Path())
		}, FyneApp.Driver().AllWindows()[0])
	})
	
	// Auto-detect button
	autoDetectBtn := widget.NewButton("Auto-Detect MariaDB", func() {
		dialog.ShowInformation("Auto-Detection", "Searching for MariaDB installation...", FyneApp.Driver().AllWindows()[0])
		go func() {
			core.DetectMariaDBBin()
			fyne.Do(func() {
				mariadbPathEntry.SetText(core.AppConfig.MariaDBBin)
				dialog.ShowInformation("Auto-Detection Complete", 
					fmt.Sprintf("MariaDB found at: %s", core.AppConfig.MariaDBBin), 
					FyneApp.Driver().AllWindows()[0])
			})
		}()
	})
	
	pathsForm := &widget.Form{
		Items: []*widget.FormItem{
			widget.NewFormItem("MariaDB Binary Directory", 
				container.NewBorder(nil, nil, nil, mariadbBrowseBtn, mariadbPathEntry)),
			widget.NewFormItem("", mariadbPathStatus),
			widget.NewFormItem("Configuration Directory", 
				container.NewBorder(nil, nil, nil, configBrowseBtn, configPathEntry)),
			widget.NewFormItem("", configPathStatus),
			widget.NewFormItem("", widget.NewSeparator()),
			widget.NewFormItem("", autoDetectBtn),
		},
	}
	
	return container.NewScroll(pathsForm)
}

// createAdvancedSettingsTabWithEntries creates the advanced settings tab and returns the numeric entries
func createAdvancedSettingsTabWithEntries() (fyne.CanvasObject, *widget.Entry, *widget.Entry, *widget.Entry) {
	// Process management settings
	processTimeoutEntry := widget.NewEntry()
	processTimeoutEntry.SetText(fmt.Sprintf("%d", core.AppConfig.ProcessTimeoutSecs))
	processTimeoutEntry.SetPlaceHolder("30")
	
	maxRetriesEntry := widget.NewEntry()
	maxRetriesEntry.SetText(fmt.Sprintf("%d", core.AppConfig.MaxRetryAttempts))
	maxRetriesEntry.SetPlaceHolder("3")
	
	// Connection settings
	connectionTimeoutEntry := widget.NewEntry()
	connectionTimeoutEntry.SetText(fmt.Sprintf("%d", core.AppConfig.ConnectionTimeoutSecs))
	connectionTimeoutEntry.SetPlaceHolder("5")
	
	// Debug settings
	debugModeCheck := widget.NewCheck("Enable debug mode", func(checked bool) {
		core.AppConfig.DebugMode = checked
	})
	debugModeCheck.SetChecked(core.AppConfig.DebugMode)
	
	verboseLoggingCheck := widget.NewCheck("Verbose logging", func(checked bool) {
		core.AppConfig.VerboseLogging = checked
	})
	verboseLoggingCheck.SetChecked(core.AppConfig.VerboseLogging)
	
	// Performance settings
	backgroundProcessingCheck := widget.NewCheck("Enable background processing", func(checked bool) {
		core.AppConfig.BackgroundProcessing = checked
	})
	backgroundProcessingCheck.SetChecked(core.AppConfig.BackgroundProcessing)
	
	advancedForm := &widget.Form{
		Items: []*widget.FormItem{
			widget.NewFormItem("Process Timeout (seconds)", processTimeoutEntry),
			widget.NewFormItem("Max Retry Attempts", maxRetriesEntry),
			widget.NewFormItem("", widget.NewSeparator()),
			widget.NewFormItem("Connection Timeout (seconds)", connectionTimeoutEntry),
			widget.NewFormItem("", widget.NewSeparator()),
			widget.NewFormItem("Debug Mode", debugModeCheck),
			widget.NewFormItem("Verbose Logging", verboseLoggingCheck),
			widget.NewFormItem("", widget.NewSeparator()),
			widget.NewFormItem("Background Processing", backgroundProcessingCheck),
		},
	}
	
	return container.NewScroll(advancedForm), processTimeoutEntry, maxRetriesEntry, connectionTimeoutEntry
}

// createAdvancedSettingsTab creates the advanced settings tab (legacy wrapper)
func createAdvancedSettingsTab() fyne.CanvasObject {
	tab, _, _, _ := createAdvancedSettingsTabWithEntries()
	return tab
}

// createAboutSettingsTab creates the about/info tab
func createAboutSettingsTab() fyne.CanvasObject {
	versionLabel := widget.NewLabel("Version: 0.0.1")
	versionLabel.TextStyle = fyne.TextStyle{Bold: true}
	
	buildLabel := widget.NewLabel("Build: Development")
	
	configDirLabel := widget.NewLabel(fmt.Sprintf("Config Directory: %s", core.AppConfig.ConfigPath))
	configDirLabel.Wrapping = fyne.TextWrapWord
	
	logDirLabel := widget.NewLabel(fmt.Sprintf("Log Directory: %s", core.GetAppDataDir()))
	logDirLabel.Wrapping = fyne.TextWrapWord
	
	mariadbLabel := widget.NewLabel(fmt.Sprintf("MariaDB Path: %s", core.AppConfig.MariaDBBin))
	mariadbLabel.Wrapping = fyne.TextWrapWord
	
	// Action buttons
	openConfigDirBtn := widget.NewButton("Open Config Directory", func() {
		openFolderCrossPlatform(core.AppConfig.ConfigPath)
	})
	
	openLogDirBtn := widget.NewButton("Open Log Directory", func() {
		openFolderCrossPlatform(core.GetAppDataDir())
	})
	
	viewLogsBtn := widget.NewButton("View Logs", func() {
		ShowLogs()
	})
	
	aboutContent := container.NewVBox(
		widget.NewCard("Application Information", "", container.NewVBox(
			versionLabel,
			buildLabel,
			widget.NewSeparator(),
			configDirLabel,
			logDirLabel,
			mariadbLabel,
		)),
		widget.NewCard("Quick Actions", "", container.NewVBox(
			openConfigDirBtn,
			openLogDirBtn,
			viewLogsBtn,
		)),
		widget.NewCard("System Information", "", container.NewVBox(
			widget.NewLabel(fmt.Sprintf("OS: %s", runtime.GOOS)),
			widget.NewLabel(fmt.Sprintf("Architecture: %s", runtime.GOARCH)),
			widget.NewLabel(fmt.Sprintf("Go Version: %s", runtime.Version())),
		)),
	)
	
	return container.NewScroll(aboutContent)
}

// resetToDefaultSettings resets all settings to their default values
func resetToDefaultSettings() {
	// Reset core configuration to defaults
	core.AppConfig.ConfigPath = core.GetUserConfigDir()
	core.DetectMariaDBBin()
	core.SaveConfig()
	
	dialog.ShowInformation("Settings Reset", "All settings have been reset to their default values.", MainWindow)
}

// ShowAppearanceSettings shows Fyne appearance settings
func ShowAppearanceSettings() {
	fyne.Do(func() {
		if FyneApp == nil {
			FyneApp = app.NewWithID("mariadb-switcher")
		}
		
		// Create appearance settings window
		settingsWindow := FyneApp.NewWindow("Appearance Settings")
		settingsWindow.Resize(fyne.NewSize(400, 300))
		
		// Create settings content using Fyne's built-in settings
		settingsContent := settings.NewSettings().LoadAppearanceScreen(settingsWindow)
		settingsWindow.SetContent(settingsContent)
		settingsWindow.Show()
	})
}

// ShowLogs shows the application logs
func ShowLogs() {
	fyne.Do(func() {
		logWindow := FyneApp.NewWindow("Application Logs")
		logWindow.Resize(fyne.NewSize(800, 600))
		
		// Read log file
		logPath := filepath.Join(core.GetAppDataDir(), "dbswitcher.log")
		logContent := "No logs available"
		
		if data, err := os.ReadFile(logPath); err == nil {
			logContent = string(data)
			// Show only last 1000 lines to avoid overwhelming the UI
			lines := strings.Split(logContent, "\n")
			if len(lines) > 1000 {
				lines = lines[len(lines)-1000:]
				logContent = strings.Join(lines, "\n")
			}
		}
		
		logText := widget.NewEntry()
		logText.MultiLine = true
		logText.Wrapping = fyne.TextWrapWord
		logText.SetText(logContent)
		logText.Disable() // Make it read-only
		
		scrollable := container.NewScroll(logText)
		
		// Buttons
		refreshBtn := widget.NewButton("Refresh", func() {
			if data, err := os.ReadFile(logPath); err == nil {
				content := string(data)
				lines := strings.Split(content, "\n")
				if len(lines) > 1000 {
					lines = lines[len(lines)-1000:]
					content = strings.Join(lines, "\n")
				}
				logText.SetText(content)
			}
		})
		
		openLogFolderBtn := widget.NewButton("Open Log Folder", func() {
			OpenFolder(core.GetAppDataDir())
		})
		
		clearBtn := widget.NewButton("Clear Logs", func() {
			dialog.ShowConfirm("Clear Logs", "Are you sure you want to clear all logs?", func(confirmed bool) {
				if confirmed {
					if err := os.WriteFile(logPath, []byte(""), 0644); err == nil {
						logText.SetText("Logs cleared")
					}
				}
			}, logWindow)
		})
		
		toolbar := container.NewHBox(refreshBtn, openLogFolderBtn, clearBtn)
		
		content := container.NewBorder(toolbar, nil, nil, nil, scrollable)
		logWindow.SetContent(content)
		logWindow.Show()
	})
}

// ShowAbout shows the about dialog
func ShowAbout() {
	aboutContent := fmt.Sprintf(`DBSwitcher - MariaDB Configuration Manager
Version: 0.0.1

A tool for managing multiple MariaDB configurations and switching between them easily.

Features:
• Multiple configuration management
• Quick switching between databases
• System tray integration
• Command-line interface
• Secure credential storage

Configuration Directory:
%s

Log Directory:
%s

© 2025 DBSwitcher Project`, core.AppConfig.ConfigPath, core.GetAppDataDir())

	dialog.ShowInformation("About DBSwitcher", aboutContent, MainWindow)
}

// ShowStatusDialog shows a detailed status dialog
func ShowStatusDialog() {
	fyne.Do(func() {
		statusWindow := FyneApp.NewWindow("MariaDB Status Details")
		statusWindow.Resize(fyne.NewSize(500, 400))
		
		status := core.GetMariaDBStatus()
		
		var statusText string
		if status.IsRunning {
			statusText = fmt.Sprintf(`MariaDB Status: RUNNING ✅

Process ID: %d
Configuration: %s
Port: %s
Data Directory: %s
Config File: %s
Version: %s

MariaDB is currently running and accepting connections.`,
				status.ProcessID,
				status.ConfigName,
				status.Port,
				status.DataPath,
				status.ConfigFile,
				status.Version)
		} else {
			statusText = `MariaDB Status: STOPPED ❌

MariaDB is not currently running.
Use the Start button to launch MariaDB with a configuration.`
		}
		
		statusLabel := widget.NewLabel(statusText)
		statusLabel.Wrapping = fyne.TextWrapWord
		
		refreshBtn := widget.NewButton("Refresh", func() {
			newStatus := core.GetMariaDBStatus()
			var newText string
			if newStatus.IsRunning {
				newText = fmt.Sprintf(`MariaDB Status: RUNNING ✅

Process ID: %d
Configuration: %s
Port: %s
Data Directory: %s
Config File: %s
Version: %s

MariaDB is currently running and accepting connections.`,
					newStatus.ProcessID,
					newStatus.ConfigName,
					newStatus.Port,
					newStatus.DataPath,
					newStatus.ConfigFile,
					newStatus.Version)
			} else {
				newText = `MariaDB Status: STOPPED ❌

MariaDB is not currently running.
Use the Start button to launch MariaDB with a configuration.`
			}
			statusLabel.SetText(newText)
			core.CurrentStatus = newStatus
		})
		
		closeBtn := widget.NewButton("Close", func() {
			statusWindow.Close()
		})
		
		buttonContainer := container.NewHBox(refreshBtn, closeBtn)
		
		content := container.NewBorder(nil, buttonContainer, nil, nil,
			container.NewScroll(statusLabel))
		
		statusWindow.SetContent(content)
		statusWindow.Show()
	})
}

// OpenFileInEditor opens a file in the default system editor
func OpenFileInEditor(path string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("notepad", path)
	case "darwin":
		cmd = exec.Command("open", "-t", path)
	default:
		// Try common Linux editors
		editors := []string{"gedit", "kate", "xed", "nano", "vim"}
		for _, editor := range editors {
			if _, err := exec.LookPath(editor); err == nil {
				cmd = exec.Command(editor, path)
				break
			}
		}
		if cmd == nil {
			cmd = exec.Command("xdg-open", path)
		}
	}

	if cmd != nil {
		cmd.Start()
	}
}

// DeleteConfigFile deletes a configuration file
func DeleteConfigFile(path string) error {
	return os.Remove(path)
}

// openFolderCrossPlatform opens a folder in the default file manager
func openFolderCrossPlatform(path string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		// Linux and other Unix systems
		cmd = exec.Command("xdg-open", path)
	}

	return cmd.Start()
}