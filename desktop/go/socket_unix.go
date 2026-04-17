//go:build unix

package main

import (
	"os"
	"path/filepath"
)

func prepareEndpoint(socketPath string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return "", err
	}
	return "unix:" + socketPath, nil
}
