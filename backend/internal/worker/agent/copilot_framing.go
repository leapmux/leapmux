package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const copilotHeaderLimit = 8192

// frameCopilotJSON uses byte counts, as the native Copilot protocol requires.
func frameCopilotJSON(payload []byte) []byte {
	header := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload)))
	return append(header, payload...)
}

// newCopilotScanner consumes shell lines and native frames through one reader.
// Changing the split state preserves bytes buffered after the shell delimiter.
func newCopilotScanner(reader io.Reader, delimiter string, limit int) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	if limit <= 0 || limit > int(^uint(0)>>1)-copilotHeaderLimit {
		scanner.Split(func([]byte, bool) (int, []byte, error) {
			return 0, nil, errors.New("invalid Copilot message size limit")
		})
		return scanner
	}
	scanner.Buffer(make([]byte, min(stdoutScannerStartBuf, limit+copilotHeaderLimit)), limit+copilotHeaderLimit)
	inPreamble := delimiter != ""
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		maximum := min(limit, liveStdoutMaxTokenSize())
		if inPreamble {
			advance, line, err := bufio.ScanLines(data, atEOF)
			if len(line) > maximum || (advance == 0 && len(data) > maximum) {
				return 0, nil, errors.New("the Copilot preamble exceeds the message size limit")
			}
			if bytes.Equal(bytes.TrimSpace(line), []byte(delimiter)) {
				inPreamble = false
			}
			return advance, line, err
		}
		return splitCopilotFrame(data, atEOF, maximum)
	})
	return scanner
}

func splitCopilotFrame(data []byte, atEOF bool, limit int) (int, []byte, error) {
	if len(data) == 0 && atEOF {
		return 0, nil, nil
	}
	end := bytes.Index(data, []byte("\r\n\r\n"))
	if end == -1 {
		if len(data) >= copilotHeaderLimit {
			return 0, nil, errors.New("the Copilot header exceeds the size limit")
		}
		if atEOF {
			return 0, nil, errors.New("the Copilot stream ended with an incomplete header")
		}
		return 0, nil, nil
	}
	headerSize := end + 4
	if headerSize > copilotHeaderLimit {
		return 0, nil, errors.New("the Copilot header exceeds the size limit")
	}
	length := 0
	for line := range bytes.SplitSeq(data[:end], []byte("\r\n")) {
		key, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			return 0, nil, errors.New("the Copilot frame contains an invalid header")
		}
		if !bytes.EqualFold(key, []byte("Content-Length")) {
			continue
		}
		if length != 0 {
			return 0, nil, errors.New("the Copilot frame contains duplicate Content-Length headers")
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || bytes.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return 0, nil, errors.New("the Copilot frame contains an invalid Content-Length")
		}
		parsed, err := strconv.ParseUint(string(value), 10, 63)
		if err != nil || parsed == 0 {
			return 0, nil, errors.New("the Copilot frame contains an invalid Content-Length")
		}
		if parsed > uint64(max(limit, 0)) {
			return 0, nil, errors.New("the Copilot frame exceeds the message size limit")
		}
		length = int(parsed)
	}
	if length == 0 {
		return 0, nil, errors.New("the Copilot frame has no Content-Length header")
	}
	if len(data)-headerSize < length {
		if atEOF {
			return 0, nil, errors.New("the Copilot stream ended with an incomplete payload")
		}
		return 0, nil, nil
	}
	end = headerSize + length
	return end, data[headerSize:end], nil
}
