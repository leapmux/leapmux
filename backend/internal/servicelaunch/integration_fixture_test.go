//go:build serviceintegration

package servicelaunch

import (
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/servicelaunch/servicefixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

// This test reads a fixture only. It never installs a native definition or
// asserts that the developer's workstation is a disposable environment.
func TestServiceFixtureAcceptsMatchingAccountDeclaration(t *testing.T) {
	current, err := user.Current()
	require.NoError(t, err)
	expected := servicefixture.Account{AccountID: current.Uid, AccountName: current.Username, DisposableEnvironment: true}
	if runtime.GOOS == "windows" {
		expected.CredentialReference = new("fixture-credential-reference")
	}
	data, err := yaml.Marshal(expected)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "account.yaml")
	require.NoError(t, atomicfile.WriteFileDurable(path, data, 0o600))
	t.Setenv("LEAPMUX_SERVICE_TEST_ACCOUNT_FILE", path)
	assert.Equal(t, expected, readServiceAccountFixture(t))
}

func TestServiceFixtureRejectsUnsafeDeclarations(t *testing.T) {
	current := &user.User{Uid: "501", Username: "fixture-user"}
	valid := "account_id: '501'\naccount_name: fixture-user\ncredential_reference: null\ndisposable_environment: true\n"
	for _, test := range []struct{ name, data string }{
		{"empty", ""},
		{"sequence", "- value"},
		{"numeric account", strings.ReplaceAll(valid, "'501'", "501")},
		{"missing field", strings.ReplaceAll(valid, "credential_reference: null\n", "")},
		{"wrong identifier", strings.ReplaceAll(valid, "'501'", "'502'")},
		{"wrong name", strings.ReplaceAll(valid, "fixture-user", "another-user")},
		{"not disposable", strings.ReplaceAll(valid, "true", "false")},
		{"string disposable", strings.ReplaceAll(valid, "true", "'true'")},
		{"unix credential", strings.ReplaceAll(valid, "null", "credential-store-entry")},
		{"duplicate", valid + "account_id: '501'\n"},
		{"unknown", valid + "password: secret-value\n"},
		{"multiple documents", valid + "---\n" + valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseServiceAccountFixture([]byte(test.data), current, "darwin")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-value")
		})
	}
	_, err := parseServiceAccountFixture([]byte(valid), current, "linux")
	require.NoError(t, err)
	_, err = parseServiceAccountFixture([]byte(valid), current, "windows")
	require.Error(t, err)
	windows := strings.ReplaceAll(valid, "null", "credential-store-entry")
	_, err = parseServiceAccountFixture([]byte(windows), current, "windows")
	require.NoError(t, err)
}
