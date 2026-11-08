//go:build windows

package servicelaunch

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// resolveWindowsAccount accepts spellings returned by the Service Control
// Manager and Task Scheduler. A local prefix retains its computer scope.
func resolveWindowsAccount(name string) (string, error) {
	sid, err := windows.StringToSid(name)
	if err == nil {
		return sid.String(), nil
	}
	if strings.EqualFold(name, "LocalSystem") {
		sid, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	} else {
		var computer string
		if strings.HasPrefix(name, `.\`) {
			computer, err = windows.ComputerName()
			if err != nil {
				return "", fmt.Errorf("resolve the local Windows computer: %w", err)
			}
		}
		var lookup string
		lookup, err = windowsAccountLookupName(name, computer)
		if err == nil {
			sid, _, _, err = windows.LookupSID("", lookup)
		}
	}
	if err != nil {
		return "", fmt.Errorf("resolve the Windows account %q: %w", name, err)
	}
	return sid.String(), nil
}

func verifyWindowsAccount(spec LaunchSpec) error {
	sid, _, kind, err := windows.LookupSID("", spec.AccountName)
	if err != nil {
		return err
	}
	if kind != windows.SidTypeUser || sid.String() != spec.AccountID {
		return fmt.Errorf("the account name does not match its saved security identifier")
	}
	return nil
}
