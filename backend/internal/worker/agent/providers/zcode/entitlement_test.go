package zcode

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeTestCanonicalModelIDs is the spelling map of a release that configures GLM-5.3 and
// GLM-5.3-Flash.
var zcodeTestCanonicalModelIDs = map[string]string{"glm-5.3-flash": "GLM-5.3-Flash", "glm-5.3": "GLM-5.3"}

// zcodeLiveBalanceReply has the shape of a reply that the billing API returned for a
// "ZCode 1024 Dev Day" Start Plan. The capabilities spell the models in lowercase.
const zcodeLiveBalanceReply = `{
  "code": 0,
  "data": {
    "server_time": 1800000000,
    "plans": [{
      "plan_id": "zcode-v3-start-plan-1024",
      "status": "active",
      "ends_at": 1900000000,
      "entitlements": [
        {"show_name": "GLM-5.3-Flash", "meter": "model_usage", "capabilities": ["model:glm-5.3-flash"]},
        {"show_name": "GLM-5.3", "meter": "model_usage", "capabilities": ["model:glm-5.3"]}
      ]
    }],
    "balances": [
      {"plan_id": "zcode-v3-start-plan-1024", "show_name": "GLM-5.3-Flash", "capabilities": ["model:glm-5.3-flash"]},
      {"plan_id": "zcode-v3-start-plan-1024", "show_name": "GLM-5.3", "capabilities": ["model:glm-5.3"]}
    ]
  }
}`

// zcodeTestIdentity is the client identity of the billing tests.
var zcodeTestIdentity = zcodeClientIdentity{appVersion: "3.16.3", deviceMid: "11111111-2222-4333-8444-555555555555"}

func zcodeAvailable(models ...string) zcodeAccountEntitlement {
	if models == nil {
		models = []string{}
	}
	return zcodeAccountEntitlement{Availability: zcodeAvailabilityAvailable, Models: models}
}

var zcodeNotEntitled = zcodeAccountEntitlement{
	Availability:      zcodeAvailabilityUnavailable,
	UnavailableReason: zcodeUnavailableNotEntitled,
}

func zcodePending(effectiveAt float64) zcodeAccountEntitlement {
	return zcodeAccountEntitlement{Availability: zcodeAvailabilityPending, EffectiveAt: &effectiveAt, Models: []string{}}
}

// zcodeBalanceReplyJSON wraps plans and balances in a successful envelope at server time
// 1800000000.
func zcodeBalanceReplyJSON(plans, balances string) string {
	return fmt.Sprintf(`{"code":0,"data":{"server_time":1800000000,"plans":%s,"balances":%s}}`, plans, balances)
}

const zcodeActiveStartPlan = `[{"plan_id":"zcode-start-plan","status":"active"}]`

func TestZCodeStartPlanBalance_Interpretation(t *testing.T) {
	t.Parallel()

	const now = 1800000000.0
	for _, test := range []struct {
		name        string
		body        string
		dateSeconds float64
		want        zcodeAccountEntitlement
		wantErr     string
	}{
		{
			name: "the balances' model capabilities in the release's spelling",
			body: zcodeLiveBalanceReply,
			want: zcodeAvailable("GLM-5.3-Flash", "GLM-5.3"),
		},
		{
			name: "a balance with no model capability falls back to its show_name",
			body: zcodeBalanceReplyJSON(zcodeActiveStartPlan, `[{"show_name":"GLM-5.3","capabilities":["tool:search"]}]`),
			want: zcodeAvailable("GLM-5.3"),
		},
		{
			name: "a duplicate that differs by case and a blank id are dropped",
			body: zcodeBalanceReplyJSON(zcodeActiveStartPlan, `[
              {"capabilities":[" MODEL: glm-5.3 ","model:GLM-5.3","model:  "]},
              {"show_name":"  "},
              {"show_name":"glm-5.3-flash"},
              {"capabilities":["model:custom-x"]}]`),
			want: zcodeAvailable("GLM-5.3", "GLM-5.3-Flash", "custom-x"),
		},
		{
			name: "the plan's entitlements grant no model without a balance",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active",
              "entitlements":[{"show_name":"GLM-5.3","meter":"model_usage"}]}]`, `[]`),
			want: zcodeAvailable(),
		},
		{
			name: "an expired plan is not held",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"expired"}]`,
				`[{"plan_id":"zcode-start-plan","capabilities":["model:glm-5.3"]}]`),
			want: zcodeNotEntitled,
		},
		{
			name: "an active plan whose end passed by the server's clock is expired",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active","ends_at":1700000000}]`,
				`[{"plan_id":"zcode-start-plan","capabilities":["model:glm-5.3"]}]`),
			want: zcodeNotEntitled,
		},
		{
			name: "an end stated as a numeric string counts",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active","ends_at":" 1700000000 "}]`, `[]`),
			want: zcodeNotEntitled,
		},
		{
			name: "the Date header wins over the server's clock",
			body: `{"code":0,"data":{"server_time":1700000000,"plans":[{"plan_id":"zcode-start-plan","status":"active","ends_at":1750000000}]}}`,
			// The server's clock puts the end ahead, but the reply's Date is past it.
			dateSeconds: 1800000000,
			want:        zcodeNotEntitled,
		},
		{
			name: "the local clock decides when the reply states no time",
			body: `{"code":0,"data":{"plans":[{"plan_id":"zcode-start-plan","status":"active","ends_at":1700000000}]}}`,
			want: zcodeNotEntitled,
		},
		{
			name: "a null or zero end never expires",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active","ends_at":null},
              {"plan_id":"zcode-start-plan-b","status":"active","ends_at":0}]`,
				`[{"plan_id":"zcode-start-plan","capabilities":["model:glm-5.3"]}]`),
			want: zcodeAvailable("GLM-5.3"),
		},
		{
			name: "the balance of an expired plan drops while a live plan keeps its own",
			body: zcodeBalanceReplyJSON(`[
              {"plan_id":"zcode-start-plan","user_plan_id":"u1","status":"active","ends_at":1700000000},
              {"plan_id":"zcode-start-plan","user_plan_id":"u2","status":"active"}]`, `[
              {"plan_id":"zcode-start-plan","user_plan_id":"u1","capabilities":["model:glm-5.2"]},
              {"plan_id":"zcode-start-plan","user_plan_id":"u2","capabilities":["model:glm-5.3"]}]`),
			want: zcodeAvailable("GLM-5.3"),
		},
		{
			name: "an active plan that another identity states is not a Start Plan",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"coding-plan-pro","name":"Coding Plan Pro","status":"active"}]`,
				`[{"plan_id":"coding-plan-pro","capabilities":["model:glm-5.3"]}]`),
			want: zcodeNotEntitled,
		},
		{
			name: "a plan name identifies a Start Plan",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"p-1024","name":"ZCode Start Plan Dev Day","status":" Active "}]`,
				`[{"plan_id":"p-1024","capabilities":["model:glm-5.3"]}]`),
			want: zcodeAvailable("GLM-5.3"),
		},
		{
			name: "an active plan that states no identity counts",
			body: zcodeBalanceReplyJSON(`[{"status":"active"}]`, `[]`),
			want: zcodeAvailable(),
		},
		{
			name: "a plan whose every entitlement takes effect later is pending",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active",
              "entitlements":[{"effective_at":1900000000},{"effective_at":"1850000000"}]}]`, `[]`),
			want: zcodePending(1850000000),
		},
		{
			name: "a plan that lists no entitlement is pending until it starts",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active","starts_at":1850000000}]`, `[]`),
			want: zcodePending(1850000000),
		},
		{
			name: "a plan with an entitlement in effect is not pending",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active",
              "entitlements":[{"effective_at":1850000000},{"effective_at":1700000000}]}]`, `[]`),
			want: zcodeAvailable(),
		},
		{
			name: "an entitlement that states no time keeps a plan from pending",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active",
              "entitlements":[{"effective_at":1850000000},{"effective_at":""}]}]`, `[]`),
			want: zcodeAvailable(),
		},
		{
			name: "a plan that grants models is not pending",
			body: zcodeBalanceReplyJSON(`[{"plan_id":"zcode-start-plan","status":"active",
              "entitlements":[{"effective_at":1850000000}]}]`, `[{"capabilities":["model:glm-5.3"]}]`),
			want: zcodeAvailable("GLM-5.3"),
		},
		{
			name:    "a reply that states success false failed",
			body:    `{"success":false,"code":0,"data":{"plans":` + zcodeActiveStartPlan + `}}`,
			want:    zcodeCredentialFailedEntitlement,
			wantErr: "success=false",
		},
		{
			name:    "a non-zero code failed",
			body:    `{"code":3001,"msg":"parameter error","data":{"plans":` + zcodeActiveStartPlan + `}}`,
			want:    zcodeCredentialFailedEntitlement,
			wantErr: "code=3001",
		},
		{
			name:    "a null code failed",
			body:    `{"code":null,"data":{"plans":` + zcodeActiveStartPlan + `}}`,
			want:    zcodeCredentialFailedEntitlement,
			wantErr: "code=null",
		},
		{
			name:    "a code spelled as a string failed",
			body:    `{"code":"0","data":{"plans":` + zcodeActiveStartPlan + `}}`,
			want:    zcodeCredentialFailedEntitlement,
			wantErr: `code="0"`,
		},
		{
			name: "code 200 succeeded",
			body: `{"code":200,"data":{"plans":` + zcodeActiveStartPlan + `}}`,
			want: zcodeAvailable(),
		},
		{
			name: "an absent code succeeded",
			body: `{"data":{"plans":` + zcodeActiveStartPlan + `}}`,
			want: zcodeAvailable(),
		},
		{
			name: "a reply with no data holds no plan",
			body: `{"code":0,"data":null}`,
			want: zcodeNotEntitled,
		},
		{
			name:    "malformed JSON is unknown",
			body:    `{"code":0,`,
			want:    zcodeUnknownEntitlement,
			wantErr: "parse the billing/balance reply",
		},
		{
			name:    "a plan id that is not a string makes the reply unreadable",
			body:    `{"code":0,"data":{"plans":[{"plan_id":5,"status":"active"}]}}`,
			want:    zcodeUnknownEntitlement,
			wantErr: "parse the billing/balance reply",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dateSeconds := test.dateSeconds
			if dateSeconds == 0 {
				dateSeconds = math.NaN()
			}
			got, err := interpretZCodeStartPlanBalance([]byte(test.body), dateSeconds, now, zcodeTestCanonicalModelIDs)
			if test.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.wantErr)
			}
			assert.Equal(t, test.want, got)
		})
	}
}

func TestZCodeStartPlanBalanceURL(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		baseURL string
		version string
		want    string
		wantErr string
	}{
		{
			name:    "the shipped base URL",
			baseURL: "https://zcode.z.ai/api/v1/zcode-plan/anthropic",
			version: "3.16.3",
			want:    "https://zcode.z.ai/api/v1/zcode-plan/billing/balance?app_version=3.16.3",
		},
		{
			name:    "a base URL with no path keeps its host",
			baseURL: " https://zcode.z.ai/ ",
			version: "3.16.3",
			want:    "https://zcode.z.ai/api/v1/zcode-plan/billing/balance?app_version=3.16.3",
		},
		{
			name:    "a port, a query, and a fragment of the base URL",
			baseURL: "HTTPS://zcode.z.ai:8443/x/anthropic?r=/y#z",
			version: "3.16.3",
			want:    "https://zcode.z.ai:8443/api/v1/zcode-plan/billing/balance?app_version=3.16.3",
		},
		{
			name:    "an unknown version states no app_version",
			baseURL: "https://zcode.z.ai/api/v1/zcode-plan/anthropic",
			version: zcodeUnknownAppVersion,
			want:    "https://zcode.z.ai/api/v1/zcode-plan/billing/balance",
		},
		{
			name:    "a version that needs escaping",
			baseURL: "https://zcode.z.ai",
			version: "3.16.3+build 1",
			want:    "https://zcode.z.ai/api/v1/zcode-plan/billing/balance?app_version=3.16.3%2Bbuild+1",
		},
		{name: "clear text is refused", baseURL: "http://zcode.z.ai/anthropic", wantErr: "not an https URL"},
		{name: "a base URL with no host is refused", baseURL: "https://", wantErr: "not an https URL"},
		{name: "an empty base URL is refused", baseURL: "  ", wantErr: "not an https URL"},
		{name: "a malformed base URL is refused", baseURL: "https://zcode.z.ai:port/x", wantErr: "not a valid URL"},
		{
			name:    "the refusal hides a password in the base URL",
			baseURL: "http://user:secret@zcode.z.ai/anthropic",
			wantErr: "user:xxxxx@zcode.z.ai",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := zcodeStartPlanBalanceURL(test.baseURL, test.version)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				assert.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestZCodeNodePlatform(t *testing.T) {
	t.Parallel()

	for goPlatform, want := range map[[2]string]string{
		{"darwin", "arm64"}:    "darwin-arm64",
		{"darwin", "amd64"}:    "darwin-x64",
		{"windows", "amd64"}:   "win32-x64",
		{"windows", "386"}:     "win32-ia32",
		{"linux", "arm64"}:     "linux-arm64",
		{"linux", "mipsle"}:    "linux-mipsel",
		{"linux", "ppc64le"}:   "linux-ppc64",
		{"illumos", "amd64"}:   "sunos-x64",
		{"freebsd", "riscv64"}: "freebsd-riscv64",
	} {
		assert.Equal(t, want, zcodeNodePlatform(goPlatform[0], goPlatform[1]), goPlatform)
	}
}

func TestZCodeFetchStartPlanEntitlement_SendsTheDesktopRequest(t *testing.T) {
	t.Parallel()

	requests := make(chan *http.Request, 1)
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.Background())
		_, _ = io.WriteString(w, zcodeLiveBalanceReply)
	})

	got, err := fetchZCodeStartPlanEntitlement(context.Background(), client,
		server.URL+"/api/v1/zcode-plan/anthropic", " test-jwt ", zcodeTestIdentity, zcodeTestCanonicalModelIDs)
	require.NoError(t, err)
	assert.Equal(t, zcodeAvailable("GLM-5.3-Flash", "GLM-5.3"), got)

	req := <-requests
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, zcodeStartPlanBalancePath, req.URL.Path)
	assert.Equal(t, "app_version=3.16.3", req.URL.RawQuery)
	for name, want := range map[string]string{
		"Authorization":       "Bearer test-jwt",
		"User-Agent":          "ZCode/3.16.3",
		"X-ZCode-App-Version": "3.16.3",
		"X-Title":             "Z Code@electron",
		"X-Platform":          zcodeNodePlatform(runtime.GOOS, runtime.GOARCH),
		"X-Device-Mid":        zcodeTestIdentity.deviceMid,
		"X-Client-Language":   "en-US",
	} {
		assert.Equal(t, want, req.Header.Get(name), name)
	}
}

func TestZCodeFetchStartPlanEntitlement_ClassifiesFailedReplies(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		want    zcodeAccountEntitlement
		wantErr string
	}{
		{
			name: "HTTP 401 means a refused credential",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, zcodeLiveBalanceReply)
			},
			want:    zcodeCredentialFailedEntitlement,
			wantErr: "HTTP 401",
		},
		{
			name: "HTTP 403 means a refused credential",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			want:    zcodeCredentialFailedEntitlement,
			wantErr: "HTTP 403",
		},
		{
			name: "another status is unknown although its body reads as a success",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, zcodeLiveBalanceReply)
			},
			want:    zcodeUnknownEntitlement,
			wantErr: "HTTP 500",
		},
		{
			name: "a reply longer than the limit is unknown",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"code":0,"pad":"`+strings.Repeat("x", zcodeStartPlanBalanceMaxBytes)+`"}`)
			},
			want:    zcodeUnknownEntitlement,
			wantErr: "exceeds",
		},
		{
			name: "an unparsable reply is unknown",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "<html>maintenance</html>")
			},
			want:    zcodeUnknownEntitlement,
			wantErr: "parse the billing/balance reply",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server, client := zcodeTestStartPlanServer(t, test.handler)
			got, err := fetchZCodeStartPlanEntitlement(context.Background(), client, server.URL, "key", zcodeTestIdentity, zcodeTestCanonicalModelIDs)
			require.ErrorContains(t, err, test.wantErr)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestZCodeFetchStartPlanEntitlement_QuotesALimitedPartOfAFailedReply(t *testing.T) {
	t.Parallel()

	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("y", 64<<10))
	})
	_, err := fetchZCodeStartPlanEntitlement(context.Background(), client, server.URL, "key", zcodeTestIdentity, nil)
	require.ErrorContains(t, err, "HTTP 502")
	assert.LessOrEqual(t, len(err.Error()), zcodeStartPlanErrorExcerptBytes+64)
}

// A redirect could carry the credential to another origin, or downgrade it to clear text.
func TestZCodeFetchStartPlanEntitlement_RefusesARedirect(t *testing.T) {
	t.Parallel()

	var followed atomic.Int32
	target, _ := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		_, _ = io.WriteString(w, zcodeLiveBalanceReply)
	})
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+zcodeStartPlanBalancePath, http.StatusFound)
	})
	got, err := fetchZCodeStartPlanEntitlement(context.Background(), client, server.URL, "key", zcodeTestIdentity, nil)
	require.ErrorContains(t, err, "HTTP 302")
	assert.Equal(t, zcodeUnknownEntitlement, got)
	assert.Zero(t, followed.Load(), "the client must not follow the redirect")
}

func TestZCodeFetchStartPlanEntitlement_SendsNothingItCannotSendSafely(t *testing.T) {
	t.Parallel()

	server, client := zcodeTestStartPlanServer(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL)
	})
	host := strings.TrimPrefix(server.URL, "https://")
	for _, test := range []struct {
		name       string
		baseURL    string
		credential string
		wantErr    string
	}{
		{name: "clear text", baseURL: "http://" + host, credential: "key", wantErr: "not an https URL"},
		{name: "no host", baseURL: "https://", credential: "key", wantErr: "not an https URL"},
		{name: "no base URL", baseURL: "", credential: "key", wantErr: "not an https URL"},
		{name: "no credential", baseURL: server.URL, credential: "  ", wantErr: "holds no credential"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := fetchZCodeStartPlanEntitlement(context.Background(), client, test.baseURL, test.credential, zcodeTestIdentity, nil)
			require.ErrorContains(t, err, test.wantErr)
			assert.Equal(t, zcodeUnknownEntitlement, got)
		})
	}
}

func TestZCodeFetchStartPlanEntitlement_ACancelledContextIsUnknown(t *testing.T) {
	t.Parallel()

	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, zcodeLiveBalanceReply)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := fetchZCodeStartPlanEntitlement(ctx, client, server.URL, "key", zcodeTestIdentity, nil)
	require.True(t, errors.Is(err, context.Canceled), "the error is %v", err)
	assert.Equal(t, zcodeUnknownEntitlement, got)
}

// zcodeTestBalanceRequestKey is the shared-request key of a billing request.
func zcodeTestBalanceRequestKey(t *testing.T, client *http.Client, baseURL, credential string) zcodeBalanceRequestKey {
	t.Helper()
	balanceURL, err := zcodeStartPlanBalanceURL(baseURL, zcodeTestIdentity.appVersion)
	require.NoError(t, err)
	return zcodeBalanceRequestKey{client: client, url: balanceURL, credential: sha256.Sum256([]byte(credential)), identity: zcodeTestIdentity}
}

// Agents that start together, as the resume sweep starts them, share one request.
func TestZCodeFetchStartPlanEntitlement_ConcurrentChecksShareOneRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	release := make(chan struct{})
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		<-release
		_, _ = io.WriteString(w, zcodeLiveBalanceReply)
	})
	key := zcodeTestBalanceRequestKey(t, client, server.URL, "key")
	results := make(chan zcodeAccountEntitlement, 2)
	for range 2 {
		go func() {
			got, _ := fetchZCodeStartPlanEntitlement(context.Background(), client, server.URL, "key", zcodeTestIdentity, zcodeTestCanonicalModelIDs)
			results <- got
		}()
	}
	require.Eventually(t, func() bool { return zcodeBalanceRequests.waiting(key) == 2 }, 30*time.Second, time.Millisecond)
	close(release)
	assert.Equal(t, zcodeAvailable("GLM-5.3-Flash", "GLM-5.3"), <-results)
	assert.Equal(t, zcodeAvailable("GLM-5.3-Flash", "GLM-5.3"), <-results)
	assert.Equal(t, int32(1), requests.Load())

	// No result outlives its request: a later check asks again.
	_, err := fetchZCodeStartPlanEntitlement(context.Background(), client, server.URL, "key", zcodeTestIdentity, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), requests.Load())
}

// One agent that stops waiting leaves the shared request to the others.
func TestZCodeFetchStartPlanEntitlement_ACancelledWaiterLeavesTheRequestToTheOthers(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var cancelled atomic.Bool
	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			_, _ = io.WriteString(w, zcodeLiveBalanceReply)
		case <-r.Context().Done():
			cancelled.Store(true)
		}
	})
	key := zcodeTestBalanceRequestKey(t, client, server.URL, "key")
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := fetchZCodeStartPlanEntitlement(ctx, client, server.URL, "key", zcodeTestIdentity, nil)
		first <- err
	}()
	second := make(chan zcodeAccountEntitlement, 1)
	go func() {
		got, _ := fetchZCodeStartPlanEntitlement(context.Background(), client, server.URL, "key", zcodeTestIdentity, zcodeTestCanonicalModelIDs)
		second <- got
	}()
	require.Eventually(t, func() bool { return zcodeBalanceRequests.waiting(key) == 2 }, 30*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	require.Eventually(t, func() bool { return zcodeBalanceRequests.waiting(key) == 1 }, 30*time.Second, time.Millisecond)
	close(release)
	assert.Equal(t, zcodeAvailable("GLM-5.3-Flash", "GLM-5.3"), <-second)
	assert.False(t, cancelled.Load(), "the request kept running for the waiter that stayed")
}

// zcodeTestStartPlanBindings binds a Z.ai and a BigModel Start Plan and a Z.ai Coding
// Plan, each with its own key, whose base URLs point at baseURL.
func zcodeTestStartPlanBindings(t *testing.T, baseURL string) []zcodeAccountBinding {
	t.Helper()
	catalog := zcodeTestCatalog(t, fmt.Sprintf(`{
      "provider": {
        "builtin:zai-coding-plan": {"kind": "anthropic", "enabled": true, "options": {"apiKey": "zai-plan-key", "baseURL": %[1]q}, "models": {"GLM-5.3": {}}},
        "builtin:zai-start-plan": {"kind": "anthropic", "options": {"apiKey": "zai-start-key", "baseURL": %[1]q}, "models": {"GLM-5.3-Flash": {}}},
        "builtin:bigmodel-start-plan": {"kind": "anthropic", "options": {"apiKey": "bigmodel-start-key", "baseURL": %[1]q}, "models": {"GLM-5.3-Flash": {}}}
      }
    }`, baseURL))
	bindings := zcodeTestBindings(t, catalog, zcodeTestTwoFamilyRelease, zcodeCredentials{})
	require.Len(t, bindings, 3)
	return bindings
}

func zcodeTestChecker(client *http.Client) zcodeStartPlanChecker {
	return zcodeStartPlanChecker{client: client, identity: zcodeTestIdentity, canonical: zcodeTestCanonicalModelIDs, agentID: "test-agent"}
}

func TestZCodeStartPlanCheck_ChecksEveryStartPlanBinding(t *testing.T) {
	t.Parallel()

	server, client := zcodeTestStartPlanServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer zai-start-key":
			_, _ = io.WriteString(w, zcodeLiveBalanceReply)
		case "Bearer bigmodel-start-key":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Errorf("a coding plan must not be checked: %s", r.Header.Get("Authorization"))
		}
	})
	check := zcodeTestChecker(client).start(context.Background(), zcodeStartPlanTargets(zcodeTestStartPlanBindings(t, server.URL)), 30*time.Second)
	assert.Equal(t, map[string]zcodeAccountEntitlement{
		"builtin:zai-start-plan":      zcodeAvailable("GLM-5.3-Flash", "GLM-5.3"),
		"builtin:bigmodel-start-plan": zcodeCredentialFailedEntitlement,
	}, check.wait())
}

func TestZCodeStartPlanCheck_FinishesAtOnceWithoutAStartPlan(t *testing.T) {
	t.Parallel()

	check := zcodeTestChecker(newZCodeStartPlanHTTPClient(zcodeOfflineTransport{t: t})).start(context.Background(), nil, 30*time.Second)
	assert.Empty(t, check.wait())
}

// zcodeBlockingStartPlanServer answers no request until the request ends. It reports each
// request that reached it.
func zcodeBlockingStartPlanServer(t *testing.T) (*http.Client, string, <-chan struct{}) {
	t.Helper()
	reached := make(chan struct{}, 4)
	server, client := zcodeTestStartPlanServer(t, func(_ http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-r.Context().Done()
	})
	return client, server.URL, reached
}

func TestZCodeStartPlanCheck_StopEndsTheRunningRequests(t *testing.T) {
	t.Parallel()

	client, baseURL, reached := zcodeBlockingStartPlanServer(t)
	check := zcodeTestChecker(client).start(context.Background(), zcodeStartPlanTargets(zcodeTestStartPlanBindings(t, baseURL)), time.Hour)
	<-reached
	<-reached
	check.stop()
	assert.Equal(t, map[string]zcodeAccountEntitlement{
		"builtin:zai-start-plan":      zcodeUnknownEntitlement,
		"builtin:bigmodel-start-plan": zcodeUnknownEntitlement,
	}, check.wait())
}

func TestZCodeStartPlanCheck_TheDeadlineEndsTheCheck(t *testing.T) {
	t.Parallel()

	client, baseURL, _ := zcodeBlockingStartPlanServer(t)
	check := zcodeTestChecker(client).start(context.Background(), zcodeStartPlanTargets(zcodeTestStartPlanBindings(t, baseURL)), time.Millisecond)
	assert.Equal(t, map[string]zcodeAccountEntitlement{
		"builtin:zai-start-plan":      zcodeUnknownEntitlement,
		"builtin:bigmodel-start-plan": zcodeUnknownEntitlement,
	}, check.wait(), "the server never answers, so only the shared deadline ends the check")
}

func TestZCodeStartPlanCheck_CancellingTheAgentEndsTheCheck(t *testing.T) {
	t.Parallel()

	client, baseURL, reached := zcodeBlockingStartPlanServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	check := zcodeTestChecker(client).start(ctx, zcodeStartPlanTargets(zcodeTestStartPlanBindings(t, baseURL)), time.Hour)
	<-reached
	<-reached
	cancel()
	assert.Len(t, check.wait(), 2)
}
