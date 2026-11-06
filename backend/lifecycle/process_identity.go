package lifecycle

import (
	"os"

	"github.com/leapmux/leapmux/util/procutil"
)

// processIdent is this process's pid-plus-start-time identity.
type processIdent = procutil.ProcessIdentity

// currentProcessIdentity identifies this process. The start time is
// what tells a reused pid from the process a record named.
func currentProcessIdentity() (processIdent, bool) {
	return procutil.IdentifyProcess(os.Getpid())
}
