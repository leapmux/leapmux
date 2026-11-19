//go:build unix

package atomicfile

import "os"

func createTemporaryFile(dir, pattern string, _ os.FileMode) (*os.File, error) {
	return os.CreateTemp(dir, pattern)
}
