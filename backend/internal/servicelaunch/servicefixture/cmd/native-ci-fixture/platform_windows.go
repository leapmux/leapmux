package main

import (
	"crypto/rand"
	"fmt"
	"os/user"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type credential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             windows.Filetime
	BlobSize                uint32
	Blob                    *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, Username   *uint16
}

var credDLL = windows.NewLazySystemDLL("advapi32.dll")
var credWrite = credDLL.NewProc("CredWriteW")
var credDelete = credDLL.NewProc("CredDeleteW")
var netSet = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserSetInfo")

func prepareCredential(account *user.User, reference string) (string, error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return "", fmt.Errorf("native service CI requires an elevated hosted runner")
	}
	local := account.Username[strings.LastIndex(account.Username, "\\")+1:]
	sid, _, _, err := windows.LookupSID(".", local)
	if err != nil || sid.String() != account.Uid {
		return "", fmt.Errorf("native service CI requires the current local runner account")
	}
	password := make([]byte, 52)
	defer clear(password)
	if _, err := rand.Read(password[:48]); err != nil {
		return "", err
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := range password[:48] {
		password[i] = alphabet[password[i]&63]
	}
	copy(password[48:], []byte("aA1!"))
	encoded := make([]uint16, len(password)+1)
	defer clear(encoded)
	blob := make([]byte, len(password)*2)
	defer clear(blob)
	for i, value := range password {
		encoded[i] = uint16(value)
		blob[i*2] = value
	}
	username, err := windows.UTF16PtrFromString(local)
	if err != nil {
		return "", err
	}
	info := struct{ Password *uint16 }{&encoded[0]}
	status, _, _ := netSet.Call(0, uintptr(unsafe.Pointer(username)), 1003, uintptr(unsafe.Pointer(&info)), 0)
	runtime.KeepAlive(encoded)
	if status != 0 {
		return "", fmt.Errorf("prepare disposable runner password: Windows code %d", status)
	}
	target, err := windows.UTF16PtrFromString(reference)
	if err != nil {
		return "", err
	}
	name, err := windows.UTF16PtrFromString(account.Username)
	if err != nil {
		return "", err
	}
	value := credential{Type: 1, TargetName: target, BlobSize: uint32(len(blob)), Blob: &blob[0], Persist: 1, Username: name}
	ok, _, nativeErr := credWrite.Call(uintptr(unsafe.Pointer(&value)), 0)
	runtime.KeepAlive(value)
	runtime.KeepAlive(blob)
	if ok == 0 {
		return "", fmt.Errorf("store disposable CI credential: %v", nativeErr)
	}
	return reference, nil
}
func cleanupCredential(reference string) error {
	target, err := windows.UTF16PtrFromString(reference)
	if err != nil {
		return err
	}
	ok, _, nativeErr := credDelete.Call(uintptr(unsafe.Pointer(target)), 1, 0)
	if ok == 0 && nativeErr != windows.ERROR_NOT_FOUND {
		return fmt.Errorf("remove CI credential: %v", nativeErr)
	}
	return nil
}
