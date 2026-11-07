//go:build darwin || linux

package servicelaunch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

func TestUnixRuntimeValidationPrecedesNativeActions(t *testing.T) {
	for kind, path := range invalidEnvironmentFiles(t) {
		for _, operation := range []string{"install", "start", "authorize"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				spec := LaunchSpec(testSpec())
				spec.Startup = lifecycle.StartupBoot
				spec.EnvironmentFile = &path
				manager, calls := runtimeValidationManager(t, spec)
				var err error
				switch operation {
				case "install":
					err = manager.Install(t.Context(), spec)
				case "start":
					err = manager.Start(t.Context(), spec)
				case "authorize":
					err = authorizeNativeStartWith(t.Context(), spec, func(context.Context) error { *calls = append(*calls, "authorize"); return nil })
				}
				require.ErrorContains(t, err, "environment file")
				assert.Empty(t, *calls, "invalid runtime data must precede native calls, writes, and authorization")
			})
		}
	}
}

func TestUnixInstallValidatesNativeFieldsBeforeMutation(t *testing.T) {
	for kind, mutate := range map[string]func(*LaunchSpec){
		"relative executable": func(spec *LaunchSpec) { spec.Executable = "leapmux" },
		"invalid identity":    func(spec *LaunchSpec) { spec.InstanceID = "../other" },
		"missing account":     func(spec *LaunchSpec) { spec.AccountID = "" },
		"registration argument": func(spec *LaunchSpec) {
			spec.RuntimeArguments = []string{"--registration-key", "private-fixture-value"}
		},
	} {
		t.Run(kind, func(t *testing.T) {
			spec := LaunchSpec(testSpec())
			spec.Startup = lifecycle.StartupBoot
			mutate(&spec)
			manager, calls := runtimeValidationManager(t, spec)
			err := manager.Install(t.Context(), spec)
			require.Error(t, err)
			assert.Empty(t, *calls)
			assert.NotContains(t, err.Error(), "private-fixture-value")
		})
	}
}

func TestUnixAuthorizationChecksRuntimeBeforeRequestingCredentials(t *testing.T) {
	spec := LaunchSpec(testSpec())
	spec.Startup = lifecycle.StartupBoot
	calls := 0
	failure := errors.New("authorization refused")
	err := authorizeNativeStartWith(t.Context(), spec, func(ctx context.Context) error {
		calls++
		assert.Equal(t, t.Context(), ctx)
		return failure
	})
	require.ErrorIs(t, err, failure)
	assert.Equal(t, 1, calls)
	spec.Startup = lifecycle.StartupLogin
	require.NoError(t, authorizeNativeStartWith(t.Context(), spec, func(context.Context) error { t.Fatal("login startup needs no system authorization"); return nil }))
	spec.Startup = lifecycle.StartupBoot
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, authorizeNativeStartWith(ctx, spec, func(context.Context) error {
		t.Fatal("canceled authorization must not request credentials")
		return nil
	}), context.Canceled)
}
