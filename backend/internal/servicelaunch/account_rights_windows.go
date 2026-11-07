//go:build windows

package servicelaunch

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var accountSecurityDLL = windows.NewLazySystemDLL("advapi32.dll")
var openAccountPolicy = accountSecurityDLL.NewProc("LsaOpenPolicy")
var addAccountRight = accountSecurityDLL.NewProc("LsaAddAccountRights")
var closeAccountPolicy = accountSecurityDLL.NewProc("LsaClose")
var logonAccount = accountSecurityDLL.NewProc("LogonUserW")

func grantWindowsLogonRight(spec LaunchSpec, right string) (result error) {
	if right != "SeServiceLogonRight" && right != "SeBatchLogonRight" {
		return fmt.Errorf("the requested account right is not supported")
	}
	if err := verifyWindowsAccount(spec); err != nil {
		return err
	}
	sid, _, _, err := windows.LookupSID("", spec.AccountName)
	if err != nil {
		return err
	}
	var attributes windows.OBJECT_ATTRIBUTES
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var policy windows.Handle
	status, _, _ := openAccountPolicy.Call(0, uintptr(unsafe.Pointer(&attributes)), 0x800|0x10, uintptr(unsafe.Pointer(&policy)))
	if status != 0 {
		return windows.NTStatus(status).Errno()
	}
	defer func() {
		status, _, _ := closeAccountPolicy.Call(uintptr(policy))
		if status != 0 {
			result = errors.Join(result, windows.NTStatus(status).Errno())
		}
	}()
	value, err := windows.NewNTUnicodeString(right)
	if err != nil {
		return err
	}
	status, _, _ = addAccountRight.Call(uintptr(policy), uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(value)), 1)
	if status != 0 {
		return windows.NTStatus(status).Errno()
	}
	return nil
}

func validateWindowsLogon(spec LaunchSpec, password []byte, kind uint32) (result error) {
	if len(password) == 0 {
		return fmt.Errorf("the selected account password is required")
	}
	username, domain, err := windowsLogonName(spec.AccountName)
	if err != nil {
		return err
	}
	account, err := windows.UTF16FromString(username)
	if err != nil {
		return err
	}
	var domainPointer *uint16
	if domain != "" {
		domainPointer, err = windows.UTF16PtrFromString(domain)
		if err != nil {
			return err
		}
	}
	secret, err := windows.UTF16FromString(string(password))
	if err != nil {
		return err
	}
	defer clear(secret)
	var token windows.Token
	success, _, nativeErr := logonAccount.Call(uintptr(unsafe.Pointer(&account[0])), uintptr(unsafe.Pointer(domainPointer)), uintptr(unsafe.Pointer(&secret[0])), uintptr(kind), 0, uintptr(unsafe.Pointer(&token)))
	if success == 0 {
		return windowsCallFailure(nativeErr)
	}
	defer func() { result = errors.Join(result, token.Close()) }()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.String() != spec.AccountID {
		return fmt.Errorf("the credential authenticated a different account")
	}
	return nil
}
