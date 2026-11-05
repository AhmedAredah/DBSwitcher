//go:build !windows

package core

// queryServerServices finds nothing: Windows services exist only on Windows.
func queryServerServices() ([]ServiceInfo, error) { return nil, nil }
