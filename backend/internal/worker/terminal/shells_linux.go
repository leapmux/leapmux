//go:build linux

package terminal

import (
	"bufio"
	"os"
	"os/user"
	"strings"
)

// detectDefaultShell returns the current user's login shell by parsing
// /etc/passwd.  Falls back to /bin/sh if the lookup fails.
func detectDefaultShell() string {
	u, err := user.Current()
	if err != nil {
		return "/bin/sh"
	}

	f, err := os.Open("/etc/passwd")
	if err != nil {
		return "/bin/sh"
	}
	defer func() { _ = f.Close() }()

	uid := u.Uid
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		// passwd format: name:password:uid:gid:gecos:home:shell
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		if fields[2] == uid {
			shell := fields[6]
			if shell != "" {
				return shell
			}
			break
		}
	}

	return "/bin/sh"
}
