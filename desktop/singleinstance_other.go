//go:build !linux && !windows

package main

func acquireSingleInstance(_ func()) (func(), error) {
	return func() {}, nil
}
