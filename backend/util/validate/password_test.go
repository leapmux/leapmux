package validate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"empty", "", true},
		{"too short (1 char)", "a", true},
		{"too short (7 chars)", "1234567", true},
		{"min length (8 chars)", "12345678", false},
		{"typical password", "my-secure-password", false},
		{"max length (128 chars)", strings.Repeat("a", 128), false},
		{"too long (129 chars)", strings.Repeat("a", 129), true},
		{"unicode characters", "pässwörd", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.password)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
