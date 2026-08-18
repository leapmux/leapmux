package validate

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sanitizeCase struct {
	name    string
	input   string
	homeDir string
	want    string
	wantErr error // if non-nil, SanitizePath must return this sentinel (via errors.Is)
}

func runSanitizeCases(t *testing.T, cases []sanitizeCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizePath(tt.input, tt.homeDir)
			if tt.wantErr != nil {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr),
					"expected error %v, got %v", tt.wantErr, err)
				assert.Equal(t, "", got)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSanitizePath_Empty covers cases that behave identically on every OS.
// OS-specific cases live in path_unix_test.go and path_windows_test.go.
func TestSanitizePath_Empty(t *testing.T) {
	cases := []sanitizeCase{
		{name: "empty string", input: "", wantErr: ErrEmptyPath},
		{name: "whitespace only", input: "   ", wantErr: ErrEmptyPath},
		{name: "control chars only", input: "\x01\x02\x03", wantErr: ErrEmptyPath},
	}
	runSanitizeCases(t, cases)
}

// TestSanitizePathDropsInvalidBytes pins the repair of the growth that this
// package removed from the name rule and left standing here.
//
// `for range` over a Go string yields U+FFFD for an invalid byte, and U+FFFD
// is not a control character, so the old loop wrote its full 3-byte encoding
// and returned a path LONGER than the one it received. A path has no byte
// limit for that growth to overflow today, so this repairs the mechanism
// rather than a reported failure -- and it stops the next reader from copying
// the pattern out of this file.
func TestSanitizePathDropsInvalidBytes(t *testing.T) {
	got, err := SanitizePath("/tmp/a\xffb", "/home/u")
	require.NoError(t, err)
	assert.True(t, utf8.ValidString(got), "the result must be valid UTF-8")
	assert.NotContains(t, got, "�", "an invalid byte is dropped, not grown into a replacement character")

	// The result never grows. 10 invalid bytes used to become 30.
	in := "/tmp/" + strings.Repeat("\xff", 10)
	got, err = SanitizePath(in, "/home/u")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(got), len(in), "SanitizePath must never grow the path it is given")

	// A U+FFFD the caller sent deliberately decodes with size 3 and survives,
	// so the case above tells the two apart by the ENCODING and not the rune.
	got, err = SanitizePath("/tmp/a�b", "/home/u")
	require.NoError(t, err)
	assert.Contains(t, got, "�")
}
