//go:build serviceintegration && darwin

package servicelaunch

import "testing"

func nativeIntegrationManager(*testing.T, serviceAccountFixture, string) Manager {
	return newLaunchdManager()
}
