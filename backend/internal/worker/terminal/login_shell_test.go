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
		{"powershell", "/usr/bin/powershell", nil},
		{"pwsh-preview", "/usr/bin/pwsh-preview", []string{"-Login"}},
		{"powershell-preview", "/usr/bin/powershell-preview", nil},
		{"pwsh.exe", `C:\Program Files\PowerShell\7\pwsh.exe`, []string{"-Login"}},
		{"powershell.exe", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil},
		{"cmd.exe", `C:\Windows\System32\cmd.exe`, nil},
		{"cmd.exe upper", `C:\Windows\System32\CMD.EXE`, nil},
		{"cmd bare", "cmd", nil},
		{"powershell.exe upper", `C:\Windows\System32\WindowsPowerShell\v1.0\POWERSHELL.EXE`, nil},
		{"unknown", "/usr/bin/xonsh", []string{"-i", "-l"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, LoginShellArgs(tt.shellPath))
		})
	}
}

// TestLoginShellArgs_WindowsPowerShellNoLogin pins the fix for a Windows
// regression where opening a terminal whose default shell resolved to
// powershell.exe (Windows PowerShell 5.1) printed:
//
//	-Login : The term '-Login' is not recognized as the name of a cmdlet…
//	+ CategoryInfo : ObjectNotFound: (-Login:String) [], CommandNotFoundException
//
// 5.1's CLI parser falls back to treating an unrecognized leading token as
// a command, so passing -Login made it try to *run* a command named
// "-Login". Only pwsh (PowerShell Core 6+) understands the switch.
func TestLoginShellArgs_WindowsPowerShellNoLogin(t *testing.T) {
	assert.Nil(t, LoginShellArgs(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`))
	assert.Nil(t, LoginShellArgs("powershell"))
	assert.Equal(t, []string{"-Login"}, LoginShellArgs(`C:\Program Files\PowerShell\7\pwsh.exe`))
	assert.Equal(t, []string{"-Login"}, LoginShellArgs("pwsh"))
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
