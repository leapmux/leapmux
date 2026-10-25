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
	req.rejectedFingerprint = zcodeSHA256Hex("another-token")
	same, err := tokens.resolve(context.Background(), zcodeCredentials{}, req)
	require.NoError(t, err)
	assert.Equal(t, first, same, "a fingerprint of another token keeps the cached one")

	req.rejectedFingerprint = zcodeSHA256Hex(first.token)
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
		"a disabled project":                        `{"enable":false}`,
		"a disabled project with irrelevant fields": `{"enable":false,"accessToken":7,"expiresIn":"ignored"}`,
		"an explicitly null enable":                 `{"enable":null,"accessToken":"t","tokenType":"Bearer","expiresIn":3600,"expiresAt":9999999999}`,
		"another token type":                        `{"enable":true,"accessToken":"t","tokenType":"Basic","expiresIn":3600,"expiresAt":9999999999}`,
		"no lifetime":                               `{"enable":true,"accessToken":"t","tokenType":"Bearer","expiresAt":9999999999}`,
		"an expiry in the past":                     `{"enable":true,"accessToken":"t","tokenType":"Bearer","expiresIn":3600,"expiresAt":1}`,
		"no token":                                  `{"tokenType":"Bearer","expiresIn":3600,"expiresAt":9999999999}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tokens, api, _ := newZCodeTestProjectTokens(t)
			api.set(func(f *zcodeFakeProjectAPI) { f.mintData = data })
			_, err := tokens.resolve(context.Background(), zcodeCredentials{}, zcodeTestTokenRequest())
			if strings.HasPrefix(name, "a disabled project") {
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
		{name: "code zero as a decimal", body: `{"code":0.0,"data":1}`, want: `1`},
		{name: "negative zero", body: `{"code":-0,"data":1}`, want: `1`},
		{name: "code 200 as a decimal", body: `{"code":200.0,"data":2}`, want: `2`},
		{name: "code 200 as an exponent", body: `{"code":2e2,"data":2}`, want: `2`},
		{name: "code 200 as escaped text", body: `{"code":"\u0032\u0030\u0030","data":2}`, want: `2`},
		{name: "code 404 as a decimal", body: `{"code":404.0}`, err: errZCodeProjectNotFound},
		{name: "code 404 as an exponent", body: `{"code":4.04e2}`, err: errZCodeProjectNotFound},
		{name: "code 404 as text", body: `{"code":"404"}`, err: errZCodeProjectInvalidReply},
		{name: "code with whitespace inside text", body: `{"code":" 200 "}`, err: errZCodeProjectInvalidReply},
		{name: "a null code", body: `{"code":null}`, err: errZCodeProjectInvalidReply},
		{name: "a boolean code", body: `{"code":false}`, err: errZCodeProjectInvalidReply},
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

func TestZCodeProjectTokens_RejectsUnsupportedFamilies(t *testing.T) {
	t.Parallel()
	tokens, api, _ := newZCodeTestProjectTokens(t)
	for _, family := range []string{"", "other"} {
		req := zcodeTestTokenRequest()
		req.family = family
		_, err := tokens.resolve(t.Context(), zcodeCredentials{}, req)
		require.Error(t, err, family)
	}
	assert.Empty(t, api.recorded())
}

func TestZCodeProjectTokens_PreservesAReplacementLocationAfterAnOldFailure(t *testing.T) {
	t.Parallel()
	tokens, _, _ := newZCodeTestProjectTokens(t)
	old := zcodeProjectLocation{OrganizationID: "old", ProjectID: "old", APIKeyID: "old"}
	replacement := zcodeProjectLocation{OrganizationID: "new", ProjectID: "new", APIKeyID: "new"}
	tokens.remember("key", old)
	oldReference := zcodeLocationReferenceForTest(t, tokens, "key")
	tokens.remember("key", replacement)
	tokens.forget("key", oldReference)
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	assert.Equal(t, replacement, *tokens.locations["key"].value)
	assert.Nil(t, tokens.locations["key"].staleStore)
}

func TestZCodeProjectTokens_FollowsDesktopLocationReplacement(t *testing.T) {
	t.Parallel()
	tokens, api, _ := newZCodeTestProjectTokens(t)
	key := zcodeProjectLocationKey(tokens.endpoints.zai, "zai", "user-1", nil)
	credentials := func(id string) zcodeCredentials {
		return zcodeCredentials{values: map[string]string{key: fmt.Sprintf(`{"organizationId":"org","projectId":"proj","apiKeyId":%q}`, id)}}
	}
	api.set(func(f *zcodeFakeProjectAPI) { f.mintStatus = http.StatusForbidden })
	_, err := tokens.resolve(t.Context(), credentials("old"), zcodeTestTokenRequest())
	require.Error(t, err)
	api.set(func(f *zcodeFakeProjectAPI) { f.mintStatus = 0 })
	got, err := tokens.resolve(t.Context(), credentials("new"), zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "new", got.apiKeyID)
	assert.Zero(t, api.count("GET "))
}

func TestZCodeProjectTokens_PreservesTokenAfterInterruptedResponseBody(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	var interrupted atomic.Bool
	var calls atomic.Int32
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if interrupted.Load() {
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, `{"code":0`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"accessToken":"token","tokenType":"Bearer","expiresIn":3600,"expiresAt":%d}}`, clock.Now().Add(time.Hour).Unix())
	})
	tokens := newZCodeProjectTokenClient(client, zcodeAccountEndpoints{zai: server.URL}, clock, func() float64 { return 0 })
	key := zcodeProjectLocationKey(server.URL, "zai", "user-1", nil)
	credentials := zcodeCredentials{values: map[string]string{key: `{"organizationId":"o","projectId":"p","apiKeyId":"k"}`}}
	_, err := tokens.resolve(t.Context(), credentials, zcodeTestTokenRequest())
	require.NoError(t, err)
	clock.Advance(time.Hour - zcodeProjectTokenRefreshMargin).MustWait(testutil.DeadlineContext(t))
	interrupted.Store(true)
	got, err := tokens.resolve(t.Context(), credentials, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "token", got.token)
	_, err = tokens.resolve(t.Context(), credentials, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "the transient failure delays the next refresh")
}

func TestZCodeProjectTokens_AbandonedMintCannotChangeReplacement(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "permanent-failure", "transient-failure"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			tokens, _, clock := newZCodeTestProjectTokens(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			done := make(chan error, 1)
			go func() {
				_, err := tokens.token(ctx, "scope", "", func(callCtx context.Context) (zcodeProjectTokenValue, error) {
					close(started)
					<-callCtx.Done()
					<-release
					switch outcome {
					case "success":
						return zcodeProjectTokenValue{token: "old", expiresAt: clock.Now().Add(time.Hour)}, nil
					case "transient-failure":
						return zcodeProjectTokenValue{}, errZCodeProjectTransient
					default:
						return zcodeProjectTokenValue{}, errZCodeProjectInvalidReply
					}
				})
				done <- err
			}()
			<-started
			tokens.mints.mu.Lock()
			old := tokens.mints.calls["scope"]
			tokens.mints.mu.Unlock()
			require.NotNil(t, old)
			cancel()
			require.ErrorIs(t, <-done, context.Canceled)
			fresh := zcodeProjectTokenValue{token: "new", expiresAt: clock.Now().Add(time.Hour)}
			_, err := tokens.token(t.Context(), "scope", "", func(context.Context) (zcodeProjectTokenValue, error) { return fresh, nil })
			require.NoError(t, err)
			tokens.mu.Lock()
			before := *tokens.tokens["scope"]
			tokens.mu.Unlock()
			unblock()
			<-old.done
			tokens.mu.Lock()
			after := tokens.tokens["scope"]
			tokens.mu.Unlock()
			require.NotNil(t, after)
			assert.Equal(t, before, *after)
		})
	}
}

func TestZCodeProjectTokens_DiscoversSeparatelyAfterSignInChanges(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var oldRequests, newRequests atomic.Int32
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/biz/customer/getCustomerInfo" {
			if r.Header.Get("Authorization") == "old" {
				oldRequests.Add(1)
			} else {
				newRequests.Add(1)
			}
			<-release
			if r.Header.Get("Authorization") == "old" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"code":0,"data":`+zcodeDefaultOrganizations+`}`)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"code":0,"data":[{"apiKey":"k","name":"zcode-api-key"}]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"accessToken":"token","tokenType":"Bearer","expiresIn":3600,"expiresAt":%d}}`, clock.Now().Add(time.Hour).Unix())
	})
	tokens := newZCodeProjectTokenClient(client, zcodeAccountEndpoints{zai: server.URL}, clock, func() float64 { return 0 })
	oldDone, newDone := make(chan error, 1), make(chan error, 1)
	request := func(login string, ch chan error) {
		req := zcodeTestTokenRequest()
		req.loginToken = login
		_, err := tokens.resolve(t.Context(), zcodeCredentials{}, req)
		ch <- err
	}
	go request("old", oldDone)
	require.Eventually(t, func() bool { return oldRequests.Load() == 1 }, 30*time.Second, time.Millisecond)
	go request("new", newDone)
	require.Eventually(t, func() bool { return tokens.discoveries.waitingInAll() == 2 }, 30*time.Second, time.Millisecond)
	unblock()
	require.Error(t, <-oldDone)
	require.NoError(t, <-newDone)
	assert.Equal(t, int32(1), newRequests.Load())
}

func TestZCodeProjectTokens_RechecksCacheAtMintAdmission(t *testing.T) {
	t.Parallel()
	tokens, _, clock := newZCodeTestProjectTokens(t)
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	result := make(chan error, 1)
	var redundant atomic.Int32
	go func() {
		got, err := tokens.tokenUsing(t.Context(), "scope", "", func(context.Context) (zcodeProjectTokenValue, error) {
			redundant.Add(1)
			return zcodeProjectTokenValue{}, errZCodeProjectInvalidReply
		},
			func(ctx context.Context, key string, timeout time.Duration, fn func(context.Context) (zcodeProjectTokenValue, error)) (zcodeProjectTokenValue, error) {
				close(arrived)
				<-release
				return tokens.mints.do(ctx, key, timeout, fn)
			})
		if err == nil {
			assert.Equal(t, "fresh", got.token)
		}
		result <- err
	}()
	<-arrived
	fresh := zcodeProjectTokenValue{token: "fresh", expiresAt: clock.Now().Add(time.Hour)}
	_, err := tokens.token(t.Context(), "scope", "", func(context.Context) (zcodeProjectTokenValue, error) { return fresh, nil })
	require.NoError(t, err)
	unblock()
	require.NoError(t, <-result)
	assert.Zero(t, redundant.Load())
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	require.NotNil(t, tokens.tokens["scope"])
	assert.Equal(t, fresh, tokens.tokens["scope"].value)
}

type zcodeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f zcodeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZCodeProjectTokens_AbandonedNotFoundCannotInvalidateReplacement(t *testing.T) {
	t.Parallel()
	tokens, api, _ := newZCodeTestProjectTokens(t)
	key := zcodeProjectLocationKey(tokens.endpoints.zai, "zai", "user-1", nil)
	credentials := zcodeCredentials{values: map[string]string{key: `{"organizationId":"o","projectId":"p","apiKeyId":"k"}`}}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	original := tokens.http.Transport
	var first atomic.Bool
	tokens.http.Transport = zcodeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && first.CompareAndSwap(false, true) {
			close(started)
			<-r.Context().Done()
			<-release
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return original.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := tokens.resolve(ctx, credentials, zcodeTestTokenRequest()); done <- err }()
	<-started
	tokens.mints.mu.Lock()
	var abandoned *zcodeSharedCall[zcodeProjectTokenValue]
	for _, call := range tokens.mints.calls {
		abandoned = call
	}
	tokens.mints.mu.Unlock()
	require.NotNil(t, abandoned)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	got, err := tokens.resolve(t.Context(), credentials, zcodeTestTokenRequest())
	require.NoError(t, err)
	assert.Equal(t, "k", got.apiKeyID)
	unblock()
	<-abandoned.done
	tokens.mu.Lock()
	cached := tokens.locations[key].value != nil
	stale := tokens.locations[key].staleStore != nil
	tokens.mu.Unlock()
	assert.True(t, cached)
	assert.False(t, stale)
	assert.Equal(t, int32(1), api.mints.Load())
}

func TestZCodeProjectTokens_OldDiscoveryCannotReplaceNewLocation(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	oldReached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/biz/customer/getCustomerInfo":
			_, _ = io.WriteString(w, `{"code":0,"data":`+zcodeDefaultOrganizations+`}`)
		case r.Method == http.MethodGet:
			key := "new"
			if r.Header.Get("Authorization") == "Bearer old" {
				close(oldReached)
				<-release
				key = "old"
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"data":[{"name":"zcode-api-key","apiKey":%q}]}`, key)
		default:
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"accessToken":"token","tokenType":"Bearer","expiresIn":3600,"expiresAt":%d}}`, clock.Now().Add(time.Hour).Unix())
		}
	})
	tokens := newZCodeProjectTokenClient(client, zcodeAccountEndpoints{zai: server.URL}, clock, func() float64 { return 0 })
	oldDone := make(chan error, 1)
	go func() {
		req := zcodeTestTokenRequest()
		req.loginToken = "old"
		_, err := tokens.resolve(t.Context(), zcodeCredentials{}, req)
		oldDone <- err
	}()
	<-oldReached
	req := zcodeTestTokenRequest()
	req.loginToken = "new"
	current, err := tokens.resolve(t.Context(), zcodeCredentials{}, req)
	require.NoError(t, err)
	require.Equal(t, "new", current.apiKeyID)
	unblock()
	require.NoError(t, <-oldDone)
	key := zcodeProjectLocationKey(server.URL, "zai", "user-1", nil)
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	assert.Equal(t, "new", tokens.locations[key].value.APIKeyID)
}

func TestZCodeSelectProject_UsesDecodedProjectTypes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw  string
		team bool
	}{
		{raw: `2`, team: true}, {raw: `2.0`, team: true}, {raw: `2e0`, team: true},
		{raw: `"2"`, team: true}, {raw: `" 2 "`, team: true}, {raw: `"\u0032"`, team: true},
		{raw: `1`}, {raw: `0`}, {raw: `-1`}, {raw: `1.5`}, {raw: `"02"`}, {raw: `null`},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()
			var customer zcodeProjectCustomer
			require.NoError(t, json.Unmarshal([]byte(`{"organizations":[{"organizationId":"org","projects":[{"projectId":"candidate","projectType":`+test.raw+`},{"projectId":"personal","projectType":1}]}]}`), &customer))
			got, err := zcodeSelectProject(customer, nil)
			require.NoError(t, err)
			want := "candidate"
			if test.team {
				want = "personal"
			}
			assert.Equal(t, zcodeProjectSelection{organizationID: "org", projectID: want}, got)
		})
	}
	for _, raw := range []string{`true`, `[]`, `{}`} {
		var customer zcodeProjectCustomer
		assert.Error(t, json.Unmarshal([]byte(`{"organizations":[{"organizationId":"org","projects":[{"projectId":"p","projectType":`+raw+`}]}]}`), &customer))
	}
}

func TestZCodeSelectProject_HandlesSelectionBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body string
		team       *zcodeTeamProject
		want       zcodeProjectSelection
		err        error
	}{
		{name: "empty", body: `{}`, err: errZCodeProjectUnavailable},
		{name: "no eligible project", body: `{"organizations":[{"organizationId":"o","projects":[{"projectId":"p","projectType":2}]}]}`, err: errZCodeProjectUnavailable},
		{name: "default organization and project", body: zcodeDefaultOrganizations, want: zcodeProjectSelection{organizationID: "org-default", projectID: "p-default"}},
		{name: "skip an empty organization id", body: `{"organizations":[{"organizationId":"","projects":[{"projectId":"bad"}]},{"organizationId":"o","projects":[{"projectId":"p"}]}]}`, want: zcodeProjectSelection{organizationID: "o", projectID: "p"}},
		{name: "refuse an empty selected project id", body: `{"organizations":[{"organizationId":"o","projects":[{"projectId":""},{"projectId":"p"}]}]}`, err: errZCodeProjectUnavailable},
		{name: "team ids match after trimming", body: `{"organizations":[{"organizationId":" o ","projects":[{"projectId":" p "}]}]}`, team: &zcodeTeamProject{organizationID: "o", projectID: "p"}, want: zcodeProjectSelection{organizationID: "o", projectID: "p"}},
		{name: "missing team project", body: `{"organizations":[{"organizationId":"o","projects":[]}]}`, team: &zcodeTeamProject{organizationID: "o", projectID: "p"}, err: errZCodeProjectUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var customer zcodeProjectCustomer
			require.NoError(t, json.Unmarshal([]byte(test.body), &customer))
			got, err := zcodeSelectProject(customer, test.team)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestZCodeSelectProjectAPIKey_UsesNativeValidationOrder(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body, want string
		team             bool
		err              error
	}{
		{name: "empty", body: `[]`, err: errZCodeProjectAPIKeyMissing},
		{name: "skip nonmatching invalid entries", body: `[null,7,{"name":"other","apiKey":7},{"name":"zcode-api-key","apiKey":" k "}]`, want: "k"},
		{name: "first valid matching key", body: `[{"name":"zcode-api-key","apiKey":"first"},{"name":"zcode-api-key","apiKey":"later"}]`, want: "first"},
		{name: "personal first matching invalid key", body: `[{"name":"zcode-api-key","apiKey":7},{"name":"zcode-api-key","apiKey":"later"}]`, err: errZCodeProjectInvalidReply},
		{name: "personal first matching invalid type", body: `[{"name":"zcode-api-key","apiKey":"first","keyType":"2"},{"name":"zcode-api-key","apiKey":"later"}]`, err: errZCodeProjectInvalidReply},
		{name: "personal fractional type", body: `[{"name":"zcode-api-key","apiKey":"k","keyType":1.5}]`, want: "k"},
		{name: "personal absent type", body: `[{"name":"zcode-api-key","apiKey":"k"}]`, want: "k"},
		{name: "personal null type", body: `[{"name":"zcode-api-key","apiKey":"k","keyType":null}]`, want: "k"},
		{name: "personal empty key", body: `[{"name":"zcode-api-key","apiKey":" "}]`, err: errZCodeProjectInvalidReply},
		{name: "team integer", body: `[{"name":"zcode-team-api-key","apiKey":"k","keyType":2}]`, team: true, want: "k"},
		{name: "team decimal", body: `[{"name":"zcode-team-api-key","apiKey":"k","keyType":2.0}]`, team: true, want: "k"},
		{name: "team exponent", body: `[{"name":"zcode-team-api-key","apiKey":"k","keyType":2e0}]`, team: true, want: "k"},
		{name: "team skips empty key", body: `[{"name":"zcode-team-api-key","apiKey":" ","keyType":2},{"name":"zcode-team-api-key","apiKey":"later","keyType":2}]`, team: true, want: "later"},
		{name: "team rejects string type", body: `[{"name":"zcode-team-api-key","apiKey":"k","keyType":"2"}]`, team: true, err: errZCodeProjectAPIKeyMissing},
		{name: "team skips missing type", body: `[{"name":"zcode-team-api-key","apiKey":"k"}]`, team: true, err: errZCodeProjectAPIKeyMissing},
		{name: "team skips invalid key", body: `[{"name":"zcode-team-api-key","apiKey":7,"keyType":2},{"name":"zcode-team-api-key","apiKey":"later","keyType":2}]`, team: true, want: "later"},
		{name: "native key whitespace", body: `[{"name":"zcode-api-key","apiKey":"\ufeffk\ufeff"}]`, want: "k"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var keys []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(test.body), &keys))
			got, err := zcodeSelectProjectAPIKey(keys, test.team)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestZCodeProjectTokens_UsesNativeUnicodeLocationKey(t *testing.T) {
	t.Parallel()
	tokens, api, _ := newZCodeTestProjectTokens(t)
	original := tokens.http.Transport
	tokens.http.Transport = zcodeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return original.RoundTrip(r)
	})
	req := zcodeTestTokenRequest()
	req.accountID = "user\u2028one"
	key := "project-key-location:v1:" + zcodeEncodeURIComponent(`["`+tokens.endpoints.zai+`","zai","user`+"\u2028"+`one",null,1,"non-team"]`)
	credentials := zcodeCredentials{values: map[string]string{key: `{"organizationId":"cached","projectId":"cached","apiKeyId":"cached-key"}`}}
	got, err := tokens.resolve(t.Context(), credentials, req)
	require.NoError(t, err)
	assert.Equal(t, "cached-key", got.apiKeyID)
	assert.Equal(t, 1, api.count("POST /api/biz/v1/organization/cached/projects/cached/api_keys/cached-key/access_tokens"))
}

func TestZCodeProjectTokens_UsesNativeBearerWhitespace(t *testing.T) {
	t.Parallel()
	tokens, api, _ := newZCodeTestProjectTokens(t)
	req := zcodeTestTokenRequest()
	req.loginToken = " \uFEFFbEaReR\u00A0login\uFEFF "
	_, err := tokens.resolve(t.Context(), zcodeCredentials{}, req)
	require.NoError(t, err)
	assert.Equal(t, 1, api.count("GET /api/biz/customer/getCustomerInfo auth=login "))
}

func TestZCodeProjectTokens_OldFailureCannotReplaceNewExclusion(t *testing.T) {
	t.Parallel()
	tokens, _, _ := newZCodeTestProjectTokens(t)
	old := zcodeProjectLocation{OrganizationID: "old", ProjectID: "old", APIKeyID: "old"}
	failed := zcodeProjectLocation{OrganizationID: "failed", ProjectID: "failed", APIKeyID: "failed"}
	current := zcodeProjectLocation{OrganizationID: "current", ProjectID: "current", APIKeyID: "current"}
	tokens.remember("key", old)
	oldReference := zcodeLocationReferenceForTest(t, tokens, "key")
	tokens.remember("key", failed)
	failedReference := zcodeLocationReferenceForTest(t, tokens, "key")
	tokens.forget("key", failedReference)
	tokens.remember("key", current)
	tokens.forget("key", oldReference)
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	assert.Equal(t, current, *tokens.locations["key"].value)
	assert.Equal(t, failed, *tokens.locations["key"].staleStore)
}

func zcodeLocationReferenceForTest(t *testing.T, tokens *zcodeProjectTokenClient, key string) zcodeProjectLocationRef {
	t.Helper()
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	entry := tokens.locations[key]
	require.NotNil(t, entry)
	require.NotNil(t, entry.value)
	return zcodeProjectLocationRef{location: *entry.value, generation: entry.generation}
}

func TestZCodeProjectTokens_OldSignInCannotInvalidateANewMintAtTheSameLocation(t *testing.T) {
	t.Parallel()
	tokens, api, _ := newZCodeTestProjectTokens(t)
	key := zcodeProjectLocationKey(tokens.endpoints.zai, "zai", "user-1", nil)
	credentials := zcodeCredentials{values: map[string]string{key: `{"organizationId":"o","projectId":"p","apiKeyId":"k"}`}}
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	original := tokens.http.Transport
	tokens.http.Transport = zcodeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		if r.Header.Get("Authorization") == "Bearer old" {
			close(reached)
			<-release
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return original.RoundTrip(r)
	})
	oldDone := make(chan error, 1)
	go func() {
		req := zcodeTestTokenRequest()
		req.loginToken = "old"
		_, err := tokens.resolve(t.Context(), credentials, req)
		oldDone <- err
	}()
	<-reached
	req := zcodeTestTokenRequest()
	req.loginToken = "new"
	fresh, err := tokens.resolve(t.Context(), credentials, req)
	require.NoError(t, err)
	unblock()
	require.ErrorIs(t, <-oldDone, errZCodeProjectInvalidReply)
	again, err := tokens.resolve(t.Context(), credentials, req)
	require.NoError(t, err, "a stale failure cannot require discovery after the new sign-in minted successfully")
	assert.Equal(t, fresh, again)
	assert.Equal(t, int32(1), api.mints.Load())
}
