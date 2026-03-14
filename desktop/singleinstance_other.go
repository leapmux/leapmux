//go:build !linux && !windows

package main

import "errors"

var errAlreadyRunning = errors.New("another instance is already running")

func acquireSingleInstance(_ func()) (func(), error) {
	return func() {}, nil
}
