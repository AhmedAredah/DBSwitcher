package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractConfigFromQuotedCommandLine(t *testing.T) {
	cases := map[string]string{
		// The service control manager quotes each argument whole, and this
		// path contains a space - taking the value up to the first space
		// returned "C:\ProgramData\MariaDB\MariaDB", which exists nowhere.
		`"C:\Program Files\MariaDB\MariaDB 11.4\bin\mysqld.exe" "--defaults-file=C:\ProgramData\MariaDB\MariaDB 11.4\data\my.ini" "MariaDB11_4"`: `C:\ProgramData\MariaDB\MariaDB 11.4\data\my.ini`,

		// Only the value quoted.
		`mysqld.exe --defaults-file="C:\path with space\my.ini" --console`: `C:\path with space\my.ini`,

		// Unquoted, as DBSwitcher starts its own servers.
		`"C:\Program Files\MariaDB\MariaDB 11.4\bin\mysqld.exe" --defaults-file=C:\configs\external.ini --console`: `C:\configs\external.ini`,

		// Separated by a space rather than '='.
		`mysqld --defaults-file /etc/my.cnf`: `/etc/my.cnf`,

		`mysqld --console`: ``,
	}

	for cmdLine, want := range cases {
		if got := extractConfigFromCmdLine(cmdLine); got != want {
			t.Errorf("extractConfigFromCmdLine(%q)\n got  %q\n want %q", cmdLine, got, want)
		}
	}
}

func TestParseServiceJSON(t *testing.T) {
	servicePath := `"C:\Program Files\MariaDB\MariaDB 11.4in\mysqld.exe" "--defaults-file=C:\ProgramData\MariaDB\MariaDB 11.4\data\my.ini" "MariaDB11_4"`
	encodedPath, err := json.Marshal(servicePath)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// A single service comes back as a bare object.
	output := `{"Name":"MariaDB11_4","DisplayName":"MariaDB11_4","State":"Running","StartMode":"Auto","ProcessId":4242,"PathName":` + string(encodedPath) + `}`

	services, err := parseServiceJSON(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(services))
	}

	service := services[0]
	if service.Name != "MariaDB11_4" {
		t.Errorf("unexpected name: %q", service.Name)
	}
	if service.PID != 4242 {
		t.Errorf("unexpected PID: %d", service.PID)
	}
	if !service.IsRunning() {
		t.Error("expected the service to be running")
	}
	if !service.StartsAutomatically() {
		t.Error("expected an automatic start mode")
	}
	if service.ConfigFile != `C:\ProgramData\MariaDB\MariaDB 11.4\data\my.ini` {
		t.Errorf("config file not extracted from the service path: %q", service.ConfigFile)
	}
	if service.Describe() != "MariaDB11_4 (auto start)" {
		t.Errorf("unexpected description: %q", service.Describe())
	}
}

func TestParseServiceJSONArrayAndEmpty(t *testing.T) {
	services, err := parseServiceJSON(`[{"Name":"A","State":"Stopped","StartMode":"Manual","ProcessId":0,"PathName":"mysqld.exe"},{"Name":"B","State":"Running","StartMode":"Auto","ProcessId":7,"PathName":"mariadbd.exe"}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	if services[0].IsRunning() || services[0].StartsAutomatically() {
		t.Error("a stopped manual service must report neither")
	}

	if services, err := parseServiceJSON("  \r\n"); err != nil || len(services) != 0 {
		t.Errorf("no services must parse as empty, got %v / %v", services, err)
	}
}

func TestIsDirEmpty(t *testing.T) {
	// The bug this guards: Readdirnames returns io.EOF for an empty directory,
	// so an empty data directory was reported as populated and never
	// initialised.
	empty := t.TempDir()
	isEmpty, err := IsDirEmpty(empty)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isEmpty {
		t.Error("an empty directory must be reported as empty")
	}

	populated := t.TempDir()
	if err := os.WriteFile(filepath.Join(populated, "ibdata1"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	isEmpty, err = IsDirEmpty(populated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isEmpty {
		t.Error("a directory with a file in it must not be reported as empty")
	}

	if _, err := IsDirEmpty(filepath.Join(empty, "missing")); err == nil {
		t.Error("a missing directory must report an error")
	}
}

func TestEscapeSingleQuotes(t *testing.T) {
	if got := escapeSingleQuotes("MariaDB11_4"); got != "MariaDB11_4" {
		t.Errorf("unexpected: %q", got)
	}
	if got := escapeSingleQuotes("it's"); got != "it''s" {
		t.Errorf("a quote must be doubled for PowerShell, got %q", got)
	}
}

func TestServiceToStart(t *testing.T) {
	AppLogger = &Logger{}

	dir := t.TempDir()

	serviceIni := filepath.Join(dir, "service.ini")
	if err := os.WriteFile(serviceIni, []byte("[mysqld]\ndatadir=C:/ProgramData/MariaDB/MariaDB 11.4/data\nport=3306\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	internal := MariaDBConfig{Name: "internal", DataDir: "C:/ProgramData/MariaDB/MariaDB 11.4/data", Port: "3306"}
	external := MariaDBConfig{Name: "external", DataDir: "D:/MariaDB/data", Port: "3306"}

	auto := ServiceInfo{Name: "MariaDB11_4", State: "Stopped", StartMode: "Auto", ConfigFile: serviceIni}
	disabled := ServiceInfo{Name: "MariaDB11_4", State: "Stopped", StartMode: "Disabled", ConfigFile: serviceIni}

	if service, ok := serviceToStart([]ServiceInfo{auto}, internal); !ok || service.Name != "MariaDB11_4" {
		t.Errorf("a service serving the data directory should be used, got %+v / %v", service, ok)
	}

	// A different data directory is a different database.
	if service, ok := serviceToStart([]ServiceInfo{auto}, external); ok {
		t.Errorf("external must not be started through the service, got %q", service.Name)
	}

	// A disabled service cannot be started, so the standalone path must be used.
	if _, ok := serviceToStart([]ServiceInfo{disabled}, internal); ok {
		t.Error("a disabled service must not be chosen")
	}

	if _, ok := serviceToStart(nil, internal); ok {
		t.Error("no services means no service start")
	}

	// A configuration with no data directory cannot be matched at all.
	if _, ok := serviceToStart([]ServiceInfo{auto}, MariaDBConfig{Name: "nodir"}); ok {
		t.Error("a configuration without a data directory must not match")
	}

	if disabled.IsDisabled() != true || auto.IsDisabled() != false {
		t.Error("IsDisabled must follow the start mode")
	}
}
