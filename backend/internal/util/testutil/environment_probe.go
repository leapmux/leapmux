package testutil

import (
	"os"

	"github.com/leapmux/leapmux/internal/util/envprobe"
)

// Native environment probes execute the current binary. Test binaries use this
// bootstrap before Go's test flag parser; shipped workers use RunAgentHelper.
func init() {
	if code, handled := envprobe.Run(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
}
