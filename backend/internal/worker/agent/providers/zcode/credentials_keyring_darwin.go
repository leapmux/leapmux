//go:build darwin

package zcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	darwinSecurityErrItemNotFound = 44

	darwinErrSecSuccess      int32  = 0
	darwinErrSecItemNotFound int32  = -25300
	darwinSecUnlockStateBit  uint32 = 0x1
	darwinCFStringUTF8       uint32 = 0x08000100
	darwinMaxCFBytes         int64  = 1 << 16
)

type darwinSecurityFramework struct {
	err error

	secKeychainSetUserInteractionAllowed func(state uint8) int32
	secKeychainFindGenericPassword       func(
		keychainOrArray uintptr,
		serviceNameLength uint32, serviceName *byte,
		accountNameLength uint32, accountName *byte,
		passwordLength *uint32, passwordData *uintptr,
		itemRef *uintptr,
	) int32
	secKeychainItemCopyKeychain   func(itemRef uintptr, keychainRef *uintptr) int32
	secKeychainGetStatus          func(keychain uintptr, keychainStatus *uint32) int32
	secKeychainItemCopyAccess     func(itemRef uintptr, access *uintptr) int32
	secAccessCopyACLList          func(access uintptr, aclList *uintptr) int32
	secACLCopyAuthorizations      func(acl uintptr) uintptr
	secACLCopyContents            func(acl uintptr, applicationList *uintptr, description *uintptr, promptSelector *uint16) int32
	secTrustedApplicationCopyData func(appRef uintptr, data *uintptr) int32

	cfRelease                         func(cf uintptr)
	cfArrayGetCount                   func(theArray uintptr) int64
	cfArrayGetValueAtIndex            func(theArray uintptr, idx int64) uintptr
	cfDataGetLength                   func(theData uintptr) int64
	cfDataGetBytePtr                  func(theData uintptr) *byte
	cfStringGetLength                 func(theString uintptr) int64
	cfStringGetMaximumSizeForEncoding func(length int64, encoding uint32) int64
	cfStringGetCString                func(theString uintptr, buffer *byte, bufferSize int64, encoding uint32) uint8
}

var (
	darwinSecFrameworkOnce sync.Once
	darwinSecFramework     darwinSecurityFramework
)

func loadDarwinSecurityFramework() *darwinSecurityFramework {
	darwinSecFrameworkOnce.Do(func() {
		secHandle, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinSecFramework.err = err
			return
		}
		cfHandle, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinSecFramework.err = err
			return
		}
		bindings := []struct {
			fnPtr  any
			handle uintptr
			name   string
		}{
			{&darwinSecFramework.secKeychainSetUserInteractionAllowed, secHandle, "SecKeychainSetUserInteractionAllowed"},
			{&darwinSecFramework.secKeychainFindGenericPassword, secHandle, "SecKeychainFindGenericPassword"},
			{&darwinSecFramework.secKeychainItemCopyKeychain, secHandle, "SecKeychainItemCopyKeychain"},
			{&darwinSecFramework.secKeychainGetStatus, secHandle, "SecKeychainGetStatus"},
			{&darwinSecFramework.secKeychainItemCopyAccess, secHandle, "SecKeychainItemCopyAccess"},
			{&darwinSecFramework.secAccessCopyACLList, secHandle, "SecAccessCopyACLList"},
			{&darwinSecFramework.secACLCopyAuthorizations, secHandle, "SecACLCopyAuthorizations"},
			{&darwinSecFramework.secACLCopyContents, secHandle, "SecACLCopyContents"},
			{&darwinSecFramework.secTrustedApplicationCopyData, secHandle, "SecTrustedApplicationCopyData"},
			{&darwinSecFramework.cfRelease, cfHandle, "CFRelease"},
			{&darwinSecFramework.cfArrayGetCount, cfHandle, "CFArrayGetCount"},
			{&darwinSecFramework.cfArrayGetValueAtIndex, cfHandle, "CFArrayGetValueAtIndex"},
			{&darwinSecFramework.cfDataGetLength, cfHandle, "CFDataGetLength"},
			{&darwinSecFramework.cfDataGetBytePtr, cfHandle, "CFDataGetBytePtr"},
			{&darwinSecFramework.cfStringGetLength, cfHandle, "CFStringGetLength"},
			{&darwinSecFramework.cfStringGetMaximumSizeForEncoding, cfHandle, "CFStringGetMaximumSizeForEncoding"},
			{&darwinSecFramework.cfStringGetCString, cfHandle, "CFStringGetCString"},
		}
		for _, b := range bindings {
			sym, err := purego.Dlsym(b.handle, b.name)
			if err != nil {
				darwinSecFramework.err = err
				return
			}
			purego.RegisterFunc(b.fnPtr, sym)
		}
		// Disable in-process Security.framework UI prompts permanently for this
		// process. Interactive Keychain reads run out-of-process via
		// /usr/bin/security, and nothing else in LeapMux calls Security.framework.
		if status := darwinSecFramework.secKeychainSetUserInteractionAllowed(0); status != darwinErrSecSuccess {
			darwinSecFramework.err = fmt.Errorf("SecKeychainSetUserInteractionAllowed failed (%d)", status)
			return
		}
	})
	return &darwinSecFramework
}

func (f *darwinSecurityFramework) release(ref uintptr) {
	if ref != 0 {
		f.cfRelease(ref)
	}
}

func (f *darwinSecurityFramework) cfString(ref uintptr) (string, bool) {
	if ref == 0 {
		return "", false
	}
	n := f.cfStringGetLength(ref)
	if n < 0 {
		return "", false
	}
	maxSize := f.cfStringGetMaximumSizeForEncoding(n, darwinCFStringUTF8) + 1
	if maxSize <= 0 || maxSize > darwinMaxCFBytes {
		return "", false
	}
	buf := make([]byte, maxSize)
	if f.cfStringGetCString(ref, &buf[0], maxSize, darwinCFStringUTF8) == 0 {
		return "", false
	}
	return zcodeParseDarwinTrustedAppPath(buf), true
}

func (f *darwinSecurityFramework) readACL(aclRef uintptr) (zcodeDarwinACLInfo, bool) {
	var info zcodeDarwinACLInfo
	if aclRef == 0 {
		return info, false
	}
	authsRef := f.secACLCopyAuthorizations(aclRef)
	defer f.release(authsRef)
	if authsRef != 0 {
		count := f.cfArrayGetCount(authsRef)
		for i := int64(0); i < count; i++ {
			if auth, ok := f.cfString(f.cfArrayGetValueAtIndex(authsRef, i)); ok {
				info.authorizations = append(info.authorizations, auth)
			}
		}
	}
	// Skip SecACLCopyContents for non-decrypt auxiliary ACLs (such as
	// PartitionID or Integrity), which may be non-simple and return
	// errSecACLNotSimple without affecting decryption access.
	if !zcodeHasDarwinDecryptAuthorization(info.authorizations) {
		return info, true
	}

	var appListRef, descRef uintptr
	var promptSelector uint16
	if status := f.secACLCopyContents(aclRef, &appListRef, &descRef, &promptSelector); status != darwinErrSecSuccess {
		return info, false
	}
	defer f.release(appListRef)
	defer f.release(descRef)

	info.promptSelector = promptSelector
	if appListRef == 0 {
		info.allowAllApps = true
		return info, true
	}
	appCount := f.cfArrayGetCount(appListRef)
	for i := int64(0); i < appCount; i++ {
		appRef := f.cfArrayGetValueAtIndex(appListRef, i)
		if appRef == 0 {
			continue
		}
		var dataRef uintptr
		if status := f.secTrustedApplicationCopyData(appRef, &dataRef); status != darwinErrSecSuccess || dataRef == 0 {
			f.release(dataRef)
			continue
		}
		length := f.cfDataGetLength(dataRef)
		if length > 0 && length <= darwinMaxCFBytes {
			if ptr := f.cfDataGetBytePtr(dataRef); ptr != nil {
				path := zcodeParseDarwinTrustedAppPath(unsafe.Slice(ptr, int(length)))
				if path != "" {
					info.trustedAppPaths = append(info.trustedAppPaths, path)
				}
			}
		}
		f.release(dataRef)
	}
	return info, true
}

// probeZCodeOSKeyAccess silently checks whether /usr/bin/security can read
// (service, account) from the macOS login Keychain without prompting the user.
// Any load error, missing symbol, locked keychain, or unexpected OSStatus fails
// safe to zcodeOSKeyAccessMayPrompt.
func probeZCodeOSKeyAccess(service, account string) zcodeOSKeyAccessState {
	if service == "" || account == "" {
		return zcodeOSKeyAccessSilent
	}
	f := loadDarwinSecurityFramework()
	if f.err != nil {
		return zcodeOSKeyAccessMayPrompt
	}

	svcBytes := []byte(service)
	acctBytes := []byte(account)
	var itemRef uintptr
	status := f.secKeychainFindGenericPassword(
		0,
		uint32(len(svcBytes)), &svcBytes[0],
		uint32(len(acctBytes)), &acctBytes[0],
		nil, nil,
		&itemRef,
	)
	runtime.KeepAlive(svcBytes)
	runtime.KeepAlive(acctBytes)
	defer f.release(itemRef)
	if status == darwinErrSecItemNotFound {
		return zcodeOSKeyAccessNotFound
	}
	if status != darwinErrSecSuccess || itemRef == 0 {
		return zcodeOSKeyAccessMayPrompt
	}

	var keychainRef uintptr
	if status := f.secKeychainItemCopyKeychain(itemRef, &keychainRef); status != darwinErrSecSuccess || keychainRef == 0 {
		f.release(keychainRef)
		return zcodeOSKeyAccessMayPrompt
	}
	defer f.release(keychainRef)

	var kcStatus uint32
	if status := f.secKeychainGetStatus(keychainRef, &kcStatus); status != darwinErrSecSuccess {
		return zcodeOSKeyAccessMayPrompt
	}
	// Check unlock state BEFORE calling SecKeychainItemCopyAccess, because
	// copying the ACL of an item in a locked keychain tries to unlock it.
	if kcStatus&darwinSecUnlockStateBit == 0 {
		return zcodeOSKeyAccessMayPrompt
	}

	var accessRef uintptr
	if status := f.secKeychainItemCopyAccess(itemRef, &accessRef); status != darwinErrSecSuccess || accessRef == 0 {
		f.release(accessRef)
		return zcodeOSKeyAccessMayPrompt
	}
	defer f.release(accessRef)

	var aclListRef uintptr
	if status := f.secAccessCopyACLList(accessRef, &aclListRef); status != darwinErrSecSuccess || aclListRef == 0 {
		f.release(aclListRef)
		return zcodeOSKeyAccessMayPrompt
	}
	defer f.release(aclListRef)

	count := f.cfArrayGetCount(aclListRef)
	acls := make([]zcodeDarwinACLInfo, 0, count)
	for i := int64(0); i < count; i++ {
		acl, ok := f.readACL(f.cfArrayGetValueAtIndex(aclListRef, i))
		if !ok {
			return zcodeOSKeyAccessMayPrompt
		}
		acls = append(acls, acl)
	}
	return zcodeEvaluateDarwinKeychainACL(true, acls)
}

// readZCodeOSKey reads ZCode's encryption key from the macOS login Keychain via
// Apple's signed /usr/bin/security CLI so the Keychain ACL entry persists across
// LeapMux updates and requires no cgo.
func readZCodeOSKey(ctx context.Context, service, account string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, darwinSecurityBinaryPath, "find-generic-password", "-s", service, "-a", account, "-w")
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		clear(stdout.Bytes())
		return "", false, ctx.Err()
	}
	if err != nil {
		clear(stdout.Bytes())
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == darwinSecurityErrItemNotFound {
				return "", false, nil
			}
			return "", false, fmt.Errorf("read macOS Keychain item for ZCode credentials (exit %d)", exitErr.ExitCode())
		}
		return "", false, fmt.Errorf("run /usr/bin/security for ZCode credentials: %w", err)
	}
	value := strings.TrimRight(stdout.String(), "\r\n")
	clear(stdout.Bytes())
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}
