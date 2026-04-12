//go:build !darwin

package main

func checkFullDiskAccess() bool { return true }

func openFullDiskAccessSettings() error { return nil }
