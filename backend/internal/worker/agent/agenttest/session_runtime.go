package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SessionRuntimeFixture controls a native session reader without an installed provider.
// The provider's test-support package owns its peer protocol and invocation checks.
type SessionRuntimeFixture struct {
	Locator       launch.Locator
	EnvEntries    []string
	AssertInvoked func(*testing.T)
}

const SessionRuntimeMarkerEnv = "LEAPMUX_TEST_SESSION_RUNTIME"
const sessionRuntimeReceiptEnv = "LEAPMUX_TEST_SESSION_RUNTIME_RECEIPT"

type sessionRuntimeInvocation struct {
	Args        []string `json:"args"`
	Home        string   `json:"home"`
	UserProfile string   `json:"userProfile"`
	WorkingDir  string   `json:"workingDir"`
	Marker      string   `json:"marker"`
}

// CreateSessionRuntimeFixture supplies a private process environment and one invocation receipt.
// The provider's peer validates its protocol before it writes that receipt.
func CreateSessionRuntimeFixture(t *testing.T, home, workingDir, helperPattern, marker string) SessionRuntimeFixture {
	t.Helper()
	require.NotEmpty(t, helperPattern)
	require.NotEmpty(t, marker)
	require.True(t, filepath.IsAbs(home))
	require.True(t, filepath.IsAbs(workingDir))
	executable, err := os.Executable()
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(executable))
	receipt := filepath.Join(t.TempDir(), "native-runtime-invocation.json")
	entries := []string{
		"HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home, "PATH=" + os.Getenv("PATH"),
		SessionRuntimeMarkerEnv + "=" + marker, sessionRuntimeReceiptEnv + "=" + receipt,
	}
	// These platform variables locate OS components. They carry no provider login.
	for _, key := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			entries = append(entries, key+"="+value)
		}
	}
	locator := launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable, PrefixArgs: []string{"-test.run=" + helperPattern, "--"}}, launch.Found
	})
	return SessionRuntimeFixture{Locator: locator, EnvEntries: entries, AssertInvoked: func(t *testing.T) {
		t.Helper()
		data, err := readSessionRuntimeReceipt(receipt)
		require.NoError(t, err, "the registered native reader must invoke its controlled peer")
		var observed sessionRuntimeInvocation
		require.NoError(t, json.Unmarshal(data, &observed))
		assert.Equal(t, home, observed.Home)
		assert.Equal(t, home, observed.UserProfile)
		canonicalDirectory, err := filepath.EvalSymlinks(workingDir)
		require.NoError(t, err)
		assert.Equal(t, canonicalDirectory, observed.WorkingDir)
		assert.Equal(t, marker, observed.Marker)
		require.GreaterOrEqual(t, len(observed.Args), 3)
		assert.Equal(t, []string{executable, "-test.run=" + helperPattern, "--"}, observed.Args[:3])
	}}
}

// WriteSessionRuntimeInvocation records one validated peer invocation.
// Exclusive creation rejects repeated calls instead of replacing their receipt.
func WriteSessionRuntimeInvocation() (resultErr error) {
	path := os.Getenv(sessionRuntimeReceiptEnv)
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("the session runtime receipt path is absent or relative")
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	return json.NewEncoder(file).Encode(sessionRuntimeInvocation{Args: os.Args, Home: os.Getenv("HOME"), UserProfile: os.Getenv("USERPROFILE"),
		WorkingDir: directory, Marker: os.Getenv(SessionRuntimeMarkerEnv)})
}

// readSessionRuntimeReceipt requires one stable, complete, regular private receipt.
func readSessionRuntimeReceipt(path string) (data []byte, resultErr error) {
	const maximum = 64 << 10
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := root.Close(); err != nil {
			data = nil
			resultErr = errors.Join(resultErr, err)
		}
	}()
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maximum {
		return nil, errors.New("the session runtime receipt is not a regular file within the size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			data = nil
			resultErr = errors.Join(resultErr, err)
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	same := func(first, second os.FileInfo) bool {
		return first.Mode().IsRegular() && second.Mode().IsRegular() && os.SameFile(first, second) && first.Size() == second.Size() && first.Mode() == second.Mode() && first.ModTime().Equal(second.ModTime())
	}
	if !same(before, opened) {
		return nil, errors.New("the session runtime receipt changed before its descriptor opened")
	}
	data, err = io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != before.Size() || len(data) > maximum || !same(before, after) || !same(before, current) {
		return nil, errors.New("the session runtime receipt changed during its complete read")
	}
	return data, nil
}
