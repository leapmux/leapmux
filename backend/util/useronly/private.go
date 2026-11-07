package useronly

import (
	"fmt"
	"io"
	"os"
)

const maxPrivateRecordBytes = 16 << 20

// ReadFile reads an account-private regular file through a verified handle.
func ReadFile(path string) ([]byte, error) {
	file, err := OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxPrivateRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPrivateRecordBytes {
		return nil, fmt.Errorf("private record %s exceeds %d bytes", path, maxPrivateRecordBytes)
	}
	return data, nil
}

// VerifyFile rejects a linked, foreign-owned, or publicly accessible record.
func VerifyFile(path string) error {
	file, err := OpenFile(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func regularFile(file *os.File, path string) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular private file", path)
	}
	return info, nil
}
