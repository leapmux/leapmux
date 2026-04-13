package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateCustomKeybindingsJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{name: "empty string", input: "", wantErr: false},
		{name: "empty array", input: "[]", wantErr: false},
		{
			name:    "valid single override",
			input:   `[{"key":"$mod+Shift+a","command":"app.newAgent"}]`,
			wantErr: false,
		},
		{
			name:    "valid with when clause",
			input:   `[{"key":"$mod+n","command":"app.newAgent","when":"!dialogOpen"}]`,
			wantErr: false,
		},
		{
			name:    "valid unbind (empty key)",
			input:   `[{"key":"","command":"app.newAgent"}]`,
			wantErr: false,
		},
		{
			name:    "valid multiple entries",
			input:   `[{"key":"$mod+a","command":"a"},{"key":"$mod+b","command":"b"}]`,
			wantErr: false,
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
			errMsg:  "invalid JSON",
		},
		{
			name:    "JSON is not an array",
			input:   `{"key":"$mod+a","command":"a"}`,
			wantErr: true,
			errMsg:  "invalid JSON",
		},
		{
			name:    "missing command",
			input:   `[{"key":"$mod+a"}]`,
			wantErr: true,
			errMsg:  "command is required",
		},
		{
			name:    "empty command",
			input:   `[{"key":"$mod+a","command":""}]`,
			wantErr: true,
			errMsg:  "command is required",
		},
		{
			name:    "key too long",
			input:   `[{"key":"` + strings.Repeat("a", maxKeybindingFieldLen+1) + `","command":"app.test"}]`,
			wantErr: true,
			errMsg:  "key too long",
		},
		{
			name:    "command too long",
			input:   `[{"key":"$mod+a","command":"` + strings.Repeat("a", maxKeybindingFieldLen+1) + `"}]`,
			wantErr: true,
			errMsg:  "command too long",
		},
		{
			name:    "when too long",
			input:   `[{"key":"$mod+a","command":"app.test","when":"` + strings.Repeat("a", maxKeybindingFieldLen+1) + `"}]`,
			wantErr: true,
			errMsg:  "when too long",
		},
		{
			name:    "too many entries",
			input:   generateNEntries(maxCustomKeybindings + 1),
			wantErr: true,
			errMsg:  "too many keybinding overrides",
		},
		{
			name:    "max entries allowed",
			input:   generateNEntries(maxCustomKeybindings),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCustomKeybindingsJSON(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func generateNEntries(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"key":"$mod+a","command":"cmd.test"}`)
	}
	b.WriteByte(']')
	return b.String()
}
