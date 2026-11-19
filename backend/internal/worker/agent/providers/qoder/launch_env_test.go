package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialsFromEnvValidatesExplicitCredentials(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`null`, `[]`, `"token"`, `{}`, `{"token":""}`, `{"token":"  "}`, `{"token":1}`, `{"token":"secret","expiresAt":0}`, `{"token":"secret","expiresAt":-1}`, `{"token":"secret","expiresAt":999999999999999999999}`, `{"token":"secret","expiresAt":1.5}`, `{"token":"secret","expiresAt":"tomorrow"}`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := qoderCredentialsFromEnv([]string{"QODER_JOB_TOKEN=" + raw})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret")
		})
	}
	_, err := qoderCredentialsFromEnv([]string{`QODER_JOB_TOKEN={"token":"secret"}`, "QODER_SERVICE_ACCOUNT_KEY=other-secret"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
}

func TestCredentialsFromEnvPreservesTheConfiguredSource(t *testing.T) {
	t.Parallel()
	local, err := qoderCredentialsFromEnv(nil)
	require.NoError(t, err)
	assert.Equal(t, qoderCredentials{}, local)
	job, err := qoderCredentialsFromEnv([]string{`QODER_JOB_TOKEN={"token":"controlled-key","expiresAt":1799999999999}`})
	require.NoError(t, err)
	assert.Equal(t, "controlled-key", job.jobToken)
	require.NotNil(t, job.jobTokenExpiresAt)
	assert.Equal(t, int64(1799999999999), *job.jobTokenExpiresAt)
	account, err := qoderCredentialsFromEnv([]string{"QODER_SERVICE_ACCOUNT_KEY=controlled-account"})
	require.NoError(t, err)
	assert.Equal(t, "controlled-account", account.serviceAccountKey)
}
