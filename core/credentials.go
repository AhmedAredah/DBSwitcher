package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// Keyring service constants
const (
	KeyringService = "DBSwitcher"
	KeyringAccount = "mysql_credentials"
)

// Global credentials storage
var SavedCredentials *MySQLCredentials

// SaveCredentialsToKeyring saves credentials to the system keyring
func SaveCredentialsToKeyring(creds MySQLCredentials) error {
	// Serialize credentials to JSON
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %v", err)
	}
	
	// Store in system keyring
	err = keyring.Set(KeyringService, KeyringAccount, string(data))
	if err != nil {
		return fmt.Errorf("failed to save to keyring: %v", err)
	}
	
	AppLogger.Log("Credentials saved to system keyring")
	return nil
}

// LoadCredentialsFromKeyring loads credentials from the system keyring
func LoadCredentialsFromKeyring() (*MySQLCredentials, error) {
	// Retrieve from system keyring
	data, err := keyring.Get(KeyringService, KeyringAccount)
	if err != nil {
		if err == keyring.ErrNotFound {
			return nil, nil // No saved credentials
		}
		return nil, fmt.Errorf("failed to load from keyring: %v", err)
	}
	
	// Deserialize credentials
	var creds MySQLCredentials
	err = json.Unmarshal([]byte(data), &creds)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal credentials: %v", err)
	}
	
	AppLogger.Log("Credentials loaded from system keyring")
	return &creds, nil
}

// DeleteCredentialsFromKeyring removes credentials from the system keyring
func DeleteCredentialsFromKeyring() error {
	err := keyring.Delete(KeyringService, KeyringAccount)
	if err != nil && err != keyring.ErrNotFound {
		return fmt.Errorf("failed to delete from keyring: %v", err)
	}
	
	AppLogger.Log("Credentials deleted from system keyring")
	return nil
}

// TestMySQLConnection tests a MySQL connection with provided credentials
func TestMySQLConnection(creds MySQLCredentials) error {
	timeout := AppConfig.ConnectionTimeoutSecs
	if timeout <= 0 {
		timeout = 5
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd, cleanup, err := clientCommand(ctx, creds, "-e", "SELECT 1")
	if err != nil {
		return err
	}
	defer cleanup()

	output, err := cmd.CombinedOutput()

	if err != nil {
		// Keep the server's own message so IsCredentialError can classify it.
		return &ClientError{Output: string(output), Err: err}
	}

	return nil
}

// InitCredentials loads saved credentials on startup
func InitCredentials() {
	if creds, err := LoadCredentialsFromKeyring(); err != nil {
		AppLogger.Error("Failed to load saved credentials: %v", err)
	} else if creds != nil {
		SavedCredentials = creds
		AppLogger.Log("Loaded saved credentials for user: %s", creds.Username)
	}
}

// GetDefaultCredentials returns default credentials for CLI use
func GetDefaultCredentials() MySQLCredentials {
	if SavedCredentials != nil {
		return *SavedCredentials
	}
	
	return MySQLCredentials{
		Username: "root",
		Host:     "localhost",
		Port:     "3306",
		Password: "",
	}
}

// SetCredentialsDefaults sets default values for empty fields
func SetCredentialsDefaults(creds *MySQLCredentials) {
	if creds.Username == "" {
		creds.Username = "root"
	}
	if creds.Host == "" {
		creds.Host = "localhost"
	}
	if creds.Port == "" {
		creds.Port = "3306"
	}
}

// IsCredentialError checks if an error is related to credential authentication.
//
// This only works because ClientError carries the client's output: the old
// errors collapsed to "shutdown failed: exit status 1", which matched nothing
// here, so the GUI never re-prompted and the CLI simply gave up.
func IsCredentialError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	for _, marker := range []string{
		"access denied",
		"using password",
		"authentication",
		"password expired",
	} {
		if strings.Contains(errStr, marker) {
			return true
		}
	}
	return false
}