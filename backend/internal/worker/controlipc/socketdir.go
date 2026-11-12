package controlipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

// SweepSocketDir removes leftover control sockets while the Worker holds its
// data-directory lease. Regular files and symlinks remain untouched.
func SweepSocketDir(dir string) error {
	if err := atomicfile.MkdirAllPrivate(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sock") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect control socket %s: %w", path, err)
		}
		if info.Mode()&os.ModeSocket != 0 {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove control socket %s: %w", path, err)
			}
		}
	}
	return nil
}
