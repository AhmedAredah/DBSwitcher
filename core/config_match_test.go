package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSameDirectory(t *testing.T) {
	if !sameDirectory("C:/ProgramData/MariaDB/MariaDB 11.4/data", "C:/ProgramData/MariaDB/MariaDB 11.4/data/") {
		t.Error("a trailing separator must not change the directory")
	}

	if runtime.GOOS == "windows" {
		// Config files use forward slashes, the service's my.ini uses
		// backslashes, and Windows paths are case-insensitive.
		if !sameDirectory("C:/ProgramData/MariaDB/MariaDB 11.4/data", `c:\programdata\mariadb\mariadb 11.4\DATA`) {
			t.Error("separator and case differences must compare equal on Windows")
		}
	}

	if sameDirectory("D:/MariaDB/data", "C:/ProgramData/MariaDB/MariaDB 11.4/data") {
		t.Error("different data directories must not compare equal")
	}
}

func TestFindConfigByDataDir(t *testing.T) {
	AppLogger = &Logger{}

	saved := AvailableConfigs
	defer func() { AvailableConfigs = saved }()

	AvailableConfigs = []MariaDBConfig{
		{Name: "external", DataDir: "D:/MariaDB/data", Port: "3306"},
		{Name: "internal", DataDir: "C:/ProgramData/MariaDB/MariaDB 11.4/data", Port: "3306"},
	}

	// The Windows service runs from its own my.ini, which DBSwitcher does not
	// manage; only the data directory identifies which database it serves.
	serviceIni := filepath.Join(t.TempDir(), "my.ini")
	contents := "[mysqld]\ndatadir=C:/ProgramData/MariaDB/MariaDB 11.4/data\nport=3306\n"
	if err := os.WriteFile(serviceIni, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	match := findConfigByDataDir(serviceIni)
	if match == nil {
		t.Fatal("expected the service config to match a known configuration")
	}
	if match.Name != "internal" {
		t.Errorf("expected 'internal', got %q", match.Name)
	}

	// A data directory nobody manages must not be forced onto a configuration.
	otherIni := filepath.Join(t.TempDir(), "other.ini")
	if err := os.WriteFile(otherIni, []byte("[mysqld]\ndatadir=E:/elsewhere/data\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if match := findConfigByDataDir(otherIni); match != nil {
		t.Errorf("expected no match, got %q", match.Name)
	}

	if match := findConfigByDataDir(filepath.Join(t.TempDir(), "missing.ini")); match != nil {
		t.Errorf("a missing file must not match, got %q", match.Name)
	}
}
