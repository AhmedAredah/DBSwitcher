package core

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
)

// RunElevated runs a command with elevated privileges
func RunElevated(name string, args ...string) error {
	switch runtime.GOOS {
	case "windows":
		// Use PowerShell to run as administrator
		psCmd := fmt.Sprintf("Start-Process '%s' -ArgumentList '%s' -Verb RunAs -Wait",
			name, strings.Join(args, "','"))
		cmd := exec.Command("powershell", "-Command", psCmd)
		return cmd.Run()
	default:
		// Unix systems use sudo
		allArgs := append([]string{name}, args...)
		cmd := exec.Command("sudo", allArgs...)
		return cmd.Run()
	}
}

// RunElevatedPowerShell runs a PowerShell script as administrator, behind a
// UAC prompt, and waits for it to finish. The script's exit code is its
// result: a non-zero code is reported as the Windows error it stands for.
//
// The script travels as -EncodedCommand, so no quoting has to survive the trip
// through Start-Process, whose -ArgumentList splits arguments on spaces.
func RunElevatedPowerShell(script string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("not a Windows host")
	}

	launcher := fmt.Sprintf(`$p = Start-Process -FilePath 'powershell.exe' -Verb RunAs -Wait -PassThru -WindowStyle Hidden `+
		`-ArgumentList '-NoProfile','-NonInteractive','-EncodedCommand','%s'; exit $p.ExitCode`,
		encodePowerShellCommand(script))

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", launcher)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	// The launcher itself failed - most often the UAC prompt was declined.
	if detail := strings.TrimSpace(string(output)); detail != "" {
		return fmt.Errorf("%s", detail)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return fmt.Errorf("%v (error %d)", syscall.Errno(code), code)
	}
	return err
}

// encodePowerShellCommand encodes a script for -EncodedCommand, which takes
// base64 of UTF-16LE text.
func encodePowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(raw[2*i:], unit)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
