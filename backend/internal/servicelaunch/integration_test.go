//go:build serviceintegration

// This file holds the SHARED half of the explicit native integration
// target (`task test-cli-services`): the one TestNativeServiceLifecycle
// and the account-fixture reader. The platform files provide the
// native fixture and permission helpers; they do not redeclare the
// shared test.
//
// Ordinary `task test` never reaches this file: the target builds the
// CLI binary first and runs with -tags=serviceintegration only.
package servicelaunch

import (
	"fmt"
	"os"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// serviceAccountFixture is the exact YAML shape the private
// SERVICE_ACCOUNT_FILE holds. It contains NO password.
type serviceAccountFixture struct {
	AccountID             string  `yaml:"account_id"`
	AccountName           string  `yaml:"account_name"`
	CredentialReference   *string `yaml:"credential_reference"`
	DisposableEnvironment bool    `yaml:"disposable_environment"`
}

// readServiceAccountFixture loads and validates the fixture. A missing
// fixture, a non-disposable one, or one naming another account than the
// test process runs as is refused BEFORE any native state changes.
func readServiceAccountFixture(t *testing.T) serviceAccountFixture {
	t.Helper()
	path := os.Getenv("LEAPMUX_SERVICE_TEST_ACCOUNT_FILE")
	require.NotEmpty(t, path,
		"LEAPMUX_SERVICE_TEST_ACCOUNT_FILE must name the private native-account fixture; run this through `task test-cli-services SERVICE_ACCOUNT_FILE=...`")

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read the fixture")
	var fixture serviceAccountFixture
	require.NoError(t, yaml.Unmarshal(data, &fixture), "parse the fixture")

	require.Equal(t, "yes-prefix", fmt.Sprint(fixture.DisposableEnvironment == false), "the fixture must state disposable_environment")
	require.True(t, fixture.DisposableEnvironment,
		"the fixture must declare a disposable environment; actual logout and reboot acceptance runs there")
	require.NotEmpty(t, fixture.AccountID)
	require.NotEmpty(t, fixture.AccountName)
	require.Nil(t, fixture.CredentialReference,
		"a Unix fixture holds credential_reference: null; a Windows one names the OS credential store entry, never a password")

	// The fixture's account must BE the account running the test: the
	// native permission brokers authorize this account, and installing
	// under another one is not what this target promises.
	current, err := user.Current()
	require.NoError(t, err)
	require.Equal(t, current.Uid, fixture.AccountID,
		"the fixture names account %s but the test runs as %s; run the target as the disposable account", fixture.AccountID, current.Uid)
	return fixture
}

// testBinaryPath names the just-built CLI the tests install.
func testBinaryPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("LEAPMUX_SERVICE_TEST_BINARY")
	require.NotEmpty(t, path,
		"LEAPMUX_SERVICE_TEST_BINARY must name the just-built leapmux executable; run this through `task test-cli-services`")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	assert.NotContains(t, path, "PATH", "the tests install the built binary, never a PATH lookup")
	return path
}
