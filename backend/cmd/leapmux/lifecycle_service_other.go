//go:build !windows

package main

func runNativeServiceEntry([]string) (bool, error) { return false, nil }
