//go:build unix

package config

import "github.com/leapmux/leapmux/locallisten"

// defaultLocalListen returns the default local-listen URL on Unix:
// "hub.sock" inside the configured data directory.
func defaultLocalListen(dataDir string) (string, error) {
	return locallisten.DefaultHubAddress(dataDir)
}
