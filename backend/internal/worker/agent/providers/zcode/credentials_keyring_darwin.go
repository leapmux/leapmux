//go:build darwin

package zcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const darwinSecurityErrItemNotFound = 44

// readZCodeOSKey reads ZCode's encryption key from the macOS login Keychain via
// Apple's signed /usr/bin/security CLI so the Keychain ACL entry persists across
// LeapMux updates and requires no cgo.
func readZCodeOSKey(ctx context.Context, service, account string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w")
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		clear(stdout.Bytes())
		return "", false, ctx.Err()
	}
	if err != nil {
		clear(stdout.Bytes())
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == darwinSecurityErrItemNotFound {
				return "", false, nil
			}
			return "", false, fmt.Errorf("read macOS Keychain item for ZCode credentials (exit %d)", exitErr.ExitCode())
		}
		return "", false, fmt.Errorf("run /usr/bin/security for ZCode credentials: %w", err)
	}
	value := strings.TrimRight(stdout.String(), "\r\n")
	clear(stdout.Bytes())
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}
