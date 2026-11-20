// Package machinelabel supplies a machine label for a credential. The Connected
// apps list and the approval page display this label.
package machinelabel

import "os"

// InstallationName is the label one machine records on a grant: the account
// and the host it runs on.
func InstallationName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	if user == "" {
		return host
	}
	return user + "@" + host
}
