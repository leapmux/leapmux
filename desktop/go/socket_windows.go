//go:build windows

package main

import "fmt"

func RunSocketServer(socketPath string, binaryHash string) error {
	_ = socketPath
	_ = binaryHash
	return fmt.Errorf("dev socket mode is not implemented on windows")
}
