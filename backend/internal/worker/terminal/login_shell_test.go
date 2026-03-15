package terminal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoginShellArgs(t *testing.T) {
	tests := []struct {
		name      string
		shellPath string
		want      []string
	}{
		{"bash", "/bin/bash", []string{"-i", "-l"}},
		{"zsh", "/bin/zsh", []string{"-i", "-l"}},
		{"fish", "/usr/bin/fish", []string{"-i", "-l"}},
		{"sh", "/bin/sh", []string{"-i", "-l"}},
		{"nu", "/usr/bin/nu", []string{"-i", "-l"}},
		{"tcsh", "/bin/tcsh", []string{"-l"}},
		{"csh", "/bin/csh", []string{"-l"}},
		{"pwsh", "/usr/bin/pwsh", []string{"-Login"}},
		{"powershell", "/usr/bin/powershell", []string{"-Login"}},
		{"pwsh-preview", "/usr/bin/pwsh-preview", []string{"-Login"}},
		{"unknown", "/usr/bin/xonsh", []string{"-i", "-l"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, LoginShellArgs(tt.shellPath))
		})
	}
}

func TestIsPwsh(t *testing.T) {
	// Positive cases
	assert.True(t, IsPwsh("pwsh"))
	assert.True(t, IsPwsh("powershell"))
	assert.True(t, IsPwsh("pwsh-preview"))
	assert.True(t, IsPwsh("powershell-preview"))

	// Negative cases
	assert.False(t, IsPwsh("bash"))
	assert.False(t, IsPwsh("zsh"))
	assert.False(t, IsPwsh("fish"))
	assert.False(t, IsPwsh("pwsh-extra-stuff"))
}
