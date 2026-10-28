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
// The final file must keep its identity and complete bytes through the read.
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
	if !checked.Mode().IsRegular() || checked.Size() < 0 || checked.Size() > limit {
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
	if !sameRegularFileSnapshot(checked, opened) || opened.Size() > limit {
		return nil, errors.New("the archive file changed before it opened")
	}
	data, err = readSizedArchiveBytes(file, opened.Size())
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("check the archive file after the read: %w", err)
	}
	if !sameRegularFileSnapshot(opened, after) {
		return nil, errors.New("the archive file changed during the read")
	}
	current, err := root.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("check the archive path after the read: %w", err)
	}
	if !sameRegularFileSnapshot(after, current) {
		return nil, errors.New("the archive path changed during the read")
	}
	return data, nil
}

// Read the checked size plus one sentinel byte. Growth and truncation both fail
// before the caller checks the file and path identities again.
func readSizedArchiveBytes(reader io.Reader, size int64) ([]byte, error) {
	maxInt := int64(^uint(0) >> 1)
	if size < 0 || size >= maxInt {
		return nil, errors.New("the checked archive file size is invalid")
	}
	data := make([]byte, int(size)+1)
	count, err := io.ReadFull(reader, data)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if int64(count) != size {
		return nil, errors.New("the archive file did not supply its complete checked bytes")
	}
	return data[:count:count], nil
}

// Compare the same inode and the metadata that describes its complete content.
func sameRegularFileSnapshot(before, after os.FileInfo) bool {
	return before.Mode().IsRegular() && after.Mode().IsRegular() && os.SameFile(before, after) &&
		before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}
