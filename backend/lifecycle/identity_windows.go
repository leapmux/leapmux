//go:build windows

package lifecycle

import (
	"golang.org/x/sys/windows"
)

// currentAccount reports the current account's security identifier. An
// empty string (a token the system would not open) fails every
// ownership comparison rather than silently matching.
func currentAccount() string {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return ""
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return ""
	}
	return user.User.Sid.String()
}

// accountNumber is unused on Windows; the SID is the identity.
func accountNumber() int { return 0 }
