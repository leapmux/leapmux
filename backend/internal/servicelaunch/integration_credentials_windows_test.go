//go:build serviceintegration && windows

package servicelaunch

import (
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

var credentialDLL = windows.NewLazySystemDLL("advapi32.dll")
var readNativeCredential = credentialDLL.NewProc("CredReadW")
var freeNativeCredential = credentialDLL.NewProc("CredFree")

// nativeCredential follows CREDENTIALW. Generic credential entries used by
// the native acceptance fixture contain a UTF-16LE password and account name.
type nativeCredential struct {
	Flags          uint32
	Type           uint32
	TargetName     *uint16
	Comment        *uint16
	LastWritten    windows.Filetime
	BlobSize       uint32
	Blob           *byte
	Persist        uint32
	AttributeCount uint32
	Attributes     uintptr
	TargetAlias    *uint16
	Username       *uint16
}

func passwordFromCredentialStore(reference, accountID string) ([]byte, error) {
	target, err := windows.UTF16PtrFromString(reference)
	if err != nil {
		return nil, err
	}
	var credential *nativeCredential
	success, _, nativeErr := readNativeCredential.Call(uintptr(unsafe.Pointer(target)), 1, 0, uintptr(unsafe.Pointer(&credential)))
	if success == 0 {
		return nil, fmt.Errorf("read the Windows credential store: %w", windowsCallFailure(nativeErr))
	}
	if credential == nil {
		return nil, fmt.Errorf("the Windows credential store returned no credential")
	}
	defer func() { _, _, _ = freeNativeCredential.Call(uintptr(unsafe.Pointer(credential))) }()
	if credential.Type != 1 || credential.Blob == nil || credential.BlobSize == 0 || credential.BlobSize > 2560 || credential.BlobSize%2 != 0 {
		return nil, fmt.Errorf("the stored credential must contain a UTF-16LE password")
	}
	blob := unsafe.Slice(credential.Blob, int(credential.BlobSize))
	defer clear(blob)
	sid, _, _, err := windows.LookupSID("", windows.UTF16PtrToString(credential.Username))
	if err != nil {
		return nil, fmt.Errorf("resolve the stored credential account: %w", err)
	}
	if sid.String() != accountID {
		return nil, fmt.Errorf("the stored credential belongs to another account")
	}
	return decodeStoredPassword(blob)
}
