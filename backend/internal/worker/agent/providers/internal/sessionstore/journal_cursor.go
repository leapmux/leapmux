package sessionstore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
)

// JournalCursor records the byte length and SHA-256 digest of a journal's
// complete newline-terminated prefix, so later reads can verify that the
// prefix stayed unchanged before parsing appended lines.
type JournalCursor struct {
	size int64
	hash [sha256.Size]byte
}

// Size returns the byte length of the complete newline-terminated prefix.
func (c JournalCursor) Size() int64 {
	return c.size
}

// CompleteJournalLines returns the longest prefix of data ending with '\n',
// or an empty slice when data contains no newline.
func CompleteJournalLines(data []byte) []byte {
	return data[:bytes.LastIndexByte(data, '\n')+1]
}

// NewJournalCursor hashes the complete newline-terminated prefix of data.
func NewJournalCursor(data []byte) JournalCursor {
	complete := CompleteJournalLines(data)
	return JournalCursor{
		size: int64(len(complete)),
		hash: sha256.Sum256(complete),
	}
}

// ReadJournalCursor hashes the complete newline-terminated prefix streamed from
// r without buffering the entire file in memory. It returns the cursor, the
// total bytes read from r, and an error if the stream exceeds maxSize (when
// maxSize >= 0) or if r fails.
func ReadJournalCursor(r io.Reader, maxSize int64) (JournalCursor, int64, error) {
	hasher := sha256.New()
	var buf [32 << 10]byte
	var pending []byte
	var totalRead int64
	var completeSize int64
	for {
		n, readErr := r.Read(buf[:])
		if n > 0 {
			totalRead += int64(n)
			if maxSize >= 0 && totalRead > maxSize {
				return JournalCursor{}, totalRead, ErrArchiveFileModeOrSize
			}
			chunk := buf[:n]
			if lastNL := bytes.LastIndexByte(chunk, '\n'); lastNL >= 0 {
				if len(pending) > 0 {
					_, _ = hasher.Write(pending)
					completeSize += int64(len(pending))
					pending = pending[:0]
				}
				_, _ = hasher.Write(chunk[:lastNL+1])
				completeSize += int64(lastNL + 1)
				if lastNL+1 < len(chunk) {
					pending = append(pending, chunk[lastNL+1:]...)
				}
			} else {
				pending = append(pending, chunk...)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return JournalCursor{}, totalRead, readErr
		}
	}
	var sum [sha256.Size]byte
	hasher.Sum(sum[:0])
	return JournalCursor{size: completeSize, hash: sum}, totalRead, nil
}

// AppendedBytes returns the complete newline-terminated lines appended after
// c in data. It returns (nil, false) if data is shorter than c.Size() or if
// the prefix hash differs from c.
func (c JournalCursor) AppendedBytes(data []byte) ([]byte, bool) {
	complete := CompleteJournalLines(data)
	if int64(len(complete)) < c.size || sha256.Sum256(complete[:c.size]) != c.hash {
		return nil, false
	}
	return complete[c.size:], true
}

// VerifyPrefix reads exactly c.Size() bytes from r and reports whether their
// SHA-256 digest matches c. A premature EOF returns (false, nil). Any other
// read error returns (false, err).
func (c JournalCursor) VerifyPrefix(r io.Reader) (bool, error) {
	hasher := sha256.New()
	if _, err := io.CopyN(hasher, r, c.size); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	var sum [sha256.Size]byte
	hasher.Sum(sum[:0])
	return sum == c.hash, nil
}
