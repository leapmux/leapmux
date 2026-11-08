package servicelaunch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
)

type credentialProvider func(context.Context, string) ([]byte, error)

// windowsLogonName supplies the separate fields LogonUserW requires.
// UPNs use a null domain; unqualified accounts use the local database.
func windowsLogonName(account string) (username, domain string, err error) {
	if account == "" || strings.ContainsRune(account, 0) {
		return "", "", fmt.Errorf("the Windows account name is invalid")
	}
	if domain, username, qualified := strings.Cut(account, `\`); qualified {
		if domain == "" || username == "" || strings.Contains(username, `\`) {
			return "", "", fmt.Errorf("the Windows account name is invalid")
		}
		return username, domain, nil
	}
	if strings.Contains(account, "@") {
		return account, "", nil
	}
	return account, ".", nil
}

func withAccountPassword(ctx context.Context, account string, provider credentialProvider, use func([]byte) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if provider == nil || use == nil {
		return fmt.Errorf("native credential setup requires its credential provider and installer")
	}
	password, err := provider(ctx, account)
	if err != nil {
		clear(password)
		return err
	}
	defer clear(password)
	if len(password) == 0 {
		return fmt.Errorf("the account password is required for noninteractive startup")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(password)
}

func windowsCallFailure(err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return syscall.Errno(31)
	}
	return err
}

func credentialErrorCode(err error) uint32 {
	var native syscall.Errno
	if errors.As(err, &native) {
		return uint32(native)
	}
	var coded interface{ ErrorCode() uintptr }
	if errors.As(err, &coded) {
		return uint32(coded.ErrorCode())
	}
	var com interface{ Code() uintptr }
	if errors.As(err, &com) {
		code := uint32(com.Code())
		if code == 0x80020009 {
			var exception interface{ SubError() error }
			if errors.As(err, &exception) {
				var native interface{ SCODE() uint32 }
				if errors.As(exception.SubError(), &native) && native.SCODE()&0x80000000 != 0 {
					code = native.SCODE()
				}
			}
		}
		if code&0xffff0000 == 0x80070000 && code&0xffff != 0 {
			return code & 0xffff
		}
		return code
	}
	return 0
}

func credentialErrorClass(err error) string {
	switch credentialErrorCode(err) {
	case 1326:
		return "the password is wrong"
	case 1330:
		return "the password has expired"
	case 1907:
		return "Windows requires the account password to be changed before startup can be configured"
	case 1331:
		return "the account is disabled"
	case 1385:
		return "the account lacks the logon right required for noninteractive startup"
	default:
		return "Windows refused the account credential"
	}
}
