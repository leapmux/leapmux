//go:build windows

package atomicfile

import (
	"os"

	"github.com/leapmux/leapmux/util/useronly"
)

func createTemporaryFile(dir, pattern string, mode os.FileMode) (*os.File, error) {
	if mode.Perm()&0o077 == 0 {
		return useronly.CreateTemp(dir, pattern)
	}
	return os.CreateTemp(dir, pattern)
}
