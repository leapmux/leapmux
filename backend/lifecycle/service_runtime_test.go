package lifecycle

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runtimeServiceSpec(t *testing.T) ServiceSpec {
	t.Helper()
	root, err := CanonicalDir(t.TempDir())
	require.NoError(t, err)
	account, err := user.Current()
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	spec := ServiceSpec{
		InstanceID: NewInstanceID(CurrentAccountID(), RoleWorker, root).String(), Mode: RoleWorker, Startup: StartupBoot,
		AccountID: CurrentAccountID(), AccountName: account.Username,
		Executable: executable, ConfigFile: filepath.Join(root, "worker.yaml"), WorkingDirectory: root, DataDirectory: root,
	}
	lease, err := AcquireDirectory(NewDirectoryOwner(spec.Mode, root), spec.Mode, root)
	require.NoError(t, err)
	require.NoError(t, WriteServiceSpec(PathsOf(root).ServiceSpec, spec))
	require.NoError(t, lease.Close())
	return spec
}

func TestServiceRuntimeRejectsWithdrawnOrChangedSpecification(t *testing.T) {
	for _, change := range []string{"removed", "changed", "different account", "different mode", "forged instance"} {
		t.Run(change, func(t *testing.T) {
			runtimeEnv(t)
			captured := runtimeServiceSpec(t)
			paths := PathsOf(captured.DataDirectory)
			state := filepath.Join(captured.DataDirectory, "state.json")
			database := filepath.Join(captured.DataDirectory, "worker.db")
			require.NoError(t, os.WriteFile(state, []byte("original credentials"), 0o600))
			require.NoError(t, os.WriteFile(database, []byte("original database"), 0o600))
			switch change {
			case "removed":
				require.NoError(t, os.Remove(paths.ServiceSpec))
			case "changed":
				changed := captured
				changed.RuntimeArguments = []string{"--log-level", "debug"}
				require.NoError(t, WriteServiceSpec(paths.ServiceSpec, changed))
			case "different account":
				captured.AccountID = "another-account"
			case "different mode":
				captured.Mode = RoleHub
			case "forged instance":
				captured.InstanceID = "another-instance"
			}
			runtime, err := StartServiceRuntime(captured)
			if runtime != nil {
				t.Cleanup(func() { _ = runtime.Finish(nil) })
			}
			require.Error(t, err)
			assert.NoFileExists(t, paths.ControlToken)
			assert.NoFileExists(t, paths.RuntimeRecord)
			assert.NoDirExists(t, paths.Logs)
			for path, expected := range map[string]string{state: "original credentials", database: "original database"} {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, expected, string(data))
			}
			lease, err := AcquireDirectory(NewDirectoryOwner(RoleWorker, paths.DataDir), RoleWorker, paths.DataDir)
			require.NoError(t, err, "a rejected managed launch must release its lease")
			require.NoError(t, lease.Close())
		})
	}
}

func TestServiceRuntimeAcceptsUnchangedSavedValues(t *testing.T) {
	runtimeEnv(t)
	spec := runtimeServiceSpec(t)
	runtime, err := StartServiceRuntime(spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Finish(nil) })
	assert.Equal(t, ExecutionService, runtime.Record().Execution)
	assert.Equal(t, spec.InstanceID, runtime.Record().InstanceID)
	assert.Equal(t, spec.DataDirectory, runtime.Record().DataDir)
}

func TestServiceSpecificationEqualityNormalizesEmptyCollections(t *testing.T) {
	left := ServiceSpec{}
	right := ServiceSpec{RuntimeArguments: []string{}, RuntimeEnvironment: map[string]string{}}
	assert.True(t, left.Equal(right))
	assert.NotNil(t, right.RuntimeArguments)
	assert.NotNil(t, right.RuntimeEnvironment)
	right.RuntimeArguments = []string{"--log-level", "debug"}
	assert.False(t, left.Equal(right))
	right.RuntimeArguments = nil
	right.RuntimeEnvironment["PATH"] = "/provider/bin"
	assert.False(t, left.Equal(right))
}
