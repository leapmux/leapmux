//go:build windows

package atomicfile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const publicationWaitMilliseconds = 5000

var waitPublicationMutex = windows.WaitForSingleObject

func publicationMutexName(account, directory string) string {
	sum := sha256.Sum256([]byte(account + "\x00" + strings.ToLower(directory)))
	return fmt.Sprintf(`Global\LeapMux.DirectoryPublication.%x`, sum)
}

// withPublicationMutex coordinates interactive commands and services across
// Windows sessions. Global mutexes need no global-file-mapping privilege.
// A crashed owner releases the mutex, but its prepared receipt stays uncertain.
func withPublicationMutex(directory string, run func() error) (result error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	account := currentAccountID()
	if account == "" {
		return fmt.Errorf("atomicfile: the publication account could not be identified")
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sG:%sD:P(A;;GA;;;%s)", account, account, account))
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor,
	}
	name, err := windows.UTF16PtrFromString(publicationMutexName(account, directory))
	if err != nil {
		return err
	}
	handle, err := windows.CreateMutexEx(&attributes, name, 0, windows.MUTEX_ALL_ACCESS)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("atomicfile: open the publication mutex: %w", err)
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(handle)) }()
	if err := verifyPublicationMutex(handle, account); err != nil {
		return err
	}
	state, err := waitPublicationMutex(handle, publicationWaitMilliseconds)
	if err != nil {
		return fmt.Errorf("atomicfile: wait for directory publication: %w", err)
	}
	switch state {
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
		defer func() { result = errors.Join(result, windows.ReleaseMutex(handle)) }()
		return run()
	case uint32(windows.WAIT_TIMEOUT):
		return fmt.Errorf("atomicfile: timed out waiting for active directory publication: %s", directory)
	default:
		return fmt.Errorf("atomicfile: directory publication returned unexpected wait state %d", state)
	}
}

func verifyPublicationMutex(handle windows.Handle, account string) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	sid, err := windows.StringToSid(account)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(sid) || acl == nil || acl.AceCount != 1 {
		return fmt.Errorf("atomicfile: the publication mutex is not private to this account")
	}
	var entry *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &entry); err != nil {
		return err
	}
	principal := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
	if entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !principal.Equals(sid) {
		return fmt.Errorf("atomicfile: the publication mutex grants access outside this account")
	}
	return nil
}
