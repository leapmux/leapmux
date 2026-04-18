//go:build unix

package config

import "path/filepath"

// defaultLocalListen returns the default local-listen URL on Unix:
// "hub.sock" inside the configured data directory.
func defaultLocalListen(dataDir string) (string, error) {
	return "unix:" + filepath.Join(dataDir, "hub.sock"), nil
}
