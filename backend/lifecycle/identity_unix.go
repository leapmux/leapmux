//go:build unix

package lifecycle

import (
	"os"
	"strconv"
)

func currentAccount() string { return strconv.Itoa(os.Getuid()) }
