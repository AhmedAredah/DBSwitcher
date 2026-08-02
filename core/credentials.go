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

	// KeyringAccount is the shared entry. It predates per-configuration
	// storage and is still read as a fallback for configurations that have no
	// entry of their own.
	KeyringAccount = "mysql_credentials"
)

// Global credentials storage. Holds the most recently loaded or saved
// credentials and is what the GUI dialogs pre-fill from.
var SavedCredentials *MySQLCredentials

// KeyringAccountFor returns the keyring account holding the credentials for a
// configuration.
//
// Every configuration points at its own data directory, and each data
// directory carries its own mysql.user table - the same account name can have
// a different password in each. One shared entry therefore cannot be correct
// for all of them, which is why switching between two configurations kept
// failing with "Access denied".
func KeyringAccountFor(configName string) string {
	if configName == "" {
		return KeyringAccount
	}
	return KeyringAccount + ":" + strings.ToLower(configName)
}

// SaveCredentialsToKeyring saves credentials to the shared keyring entry
func SaveCredentialsToKeyring(creds MySQLCredentials) error {
	return SaveCredentialsForConfig("", creds)
}

// SaveCredentialsForConfig stores credentials under the configuration they
// were proven against. An empty configName writes the shared entry.
func SaveCredentialsForConfig(configName string, creds MySQLCredentials) error {
	// Serialize credentials to JSON
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %v", err)
	}

	// Store in system keyring
	if err := keyring.Set(KeyringService, KeyringAccountFor(configName), string(data)); err != nil {
		return fmt.Errorf("failed to save to keyring: %v", err)
	}

	saved := creds
	SavedCredentials = &saved

	AppLogger.Log("Credentials saved to system keyring for %s", describeCredentialScope(configName))
	return nil
}

// LoadCredentialsFromKeyring loads the shared credentials from the keyring
func LoadCredentialsFromKeyring() (*MySQLCredentials, error) {
	creds, err := loadFromKeyring(KeyringAccount)
	if err != nil {
		return nil, err
	}
	if creds != nil {
		AppLogger.Log("Credentials loaded from system keyring")
	}
	return creds, nil
}

// LoadCredentialsForConfig returns the stored credentials for a configuration.
// The second result reports whether the hit was the configuration's own entry;
// false means the shared entry was used as a fallback.
func LoadCredentialsForConfig(configName string) (*MySQLCredentials, bool, error) {
	if configName != "" {
		creds, err := loadFromKeyring(KeyringAccountFor(configName))
		if err != nil {
			return nil, false, err
		}
		if creds != nil {
			AppLogger.Log("Credentials loaded from system keyring for configuration '%s'", configName)
			return creds, true, nil
		}
	}

	creds, err := loadFromKeyring(KeyringAccount)
	if err != nil {
		return nil, false, err
	}
	if creds != nil && configName != "" {
		AppLogger.Log("No credentials stored for '%s' yet, falling back to the shared entry", configName)
	}
	return creds, false, nil
}

func loadFromKeyring(account string) (*MySQLCredentials, error) {
	data, err := keyring.Get(KeyringService, account)
	if err != nil {
		if err == keyring.ErrNotFound {
			return nil, nil // No saved credentials
		}
		return nil, fmt.Errorf("failed to load from keyring: %v", err)
	}

	// Deserialize credentials
	var creds MySQLCredentials
	if err := json.Unmarshal([]byte(data), &creds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal credentials: %v", err)
	}

	return &creds, nil
}

// DeleteCredentialsFromKeyring removes the shared credentials
func DeleteCredentialsFromKeyring() error {
	return DeleteCredentialsForConfig("")
}

// DeleteCredentialsForConfig removes the credentials stored for a
// configuration. An empty configName removes the shared entry.
func DeleteCredentialsForConfig(configName string) error {
	err := keyring.Delete(KeyringService, KeyringAccountFor(configName))
	if err != nil && err != keyring.ErrNotFound {
		return fmt.Errorf("failed to delete from keyring: %v", err)
	}

	AppLogger.Log("Credentials deleted from system keyring for %s", describeCredentialScope(configName))
	return nil
}

// describeCredentialScope names the entry being touched, for the log.
func describeCredentialScope(configName string) string {
	if configName == "" {
		return "all configurations (shared entry)"
	}
	return fmt.Sprintf("configuration '%s'", configName)
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

// RecordWorkingCredentials files credentials under a configuration that has no
// entry of its own yet, after they have been proven to work.
//
// It only ever copies a secret that is already in the keyring under the shared
// entry, so it needs no separate consent; credentials the user typed but chose
// not to save are never written here.
func RecordWorkingCredentials(configName string, creds MySQLCredentials) bool {
	if configName == "" {
		return false
	}

	existing, ownEntry, err := LoadCredentialsForConfig(configName)
	if err != nil || ownEntry || existing == nil {
		return false
	}

	if err := SaveCredentialsForConfig(configName, creds); err != nil {
		AppLogger.Warn("Could not record credentials for '%s': %v", configName, err)
		return false
	}

	return true
}

// GetCredentialsForConfig returns usable credentials for a configuration,
// falling back to the shared entry and then to the defaults.
func GetCredentialsForConfig(configName string) MySQLCredentials {
	creds, _, err := LoadCredentialsForConfig(configName)
	if err != nil {
		AppLogger.Error("Failed to load credentials for '%s': %v", configName, err)
	}

	if creds == nil {
		return GetDefaultCredentials()
	}

	resolved := *creds
	SetCredentialsDefaults(&resolved)
	return resolved
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