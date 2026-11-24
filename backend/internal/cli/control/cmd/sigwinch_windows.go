//go:build windows

package cmd

import "os"

func notifyResizeSignals() (<-chan os.Signal, func()) {
	return nil, func() {}
}
