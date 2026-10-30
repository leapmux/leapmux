//go:build !windows

package procutil

// CheckNoJob succeeds on platforms without Windows Jobs.
func CheckNoJob() error { return nil }
