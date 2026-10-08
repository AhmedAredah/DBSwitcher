package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"mariadb-monitor/core"
)

// CreateQuickActionsCard creates the quick actions control panel
func CreateQuickActionsCard() *widget.Card {
	// Quick start dropdown
	configOptions := []string{}
	for _, cfg := range core.AvailableConfigs {
		configOptions = append(configOptions, cfg.Name)
	}

	GlobalConfigSelect = widget.NewSelect(configOptions, func(selected string) {
		// Find and start the selected config
		for _, cfg := range core.AvailableConfigs {
			if cfg.Name == selected {
				go func(config core.MariaDBConfig) {
					// Show starting notification
					fyne.CurrentApp().SendNotification(&fyne.Notification{
						Title:   "Starting MariaDB",
						Content: fmt.Sprintf("Starting %s configuration...", config.Name),
					})

					err := core.StartMariaDBForConfig(config, true)

					// Update status after start attempt
					RefreshMainUI()

					// Update UI on main thread
					fyne.Do(func() {
						fyne.CurrentApp().Driver().CanvasForObject(MainWindow.Content()).Refresh(MainWindow.Content())

						// Show result notification
						if err != nil {
							fyne.CurrentApp().SendNotification(&fyne.Notification{
								Title:   "MariaDB Start Failed",
								Content: err.Error(),
							})
							dialog.ShowError(err, MainWindow)
						} else {
							fyne.CurrentApp().SendNotification(&fyne.Notification{
								Title:   "MariaDB Started",
								Content: fmt.Sprintf("Successfully started with %s configuration on port %s", config.Name, config.Port),
							})
							dialog.ShowInformation("Success",
								fmt.Sprintf("MariaDB started with %s configuration\nPort: %s", config.Name, config.Port),
								MainWindow)
						}
					})
				}(cfg)
				break
			}
		}
	})
	GlobalConfigSelect.PlaceHolder = "Select configuration..."

	// Refresh button to reload configurations
	refreshBtn := widget.NewButton("⟳", func() {
		RefreshConfigurations()
	})
	refreshBtn.Importance = widget.LowImportance

	startBtn := widget.NewButton("Start", func() {
		if GlobalConfigSelect.Selected != "" {
			GlobalConfigSelect.OnChanged(GlobalConfigSelect.Selected)
		}
	})

	stopBtn := widget.NewButton("Stop MariaDB", func() {
		if core.CurrentStatus.IsRunning {
			go func() {
				fyne.CurrentApp().SendNotification(&fyne.Notification{
					Title:   "Stopping MariaDB",
					Content: "Stopping MariaDB service...",
				})

				StopMariaDBServiceWithUI(MainWindow, func(err error) {
					RefreshMainUI()

					// Force UI refresh
					fyne.Do(func() {
						fyne.CurrentApp().Driver().CanvasForObject(MainWindow.Content()).Refresh(MainWindow.Content())

						if err != nil {
							fyne.CurrentApp().SendNotification(&fyne.Notification{
								Title:   "Stop Failed",
								Content: err.Error(),
							})
							dialog.ShowError(err, MainWindow)
						} else {
							fyne.CurrentApp().SendNotification(&fyne.Notification{
								Title:   "MariaDB Stopped",
								Content: "MariaDB has been stopped successfully",
							})
							dialog.ShowInformation("Success", "MariaDB stopped successfully", MainWindow)
						}
					})
				})
			}()
		}
	})

	restartBtn := widget.NewButton("Restart", func() {
		if core.CurrentStatus.IsRunning {
			go func() {
				// Get current config
				currentConfig := core.CurrentStatus.ConfigFile
				if currentConfig == "" && core.AppConfig.LastUsedConfig != "" {
					currentConfig = core.AppConfig.LastUsedConfig
				}

				// Stop with UI credential handling
				StopMariaDBServiceWithUI(MainWindow, func(stopErr error) {
					if stopErr != nil {
						// Update UI on main thread
						fyne.Do(func() {
							dialog.ShowError(stopErr, MainWindow)
						})
						return
					}

					// Wait for the server to actually be gone instead of
					// assuming three seconds: a large buffer pool takes
					// longer, and the port is freed a moment after it exits.
					if waitErr := core.WaitForMariaDBStopped(0, core.CurrentStatus.Port, core.ShutdownTimeout()); waitErr != nil {
						fyne.Do(func() {
							dialog.ShowError(waitErr, MainWindow)
						})
						return
					}

					// Start with same config
					if currentConfig != "" {
						var startErr error
						if config := core.FindConfigByPath(currentConfig); config != nil {
							startErr = core.StartMariaDBForConfig(*config, true)
						} else {
							startErr = core.StartMariaDBWithConfig(currentConfig)
						}
						RefreshMainUI()

						// Update UI on main thread
						fyne.Do(func() {
							if startErr != nil {
								dialog.ShowError(startErr, MainWindow)
							} else {
								dialog.ShowInformation("Success", "MariaDB restarted successfully", MainWindow)
							}
						})
					}
				})
			}()
		}
	})

	openFolderBtn := widget.NewButton("Open Config Folder", func() {
		OpenFolder(core.AppConfig.ConfigPath)
	})

	return widget.NewCard("Quick Actions", "", container.NewVBox(
		container.NewBorder(nil, nil,
			widget.NewLabel("Start with:"),
			container.NewHBox(refreshBtn, startBtn),
			GlobalConfigSelect),
		container.NewGridWithColumns(2, stopBtn, restartBtn),
		openFolderBtn,
	))
}

// RefreshConfigurations reloads and updates both dropdown and config list
func RefreshConfigurations() {
	// Save current selection from dropdown
	var currentSelection string
	if GlobalConfigSelect != nil {
		currentSelection = GlobalConfigSelect.Selected
	}

	// Rescan for configurations
	core.ScanForConfigs()

	// Update dropdown if it exists
	if GlobalConfigSelect != nil {
		newOptions := []string{}
		for _, cfg := range core.AvailableConfigs {
			newOptions = append(newOptions, cfg.Name)
		}
		GlobalConfigSelect.Options = newOptions

		// Restore selection if it still exists
		for _, option := range newOptions {
			if option == currentSelection {
				GlobalConfigSelect.SetSelected(currentSelection)
				break
			}
		}

		GlobalConfigSelect.Refresh()
	}

	// Update config list if it exists
	if GlobalConfigList != nil {
		GlobalConfigList.Refresh()
	}

	// Show notification
	if FyneApp != nil {
		fyne.CurrentApp().SendNotification(&fyne.Notification{
			Title:   "Configurations Refreshed",
			Content: fmt.Sprintf("Found %d configuration(s)", len(core.AvailableConfigs)),
		})
	}
}

// RefreshMainUI refreshes the main UI components
func RefreshMainUI() {
	if MainWindow != nil && MainWindow.Content() != nil {
		// Use the UI-enabled version that can prompt for credentials
		GetMariaDBStatusWithUI(MainWindow, func(status core.MariaDBStatus) {
			core.CurrentStatus = status

			// Also refresh configurations
			RefreshConfigurations()

			// Refresh all UI components in the main UI thread
			fyne.Do(func() {
				fyne.CurrentApp().Driver().CanvasForObject(MainWindow.Content()).Refresh(MainWindow.Content())
			})

			// Update status card (already has its own fyne.Do() wrapper)
			if StatusCardRef != nil {
				UpdateStatusCard(StatusCardRef)
			}
		})
	}
}
