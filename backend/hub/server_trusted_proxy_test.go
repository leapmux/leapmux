package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/requestsource"
)

func TestServer_AppliesTrustedProxyIdentityOutsideTheHandlerStack(t *testing.T) {
	base := "127.0.0.1:" + strconv.Itoa(freePorts(t, 1)[0])
	frontend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Client-IP", peer.ClientIP(r.Context()))
		w.Header().Set("X-Test-Scheme", r.URL.Scheme)
		w.Header().Set("X-Test-Remote-Addr", r.RemoteAddr)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := startTestServer(t, &config.Config{Listen: base}, WithFrontendHandler(frontend))
	requireAnswers(t, base)

	client := &http.Client{Timeout: 10 * time.Second}
	request := func() *http.Request {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+base+"/", nil)
		require.NoError(t, err)
		req.Header.Set("Forwarded", "for=198.51.100.7;proto=https")
		return req
	}

	direct, err := client.Do(request())
	require.NoError(t, err)
	require.NoError(t, direct.Body.Close())
	assert.Equal(t, "127.0.0.1", direct.Header.Get("X-Test-Client-IP"))
	assert.Equal(t, "http", direct.Header.Get("X-Test-Scheme"))
	assert.Contains(t, direct.Header.Get("X-Test-Remote-Addr"), "127.0.0.1:")

	require.NoError(t, srv.settings.Update(context.Background(), requestsource.KeyTrustedProxyRanges,
		json.RawMessage(`["127.0.0.1"]`)))
	proxied, err := client.Do(request())
	require.NoError(t, err)
	require.NoError(t, proxied.Body.Close())
	assert.Equal(t, "198.51.100.7", proxied.Header.Get("X-Test-Client-IP"))
	assert.Equal(t, "https", proxied.Header.Get("X-Test-Scheme"))
	assert.Contains(t, proxied.Header.Get("X-Test-Remote-Addr"), "127.0.0.1:",
		"request-source handling must not replace the physical peer")
}
