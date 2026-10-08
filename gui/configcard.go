package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"mariadb-monitor/core"
)

// CreateConfigCard creates the configuration management tab
func CreateConfigCard() fyne.CanvasObject {
	// Refresh configs
	core.ScanForConfigs()

	// Track selected config index
	var selectedConfig int = -1

	// Create config list with better formatting
	GlobalConfigList = widget.NewList(
		func() int { return len(core.AvailableConfigs) },
		func() fyne.CanvasObject {
			nameLabel := widget.NewLabel("Config Name")
			nameLabel.TextStyle = fyne.TextStyle{Bold: true}
			portLabel := widget.NewLabel("Port: 3306")
			statusLabel := widget.NewLabel("Ready")
			descLabel := widget.NewLabel("Description")

			return container.NewVBox(
				container.NewHBox(nameLabel, layout.NewSpacer(), portLabel, statusLabel),
				descLabel,
				widget.NewSeparator(),
			)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			c := o.(*fyne.Container)
			cfg := core.AvailableConfigs[i]

			topRow := c.Objects[0].(*fyne.Container)
			nameLabel := topRow.Objects[0].(*widget.Label)
			portLabel := topRow.Objects[2].(*widget.Label)
			statusLabel := topRow.Objects[3].(*widget.Label)
			descLabel := c.Objects[1].(*widget.Label)

			nameLabel.SetText(cfg.Name)
			portLabel.SetText("Port: " + cfg.Port)

			status := "Ready"
			if cfg.IsActive && core.CurrentStatus.IsRunning {
				status = "● ACTIVE"
				statusLabel.TextStyle = fyne.TextStyle{Bold: true}
			} else if !cfg.Exists {
				status = "Missing"
			}
			statusLabel.SetText(status)

			desc := cfg.Description
			if desc == "" {
				desc = fmt.Sprintf("Data: %s", cfg.DataDir)
			}
			descLabel.SetText(desc)
		},
	)

	// Handle selection
	GlobalConfigList.OnSelected = func(id widget.ListItemID) {
		selectedConfig = id
	}

	// Status bar
	statusBar := widget.NewLabel("")
	updateStatusBar := func() {
		fyne.Do(func() {
			if core.CurrentStatus.IsRunning {
				statusBar.SetText(fmt.Sprintf("MariaDB is running with %s configuration on port %s",
					core.CurrentStatus.ConfigName, core.CurrentStatus.Port))
			} else {
				statusBar.SetText("MariaDB is not running")
			}
		})
	}
	updateStatusBar()

	// Buttons
	startBtn := widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), func() {
		if selectedConfig >= 0 && selectedConfig < len(core.AvailableConfigs) {
			cfg := core.AvailableConfigs[selectedConfig]
			statusBar.SetText(fmt.Sprintf("Starting %s configuration...", cfg.Name))

			go func(config core.MariaDBConfig) {
				err := core.StartMariaDBForConfig(config, true)

				// Update status after operation
				RefreshMainUI()

				// Update UI on main thread
				fyne.Do(func() {
					fyne.CurrentApp().Driver().CanvasForObject(GlobalConfigList).Refresh(GlobalConfigList)
					updateStatusBar()

					if err != nil {
						fyne.CurrentApp().SendNotification(&fyne.Notification{
							Title:   "Start Failed",
							Content: fmt.Sprintf("Failed to start %s: %v", config.Name, err),
						})
						statusBar.SetText("Failed to start MariaDB")
						dialog.ShowError(err, MainWindow)
					} else {
						fyne.CurrentApp().SendNotification(&fyne.Notification{
							Title:   "MariaDB Started",
							Content: fmt.Sprintf("Successfully started %s on port %s", config.Name, config.Port),
						})
						dialog.ShowInformation("Success",
							fmt.Sprintf("Started MariaDB with %s configuration\nPort: %s\nData: %s",
								config.Name, config.Port, config.DataDir), MainWindow)
						GlobalConfigList.Refresh()
						updateStatusBar()
					}
				})
			}(cfg)
		}
	})

	stopBtn := widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		statusBar.SetText("Stopping MariaDB...")
		go func() {
			StopMariaDBServiceWithUI(MainWindow, func(err error) {
				fyne.Do(func() {
					if err != nil {
						dialog.ShowError(err, MainWindow)
						statusBar.SetText("Failed to stop MariaDB")
					} else {
						dialog.ShowInformation("Success", "MariaDB stopped successfully", MainWindow)
						GlobalConfigList.Refresh()
						updateStatusBar()
					}
				})
			})
		}()
	})

	editBtn := widget.NewButtonWithIcon("Edit", theme.DocumentIcon(), func() {
		if selectedConfig >= 0 && selectedConfig < len(core.AvailableConfigs) {
			cfg := core.AvailableConfigs[selectedConfig]
			OpenFileInEditor(cfg.Path)
		}
	})

	deleteBtn := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
		if selectedConfig >= 0 && selectedConfig < len(core.AvailableConfigs) {
			cfg := core.AvailableConfigs[selectedConfig]
			dialog.ShowConfirm("Delete Configuration",
				fmt.Sprintf("Are you sure you want to delete %s.ini?", cfg.Name),
				func(confirm bool) {
					if confirm {
						// Remove the config file
						if err := DeleteConfigFile(cfg.Path); err != nil {
							dialog.ShowError(err, MainWindow)
						} else {
							RefreshConfigurations()
						}
					}
				}, MainWindow)
		}
	})

	openFolderBtn := widget.NewButtonWithIcon("Open Folder", theme.FolderOpenIcon(), func() {
		OpenFolder(core.AppConfig.ConfigPath)
	})

	refreshBtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		RefreshConfigurations()
		updateStatusBar()
	})

	// Toolbar
	toolbar := container.NewHBox(
		startBtn,
		stopBtn,
		widget.NewSeparator(),
		editBtn,
		deleteBtn,
		widget.NewSeparator(),
		openFolderBtn,
		refreshBtn,
	)

	// Info label
	infoLabel := widget.NewLabel("Select a configuration from the list to start, edit, or delete it.")

	// Main content layout
	content := container.NewBorder(
		container.NewVBox(
			toolbar,
			widget.NewSeparator(),
			statusBar,
			infoLabel,
		),
		nil,              // bottom
		nil,              // left
		nil,              // right
		GlobalConfigList, // center - this will fill the remaining space
	)

	return content
}
