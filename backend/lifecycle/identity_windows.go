//go:build windows

package lifecycle

import (
	"golang.org/x/sys/windows"
)

// currentAccount reports the current account's security identifier. An
// empty string (a token the system could not query) fails every
// ownership comparison rather than silently matching.
func currentAccount() string {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return ""
	}
	return user.User.Sid.String()
}
