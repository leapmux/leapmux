//go:build !darwin && !windows && !linux

package zcode

import (
	"context"
	"errors"
)

func readZCodeOSKey(context.Context, string, string) (string, bool, error) {
	return "", false, errors.New("OS credential storage is not supported on this platform")
}

func probeZCodeOSKeyAccess(string, string) zcodeOSKeyAccessState {
	return zcodeOSKeyAccessSilent
}
