//go:build serviceintegration && linux

package servicelaunch

import "testing"

func nativeIntegrationManager(*testing.T, serviceAccountFixture, string) Manager {
	return newSystemdManager()
}
