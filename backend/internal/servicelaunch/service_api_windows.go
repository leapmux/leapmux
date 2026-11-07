//go:build windows

package servicelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const accountServiceAccess = windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS | windows.SERVICE_START | windows.SERVICE_STOP | windows.SERVICE_INTERROGATE | windows.READ_CONTROL

func withWindowsService(spec LaunchSpec, access uint32, use func(*mgr.Service) error) (result error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseServiceHandle(manager)) }()
	name, err := windows.UTF16PtrFromString(serviceName(spec))
	if err != nil {
		return err
	}
	handle, err := windows.OpenService(manager, name, access)
	if err != nil {
		return err
	}
	service := &mgr.Service{Name: serviceName(spec), Handle: handle}
	defer func() { result = errors.Join(result, service.Close()) }()
	return use(service)
}

func inspectWindowsService(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	if err := ctx.Err(); err != nil {
		return NativeStatus{}, err
	}
	var status NativeStatus
	err := withWindowsService(spec, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS|windows.READ_CONTROL, func(service *mgr.Service) error {
		configuration, err := service.Config()
		if err != nil {
			return err
		}
		current, err := service.Query()
		if err != nil {
			return err
		}
		matches, err := windowsServiceMatches(service, configuration, spec)
		if err != nil {
			return err
		}
		status = NativeStatus{Installed: true, DefinitionMatches: matches, PID: int(current.ProcessId)}
		switch current.State {
		case svc.Running:
			status.Running, status.State = true, "running"
		case svc.StartPending:
			status.Running, status.State = true, "start-pending"
		case svc.StopPending:
			status.Running, status.State = true, "stop-pending"
		case svc.Stopped:
			status.State = "stopped"
			if current.Win32ExitCode != 0 || current.ServiceSpecificExitCode != 0 {
				status.State = "failed"
				status.Failure = fmt.Sprintf("the service exited with Windows code %d and service code %d", current.Win32ExitCode, current.ServiceSpecificExitCode)
			}
		default:
			status.Running, status.State = true, "pending"
		}
		return ctx.Err()
	})
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return NativeStatus{State: "not installed"}, nil
	}
	return status, err
}

func windowsServiceMatches(service *mgr.Service, configuration mgr.Config, spec LaunchSpec) (bool, error) {
	arguments, err := windows.DecomposeCommandLine(configuration.BinaryPathName)
	if err != nil {
		return false, err
	}
	if len(arguments) == 0 {
		return false, nil
	}
	account, err := resolveWindowsAccount(configuration.ServiceStartName)
	if err != nil {
		return false, err
	}
	actions, err := service.RecoveryActions()
	if err != nil {
		return false, err
	}
	for _, action := range actions {
		if action.Type != mgr.NoAction {
			return false, nil
		}
	}
	access, err := windowsServiceAccessMatches(service, spec.AccountID)
	if err != nil {
		return false, err
	}
	return access && strings.EqualFold(filepath.Clean(arguments[0]), filepath.Clean(spec.Executable)) &&
		slices.Equal(arguments[1:], windowsServiceArguments(spec)) && account == spec.AccountID && configuration.StartType == mgr.StartAutomatic &&
		configuration.ServiceType == windows.SERVICE_WIN32_OWN_PROCESS && !configuration.DelayedAutoStart, nil
}

func windowsServiceAccessMatches(service *mgr.Service, account string) (bool, error) {
	descriptor, err := windows.GetSecurityInfo(service.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return false, err
	}
	if acl == nil {
		return false, nil
	}
	expected := map[string]windows.ACCESS_MASK{"S-1-5-18": windows.SERVICE_ALL_ACCESS, "S-1-5-32-544": windows.SERVICE_ALL_ACCESS, account: accountServiceAccess}
	if int(acl.AceCount) != len(expected) {
		return false, nil
	}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return false, err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false, nil
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		mask, exists := expected[sid]
		actual := ace.Mask
		if actual&windows.GENERIC_ALL != 0 {
			actual = actual&^windows.GENERIC_ALL | windows.SERVICE_ALL_ACCESS
		}
		if !exists || mask != actual {
			return false, nil
		}
		delete(expected, sid)
	}
	return len(expected) == 0, nil
}

func configureWindowsServiceAccess(service *mgr.Service, account string) error {
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x%x;;;%s)", accountServiceAccess, account))
	if err != nil {
		return err
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(service.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func upsertWindowsService(ctx context.Context, spec LaunchSpec, password []byte) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyWindowsAccount(spec); err != nil {
		return err
	}
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, manager.Disconnect()) }()
	configuration := mgr.Config{ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: mgr.StartAutomatic, ServiceStartName: spec.AccountName, Password: string(password), DisplayName: "LeapMux " + spec.Mode.String()}
	service, err := manager.OpenService(serviceName(spec))
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		service, err = manager.CreateService(serviceName(spec), spec.Executable, configuration, windowsServiceArguments(spec)...)
	} else if err == nil {
		defer func() { result = errors.Join(result, service.Close()) }()
		existing, err := service.Config()
		if err != nil {
			return err
		}
		owner, err := resolveWindowsAccount(existing.ServiceStartName)
		if err != nil {
			return err
		}
		if owner != spec.AccountID {
			return fmt.Errorf("the existing service belongs to another account")
		}
		current, err := service.Query()
		if err != nil {
			return err
		}
		if current.State != svc.Stopped {
			matches, err := windowsServiceMatches(service, existing, spec)
			if err != nil {
				return err
			}
			if !matches {
				return fmt.Errorf("stop the service before changing its configuration")
			}
		}
		configuration.BinaryPathName = windows.ComposeCommandLine(append([]string{spec.Executable}, windowsServiceArguments(spec)...))
		if err := service.UpdateConfig(configuration); err != nil {
			return err
		}
		return finalizeWindowsService(service, spec)
	}
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, service.Close()) }()
	return finalizeWindowsService(service, spec)
}

func finalizeWindowsService(service *mgr.Service, spec LaunchSpec) error {
	// ResetRecoveryActions removes the list. The SDK's SetRecoveryActions
	// indexes its first element even when the caller supplies an empty slice.
	if err := service.ResetRecoveryActions(); err != nil {
		return err
	}
	return configureWindowsServiceAccess(service, spec.AccountID)
}

func startWindowsService(ctx context.Context, spec LaunchSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return withWindowsService(spec, windows.SERVICE_START|windows.SERVICE_QUERY_CONFIG, func(service *mgr.Service) error {
		if err := verifyServiceOwner(service, spec); err != nil {
			return err
		}
		return service.Start()
	})
}

func stopWindowsService(ctx context.Context, spec LaunchSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return withWindowsService(spec, windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG, func(service *mgr.Service) error {
		if err := verifyServiceOwner(service, spec); err != nil {
			return err
		}
		state, err := service.Query()
		if err != nil {
			return err
		}
		if state.State == svc.Stopped {
			return nil
		}
		if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			state, err := service.Query()
			if err != nil {
				return err
			}
			if state.State == svc.Stopped {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	})
}

func removeWindowsService(ctx context.Context, spec LaunchSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := withWindowsService(spec, windows.DELETE|windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS, func(service *mgr.Service) error {
		if err := verifyServiceOwner(service, spec); err != nil {
			return err
		}
		state, err := service.Query()
		if err != nil {
			return err
		}
		if state.State != svc.Stopped {
			return fmt.Errorf("stop the service before removing its startup definition")
		}
		return service.Delete()
	})
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	return err
}

func verifyServiceOwner(service *mgr.Service, spec LaunchSpec) error {
	configuration, err := service.Config()
	if err != nil {
		return err
	}
	account, err := resolveWindowsAccount(configuration.ServiceStartName)
	if err != nil {
		return err
	}
	if account != spec.AccountID {
		return fmt.Errorf("the service belongs to another account")
	}
	return nil
}

// sameWindowsExecutable compares file identity rather than display spelling.
func sameWindowsExecutable(path string) bool {
	current, err := os.Executable()
	if err != nil {
		return false
	}
	expected, err := os.Stat(current)
	if err != nil {
		return false
	}
	actual, err := os.Stat(path)
	return err == nil && os.SameFile(expected, actual)
}
