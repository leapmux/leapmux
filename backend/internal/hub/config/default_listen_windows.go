//go:build windows

package config

import "github.com/leapmux/leapmux/locallisten"

// defaultLocalListen returns the default per-user named-pipe URL on Windows.
func defaultLocalListen(dataDir string) (string, error) {
	return locallisten.DefaultHubAddress(dataDir)
}
