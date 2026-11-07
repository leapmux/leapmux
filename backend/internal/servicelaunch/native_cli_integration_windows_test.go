//go:build serviceintegration && windows

package servicelaunch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func assertNativeCLIPrerequisites(t *testing.T) {
	t.Helper()
	require.True(t, windows.GetCurrentProcessToken().IsElevated(), "run native CLI acceptance from the prepared disposable account's elevated shell")
}

func nativeCLIFixturePassword(fixture serviceAccountFixture) func(context.Context) ([]byte, error) {
	return func(ctx context.Context) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if fixture.CredentialReference == nil {
			return nil, errors.New("the Windows fixture requires a credential-store entry")
		}
		return passwordFromCredentialStore(*fixture.CredentialReference, fixture.AccountID)
	}
}
