package qoder

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/util/envutil"
)

var qoderStrippedEnvKeys = []string{
	"QODER_SESSION_ID", "QODER_PID", "QODER_SESSION_TYPE", "QODER_REMOTE_CHILD",
	"QODER_AGENT_SDK_ENTRYPOINT", "QODER_SDK_AUTH_PAYLOAD_FILE", "QODER_SDK_AUTH_CONFIG_DIR",
	"QODER_SDK_ACCESS_TOKEN", "QODER_CLI", "QODERCN_CLI", "QODER_PERSONAL_ACCESS_TOKEN",
}

var qoderLaunchEnvPins = []string{
	qoderSDKCustomProvidersEnv,
	"QODER_SITE=GLOBAL", "QODER_NO_RC=1", "QODER_FORCE_FILE_STORAGE=1", "QODER_HTTPDNS=0",
	"QODER_MCP_LAZY=0",
}

const qoderSDKCustomProvidersEnv = "QODER_SDK_CUSTOM_BASE_URL_BYOK=1"

type qoderCredentials struct {
	jobToken          string
	jobTokenExpiresAt *int64
	serviceAccountKey string
}

// qoderCredentialsFromEnv reads job-token and service-account credentials for the SDK.
// Without one, the SDK borrows the saved local CLI login.
func qoderCredentialsFromEnv(environ []string) (qoderCredentials, error) {
	value := func(key string) string {
		values := envutil.ValuesFor(environ, key)
		if len(values) == 0 {
			return ""
		}
		return values[len(values)-1]
	}
	credentials := qoderCredentials{serviceAccountKey: value("QODER_SERVICE_ACCOUNT_KEY")}
	if raw := value("QODER_JOB_TOKEN"); raw != "" {
		if credentials.serviceAccountKey != "" {
			return qoderCredentials{}, fmt.Errorf("qoder job tokens and service account keys cannot be combined")
		}
		var token struct {
			Token     string `json:"token"`
			ExpiresAt *int64 `json:"expiresAt"`
		}
		if err := json.Unmarshal([]byte(raw), &token); err != nil || strings.TrimSpace(token.Token) == "" {
			return qoderCredentials{}, fmt.Errorf("QODER_JOB_TOKEN must be a JSON object with a non-empty token")
		}
		if token.ExpiresAt != nil && *token.ExpiresAt < 1_000_000_000_000 {
			return qoderCredentials{}, fmt.Errorf("QODER_JOB_TOKEN expiresAt must be a Unix timestamp in milliseconds")
		}
		credentials.jobToken, credentials.jobTokenExpiresAt = token.Token, token.ExpiresAt
	}
	return credentials, nil
}
