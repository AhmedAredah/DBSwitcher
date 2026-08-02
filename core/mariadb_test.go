package core

import "testing"

func TestParseProcessJSONSingleObject(t *testing.T) {
	// ConvertTo-Json emits a bare object when exactly one process matches.
	output := `{"ProcessId":33856,"CommandLine":"\"C:\\Program Files\\MariaDB 11.4\\bin\\mysqld.exe\" --defaults-file=C:\\configs\\external.ini --console"}`

	procs, err := parseProcessJSON(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("expected 1 process, got %d", len(procs))
	}
	if procs[0].PID != 33856 {
		t.Errorf("expected PID 33856, got %d", procs[0].PID)
	}
	if got := extractConfigFromCmdLine(procs[0].CommandLine); got != `C:\configs\external.ini` {
		t.Errorf("expected the config file to be extracted, got %q", got)
	}
}

func TestParseProcessJSONArray(t *testing.T) {
	output := `[{"ProcessId":1,"CommandLine":"mysqld.exe --a"},{"ProcessId":2,"CommandLine":"mariadbd.exe --b"}]`

	procs, err := parseProcessJSON(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("expected 2 processes, got %d", len(procs))
	}
}

func TestParseProcessJSONEmptyMeansNoProcess(t *testing.T) {
	procs, err := parseProcessJSON("   \r\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(procs) != 0 {
		t.Fatalf("expected no processes, got %d", len(procs))
	}
}

func TestParseProcessJSONNullCommandLine(t *testing.T) {
	// CommandLine is null when the process is not readable by this user.
	procs, err := parseProcessJSON(`{"ProcessId":42,"CommandLine":null}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(procs) != 1 || procs[0].PID != 42 {
		t.Fatalf("expected the process to survive a null command line, got %+v", procs)
	}
}

func TestParseProcessJSONMalformedIsAnError(t *testing.T) {
	// A broken query must not look like "nothing is running".
	if _, err := parseProcessJSON("Get-CimInstance : Invalid query"); err == nil {
		t.Fatal("expected an error for unparseable output")
	}
}

func TestMatchesProcessName(t *testing.T) {
	windowsNames := []string{"mysqld.exe", "mariadbd.exe"}
	unixNames := []string{"mysqld", "mariadbd"}

	cases := []struct {
		cmdLine string
		names   []string
		want    bool
	}{
		// The quoted path contains a space, so the executable cannot be taken
		// as "everything up to the first space".
		{`"C:\Program Files\MariaDB 11.4\bin\mysqld.exe" --console`, windowsNames, true},
		{`C:\MariaDB\bin\mariadbd.exe --defaults-file=x.ini`, windowsNames, true},
		{`/usr/sbin/mariadbd --datadir=/var/lib/mysql`, unixNames, true},
		// A client, not a server.
		{`C:\bin\mysqladmin.exe -u root shutdown`, windowsNames, false},
		// Merely mentioning the name is not enough.
		{`notepad.exe mysqld.exe.log`, windowsNames, false},
	}

	for _, tc := range cases {
		if got := matchesProcessName(tc.cmdLine, tc.names); got != tc.want {
			t.Errorf("matchesProcessName(%q) = %v, want %v", tc.cmdLine, got, tc.want)
		}
	}
}

func TestQuoteOptionValue(t *testing.T) {
	cases := map[string]string{
		"simple":    "simple",
		"":          "",
		"has space": `"has space"`,
		"hash#tag":  `"hash#tag"`,
		`back\sl`:   `"back\\sl"`,
		`qu"ote`:    `"qu\"ote"`,
	}

	for input, want := range cases {
		if got := quoteOptionValue(input); got != want {
			t.Errorf("quoteOptionValue(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseMariaDBErrorReportsBindFailure(t *testing.T) {
	// Taken from a real failed start: the old parser returned the first [Note].
	output := `2026-06-30 16:05:36 0 [Note] Starting MariaDB 11.4.8
2026-06-30 16:05:37 0 [Note] Server socket created on IP: '0.0.0.0', port: '3306'.
2026-06-30 16:05:37 0 [ERROR] Can't start server: Bind on TCP/IP port. Got error: 10048
2026-06-30 16:05:37 0 [ERROR] Aborting`

	got := ParseMariaDBError(output)
	if got != "Port already in use - another server still holds it" {
		t.Errorf("unexpected message: %q", got)
	}
}

func TestParseMariaDBErrorPrefersErrorLines(t *testing.T) {
	output := `2026-06-12 15:39:34 0 [Note] InnoDB: Using ThreadPool
2026-06-12 15:39:34 0 [ERROR] Recovery failed! You must enable all engines`

	got := ParseMariaDBError(output)
	if got != "Recovery failed! You must enable all engines" {
		t.Errorf("unexpected message: %q", got)
	}
}

func TestIsCredentialErrorUsesClientOutput(t *testing.T) {
	// The exact shape of the failure that used to abort every switch.
	err := &ClientError{
		Output: "mysqladmin.exe: connect to server at 'localhost' failed\nerror: 'Access denied for user 'moves'@'localhost' (using password: YES)'",
		Err:    errExitStatus1{},
	}

	if !IsCredentialError(err) {
		t.Fatal("an access denied failure must be recognised as a credential error")
	}

	if got := ClientOutput(err); got != "error: 'Access denied for user 'moves'@'localhost' (using password: YES)'" {
		t.Errorf("unexpected client output: %q", got)
	}
}

type errExitStatus1 struct{}

func (errExitStatus1) Error() string { return "exit status 1" }

func TestKeyringAccountFor(t *testing.T) {
	// Every configuration has its own data directory and therefore its own
	// accounts, so each needs its own keyring entry.
	cases := map[string]string{
		"":         "mysql_credentials",
		"external": "mysql_credentials:external",
		"internal": "mysql_credentials:internal",
		// Config names come from file names, which are case-insensitive on
		// Windows; the entry must not depend on how it was typed.
		"External": "mysql_credentials:external",
	}

	for configName, want := range cases {
		if got := KeyringAccountFor(configName); got != want {
			t.Errorf("KeyringAccountFor(%q) = %q, want %q", configName, got, want)
		}
	}

	if KeyringAccountFor("external") == KeyringAccountFor("internal") {
		t.Error("two configurations must not share a keyring entry")
	}
}
