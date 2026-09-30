package sessionstore

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// RegularFileRoot checks and opens a path relative to one archive root.
// Production callers use os.Root. A test can wrap that root to change a path
// between Lstat and Open.
type RegularFileRoot interface {
	Lstat(string) (os.FileInfo, error)
	Open(string) (*os.File, error)
}

// ErrArchiveFileModeOrSize identifies a final file that is not regular or
// exceeds the caller's size limit. A provider can add its native error text.
var ErrArchiveFileModeOrSize = errors.New("archive file mode or size is invalid")

// ReadRegularFile reads one checked file under a caller's archive root.
// Callers own native path validation and any rule about ancestor symlinks.
// The final file must keep its identity between Lstat and Open.
func ReadRegularFile(root RegularFileRoot, path string, limit int64) (data []byte, err error) {
	if limit < 0 || limit == math.MaxInt64 {
		return nil, errors.New("the archive file limit is invalid")
	}
	checked, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if checked.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("the archive file is a symlink, not a regular file within %d bytes", limit)
	}
	if !checked.Mode().IsRegular() || checked.Size() > limit {
		return nil, fmt.Errorf("the archive file is not a regular file within %d bytes: %w", limit, ErrArchiveFileModeOrSize)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			data, err = nil, errors.Join(err, closeErr)
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || opened.Size() > limit || !os.SameFile(checked, opened) {
		return nil, errors.New("the archive file changed before it opened")
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the archive file exceeds %d bytes", limit)
	}
	return data, nil
}
