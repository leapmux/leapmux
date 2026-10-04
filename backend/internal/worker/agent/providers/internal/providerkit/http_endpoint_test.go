package providerkit

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLoopbackHTTPURL(t *testing.T) {
	t.Parallel()

	accepted := map[string]string{
		"http://127.0.0.1:4096":      "http://127.0.0.1:4096",
		"http://127.0.0.1:4096/":     "http://127.0.0.1:4096",
		"http://localhost:80/api/":   "http://localhost:80/api",
		"http://[::1]:9000":          "http://[::1]:9000",
		"  http://127.0.0.2:1234  ":  "http://127.0.0.2:1234",
		"http://LOCALHOST:8080/base": "http://LOCALHOST:8080/base",
	}
	for raw, want := range accepted {
		u, err := ParseLoopbackHTTPURL(raw)
		if assert.NoError(t, err, raw) {
			assert.Equal(t, want, u.String(), raw)
		}
	}

	refused := []string{
		"",
		"127.0.0.1:4096",
		"https://127.0.0.1:4096",
		"ws://127.0.0.1:4096",
		"http://127.0.0.1",
		"http://example.com:80",
		"http://10.0.0.1:80",
		"http://0.0.0.0:80",
		"http://user:pass@127.0.0.1:80",
		"http://127.0.0.1:80/?x=1",
		"http://127.0.0.1:80/#frag",
		"http://%zz",
	}
	for _, raw := range refused {
		_, err := ParseLoopbackHTTPURL(raw)
		assert.Error(t, err, raw)
	}
}

func TestHTTPEndpointURL(t *testing.T) {
	t.Parallel()

	endpoint, err := NewHTTPEndpoint("http://127.0.0.1:4096/api/", nil)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:4096/api", endpoint.BaseURL())
	assert.Equal(t, "http://127.0.0.1:4096/api/sessions/a%20b", endpoint.URL("/sessions/a b", nil))
	assert.Equal(t, "http://127.0.0.1:4096/api/list?cwd=%2Frepo&n=2",
		endpoint.URL("/list", url.Values{"cwd": {"/repo"}, "n": {"2"}}))
}

func TestHTTPEndpointDo(t *testing.T) {
	t.Parallel()

	// The handler runs on the server's goroutine. The mutex gives the race
	// detector the ordering that the request and the reply already imply.
	var mu sync.Mutex
	var gotAuth, gotType, gotAccept, gotMethod, gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
		}
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, `{"id":"s-1","count":2}`)
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		case "/busy":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, "  the session is busy  ")
		case "/garbage":
			_, _ = io.WriteString(w, `not json`)
		}
	}))
	t.Cleanup(server.Close)

	endpoint, err := NewHTTPEndpoint(server.URL, BearerAuth("secret-token"))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	t.Run("sends JSON and decodes the reply", func(t *testing.T) {
		var out struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		}
		require.NoError(t, endpoint.Do(t.Context(), http.MethodPost, "/ok", map[string]any{"text": "hi"}, &out))
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, "s-1", out.ID)
		assert.Equal(t, 2, out.Count)
		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/ok", gotPath)
		assert.Equal(t, "Bearer secret-token", gotAuth)
		assert.Equal(t, "application/json", gotType)
		assert.Equal(t, "application/json", gotAccept)
		assert.Equal(t, map[string]any{"text": "hi"}, gotBody)
	})

	t.Run("sends no body and no content type for a nil body", func(t *testing.T) {
		require.NoError(t, endpoint.Do(t.Context(), http.MethodGet, "/ok", nil, nil))
		mu.Lock()
		defer mu.Unlock()
		assert.Empty(t, gotType)
		assert.Nil(t, gotBody)
	})

	t.Run("leaves out unchanged for an empty reply", func(t *testing.T) {
		out := map[string]any{"kept": true}
		require.NoError(t, endpoint.Do(t.Context(), http.MethodDelete, "/empty", nil, &out))
		assert.Equal(t, map[string]any{"kept": true}, out)
	})

	t.Run("returns the status and the reason of a non-2xx reply", func(t *testing.T) {
		err := endpoint.Do(t.Context(), http.MethodPost, "/busy", map[string]any{}, nil)
		require.Error(t, err)
		assert.True(t, IsHTTPStatus(err, http.StatusConflict))
		assert.False(t, IsHTTPStatus(err, http.StatusNotFound))
		var statusErr *HTTPStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, "the session is busy", statusErr.Body)
		assert.Contains(t, err.Error(), "POST /busy: 409 Conflict: the session is busy")
	})

	t.Run("reports a reply that is not JSON", func(t *testing.T) {
		var out map[string]any
		err := endpoint.Do(t.Context(), http.MethodGet, "/garbage", nil, &out)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode the reply")
	})

	t.Run("reports a body that cannot be encoded", func(t *testing.T) {
		err := endpoint.Do(t.Context(), http.MethodPost, "/ok", map[string]any{"bad": make(chan int)}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "encode the request")
	})

	t.Run("stops at the context deadline", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := endpoint.Do(ctx, http.MethodGet, "/ok", nil, nil)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestHTTPEndpointDoRefusesAnOversizedReply(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for written := 0; written <= MaxEndpointResponseBytes; written += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	err = endpoint.Do(t.Context(), http.MethodGet, "/big", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestHTTPEndpointBasicAuth(t *testing.T) {
	t.Parallel()

	type credential struct {
		user, password string
		ok             bool
	}
	got := make(chan credential, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		got <- credential{user, password, ok}
	}))
	t.Cleanup(server.Close)
	endpoint, err := NewHTTPEndpoint(server.URL, BasicAuth("mimocode", "p@ss"))
	require.NoError(t, err)
	require.NoError(t, endpoint.Do(t.Context(), http.MethodGet, "/", nil, nil))
	assert.Equal(t, credential{"mimocode", "p@ss", true}, <-got)
}

func TestHTTPEndpointWithHeader(t *testing.T) {
	t.Parallel()

	got := make(chan http.Header, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	t.Cleanup(server.Close)
	plain, err := NewHTTPEndpoint(server.URL, BasicAuth("mimocode", "secret"))
	require.NoError(t, err)
	scoped := plain.WithHeader("X-Scope", "/repo")

	t.Run("every request of the copy carries the fixed header and the credential", func(t *testing.T) {
		require.NoError(t, scoped.Do(t.Context(), http.MethodGet, "/a", nil, nil))
		header := <-got
		assert.Equal(t, "/repo", header.Get("X-Scope"))
		user, _, ok := (&http.Request{Header: header}).BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "mimocode", user)
	})

	t.Run("the original endpoint does not change", func(t *testing.T) {
		require.NoError(t, plain.Do(t.Context(), http.MethodGet, "/b", nil, nil))
		assert.Empty(t, (<-got).Get("X-Scope"))
	})

	t.Run("a second copy adds to the first without changing it", func(t *testing.T) {
		both := scoped.WithHeader("X-Other", "1")
		require.NoError(t, both.Do(t.Context(), http.MethodGet, "/c", nil, nil))
		header := <-got
		assert.Equal(t, "/repo", header.Get("X-Scope"))
		assert.Equal(t, "1", header.Get("X-Other"))
		require.NoError(t, scoped.Do(t.Context(), http.MethodGet, "/d", nil, nil))
		assert.Empty(t, (<-got).Get("X-Other"))
	})

	t.Run("a per-request header replaces the fixed value", func(t *testing.T) {
		response, err := scoped.OpenStream(t.Context(), http.MethodGet, "/e", http.Header{"X-Scope": {"/other"}})
		require.NoError(t, err)
		_ = response.Body.Close()
		assert.Equal(t, []string{"/other"}, (<-got).Values("X-Scope"))
	})
}

// A request to a loopback host never takes a proxy from the environment, so no
// request can show the difference. The transport itself states it: the default
// is http.ProxyFromEnvironment, and this endpoint must carry no proxy at all.
func TestHTTPEndpointUsesNoProxy(t *testing.T) {
	t.Parallel()

	endpoint, err := NewHTTPEndpoint("http://127.0.0.1:4096", nil)
	require.NoError(t, err)
	transport, ok := endpoint.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Zero(t, endpoint.client.Timeout, "a stream stays open for the session, so only the context sets a deadline")
}

func TestHTTPEndpointOpenStream(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	endpoint, err := NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)

	t.Run("returns the open stream for a 2xx reply", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		response, err := endpoint.OpenStream(ctx, http.MethodGet, "/events", http.Header{"Accept": {"text/event-stream"}})
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		got := make(chan string, 1)
		go func() {
			_ = ReadSSE(response.Body, 1024, func(event SSEEvent) { got <- string(event.Data) })
		}()
		select {
		case data := <-got:
			assert.Equal(t, "first", data)
		case <-time.After(10 * time.Second):
			t.Fatal("the first event never arrived while the stream stayed open")
		}
	})

	t.Run("returns an HTTPStatusError for a non-2xx reply", func(t *testing.T) {
		response, err := endpoint.OpenStream(t.Context(), http.MethodGet, "/missing", nil)
		assert.Nil(t, response)
		assert.True(t, IsHTTPStatus(err, http.StatusNotFound))
	})
}

// A query travels as a query. A caller that appended `?since_seq=4` to the
// path would reach the server with the `?` escaped into the path, and the
// server would read no parameter at all.
func TestHTTPEndpointQueryVariants(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPaths, gotQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPaths = append(gotPaths, r.URL.Path)
		gotQueries = append(gotQueries, r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	endpoint, err := NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)

	var out struct {
		OK bool `json:"ok"`
	}
	require.NoError(t, endpoint.DoQuery(t.Context(), http.MethodGet, "/models", url.Values{"limit": {"250"}, "cursor": {"a b"}}, nil, &out))
	assert.True(t, out.OK)

	response, err := endpoint.OpenStreamQuery(t.Context(), http.MethodGet, "/events", url.Values{"since_seq": {"4"}}, nil)
	require.NoError(t, err)
	_ = response.Body.Close()

	// A nil query adds nothing, which is what Do and OpenStream pass.
	require.NoError(t, endpoint.DoQuery(t.Context(), http.MethodGet, "/plain", nil, nil, nil))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/models", "/events", "/plain"}, gotPaths)
	assert.Equal(t, []string{"cursor=a+b&limit=250", "since_seq=4", ""}, gotQueries)
}

func TestNewHTTPEndpointRefusesANonLoopbackAddress(t *testing.T) {
	t.Parallel()

	_, err := NewHTTPEndpoint("http://192.168.1.10:8080", BearerAuth("x"))
	assert.Error(t, err)
}

func TestHTTPEndpointOpenWebSocket(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotAuth, gotExtra, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/refused" {
			http.Error(w, `{"code":40101,"msg":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		mu.Lock()
		gotAuth, gotExtra, gotPath = r.Header.Get("Authorization"), r.Header.Get("X-Extra"), r.URL.Path
		mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		kind, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), kind, append([]byte("echo:"), data...))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(server.Close)

	endpoint, err := NewHTTPEndpoint(server.URL+"/api", BearerAuth("secret"))
	require.NoError(t, err)

	t.Run("sends the credential and the caller's headers on the upgrade", func(t *testing.T) {
		conn, err := endpoint.OpenWebSocket(t.Context(), "/ws", &websocket.DialOptions{HTTPHeader: http.Header{"X-Extra": {"kept"}}})
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()
		require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte("hi")))
		_, data, err := conn.Read(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "echo:hi", string(data))
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, "Bearer secret", gotAuth)
		assert.Equal(t, "kept", gotExtra)
		assert.Equal(t, "/api/ws", gotPath)
	})

	t.Run("accepts nil options", func(t *testing.T) {
		conn, err := endpoint.OpenWebSocket(t.Context(), "/ws", nil)
		require.NoError(t, err)
		_ = conn.CloseNow()
	})

	t.Run("returns an HTTPStatusError for a refused upgrade", func(t *testing.T) {
		conn, err := endpoint.OpenWebSocket(t.Context(), "/refused", nil)
		assert.Nil(t, conn)
		assert.True(t, IsHTTPStatus(err, http.StatusUnauthorized), "got %v", err)
		var statusErr *HTTPStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Contains(t, statusErr.Body, "unauthorized")
	})

	t.Run("fails when the context ends before the dial", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		conn, err := endpoint.OpenWebSocket(ctx, "/ws", nil)
		assert.Nil(t, conn)
		assert.Error(t, err)
	})
}

// A local agent API has no reason to redirect, and a redirect is the one way a
// reply can move a request off the loopback address that NewHTTPEndpoint
// checked. So every entry point returns the 3xx reply as an HTTPStatusError, and
// the target of the redirect receives nothing: no body, no fixed header.
func TestHTTPEndpointRefusesARedirect(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		targetHits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusFound
		if r.Method == http.MethodPost {
			code = http.StatusTemporaryRedirect
		}
		http.Redirect(w, r, target.URL+r.URL.Path, code)
	}))
	t.Cleanup(source.Close)
	base, err := NewHTTPEndpoint(source.URL, BearerAuth("secret"))
	require.NoError(t, err)
	endpoint := base.WithHeader("X-Directory", "/private/project")

	t.Run("do with a GET", func(t *testing.T) {
		err := endpoint.Do(t.Context(), http.MethodGet, "/session", nil, nil)
		assert.True(t, IsHTTPStatus(err, http.StatusFound), "got %v", err)
	})
	t.Run("do with a POST body", func(t *testing.T) {
		err := endpoint.Do(t.Context(), http.MethodPost, "/prompt", map[string]string{"text": "secret prompt"}, nil)
		assert.True(t, IsHTTPStatus(err, http.StatusTemporaryRedirect), "got %v", err)
	})
	t.Run("open a stream", func(t *testing.T) {
		response, err := endpoint.OpenStream(t.Context(), http.MethodGet, "/events", nil)
		assert.Nil(t, response)
		assert.True(t, IsHTTPStatus(err, http.StatusFound), "got %v", err)
	})
	t.Run("open a WebSocket", func(t *testing.T) {
		conn, err := endpoint.OpenWebSocket(t.Context(), "/ws", nil)
		assert.Nil(t, conn)
		assert.True(t, IsHTTPStatus(err, http.StatusFound), "got %v", err)
	})

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, targetHits, "a redirect must not move a request to another address")
}

// The address check in ParseLoopbackHTTPURL reads the URL, and `localhost` is a
// NAME that the system resolver answers. The transport checks each connection
// again, on the address that it really dials, so no resolver answer can move the
// traffic off the machine.
func TestHTTPEndpointDialsLoopbackAddressesOnly(t *testing.T) {
	t.Parallel()

	endpoint, err := NewHTTPEndpoint("http://localhost:4096", nil)
	require.NoError(t, err)
	transport, ok := endpoint.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.DialContext)

	// 192.0.2.1 is TEST-NET-1 (RFC 5737), which no network routes. The check
	// refuses it before the connection starts.
	conn, err := transport.DialContext(t.Context(), "tcp", "192.0.2.1:9")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a loopback address")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	conn, err = transport.DialContext(t.Context(), "tcp", listener.Addr().String())
	require.NoError(t, err, "a loopback address must stay reachable")
	_ = conn.Close()
}

// WithHeader promises that every request carries its headers, and the
// WebSocket upgrade is a request too. A caller's own header of the same name
// replaces the fixed value, as it does for OpenStream.
func TestHTTPEndpointOpenWebSocketSendsTheFixedHeaders(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotDirectory, gotOverride string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotDirectory, gotOverride = r.Header.Get("X-Directory"), r.Header.Get("X-Scope")
		mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(server.Close)
	base, err := NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	endpoint := base.WithHeader("X-Directory", "/work").WithHeader("X-Scope", "fixed")

	conn, err := endpoint.OpenWebSocket(t.Context(), "/ws", &websocket.DialOptions{HTTPHeader: http.Header{"X-Scope": {"per-call"}}})
	require.NoError(t, err)
	_ = conn.CloseNow()
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "/work", gotDirectory)
	assert.Equal(t, "per-call", gotOverride)
}

// An error reply with no body states the status alone, and a long body keeps
// only its start: the reason is there, and a log line has no use for the rest.
func TestHTTPStatusErrorKeepsTheStartOfTheReason(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("r", maxErrorBodyBytes) + "the rest"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/long":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, long)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)

	err = endpoint.Do(t.Context(), http.MethodGet, "/empty", nil, nil)
	var statusErr *HTTPStatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Empty(t, statusErr.Body)
	assert.Equal(t, "GET /empty: 503 Service Unavailable", err.Error())

	err = endpoint.Do(t.Context(), http.MethodGet, "/long", nil, nil)
	require.ErrorAs(t, err, &statusErr)
	assert.Len(t, statusErr.Body, maxErrorBodyBytes)
	assert.NotContains(t, statusErr.Body, "the rest")
}

// The dialer check reads the address that the dialer really connects to. An
// IPv6 loopback address passes, and an address that it cannot read fails.
func TestRefuseNonLoopbackAddress(t *testing.T) {
	t.Parallel()
	assert.NoError(t, refuseNonLoopbackAddress("tcp", "127.0.0.1:80", nil))
	assert.NoError(t, refuseNonLoopbackAddress("tcp6", "[::1]:80", nil))
	assert.ErrorContains(t, refuseNonLoopbackAddress("tcp", "[::ffff:10.0.0.1]:80", nil), "not a loopback address")
	assert.ErrorContains(t, refuseNonLoopbackAddress("tcp", "localhost:80", nil), "not a loopback address",
		"a name is not an address, and the dialer states an address")
	assert.ErrorContains(t, refuseNonLoopbackAddress("tcp", "no-port", nil), "cannot be read")
}
