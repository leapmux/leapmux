//go:build windows

package terminal

import "os/exec"

func detectDefaultShell() string {
	for _, name := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	// Every supported Windows release ships this path.
	return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
}
