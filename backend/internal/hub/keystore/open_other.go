//go:build !windows

package keystore

import "os"

func openKeyRing(path string) (*os.File, error) { return os.Open(path) }
