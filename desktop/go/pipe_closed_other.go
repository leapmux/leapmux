//go:build !windows

package main

func isPipeClosed(error) bool { return false }
