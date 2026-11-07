//go:build windows

package atomicfile

import (
	"os"
	"testing"
)

func observeDurableReplacement(t *testing.T, observe func()) {
	t.Helper()
	original := moveFileExDurable
	moveFileExDurable = func(from, to *uint16, flags uint32) error {
		observe()
		return original(from, to, flags)
	}
	t.Cleanup(func() { moveFileExDurable = original })
}

func injectDurableReplacementFailure(t *testing.T, stage string) {
	t.Helper()
	if stage != "rename" {
		return
	}
	original := moveFileExDurable
	moveFileExDurable = func(_, _ *uint16, _ uint32) error { return os.ErrPermission }
	t.Cleanup(func() { moveFileExDurable = original })
}
