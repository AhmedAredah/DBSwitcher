//go:build windows

package core

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// queryServerServices lists the services that run a database server, straight
// from the service control manager. It asks only for the rights an ordinary
// user has - to enumerate services and to read their configuration - so it
// needs no elevation.
func queryServerServices() ([]ServiceInfo, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil, fmt.Errorf("cannot open the service control manager: %v", err)
	}
	defer windows.CloseServiceHandle(scm)

	statuses, err := enumerateServices(scm)
	if err != nil {
		return nil, err
	}

	var services []ServiceInfo
	for _, status := range statuses {
		binaryPath, startType, err := queryServiceConfig(scm, status.name)
		if err != nil {
			// Some services refuse ordinary users even this; none of ours do.
			AppLogger.Debug("Cannot read the configuration of service %s: %v", status.name, err)
			continue
		}
		if !isServerBinary(binaryPath) {
			continue
		}
		services = append(services, newServiceInfo(status.name, status.displayName,
			status.state, startType, status.pid, binaryPath))
	}

	return services, nil
}

// serviceStatus is one entry of the service list, copied out of the buffer the
// service control manager filled.
type serviceStatus struct {
	name        string
	displayName string
	state       uint32
	pid         int
}

// enumerateServices lists every Win32 service with its state and process.
func enumerateServices(scm windows.Handle) ([]serviceStatus, error) {
	var buf []byte
	for {
		var needed, returned, resume uint32
		var first *byte
		if len(buf) > 0 {
			first = &buf[0]
		}

		err := windows.EnumServicesStatusEx(scm, windows.SC_ENUM_PROCESS_INFO, windows.SERVICE_WIN32,
			windows.SERVICE_STATE_ALL, first, uint32(len(buf)), &needed, &returned, &resume, nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) && needed > uint32(len(buf)) {
			// Start over with room for the whole list; services may be
			// added in between, hence the margin.
			buf = make([]byte, needed+4096)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot list services: %v", err)
		}
		if returned == 0 {
			return nil, nil
		}

		entries := unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0])), returned)
		statuses := make([]serviceStatus, 0, len(entries))
		for _, entry := range entries {
			statuses = append(statuses, serviceStatus{
				name:        windows.UTF16PtrToString(entry.ServiceName),
				displayName: windows.UTF16PtrToString(entry.DisplayName),
				state:       entry.ServiceStatusProcess.CurrentState,
				pid:         int(entry.ServiceStatusProcess.ProcessId),
			})
		}
		return statuses, nil
	}
}

// queryServiceConfig returns the command line a service launches and its start
// type.
func queryServiceConfig(scm windows.Handle, name string) (string, uint32, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", 0, err
	}

	service, err := windows.OpenService(scm, namePtr, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return "", 0, err
	}
	defer windows.CloseServiceHandle(service)

	var needed uint32
	err = windows.QueryServiceConfig(service, nil, 0, &needed)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		if err == nil {
			err = fmt.Errorf("no configuration returned")
		}
		return "", 0, err
	}

	buf := make([]byte, needed)
	config := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if err := windows.QueryServiceConfig(service, config, needed, &needed); err != nil {
		return "", 0, err
	}

	return windows.UTF16PtrToString(config.BinaryPathName), config.StartType, nil
}
