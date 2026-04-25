package validate

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
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
