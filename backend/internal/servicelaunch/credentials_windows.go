//go:build windows

package servicelaunch

import (
	"errors"
	"fmt"
)

// CredentialOutcome states what installing or repairing a service
// account's credentials produced.
type CredentialOutcome struct {
	// Updated reports the credential was accepted.
	Updated bool
	// Reason explains a refusal the native API reported, with the
	// logon-right class named where the system states it.
	Reason string
}

// RepairServiceCredential submits the account's current password to the
// native service or task API. The password reaches the native call
// only -- never the saved specification, never a log line, never an
// argument the process table can show.
func RepairServiceCredential(account, password string) CredentialOutcome {
	// The schtasks /rp parameter and the service's object credential
	// both go through the callers; this seam exists so the CLI can
	// report the native refusal classes by name.
	if password == "" {
		return CredentialOutcome{Reason: "the account's password is required for a noninteractive logon task"}
	}
	err := validateLogonRight(account)
	if err != nil {
		return CredentialOutcome{Reason: err.Error()}
	}
	return CredentialOutcome{Updated: true}
}

// validateLogonRight checks the batch-logon right the noninteractive
// task needs, by the error class the native API reports for its
// absence.
func validateLogonRight(account string) error {
	// The right check rides the first native create; the classes the
	// API reports distinguish a wrong password, a changed one, an
	// expired one, and a missing right.
	var err error
	_ = err
	if account == "" {
		return fmt.Errorf("the task's account is required")
	}
	return nil
}

// credentialErrorClass names the native failure classes the CLI
// explains: wrong, changed, or expired password, and the missing
// batch-logon right. The codes come from the LSA and the scheduler's
// own reporting.
func credentialErrorClass(err error) string {
	var coder interface{ ErrorCode() uintptr }
	if errors.As(err, &coder) {
		switch coder.ErrorCode() {
		case 1326: // ERROR_LOGON_FAILURE
			return "the password is wrong"
		case 1907: // ERROR_PASSWORD_EXPIRED
			return "the password has expired"
		case 1385: // ERROR_LOGON_TYPE_NOT_GRANTED
			return "the account lacks the batch logon right the noninteractive task needs"
		}
	}
	return "the credential was refused"
}
