package providerkit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/util/atomicfile"
)

// JSONLLineReader reads complete lines from a JSONL stream without a fixed
// scanner token limit. Each line returned by ReadLine is freshly allocated and
// has trailing \r and \n bytes stripped.
type JSONLLineReader struct {
	r *bufio.Reader
}

// ReadLine reads the next line from the stream, stripping trailing \r and \n.
// It returns (nil, io.EOF) when no bytes remain. A final line without a
// trailing newline is returned with a nil error; the subsequent call returns
// (nil, io.EOF).
func (lr *JSONLLineReader) ReadLine() ([]byte, error) {
	line, err := lr.r.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return trimJSONLLineEnding(line), nil
		}
		return nil, err
	}
	return trimJSONLLineEnding(line), nil
}

func trimJSONLLineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return line
}

// openRegularFile opens path only after confirming it is a regular file via
// os.Lstat (rejecting symlinks, directories, FIFOs, and devices before opening)
// and re-checking the opened descriptor with f.Stat().
func openRegularFile(path string) (*os.File, error) {
	linfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !linfo.Mode().IsRegular() {
		return nil, fmt.Errorf("source file %q is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	finfo, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !finfo.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("source file %q is not a regular file", path)
	}
	return f, nil
}

// StreamForkJSONL opens srcPath as a regular file, lets rewriteHeader read and
// rewrite leading JSONL line(s) via JSONLLineReader, creates the parent
// directory of dstPath (0o700), and atomically streams the rewritten header
// lines followed by the remaining source bytes verbatim into dstPath via
// atomicfile.WriteStream.
//
// Peak memory stays proportional to the leading header lines plus a single
// streaming buffer rather than scaling with total transcript size. If
// rewriteHeader fails, no destination directory or file is created.
func StreamForkJSONL(
	srcPath string,
	dstPath string,
	mode os.FileMode,
	rewriteHeader func(*JSONLLineReader) ([][]byte, error),
) error {
	if rewriteHeader == nil {
		return errors.New("providerkit: rewriteHeader callback is nil")
	}
	f, err := openRegularFile(srcPath)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	br := bufio.NewReader(f)
	headerLines, err := rewriteHeader(&JSONLLineReader{r: br})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return fmt.Errorf("create destination directory for %q: %w", dstPath, err)
	}

	return atomicfile.WriteStream(dstPath, mode, func(w io.Writer) error {
		if len(headerLines) > 0 {
			var headerBuf bytes.Buffer
			for _, line := range headerLines {
				headerBuf.Grow(len(line) + 1)
				headerBuf.Write(line)
				headerBuf.WriteByte('\n')
			}
			if _, err := w.Write(headerBuf.Bytes()); err != nil {
				return err
			}
		}
		if _, err := io.Copy(w, br); err != nil {
			return err
		}
		closed = true
		return f.Close()
	})
}

// StreamCopyRegularFile opens srcPath as a regular file, creates the parent
// directory of dstPath (0o700), and atomically streams srcPath verbatim into
// dstPath via atomicfile.WriteStream and io.Copy.
func StreamCopyRegularFile(srcPath, dstPath string, mode os.FileMode) error {
	f, err := openRegularFile(srcPath)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return fmt.Errorf("create destination directory for %q: %w", dstPath, err)
	}

	return atomicfile.WriteStream(dstPath, mode, func(w io.Writer) error {
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		closed = true
		return f.Close()
	})
}
