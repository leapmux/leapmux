//go:build serviceintegration && windows

package servicelaunch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	ole "github.com/go-ole/go-ole"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/lifecycle"
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

func nativeCLIDefinitionDiagnostic(ctx context.Context, manager Manager, spec LaunchSpec) (string, error) {
	if spec.Startup != lifecycle.StartupLogin {
		notes, err := manager.Diagnose(ctx, spec)
		return strings.Join(notes, "\n"), err
	}
	var snapshot windowsTaskSnapshot
	err := withTaskFolder(ctx, func(folder *ole.IDispatch) error {
		return withRegisteredTask(folder, spec, func(task *ole.IDispatch) error {
			var err error
			snapshot, err = readTaskSnapshot(task)
			return err
		})
	})
	if err != nil {
		return "", err
	}
	data, err := taskDefinitionXML(spec, windows.ComposeCommandLine(spec.RunArguments()))
	if err != nil {
		return "", err
	}
	expected, err := parseWindowsTaskDefinition(string(data))
	if err != nil {
		return "", err
	}
	// The fixture logs typed task metadata. Passwords and the private runtime
	// environment are absent from task definitions and this snapshot.
	evidence := struct {
		DefinitionMatches  bool
		AccessMatches      bool
		Actual             windowsTaskSnapshot
		ExpectedDefinition windowsTaskDefinition
		ExpectedIdentity   windowsTaskIdentity
		ExpectedAccess     windowsTaskAccess
	}{
		DefinitionMatches: snapshot.Definition.matches(spec, snapshot.Identity),
		AccessMatches:     snapshot.Access.matches(spec.AccountID), Actual: snapshot,
		ExpectedDefinition: expected,
		ExpectedIdentity:   windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID},
		ExpectedAccess:     installedTaskAccess(spec.AccountID),
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	return string(encoded), err
}
