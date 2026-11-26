//go:build unix

package locallisten

import "fmt"

// DefaultHubAddress returns the default local inter-process communication (IPC)
// URL for a Hub on Unix: "unix:<dataDir>/hub.sock".
func DefaultHubAddress(dataDir string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("data directory is required for local-listen default")
	}
	return string(SchemeUnix) + ":" + DefaultHubSocketPath(dataDir), nil
}
