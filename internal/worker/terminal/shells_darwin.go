//go:build darwin

package terminal

import (
	"os/exec"
	"os/user"
	"regexp"
)

var dsclShellRe = regexp.MustCompile(`UserShell:\s+(/\S+)`)

// detectDefaultShell returns the current user's login shell by querying the
// macOS Directory Services (dscl).  Falls back to /bin/zsh (the macOS default
// since Catalina) if the lookup fails.
func detectDefaultShell() string {
	u, err := user.Current()
	if err != nil {
		return "/bin/zsh"
	}

	out, err := exec.Command("dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
	if err != nil {
		return "/bin/zsh"
	}

	m := dsclShellRe.FindSubmatch(out)
	if m == nil {
		return "/bin/zsh"
	}

	return string(m[1])
}
