package locallisten

import "path/filepath"

// HubSocketName is the Unix domain socket file name inside a Hub data directory.
const HubSocketName = "hub.sock"

// DefaultHubSocketPath returns the default Unix domain socket path for a Hub
// inside dataDir.
func DefaultHubSocketPath(dataDir string) string {
	return filepath.Join(dataDir, HubSocketName)
}
