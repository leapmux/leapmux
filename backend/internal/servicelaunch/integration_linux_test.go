//go:build serviceintegration && linux

package servicelaunch

import (
	"testing"

	"github.com/leapmux/leapmux/internal/servicelaunch/servicefixture"
)

func nativeIntegrationManager(*testing.T, servicefixture.Account, string) Manager {
	return newSystemdManager()
}
