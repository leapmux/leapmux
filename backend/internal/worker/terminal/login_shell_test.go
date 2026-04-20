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
		{"pwsh.exe", `C:\Program Files\PowerShell\7\pwsh.exe`, []string{"-Login"}},
		{"powershell.exe", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, []string{"-Login"}},
		{"cmd.exe", `C:\Windows\System32\cmd.exe`, nil},
		{"cmd.exe upper", `C:\Windows\System32\CMD.EXE`, nil},
		{"cmd bare", "cmd", nil},
		{"powershell.exe upper", `C:\Windows\System32\WindowsPowerShell\v1.0\POWERSHELL.EXE`, []string{"-Login"}},
		{"unknown", "/usr/bin/xonsh", []string{"-i", "-l"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, LoginShellArgs(tt.shellPath))
		})
	}
}

func TestIsPwsh(t *testing.T) {
	assert.True(t, IsPwsh("pwsh"))
	assert.True(t, IsPwsh("powershell"))
	assert.True(t, IsPwsh("pwsh-preview"))
	assert.True(t, IsPwsh("powershell-preview"))

	assert.False(t, IsPwsh("bash"))
	assert.False(t, IsPwsh("zsh"))
	assert.False(t, IsPwsh("fish"))
	assert.False(t, IsPwsh("pwsh-extra-stuff"))
	// IsPwsh expects normalized input — callers must strip .exe via ShellBaseName.
	assert.False(t, IsPwsh("pwsh.exe"))
}
