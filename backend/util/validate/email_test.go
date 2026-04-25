package validate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateEmail_ValidAddresses(t *testing.T) {
	valid := []string{
		"",
		"user@example.com",
		"alice.bob@example.co.uk",
		"user+tag@domain.org",
		"a@b.co",
		"test123@sub.domain.com",
	}
	for _, email := range valid {
		t.Run(email, func(t *testing.T) {
			assert.NoError(t, ValidateEmail(email))
		})
	}
}

func TestValidateEmail_InvalidAddresses(t *testing.T) {
	tests := []struct {
		name  string
		email string
	}{
		{"no at", "userexample.com"},
		{"no domain", "user@"},
		{"no local part", "@example.com"},
		{"no dot in domain", "user@localhost"},
		{"display name", "Alice <alice@example.com>"},
		{"spaces", "user @example.com"},
		{"comma", "a@b.com,c@d.com"},
		{"angle brackets", "<user@example.com>"},
		{"double at", "user@@example.com"},
		{"too long", strings.Repeat("a", 250) + "@b.co"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEmail(tt.email)
			assert.Error(t, err)
		})
	}
}
