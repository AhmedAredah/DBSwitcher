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
		// Try to use saved credentials first, aimed at the port the server is
		// really listening on.
		creds := core.GetCredentialsForRunningInstance()
		err := core.StopMySQLWithCredentials(creds)

		// If credentials failed, show credential dialog
		if err != nil && core.IsCredentialError(err) {
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
			// Either success or non-credential error
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