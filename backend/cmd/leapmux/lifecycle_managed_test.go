package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func managedTestSpec(t *testing.T) lifecycle.ServiceSpec {
	t.Helper()
	root := mustCanonicalDir(t, t.TempDir())
	executable, err := os.Executable()
	require.NoError(t, err)
	configuration := filepath.Join(t.TempDir(), "hub.yaml")
	require.NoError(t, os.WriteFile(configuration, []byte("data_dir: "+filepath.ToSlash(filepath.Join(t.TempDir(), "wrong-root"))+"\n"), 0o600))
	return lifecycle.ServiceSpec{
		InstanceID: lifecycle.NewInstanceID(lifecycle.CurrentAccountID(), lifecycle.RoleHub, root).String(),
		Mode:       lifecycle.RoleHub, Startup: lifecycle.StartupBoot,
		AccountID: lifecycle.CurrentAccountID(), AccountName: currentAccountName(),
		Executable: executable, WorkingDirectory: t.TempDir(), ConfigFile: configuration, DataDirectory: root,
		RuntimeArguments: []string{"--listen", locallistentest.UniqueListenURL(t, "lmx-managed")},
	}
}

func saveManagedTestSpec(t *testing.T, spec lifecycle.ServiceSpec) string {
	t.Helper()
	lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(spec.Mode, spec.DataDirectory), spec.Mode, spec.DataDirectory)
	require.NoError(t, err)
	require.NoError(t, lease.Close())
	path := lifecycle.PathsOf(spec.DataDirectory).ServiceSpec
	require.NoError(t, lifecycle.WriteServiceSpec(path, spec))
	return path
}

func TestManagedRunRejectsInvalidIdentityBeforeRuntime(t *testing.T) {
	for _, field := range []string{"account", "instance", "mode", "path"} {
		t.Run(field, func(t *testing.T) {
			spec := managedTestSpec(t)
			switch field {
			case "account":
				spec.AccountID = "another-account"
			case "instance":
				spec.InstanceID = "another-instance"
			case "mode":
				spec.Mode = lifecycle.RoleWorker
			}
			path := saveManagedTestSpec(t, spec)
			if field == "path" {
				other := filepath.Join(t.TempDir(), "service.yaml")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, atomicfile.WriteFileDurable(other, data, 0o600))
				path = other
			}
			result := invokeLifecycleCLI(t, t.TempDir(), "hub", "run", "--service-spec", path)
			require.Equal(t, 1, result.exitCode)
			require.Contains(t, result.stderr, "managed launch")
			if field == "path" {
				require.Contains(t, result.stderr, "does not match the selected account, mode, or data directory")
			}
			require.NoFileExists(t, lifecycle.PathsOf(spec.DataDirectory).RuntimeRecord)
		})
	}
}

func TestManagedRunUsesSavedRootAndReportsService(t *testing.T) {
	spec := managedTestSpec(t)
	saveManagedTestSpec(t, spec)
	home := t.TempDir()
	defaultConfig := filepath.Join(home, ".config", "leapmux", "hub", "hub.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultConfig), 0o700))
	require.NoError(t, os.WriteFile(defaultConfig, []byte("listen: [unterminated"), 0o600))
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	command := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestHelperProcessLifecycleCLI$", "--"}, launch.RunArguments()...)...)
	command.Env = lifecycleCLIEnvironment(home)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())
	done := make(chan struct{})
	var commandErr error
	go func() { commandErr = command.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-done
	})
	require.Eventually(t, func() bool {
		status, err := lifecycle.Observe(ctx, spec.DataDirectory)
		return err == nil && status.Readiness != nil && *status.Readiness == lifecycle.ReadinessReady &&
			status.Execution != nil && *status.Execution == lifecycle.ExecutionService
	}, 30*time.Second, 50*time.Millisecond)
	stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	record, err := lifecycle.ReadRuntimeRecord(lifecycle.PathsOf(spec.DataDirectory).RuntimeRecord)
	require.NoError(t, err)
	_, err = lifecycle.RequestStop(stopCtx, record)
	require.NoError(t, err)
	select {
	case <-done:
		require.NoError(t, commandErr, output.String())
	case <-ctx.Done():
		t.Fatal("managed run did not join its shutdown")
	}
}

func TestManagedRunRejectsSelectorOverridesBeforeRuntime(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    func(string) []string
		message string
	}{
		{"empty specification", func(string) []string { return []string{"--service-spec="} }, "exactly one nonempty service specification"},
		{"repeated specification", func(path string) []string { return []string{"--service-spec", path, "--service-spec", path} }, "exactly one nonempty service specification"},
		{"listen override", func(path string) []string { return []string{"--service-spec", path, "--listen", "127.0.0.1:0"} }, "cannot override --listen"},
		{"config override", func(path string) []string { return []string{"--service-spec", path, "--config", "unread-config.yaml"} }, "cannot override --config"},
		{"wrong root", func(path string) []string { return []string{"--service-spec", path, "--data-dir", filepath.Dir(path)} }, "data directory differs"},
		{"empty root", func(path string) []string { return []string{"--service-spec", path, "--data-dir="} }, "data directory differs"},
		{"relative specification", func(string) []string { return []string{"--service-spec", "service.yaml"} }, "absolute service specification path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := managedTestSpec(t)
			path := saveManagedTestSpec(t, spec)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			result := invokeLifecycleCLI(t, t.TempDir(), append([]string{"hub", "run"}, test.args(path)...)...)
			require.Equal(t, 1, result.exitCode)
			require.Contains(t, result.stderr, test.message)
			require.NoFileExists(t, lifecycle.PathsOf(spec.DataDirectory).RuntimeRecord)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestManagedRunRejectsMalformedSavedConfigurationBeforeRuntime(t *testing.T) {
	spec := managedTestSpec(t)
	path := saveManagedTestSpec(t, spec)
	require.NoError(t, os.WriteFile(spec.ConfigFile, []byte("listen: [unterminated"), 0o600))
	result := invokeLifecycleCLI(t, t.TempDir(), "hub", "run", "--service-spec", path)
	require.Equal(t, 1, result.exitCode)
	require.Contains(t, result.stderr, "config")
	require.Contains(t, result.stderr, "yaml")
	require.NoFileExists(t, lifecycle.PathsOf(spec.DataDirectory).RuntimeRecord)
}

func TestManagedEnvironmentDiscoversProviderFromFilePath(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "environment.yaml")
	data, err := yaml.Marshal(map[string]string{"PATH": filepath.Dir(executable)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	spec, err := desiredSpec("hub", lifecycle.StartupBoot, t.TempDir(), []string{"--environment-file", path})
	require.NoError(t, err)
	restore, err := applyManagedEnvironment(spec)
	require.NoError(t, err)
	defer func() { require.NoError(t, restore()) }()
	found, err := exec.LookPath(filepath.Base(executable))
	require.NoError(t, err)
	actual, err := os.Stat(found)
	require.NoError(t, err)
	expected, err := os.Stat(executable)
	require.NoError(t, err)
	require.True(t, os.SameFile(expected, actual))
}
