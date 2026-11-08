//go:build darwin || linux

package servicelaunch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

func TestSystemDefinitionPublicationIsStaged(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			spec, err := LaunchSpecFromService(testSpec())
			require.NoError(t, err)
			spec.Startup = lifecycle.StartupBoot
			spec.DataDirectory = t.TempDir()
			require.NoError(t, os.Mkdir(lifecycle.PathsOf(spec.DataDirectory).Dir, 0o700))
			path := "/Library/LaunchDaemons/com.leapmux.worker." + spec.InstanceID + ".plist"
			var calls [][]string
			var source string
			err = publishNativeDefinitionWith(t.Context(), spec, path, []byte("complete definition"), func(_ context.Context, args []string) error {
				calls = append(calls, append([]string(nil), args...))
				if len(calls) == 1 {
					source = args[len(args)-2]
					data, err := os.ReadFile(source)
					require.NoError(t, err)
					assert.Equal(t, "complete definition", string(data))
					assert.NotEqual(t, path, args[len(args)-1], "install must not replace the live definition before the complete file is staged")
				}
				if failAt == len(calls) {
					return assert.AnError
				}
				return nil
			})
			if failAt == 0 {
				require.NoError(t, err)
				require.Len(t, calls, 2)
				assert.Equal(t, []string{"/bin/mv", "-f", "--", path + ".new", path}, calls[1])
			} else {
				require.ErrorIs(t, err, assert.AnError)
				assert.Len(t, calls, failAt)
			}
			assert.NoFileExists(t, source)
		})
	}
}

func TestSystemDefinitionRefusesInvalidPathAndCancellation(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.Startup = lifecycle.StartupBoot
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if canceled {
			cancel()
		}
		calls := 0
		err := publishNativeDefinitionWith(ctx, spec, "/etc/another-service", nil, func(context.Context, []string) error { calls++; return nil })
		require.Error(t, err)
		assert.Zero(t, calls)
		cancel()
	}
}

func TestNativeRemovalDoesNotRequireEnvironmentFile(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.Startup = lifecycle.StartupBoot
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	spec.EnvironmentFile = &missing
	require.NoError(t, validateSystemDefinitionPath(spec, "/Library/LaunchDaemons/com.leapmux.worker."+spec.InstanceID+".plist"))
}
