package control

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultDeviceName_FallsBackToTheHostname exercises the case where
// neither USER nor USERNAME is set (containers, minimal CI runners). The
// result must still identify the machine -- never be empty.
func TestDefaultDeviceName_FallsBackToTheHostname(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")
	got := DefaultDeviceName()
	assert.NotEmpty(t, got)
	assert.NotContains(t, got, "@", "with no user the label is the hostname alone")
}

// TestDefaultDeviceName_PrefersUSEROverUSERNAME documents the POSIX-first
// lookup order: USER wins on Linux and macOS; USERNAME is the Windows
// fallback.
func TestDefaultDeviceName_PrefersUSEROverUSERNAME(t *testing.T) {
	t.Setenv("USER", "alice")
	t.Setenv("USERNAME", "bob")
	assert.True(t, strings.HasPrefix(DefaultDeviceName(), "alice@"))
}

// TestDefaultDeviceName_FallsBackToUSERNAME covers the Windows path, where
// only USERNAME is populated.
func TestDefaultDeviceName_FallsBackToUSERNAME(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "winuser")
	assert.True(t, strings.HasPrefix(DefaultDeviceName(), "winuser@"))
}

// TestDefaultDeviceName_IsTheOnlySourceOfTheLabel is the point of having one
// function, and it reads the CALL SITES rather than the function.
//
// The two legs used to compute the label differently -- "user@host" for a
// login and a bare hostname for a step-up -- so the page that asked a person
// to approve a step-up specified a device that matched nothing in their
// credential list. A test that called DefaultDeviceName twice and compared the
// results could not see that, because the defect was never in the function: it
// was that one call site did not use it.
func TestDefaultDeviceName_IsTheOnlySourceOfTheLabel(t *testing.T) {
	t.Parallel()

	// Every file that puts an installation_name on the wire must take the
	// label from the one function. cmd/auth.go reaches it through its
	// --device-name flag default, which is the same source with an override.
	for _, path := range []string{"elevate.go", "cmd/auth.go"} {
		src, err := os.ReadFile(path)
		require.NoError(t, err, "%s must be readable", path)
		text := string(src)
		require.Contains(t, text, "installation_name",
			"%s is listed here because it writes an installation_name; remove it if that stopped being true", path)
		assert.Contains(t, text, "DefaultDeviceName()",
			"%s writes an installation_name, so it must take the label from DefaultDeviceName rather than build its own", path)
	}
}

func TestDeviceGrant_PollIntervalAndDeadline(t *testing.T) {
	t.Parallel()

	grant := DeviceGrant{Interval: 7, ExpiresIn: 600}
	assert.Equal(t, 7*time.Second, grant.PollInterval())

	now := time.Now()
	assert.Equal(t, now.Add(10*time.Minute), grant.Deadline(now))

	// A hub that specifies no interval gets the fallback, and so does one
	// that specifies a negative one.
	assert.Equal(t, DeviceCodePollFallback, DeviceGrant{}.PollInterval())
	assert.Equal(t, DeviceCodePollFallback, DeviceGrant{Interval: -1}.PollInterval())
}

// answerWith serves one body and status, and returns the response the
// helpers read.
func answerWith(t *testing.T, status int, contentType, body string) *http.Response {
	t.Helper()
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestDeviceFlowError_MapsTheTwoAnswersThatMeanPollAgain(t *testing.T) {
	t.Parallel()

	pending := answerWith(t, http.StatusBadRequest, "application/json", `{"error":"authorization_pending"}`)
	assert.ErrorIs(t, DeviceFlowError(pending), ErrAuthorizationPending)

	slow := answerWith(t, http.StatusBadRequest, "application/json", `{"error":"slow_down"}`)
	assert.ErrorIs(t, DeviceFlowError(slow), ErrSlowDown)
}

func TestDeviceFlowError_CarriesTheHubsReason(t *testing.T) {
	t.Parallel()

	denied := answerWith(t, http.StatusBadRequest, "application/json",
		`{"error":"access_denied","error_description":"the request was declined"}`)
	err := DeviceFlowError(denied)
	require.Error(t, err)
	assert.Equal(t, "access_denied: the request was declined", err.Error())

	bare := answerWith(t, http.StatusBadRequest, "application/json", `{"error":"expired_token"}`)
	assert.Equal(t, "expired_token", DeviceFlowError(bare).Error())
}

// TestDeviceFlowError_ReportsTheStatusWhenTheBodyCarriesNoOAuthError is the
// transport failure a proxy produces.
//
// Formatting the decoded fields gave the user the error ": ", which states
// neither the failure nor the address. The status is what is left to report.
func TestDeviceFlowError_ReportsTheStatusWhenTheBodyCarriesNoOAuthError(t *testing.T) {
	t.Parallel()

	html := answerWith(t, http.StatusBadGateway, "text/html", "<html>502 Bad Gateway</html>")
	err := DeviceFlowError(html)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")

	empty := answerWith(t, http.StatusServiceUnavailable, "application/json", `{}`)
	assert.Contains(t, DeviceFlowError(empty).Error(), "503")

	truncated := answerWith(t, http.StatusBadRequest, "application/json", `{"error":`)
	assert.Contains(t, DeviceFlowError(truncated).Error(), "400")
}
