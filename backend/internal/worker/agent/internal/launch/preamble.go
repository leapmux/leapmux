package launch

import (
	"bufio"
	"bytes"
)

// PreambleSplit switches to a provider's native splitter after a complete
// readiness line. A clean EOF can end a native record but does not frame the
// shell's readiness marker.
func PreambleSplit(delimiter string, shellLines, nativeFrames bufio.SplitFunc) bufio.SplitFunc {
	if delimiter == "" {
		return nativeFrames
	}
	marker := []byte(delimiter)
	readingPreamble := true
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if !readingPreamble {
			return nativeFrames(data, atEOF)
		}
		advance, token, err := shellLines(data, atEOF)
		if err == nil && bytes.Equal(token, marker) {
			if advance == 0 || data[advance-1] != '\n' {
				return advance, nil, nil
			}
			readingPreamble = false
		}
		return advance, token, err
	}
}
