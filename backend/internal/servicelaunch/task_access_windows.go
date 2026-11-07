//go:build windows

package servicelaunch

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

type taskMethodCaller func(*ole.IDispatch, string, ...interface{}) (*ole.VARIANT, error)

func readTaskSecurityDescriptorWith(task *ole.IDispatch, call taskMethodCaller) (sddl string, result error) {
	value, err := call(task, "GetSecurityDescriptor", int32(windows.DACL_SECURITY_INFORMATION))
	if value != nil {
		defer func() { result = errors.Join(result, value.Clear()) }()
	}
	if err != nil {
		return "", fmt.Errorf("read the scheduled task access permissions: %w", err)
	}
	if value == nil || value.VT != ole.VT_BSTR {
		return "", fmt.Errorf("the Windows call returned an invalid scheduled task security descriptor")
	}
	sddl = value.ToString()
	if sddl == "" {
		return "", fmt.Errorf("the Windows call returned an empty scheduled task security descriptor")
	}
	return sddl, nil
}

func readTaskAccess(task *ole.IDispatch) (windowsTaskAccess, error) {
	sddl, err := readTaskSecurityDescriptorWith(task, oleutil.CallMethod)
	if err != nil {
		return windowsTaskAccess{}, err
	}
	return parseTaskAccess(sddl)
}

func parseTaskAccess(sddl string) (windowsTaskAccess, error) {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return windowsTaskAccess{}, fmt.Errorf("parse the scheduled task security descriptor: %w", err)
	}
	defer runtime.KeepAlive(descriptor)
	control, _, err := descriptor.Control()
	if err != nil {
		return windowsTaskAccess{}, fmt.Errorf("read the scheduled task access control: %w", err)
	}
	access := windowsTaskAccess{
		Present:   control&windows.SE_DACL_PRESENT != 0,
		Protected: control&windows.SE_DACL_PROTECTED != 0,
		Defaulted: control&windows.SE_DACL_DEFAULTED != 0,
	}
	if !access.Present {
		// A valid descriptor with no DACL is known drift, not a query failure.
		return access, nil
	}
	acl, defaulted, err := descriptor.DACL()
	if err != nil {
		return windowsTaskAccess{}, fmt.Errorf("read the scheduled task access list: %w", err)
	}
	access.Defaulted = access.Defaulted || defaulted
	if acl == nil {
		// A null DACL grants unrestricted access.
		return access, nil
	}
	access.Entries = make([]windowsTaskAccessEntry, 0, acl.AceCount)
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var native *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &native); err != nil {
			return windowsTaskAccess{}, fmt.Errorf("read a scheduled task access entry: %w", err)
		}
		if native == nil {
			return windowsTaskAccess{}, fmt.Errorf("the Windows call returned an empty scheduled task access entry")
		}
		entry := windowsTaskAccessEntry{Kind: native.Header.AceType, Flags: native.Header.AceFlags}
		if native.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE {
			// ACCESS_ALLOWED_ACE places a variable-length SID at SidStart.
			const minimumSIDSize = 8
			sidOffset := unsafe.Offsetof(native.SidStart)
			if uintptr(native.Header.AceSize) < sidOffset+minimumSIDSize {
				return windowsTaskAccess{}, fmt.Errorf("the Windows call returned a truncated scheduled task access entry")
			}
			sid := (*windows.SID)(unsafe.Pointer(&native.SidStart))
			if !sid.IsValid() || uintptr(sid.Len()) > uintptr(native.Header.AceSize)-sidOffset {
				return windowsTaskAccess{}, fmt.Errorf("the Windows call returned an invalid scheduled task access account")
			}
			entry.SID = sid.String()
			if entry.SID == "" {
				return windowsTaskAccess{}, fmt.Errorf("the Windows call returned an invalid scheduled task access account")
			}
			entry.Mask = uint32(native.Mask)
		}
		access.Entries = append(access.Entries, entry)
	}
	return access, nil
}
