//go:build darwin || linux

package servicelaunch

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/servicelaunch/servicefixture"
	"github.com/stretchr/testify/require"
)

func assertNativeCLIPrerequisites(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "-v").CombinedOutput()
	require.NoError(t, err, "prepare sudo authorization for the disposable account before running native CLI acceptance: %s", output)
}

func nativeCLIFixturePassword(servicefixture.Account) func(context.Context) ([]byte, error) {
	return nil
}

func nativeCLIDefinitionDiagnostic(ctx context.Context, manager Manager, spec LaunchSpec) (string, error) {
	notes, err := manager.Diagnose(ctx, spec)
	return strings.Join(notes, "\n"), err
}
