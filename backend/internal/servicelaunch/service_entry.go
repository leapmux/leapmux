package servicelaunch

import (
	"fmt"
	"regexp"

	"github.com/leapmux/leapmux/lifecycle"
)

var windowsServiceNamePattern = regexp.MustCompile(`^leapmux-(hub|worker|solo|dev)-[0-9a-f]{16}$`)

// ParseNativeServiceEntry validates the arguments that a Windows service entry
// passes to the binary:
//
//	--native-service <service-name> daemon <mode> run [flags...]
//
// When args do not start with WindowsServiceEntryFlag, it returns an empty
// serviceName, an empty role, nil runArgs, and a nil error.
func ParseNativeServiceEntry(args []string) (serviceName string, role lifecycle.Role, runArgs []string, err error) {
	if len(args) == 0 || args[0] != WindowsServiceEntryFlag {
		return "", "", nil, nil
	}
	if len(args) < 5 || args[2] != "daemon" || args[4] != "run" {
		return "", "", nil, fmt.Errorf("invalid Windows service entry")
	}
	matches := windowsServiceNamePattern.FindStringSubmatch(args[1])
	if matches == nil {
		return "", "", nil, fmt.Errorf("invalid Windows service entry")
	}
	role, err = lifecycle.ParseRole(args[3])
	if err != nil {
		return "", "", nil, err
	}
	if matches[1] != role.String() {
		return "", "", nil, fmt.Errorf("invalid Windows service entry: service name role %q does not match command role %q", matches[1], role)
	}
	runArgs = append([]string{"daemon", role.String(), "run"}, args[5:]...)
	return args[1], role, runArgs, nil
}
