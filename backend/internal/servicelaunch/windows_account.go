package servicelaunch

import (
	"fmt"
	"strings"
)

// windowsAccountLookupName expands the Service Control Manager's local account
// prefix before lookup. Domain-qualified names and user principal names keep
// their original scope.
func windowsAccountLookupName(name, computer string) (string, error) {
	username, _, err := windowsLogonName(name)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(name, `.\`) {
		return name, nil
	}
	if computer == "" || strings.ContainsAny(computer, "\\/\x00") {
		return "", fmt.Errorf("the local Windows computer name is required for this account")
	}
	return computer + `\` + username, nil
}
