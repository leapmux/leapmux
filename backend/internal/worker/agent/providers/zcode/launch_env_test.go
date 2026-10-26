package zcode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func zcodeWriteStoreWithSecret(t *testing.T, home, secret string) {
	t.Helper()
	key := sha256.Sum256([]byte(zcodeCredentialSecret(func(string) string { return secret }, home, zcodeNodeUsername())))
	data, err := json.Marshal(map[string]string{zcodeCredentialZCodeJWT: zcodeTestEncryptCredential(t, key, "fixture-jwt")})
	require.NoError(t, err)
	path := filepath.Join(home, ".zcode", "v2", "credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func TestZCodeAccountEnvironment_UsesFinalizedEnvironment(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	for _, secret := range []string{"explicit-secret", "", "line one\nline two\r\n"} {
		t.Run(secret, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			zcodeWriteStoreWithSecret(t, home, secret)
			opts := agent.Options{HomeDir: home, WorkingDir: t.TempDir(), Shell: shell, ExtraEnv: []string{zcodeCredentialSecretEnv + "=wrong", zcodeCredentialSecretEnv + "=" + secret}}
			environment := envutil.PinEnv(os.Environ(), "HOME="+home, "USERPROFILE="+home, zcodeCredentialSecretEnv+"=worker")
			got, err := resolveZCodeAccountEnvironment(t.Context(), opts, providerkit.FinalizeAgentEnv(environment, opts))
			require.NoError(t, err)
			credentials, err := got.credentials.read()
			require.NoError(t, err)
			jwt, err := credentials.get(zcodeCredentialZCodeJWT)
			require.NoError(t, err)
			assert.Equal(t, "fixture-jwt", jwt)
			assert.Equal(t, home, got.home)
		})
	}
}

func TestZCodeAccountEnvironment_UsesProfileAndWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture executes a POSIX shell script")
	}
	t.Parallel()
	home, oldHome, working := t.TempDir(), t.TempDir(), t.TempDir()
	zcodeWriteStoreWithSecret(t, home, "profile-secret")
	require.NoError(t, os.WriteFile(filepath.Join(working, "secret-source"), []byte("profile-secret"), 0o600))
	shell := filepath.Join(t.TempDir(), "sh")
	require.NoError(t, os.WriteFile(shell, []byte("#!/bin/sh\nexport HOME=\"$LEAPMUX_TEST_ACCOUNT_HOME\"\nexport ZCODE_CREDENTIAL_SECRET=\"$(cat secret-source)\"\nexec /bin/sh \"$@\"\n"), 0o700))
	opts := agent.Options{HomeDir: oldHome, WorkingDir: working, Shell: shell, ExtraEnv: []string{zcodeCredentialSecretEnv + "=extra"}}
	environment := envutil.PinEnv(os.Environ(), "HOME="+oldHome, "LEAPMUX_TEST_ACCOUNT_HOME="+home)
	got, err := resolveZCodeAccountEnvironment(context.Background(), opts, providerkit.FinalizeAgentEnv(environment, opts))
	require.NoError(t, err)
	credentials, err := got.credentials.read()
	require.NoError(t, err)
	jwt, err := credentials.get(zcodeCredentialZCodeJWT)
	require.NoError(t, err)
	assert.Equal(t, "fixture-jwt", jwt)
	assert.Equal(t, home, got.home)
}

func TestZCodeAccountEnvironment_UsesTheProfileSelectedProviderRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture executes a POSIX shell script")
	}
	t.Parallel()
	for _, relative := range []bool{false, true} {
		t.Run(fmt.Sprintf("relative=%v", relative), func(t *testing.T) {
			t.Parallel()
			home, working, profileDirectory := t.TempDir(), t.TempDir(), t.TempDir()
			oldPath := filepath.Join(working, "provider.json")
			newPath := filepath.Join(profileDirectory, "provider.json")
			require.NoError(t, os.WriteFile(oldPath, []byte(`{"revision":1}`), 0o600))
			require.NoError(t, os.WriteFile(newPath, []byte(`{"revision":2}`), 0o600))
			selected := newPath
			if relative {
				selected = "provider.json"
			}
			shell := filepath.Join(t.TempDir(), "sh")
			require.NoError(t, os.WriteFile(shell, []byte("#!/bin/sh\nexport ZCODE_BUILTIN_PROVIDER_CONFIG_FILE=\"$LEAPMUX_TEST_SELECTED_RELEASE\"\ncd \"$LEAPMUX_TEST_PROFILE_DIRECTORY\" || exit 2\nexec /bin/sh \"$@\"\n"), 0o700))
			opts := agent.Options{HomeDir: home, WorkingDir: working, Shell: shell}
			environment := envutil.PinEnv(os.Environ(), "HOME="+home, "USERPROFILE="+home,
				zcodeBuiltinProviderConfigEnv+"="+oldPath, "LEAPMUX_TEST_SELECTED_RELEASE="+selected,
				"LEAPMUX_TEST_PROFILE_DIRECTORY="+profileDirectory)
			got, err := resolveZCodeAccountEnvironment(t.Context(), opts, environment)
			require.NoError(t, err)
			expectedDirectory, err := filepath.EvalSymlinks(profileDirectory)
			require.NoError(t, err)
			expectedPath := newPath
			if relative {
				expectedPath = filepath.Join(expectedDirectory, "provider.json")
			}
			assert.Equal(t, expectedPath, got.builtinProviderConfigPath)
			_, revision, err := loadZCodeBuiltinProviderFile(got.builtinProviderConfigPath)
			require.NoError(t, err)
			_, expectedRevision, err := loadZCodeBuiltinProviderFile(expectedPath)
			require.NoError(t, err)
			assert.Equal(t, expectedRevision, revision)
		})
	}
}
