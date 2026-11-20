// Package machinelabel names the machine a credential is recorded on: the
// label a grant's Connected-apps list shows and the approval page names.
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
