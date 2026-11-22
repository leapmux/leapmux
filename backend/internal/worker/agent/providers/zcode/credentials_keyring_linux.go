//go:build linux

package zcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// readZCodeOSKey reads ZCode's encryption key from the Linux Freedesktop Secret
// Service using the "service" and "username" attributes written by @napi-rs/keyring.
func readZCodeOSKey(ctx context.Context, service, account string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup", "service", service, "username", account)
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
		if errors.Is(err, exec.ErrNotFound) {
			return "", false, errors.New("secret-tool is not installed; install libsecret-tools or configure ZCODE_CREDENTIAL_SECRET_FILE")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(stderr.String()) == "" {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read Linux Secret Service item for ZCode credentials: %w", err)
	}
	value := strings.TrimRight(stdout.String(), "\r\n")
	clear(stdout.Bytes())
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}
