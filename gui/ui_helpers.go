package gui

import (
	"fyne.io/fyne/v2"
	"mariadb-monitor/core"
)

// SystrayRunning tracks if system tray is running
var SystrayRunning bool

// StopMariaDBServiceWithUI stops MariaDB with UI credential handling
func StopMariaDBServiceWithUI(window fyne.Window, callback func(error)) {
	go func() {
		// Credentials belong to the running configuration, and the port comes
		// from the server rather than from whatever was stored.
		configName := core.GetMariaDBStatus().ConfigName

		creds := core.GetCredentialsForRunningInstance()
		err := core.StopMySQLWithCredentials(creds)

		if err == nil {
			core.RecordWorkingCredentials(configName, creds)
			callback(nil)
			return
		}

		// If credentials failed, show credential dialog
		if core.IsCredentialError(err) {
			core.AppLogger.Log("Saved credentials were rejected (%s), asking the user", core.ClientOutput(err))
			// Run credential dialog on main UI thread
			fyne.Do(func() {
				ShowCredentialsDialog(window, func(newCreds core.MySQLCredentials) {
					// Try again with new credentials
					go func() {
						err := core.StopMySQLWithCredentials(newCreds)
						callback(err)
					}()
				}, func() {
					// User cancelled credential dialog
					callback(err) // Return original error
				})
			})
		} else {
			// Non-credential failure: nothing the dialog can fix
			callback(err)
		}
	}()
}

// GetMariaDBStatusWithUI gets status with UI credential support
func GetMariaDBStatusWithUI(window fyne.Window, callback func(core.MariaDBStatus)) {
	go func() {
		// For status checking, we don't typically need credentials
		// Just use the regular status function
		status := core.GetMariaDBStatus()
		callback(status)
	}()
}