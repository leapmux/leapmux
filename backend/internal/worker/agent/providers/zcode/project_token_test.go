package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// zcodeFakeProjectAPI serves the project API of one account over TLS.
type zcodeFakeProjectAPI struct {
	t     *testing.T
	clock quartz.Clock

	mu sync.Mutex
	// organizations is the data of getCustomerInfo.
	organizations string
	// keys maps "<organization>/<project>" to the data of its API key list.
	keys map[string]string
	// mintStatus is the HTTP status of a mint, 200 when zero.
	mintStatus int
	// mintData overrides the data of a successful mint.
	mintData string
	// release, when set, holds each mint until it closes.
	release chan struct{}
	calls   []string

	mints atomic.Int32
}

func (f *zcodeFakeProjectAPI) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.EscapedPath()+" auth="+r.Header.Get("Authorization")+
		" org="+r.Header.Get("bigmodel-organization")+" project="+r.Header.Get("bigmodel-project"))
	organizations, keys, status, data, release := f.organizations, f.keys, f.mintStatus, f.mintData, f.release
	f.mu.Unlock()

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/biz/customer/getCustomerInfo":
		_, _ = io.WriteString(w, `{"code":200,"data":`+organizations+`}`)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/api_keys"):
		parts := strings.Split(strings.TrimPrefix(path, "/api/biz/v1/organization/"), "/")
		_, _ = io.WriteString(w, `{"code":"200","data":`+keys[parts[0]+"/"+parts[2]]+`}`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/access_tokens"):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(f.t, map[string]string{"clientType": "zcode", "clientVersion": "3.16.3"}, body)
		if release != nil {
			<-release
		}
		n := f.mints.Add(1)
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if data == "" {
			// The shape of a live reply, which states "enabled" and no "enable".
			expiresAt := f.clock.Now().Add(time.Hour).Unix()
			data = fmt.Sprintf(`{"accessToken":"token-%d","enabled":true,"expiresAt":%d,"expiresIn":3600,"tokenType":"Bearer"}`, n, expiresAt)
		}
		_, _ = io.WriteString(w, `{"code":0,"data":`+data+`}`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/api_keys"):
		f.t.Errorf("LeapMux must never create an API key: %s", path)
		w.WriteHeader(http.StatusForbidden)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *zcodeFakeProjectAPI) set(update func(*zcodeFakeProjectAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f)
}

func (f *zcodeFakeProjectAPI) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *zcodeFakeProjectAPI) count(prefix string) int {
	n := 0
	for _, call := range f.recorded() {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

// zcodeDefaultOrganizations is an account with a team organization and a default one.
// The default organization holds a team project, its default project, and another one.
const zcodeDefaultOrganizations = `{"organizations":[
  {"organizationId":"org-team","organizationName":"Team Org","projects":[{"projectId":"p-x","projectName":"x","projectType":1}]},
  {"organizationId":"org-default","organizationName":"默认机构 of user","projects":[
    {"projectId":"p-team","projectName":"team","projectType":"2"},
    {"projectId":"p-other","projectName":"other","projectType":1},
    {"projectId":"p-default","projectName":"默认项目","projectType":1}]}]}`

// newZCodeTestProjectTokens returns a token client against a fake project API that
// serves both families, and the mock clock that it runs on.
func newZCodeTestProjectTokens(t *testing.T) (*zcodeProjectTokenClient, *zcodeFakeProjectAPI, *quartz.Mock) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	api := &zcodeFakeProjectAPI{
		t:             t,
		clock:         clock,
		organizations: zcodeDefaultOrganizations,
		keys: map[string]string{
			"org-default/p-default": `[{"apiKey":"key-other","name":"something"},{"apiKey":"key-1","name":"zcode-api-key"}]`,
		},
	}
	server, client := zcodeTestStartPlanServer(t, api.handle)
	return newZCodeProjectTokenClient(client, zcodeAccountEndpoints{zai: server.URL, bigModel: server.URL}, clock, func() float64 { return 0 }), api, clock
}

func zcodeTestTokenRequest() zcodeProjectTokenRequest {
	return zcodeProjectTokenRequest{family: zcodeFamilyZAI, loginToken: "login", accountID: "user-1", appVersion: "3.16.3"}
}

func TestZCodeProjectTokens_DiscoversTheDefaultProjectAndMints(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	got, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, zcodeProjectToken{token: "token-1", apiKeyID: "key-1"}, got)
	assert.Equal(t, []string{
		"GET /api/biz/customer/getCustomerInfo auth=login org= project=",
		"GET /api/biz/v1/organization/org-default/projects/p-default/api_keys auth=Bearer login org= project=",
		"POST /api/biz/v1/organization/org-default/projects/p-default/api_keys/key-1/access_tokens auth=Bearer login org= project=",
	}, api.recorded(), "the default organization and project, without its team project, as the desktop chooses them")

	again, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, got, again)
	assert.Len(t, api.recorded(), 3, "a valid token and a known location send no request")
}

func TestZCodeProjectTokens_BigModelSendsTheRawLoginToken(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	req := zcodeTestTokenRequest()
	req.family = zcodeFamilyBigModel
	req.loginToken = "bearer   login"
	_, err := tokens.resolve(context.Background(), zcodeCredentials{}, req)
	require.NoError(t, err)
	assert.Equal(t, 1, api.count("GET /api/biz/v1/organization/org-default/projects/p-default/api_keys auth=login "),
		"only Z.ai sends the key list a Bearer token, and a stored scheme is stripped in any case")
}

func TestZCodeProjectTokens_UsesTheDesktopsCachedLocation(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	key := zcodeProjectLocationKey(tokens.endpoints.zai, "zai", "user-1", nil)
	credentials := zcodeTestReadCredentials(t, zcodeTestCredentialStore(t, map[string]string{
		key: `{"organizationId":"org-cached","projectId":"p-cached","apiKeyId":"key-cached"}`,
	}))
	got, err := tokens.resolve(context.Background(), credentials, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "key-cached", got.apiKeyID)
	assert.Equal(t, []string{
		"POST /api/biz/v1/organization/org-cached/projects/p-cached/api_keys/key-cached/access_tokens auth=Bearer login org= project=",
	}, api.recorded(), "a cached location needs no discovery")
}

func TestZCodeProjectTokens_NeverCreatesAnAPIKey(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	api.set(func(f *zcodeFakeProjectAPI) {
		f.keys["org-default/p-default"] = `[{"apiKey":"key-other","name":"something"}]`
	})
	_, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.ErrorIs(t, err, errZCodeProjectAPIKeyMissing)
	assert.Contains(t, err.Error(), "use the coding plan in the ZCode application once")
	assert.Zero(t, api.count("POST /api/biz/v1/organization/org-default/projects/p-default/api_keys "), "no key is created")
}

func TestZCodeProjectTokens_RefreshesBeforeTheTokenExpires(t *testing.T) {
	t.Parallel()

	tokens, api, clock := newZCodeTestProjectTokens(t)
	_, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)

	clock.Advance(time.Hour - zcodeProjectTokenRefreshMargin - time.Second).MustWait(testutil.DeadlineContext(t))
	got, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "token-1", got.token, "the token is still outside its refresh margin")

	clock.Advance(2 * time.Second).MustWait(testutil.DeadlineContext(t))
	got, err = tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "token-2", got.token, "the token is minted again two minutes before it expires")
	assert.Equal(t, int32(2), api.mints.Load())
}

// Each sign-in mints under a scope of its own, and the desktop renews the sign-in,
// so the cache keeps no token of an earlier sign-in once that token expires.
func TestZCodeProjectTokens_DropsTheExpiredTokensOfEarlierSignIns(t *testing.T) {
	t.Parallel()

	tokens, _, clock := newZCodeTestProjectTokens(t)
	cached := func() int {
		tokens.mu.Lock()
		defer tokens.mu.Unlock()
		return len(tokens.tokens)
	}
	_, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)

	renewed := zcodeTestTokenRequest()
	renewed.loginToken = "login-2"
	_, err = tokens.resolve(context.Background(), zcodeCredentials{}, renewed)
	require.NoError(t, err)
	assert.Equal(t, 2, cached(), "a token that is still valid stays")

	clock.Advance(time.Hour).MustWait(testutil.DeadlineContext(t))
	renewed.loginToken = "login-3"
	_, err = tokens.resolve(context.Background(), zcodeCredentials{}, renewed)
	require.NoError(t, err)
	assert.Equal(t, 1, cached(), "the expired tokens are gone")
}

func TestZCodeProjectTokens_MintsAgainForARejectedToken(t *testing.T) {
	t.Parallel()

	tokens, _, _ := newZCodeTestProjectTokens(t)
	first, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)

	req := zcodeTestTokenRequest()
	req.rejectedFingerprint = zcodeTokenFingerprint("another-token")
	same, err := tokens.resolve(context.Background(), zcodeCredentials{}, req)
	require.NoError(t, err)
	assert.Equal(t, first, same, "a fingerprint of another token keeps the cached one")

	req.rejectedFingerprint = zcodeTokenFingerprint(first.token)
	fresh, err := tokens.resolve(context.Background(), zcodeCredentials{}, req)
	require.NoError(t, err)
	assert.Equal(t, "token-2", fresh.token)
}

func TestZCodeProjectTokens_KeepsAValidTokenThroughATransientFailure(t *testing.T) {
	t.Parallel()

	tokens, api, clock := newZCodeTestProjectTokens(t)
	_, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)

	api.set(func(f *zcodeFakeProjectAPI) { f.mintStatus = http.StatusServiceUnavailable })
	clock.Advance(time.Hour - zcodeProjectTokenRefreshMargin).MustWait(testutil.DeadlineContext(t))
	got, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "token-1", got.token, "a token that is still valid serves through the outage")

	clock.Advance(zcodeProjectTokenRefreshMargin).MustWait(testutil.DeadlineContext(t))
	_, err = tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.ErrorIs(t, err, errZCodeProjectTransient, "an expired token does not serve")
}

func TestZCodeProjectTokens_ForgetsALocationThatTheAPINoLongerKnows(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	key := zcodeProjectLocationKey(tokens.endpoints.zai, "zai", "user-1", nil)
	credentials := zcodeTestReadCredentials(t, zcodeTestCredentialStore(t, map[string]string{
		key: `{"organizationId":"org-gone","projectId":"p-gone","apiKeyId":"key-gone"}`,
	}))
	api.set(func(f *zcodeFakeProjectAPI) { f.mintStatus = http.StatusNotFound })
	_, err := tokens.resolve(context.Background(), credentials, zcodeTestTokenRequest())
	require.ErrorIs(t, err, errZCodeProjectInvalidReply)

	api.set(func(f *zcodeFakeProjectAPI) { f.mintStatus = 0 })
	got, err := tokens.resolve(context.Background(), credentials, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "key-1", got.apiKeyID, "LeapMux cannot delete the desktop's stale entry, so it discovers the location again")
	assert.Equal(t, 1, api.count("GET /api/biz/customer/getCustomerInfo"))
}

func TestZCodeProjectTokens_RefusesAnUnusableMint(t *testing.T) {
	t.Parallel()

	for name, data := range map[string]string{
		"a disabled project":    `{"enable":false}`,
		"another token type":    `{"enable":true,"accessToken":"t","tokenType":"Basic","expiresIn":3600,"expiresAt":9999999999}`,
		"no lifetime":           `{"enable":true,"accessToken":"t","tokenType":"Bearer","expiresAt":9999999999}`,
		"an expiry in the past": `{"enable":true,"accessToken":"t","tokenType":"Bearer","expiresIn":3600,"expiresAt":1}`,
		"no token":              `{"tokenType":"Bearer","expiresIn":3600,"expiresAt":9999999999}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tokens, api, _ := newZCodeTestProjectTokens(t)
			api.set(func(f *zcodeFakeProjectAPI) { f.mintData = data })
			_, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
			if name == "a disabled project" {
				require.ErrorIs(t, err, errZCodeProjectDisabled)
				return
			}
			require.ErrorIs(t, err, errZCodeProjectInvalidReply)
		})
	}
}

// The desktop's schema reads "enable" and ignores the "enabled" that the live API
// states, so a reply with "enabled": false still grants its token.
func TestZCodeProjectTokens_ReadsEnableAsTheDesktopDoes(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	api.set(func(f *zcodeFakeProjectAPI) {
		f.mintData = `{"accessToken":"t","enabled":false,"expiresAt":9999999999,"expiresIn":3600,"tokenType":"Bearer"}`
	})
	got, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "t", got.token)
}

func TestZCodeProjectTokens_TeamPlan(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	api.set(func(f *zcodeFakeProjectAPI) {
		f.organizations = `{"organizations":[{"organizationId":" org-t ","projects":[{"projectId":"proj-t","projectType":"2"}]}]}`
		f.keys["org-t/proj-t"] = `[{"apiKey":"key-personal","name":"zcode-team-api-key","keyType":1},{"apiKey":"key-team","name":"zcode-team-api-key","keyType":2}]`
	})
	req := zcodeTestTokenRequest()
	req.family = zcodeFamilyBigModel
	req.team = &zcodeTeamProject{organizationID: "org-t", projectID: "proj-t", productID: "prod"}
	got, err := tokens.resolve(context.Background(), zcodeCredentials{}, req)
	require.NoError(t, err)
	assert.Equal(t, "key-team", got.apiKeyID, "a team plan takes the team key")
	assert.Equal(t, 1, api.count("GET /api/biz/v1/organization/org-t/projects/proj-t/api_keys auth=login org=org-t project=proj-t"))

	req.team = &zcodeTeamProject{organizationID: "org-t", projectID: "proj-missing"}
	_, err = tokens.resolve(context.Background(), zcodeCredentials{}, req)
	require.ErrorIs(t, err, errZCodeProjectUnavailable)
}

func TestZCodeProjectTokens_RequiresASignIn(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	for _, req := range []zcodeProjectTokenRequest{
		{family: zcodeFamilyZAI, loginToken: " \t ", accountID: "user-1"},
		{family: zcodeFamilyZAI, loginToken: "login", accountID: " "},
	} {
		_, err := tokens.resolve(context.Background(), zcodeCredentials{}, req)
		require.ErrorIs(t, err, errZCodeProjectLoginRequired)
	}
	assert.Empty(t, api.recorded())
}

func TestZCodeProjectTokens_ConcurrentRequestsShareOneMint(t *testing.T) {
	t.Parallel()

	tokens, api, _ := newZCodeTestProjectTokens(t)
	key := zcodeProjectLocationKey(tokens.endpoints.zai, "zai", "user-1", nil)
	credentials := zcodeTestReadCredentials(t, zcodeTestCredentialStore(t, map[string]string{
		key: `{"organizationId":"org-default","projectId":"p-default","apiKeyId":"key-1"}`,
	}))
	release := make(chan struct{})
	api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
	results := make(chan zcodeProjectToken, 2)
	for range 2 {
		go func() {
			got, err := tokens.resolve(context.Background(), credentials, zcodeTestTokenRequest())
			assert.NoError(t, err)
			results <- got
		}()
	}
	require.Eventually(t, func() bool { return tokens.mints.waitingInAll() == 2 }, 30*time.Second, time.Millisecond)
	close(release)
	assert.Equal(t, "token-1", (<-results).token)
	assert.Equal(t, "token-1", (<-results).token)
	assert.Equal(t, int32(1), api.mints.Load())
}

// call reads each reply of the project API as the desktop's readData does: only
// an envelope code of 0 or 200 carries data, and a status that can pass is
// transient.
func TestZCodeProjectTokenClient_CallClassifiesReplies(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
		err    error
	}{
		{name: "code 0", body: `{"code":0,"data":{"a":1}}`, want: `{"a":1}`},
		{name: "code 200", body: `{"code":200,"data":2}`, want: `2`},
		{name: "code 0 as a string", body: `{"code":"0","data":3}`, want: `3`},
		{name: "code 200 as a string", body: `{"code":"200","data":4}`, want: `4`},
		{name: "another code", body: `{"code":1001,"data":{}}`, err: errZCodeProjectInvalidReply},
		{name: "no code", body: `{"data":{}}`, err: errZCodeProjectInvalidReply},
		{name: "code 404 in the envelope", body: `{"code":404}`, err: errZCodeProjectNotFound},
		{name: "a reply that is not JSON", body: `<html>`, err: errZCodeProjectInvalidReply},
		{name: "a reply over the size limit", body: `{"code":0,"data":"` + strings.Repeat("x", zcodeProjectReplyMaxBytes) + `"}`, err: errZCodeProjectInvalidReply},
		{name: "HTTP 404", status: http.StatusNotFound, err: errZCodeProjectNotFound},
		{name: "HTTP 429", status: http.StatusTooManyRequests, err: errZCodeProjectTransient},
		{name: "HTTP 502", status: http.StatusBadGateway, err: errZCodeProjectTransient},
		{name: "HTTP 401", status: http.StatusUnauthorized, err: errZCodeProjectRequestFailed},
		{name: "a redirect", status: http.StatusFound, err: errZCodeProjectRequestFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var moved atomic.Int32
			server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/moved" {
					moved.Add(1)
				}
				if test.status == http.StatusFound {
					w.Header().Set("Location", "/moved")
				}
				if test.status != 0 {
					w.WriteHeader(test.status)
				}
				_, _ = io.WriteString(w, test.body)
			})
			tokens := newZCodeProjectTokenClient(client, zcodeAccountEndpoints{}, testutil.NewQuartzMock(t), func() float64 { return 0 })
			data, err := tokens.call(context.Background(), http.MethodGet, server.URL+"/api", http.Header{}, nil)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
				assert.Zero(t, moved.Load(), "a redirect is never followed")
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, test.want, string(data))
		})
	}
}

func TestZCodeProjectTokenClient_CallSendsNothingItCannotSendSafely(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	count := func(http.ResponseWriter, *http.Request) { hits.Add(1) }
	server, client := zcodeTestStartPlanServer(t, count)
	// A plain server, so that a request over http would reach the handler.
	plain := httptest.NewServer(http.HandlerFunc(count))
	t.Cleanup(plain.Close)
	tokens := newZCodeProjectTokenClient(client, zcodeAccountEndpoints{}, testutil.NewQuartzMock(t), func() float64 { return 0 })
	for _, target := range []string{plain.URL + "/api", "https:///api", "://"} {
		_, err := tokens.call(context.Background(), http.MethodGet, target, http.Header{"Authorization": {"login"}}, nil)
		require.ErrorIs(t, err, errZCodeProjectRequestFailed, target)
	}
	assert.Zero(t, hits.Load(), "a credential never leaves over plain http or to no host")

	server.Close()
	_, err := tokens.call(context.Background(), http.MethodGet, server.URL+"/api", http.Header{}, nil)
	require.ErrorIs(t, err, errZCodeProjectTransient, "a transport failure can pass")
}
