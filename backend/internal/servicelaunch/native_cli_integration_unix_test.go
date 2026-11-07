//go:build serviceintegration && (darwin || linux)

package servicelaunch

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func assertNativeCLIPrerequisites(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "-v").CombinedOutput()
	require.NoError(t, err, "prepare sudo authorization for the disposable account before running native CLI acceptance: %s", output)
}

func nativeCLIFixturePassword(serviceAccountFixture) func(context.Context) ([]byte, error) {
	return nil
}
