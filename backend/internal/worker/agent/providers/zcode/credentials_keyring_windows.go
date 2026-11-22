//go:build windows

package zcode

import (
	"context"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsCredTypeGeneric      = 1
	windowsCredBlobMaxSizeBytes = 4096
)

type windowsCredential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             windows.Filetime
	BlobSize                uint32
	Blob                    *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, Username   *uint16
}

var (
	zcodeCredAdvapi32 = windows.NewLazySystemDLL("advapi32.dll")
	zcodeCredReadW    = zcodeCredAdvapi32.NewProc("CredReadW")
	zcodeCredFree     = zcodeCredAdvapi32.NewProc("CredFree")
)

// readZCodeOSKey reads ZCode's encryption key from Windows Credential Manager
// using the "{account}.{service}" target name convention of @napi-rs/keyring.
func readZCodeOSKey(ctx context.Context, service, account string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	target, err := windows.UTF16PtrFromString(account + "." + service)
	if err != nil {
		return "", false, fmt.Errorf("encode Windows credential target name: %w", err)
	}
	var cred *windowsCredential
	ok, _, nativeErr := zcodeCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		windowsCredTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	runtime.KeepAlive(target)
	if ok == 0 {
		if nativeErr == windows.ERROR_NOT_FOUND {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read Windows Credential Manager item for ZCode credentials: %w", nativeErr)
	}
	if cred == nil {
		return "", false, nil
	}
	defer func() {
		_, _, _ = zcodeCredFree.Call(uintptr(unsafe.Pointer(cred)))
	}()

	if cred.BlobSize == 0 || cred.Blob == nil {
		return "", false, nil
	}
	if cred.BlobSize > windowsCredBlobMaxSizeBytes {
		return "", false, fmt.Errorf("ZCode Windows credential blob exceeds %d bytes", windowsCredBlobMaxSizeBytes)
	}
	raw := make([]byte, cred.BlobSize)
	defer clear(raw)
	copy(raw, unsafe.Slice(cred.Blob, cred.BlobSize))
	return zcodeDecodeWindowsCredentialBlob(raw), true, nil
}

func probeZCodeOSKeyAccess(string, string) zcodeOSKeyAccessState {
	return zcodeOSKeyAccessSilent
}
