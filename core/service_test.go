package core

import (
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

func TestNewServiceInfo(t *testing.T) {
	// What the service control manager reports for MariaDB11_4: running
	// (state 4), automatic start (type 2), and a command line quoted whole.
	binaryPath := `"C:\Program Files\MariaDB\MariaDB 11.4\bin\mysqld.exe" "--defaults-file=C:\ProgramData\MariaDB\MariaDB 11.4\data\my.ini" "MariaDB11_4"`

	service := newServiceInfo("MariaDB11_4", "MariaDB11_4", 4, 2, 4242, binaryPath)

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

	// Stopped (1) and manual (3), as service setup leaves it.
	manual := newServiceInfo("MariaDB11_4", "", 1, 3, 0, binaryPath)
	if manual.IsRunning() || manual.StartsAutomatically() || manual.IsDisabled() {
		t.Error("a stopped manual service must report none of running, automatic or disabled")
	}
	if manual.Describe() != "MariaDB11_4 (manual start)" {
		t.Errorf("unexpected description: %q", manual.Describe())
	}

	if !newServiceInfo("Off", "", 1, 4, 0, binaryPath).IsDisabled() {
		t.Error("start type 4 must read as disabled")
	}
}

func TestIsServerBinary(t *testing.T) {
	cases := map[string]bool{
		`"C:\Program Files\MariaDB\MariaDB 11.4\bin\mysqld.exe" "--defaults-file=x"`: true,
		`C:\Program Files\MariaDB 12\bin\MARIADBD.EXE`:                               true,
		`C:\Windows\system32\svchost.exe -k netsvcs`:                                 false,
		``: false,
	}
	for path, want := range cases {
		if got := isServerBinary(path); got != want {
			t.Errorf("isServerBinary(%q) = %t, want %t", path, got, want)
		}
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

func TestGrantServiceControl(t *testing.T) {
	const sid = "S-1-12-1-4088911202-1247884903-469420460-390641804"

	// The permissions MariaDB11_4 is installed with: interactive users may
	// query it, but only SYSTEM and administrators may start or stop it.
	installed := "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"

	granted, changed, err := grantServiceControl(installed, sid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected access to be added")
	}
	want := installed + "(A;;" + serviceControlRights + ";;;" + sid + ")"
	if granted != want {
		t.Errorf("got  %s\nwant %s", granted, want)
	}

	// Running setup again changes nothing.
	again, changed, err := grantServiceControl(granted, sid)
	if err != nil || changed || again != granted {
		t.Errorf("second grant changed the permissions: %q, %v, %v", again, changed, err)
	}

	// The entry goes into the access list, not after the audit list.
	withAudit := "O:SYG:SYD:(A;;CCLCSWLOCRRC;;;IU)S:(AU;FA;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;WD)"
	granted, _, err = grantServiceControl(withAudit, sid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want = "O:SYG:SYD:(A;;CCLCSWLOCRRC;;;IU)(A;;" + serviceControlRights + ";;;" + sid + ")S:(AU;FA;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;WD)"
	if granted != want {
		t.Errorf("got  %s\nwant %s", granted, want)
	}

	// Query rights alone are not start/stop rights.
	queryOnly := "D:(A;;CCLCSWLOCRRC;;;" + sid + ")"
	if _, changed, _ := grantServiceControl(queryOnly, sid); !changed {
		t.Error("query-only access was taken for start/stop access")
	}

	if _, _, err := grantServiceControl("O:SYG:SY", sid); err == nil {
		t.Error("expected an error for permissions without an access list")
	}
}

func TestHasRightReadsTwoLetterCodes(t *testing.T) {
	// "LOCR" contains the letters "OC", which is not one of its codes.
	if hasRight("LOCR", "OC") {
		t.Error("matched a code across a boundary")
	}
	if !hasRight("CCLCSWRPWPLOCRRC", "WP") {
		t.Error("missed WP")
	}
}

func TestEncodePowerShellCommand(t *testing.T) {
	// PowerShell's -EncodedCommand takes base64 of UTF-16LE.
	if got := encodePowerShellCommand("exit 0"); got != "ZQB4AGkAdAAgADAA" {
		t.Errorf("got %q", got)
	}
}
