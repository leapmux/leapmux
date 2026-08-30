package hub

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// declaredProcedures walks the whole leapmux.v1 registry and returns every
// method's Connect path, so this test sees a service nobody thought to list.
func declaredProcedures(t *testing.T) []string {
	t.Helper()
	var out []string
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := range services.Len() {
			svc := services.Get(i)
			if !strings.HasPrefix(string(svc.FullName()), "leapmux.v1.") {
				return true
			}
			for j := range svc.Methods().Len() {
				out = append(out, "/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name()))
			}
		}
		return true
	})
	require.NotEmpty(t, out, "the descriptor walk found no leapmux.v1 methods; it proved nothing")
	return out
}

// TestScopeClassificationMatchesTheMountedMux cross-checks the scope map's
// ScopeNotHubServed claim against what NewServer actually mounts.
//
// procedure_scopes.go records two service families as "the Hub's Connect mux
// never mounts this" -- the Worker's local IPC socket and the methods dispatched
// by NAME inside a Noise channel. That record makes the descriptor walk TOTAL,
// which is what keeps a newly declared procedure from being unclassified. But
// the claim is only a comment: nothing in that package can see the mux.
//
// So this is the other half. Mounting one of those procedures on the Hub turns
// the record into a test failure rather than a procedure that answers with a
// scope requirement nobody assigned it.
func TestScopeClassificationMatchesTheMountedMux(t *testing.T) {
	srv := startTestServer(t, &config.Config{})
	handler := srv.server.Handler

	// The probe signal is the ABSENCE of the SPA fallback.
	//
	// The mux serves the SPA from "/" for any unmatched path, as 200
	// text/html. A mounted Connect procedure never answers that way: it
	// refuses an empty unauthenticated POST, and WHICH refusal depends on the
	// procedure -- a unary one answers 4xx, and a STREAMING one answers 505
	// because connect-go refuses a stream over HTTP/1.1. Testing for a 4xx
	// band would call every streaming procedure unmounted, so the test asks
	// the question it actually means.
	mounted := func(path string) bool {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(nil))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		servedTheSPA := rec.Code == http.StatusOK &&
			strings.Contains(rec.Header().Get("Content-Type"), "text/html")
		return !servedTheSPA
	}

	for _, path := range declaredProcedures(t) {
		requirement := auth.ScopeRequirementFor(path)
		require.Truef(t, requirement.IsAssigned(),
			"procedure %q has no scope classification; internal/hub/auth's own walk should have caught this first", path)

		t.Run(path, func(t *testing.T) {
			if requirement.IsHubServed() {
				assert.Truef(t, mounted(path),
					"%q carries a scope classification that assumes the Hub serves it, but NewServer mounts no handler for it -- "+
						"either mount it or classify it ScopeNotHubServed", path)
				return
			}
			assert.Falsef(t, mounted(path),
				"%q is classified ScopeNotHubServed, but NewServer mounts it -- "+
					"the scope rung now guards a procedure whose record says it is unreachable", path)
		})
	}
}

// TestWellKnownMetadataIsMounted pins the two anonymous discovery documents.
//
// A client library fetches them BEFORE it holds anything, and derives every
// endpoint from what they say -- so a hub that does not serve them is one no
// conformant client can talk to at all. They are easy to lose: they hang off
// the OAuth handler's RegisterRoutes rather than off a Connect service, so no
// descriptor walk covers them.
func TestWellKnownMetadataIsMounted(t *testing.T) {
	// A LISTEN address, because the documents are built from the hub's own
	// address and a hub that has none refuses to publish them at all. See
	// TestWellKnownMetadataRefusesWithoutAnAddress.
	srv := startTestServer(t, &config.Config{Listen: "127.0.0.1:4327"})
	handler := srv.server.Handler

	for _, path := range []string{
		"/.well-known/oauth-authorization-server",
		"/.well-known/oauth-protected-resource",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equalf(t, http.StatusOK, rec.Code, "%s must be served anonymously: %s", path, rec.Body.String())
			assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			// A DOCUMENT, not the SPA. The "/" fallback answers 200 too, so
			// the status alone proves nothing: decode the body, and on the
			// RFC 8414 document parse the issuer a client library derives
			// every endpoint from.
			var doc struct {
				Issuer   string `json:"issuer"`
				Resource string `json:"resource"`
			}
			require.NoErrorf(t, json.Unmarshal(rec.Body.Bytes(), &doc),
				"%s must be a JSON document, not a page: %s", path, rec.Body.String())
			if path == "/.well-known/oauth-authorization-server" {
				parsed, err := url.Parse(doc.Issuer)
				require.NoErrorf(t, err, "%s carries an unparseable issuer %q", path, doc.Issuer)
				assert.Equalf(t, "http", parsed.Scheme, "%s issuer must be absolute: %q", path, doc.Issuer)
				assert.Equal(t, "127.0.0.1:4327", parsed.Host)
			} else {
				// RFC 9728 has no issuer; its address-bearing field is the
				// resource itself, and it must parse just as absolutely.
				parsed, err := url.Parse(doc.Resource)
				require.NoErrorf(t, err, "%s carries an unparseable resource %q", path, doc.Resource)
				assert.Equalf(t, "http", parsed.Scheme, "%s resource must be absolute: %q", path, doc.Resource)
				assert.Equal(t, "127.0.0.1:4327", parsed.Host)
			}
			assert.Contains(t, rec.Body.String(), "\"scopes_supported\"")
		})
	}
}

// A hub that cannot state its own address publishes NOTHING rather than a
// document with a broken one.
//
// With no public_url and no TCP listener, the derived base is "http://" -- and
// a document built from it names every endpoint "http:/oauth/...". A
// conformant client builds exactly what the document said and fails somewhere
// unrelated, on a request it made correctly.
//
// The refusal lands at DISCOVERY, which is the first thing any client fetches,
// and it names the setting that fixes it.
func TestWellKnownMetadataRefusesWithoutAnAddress(t *testing.T) {
	srv := startTestServer(t, &config.Config{})
	handler := srv.server.Handler

	for _, path := range []string{
		"/.well-known/oauth-authorization-server",
		"/.well-known/oauth-protected-resource",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Contains(t, rec.Body.String(), "public_url",
				"the refusal must name the setting an operator can act on")
			// And it publishes no endpoint at all, so nothing can be derived
			// from a partial document.
			assert.NotContains(t, rec.Body.String(), "/oauth/authorize")
		})
	}
}

// mountedLiteralRoutes returns every path the hub mounts on its mux BY HAND,
// derived from the source rather than restated here.
//
// "By hand" is the split, and it is drawn on the shape of the first argument:
//
//   - A STRING LITERAL is a hand-written path. Every one lands here.
//   - A PACKAGE-QUALIFIED CONSTANT (service.IdPCompleteSignupPath) is one too.
//     It is spelled that way because two packages must agree on the value, and
//     it must not escape this record for being tidier than a literal.
//   - A BARE LOCAL VARIABLE is how a Connect service mounts itself
//     (`path, handler := NewXServiceHandler(...)`), and those are covered by
//     the descriptor walk above instead. They are skipped here.
//
// The limit that leaves: a hand-written route assigned to a local variable
// first would read as a Connect mount and escape. Nothing does that today, and
// the reverse direction below catches its consequence -- an entry that names a
// route the scan no longer finds fails.
//
// It walks the whole module because a route can be mounted from any package
// that takes a *http.ServeMux: the OAuth server, the IdP handler and the worker
// delegation handler each register their own inside a RegisterRoutes method,
// and a scan of hub/server.go alone would see none of them.
func mountedLiteralRoutes(t *testing.T) []string {
	t.Helper()

	root, err := filepath.Abs("..")
	require.NoError(t, err)

	seen := map[string]bool{}
	scanned := testutil.ForEachRepoSourceFile(t, root, func(_ *token.FileSet, _ string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			// The receiver is the mux the hub builds. Named `mux` at every
			// site, and a rename shows up as a missing route rather than as a
			// silent pass, because the record below is bidirectional.
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "mux" {
				return true
			}
			switch arg := call.Args[0].(type) {
			case *ast.BasicLit:
				if arg.Kind != token.STRING {
					return true
				}
				path, unquoteErr := strconv.Unquote(arg.Value)
				require.NoError(t, unquoteErr)
				seen[path] = true
			case *ast.SelectorExpr:
				// pkg.Const: recorded under its source spelling, because the
				// value lives in another package and this scan does not
				// evaluate it. The record below names the same spelling.
				pkg, ok := arg.X.(*ast.Ident)
				if !ok {
					return true
				}
				seen[pkg.Name+"."+arg.Sel.Name] = true
			}
			return true
		})
	})
	require.Greater(t, scanned, 200, "the repo walk scanned suspiciously few files; the net is broken, not the code")

	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	require.NotEmpty(t, out, "the scan found no hand-mounted route; it proved nothing")
	return out
}

// routeCredentialRule says what a hand-mounted route demands of a caller.
type routeCredentialRule int

const (
	// routeNoLeapMuxBearer carries no leapmux credential at all: it is
	// anonymous, or it authenticates with something else entirely (a worker
	// auth_token, a browser session the page itself establishes, an OAuth
	// secret the request presents).
	routeNoLeapMuxBearer routeCredentialRule = iota
	// routeRequiresScope authenticates a leapmux bearer and states the one
	// scope that bearer must hold.
	routeRequiresScope
)

// handMountedRoutes records, for every route the hub mounts by literal path,
// either the scope it requires of a leapmux bearer or the reason it carries
// none.
//
// This is the half of the scope map that a descriptor walk cannot reach. The
// Connect interceptor's scope rung sees PROCEDURES, and procedure_scopes.go's
// own tripwire makes that set total -- but a route mounted by hand is not a
// procedure, so a new WebSocket endpoint or a new /oauth leg is outside every
// check in that package. Two of them, /ws/channel and /ws/userevents, do accept
// a scoped bearer and are the account's whole event feed and its channels to
// every machine it owns.
//
// A route with no entry FAILS, which is the polarity that makes the record
// worth having: the answer nobody wrote is the answer nobody thought about.
var handMountedRoutes = map[string]struct {
	rule   routeCredentialRule
	scope  leapmuxv1.Scope
	reason string
}{
	// --- The two long-lived sockets: the only hand-mounted routes that take
	// a scoped bearer, and each states its scope at construction. See
	// newWSAuthenticator.
	// Recorded by its CONSTANT spelling, which is how server.go mounts it:
	// the route is a wire contract owned by contracts/wire.json (the browser
	// and every dialer spell it from the same file).
	"contracts.WSRouteChannel": {routeRequiresScope, leapmuxv1.Scope_SCOPE_WORKER_READ,
		"the socket carries a Noise tunnel to a machine; each inner method then states its own scope"},
	"contracts.WSRouteUserEvents": {routeRequiresScope, leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		"the stream is the account's layout document and every change to it"},

	// --- The authorization server. Every leg here either runs before any
	// credential exists, or authenticates with an OAuth artifact rather than
	// with a leapmux bearer.
	"/.well-known/oauth-authorization-server": {routeNoLeapMuxBearer, 0,
		"RFC 8414 discovery, fetched before a client holds anything"},
	"/.well-known/oauth-protected-resource": {routeNoLeapMuxBearer, 0,
		"RFC 9728 discovery, fetched before a client holds anything"},
	"/oauth/authorize": {routeNoLeapMuxBearer, 0,
		"an elevated browser SESSION grants the consent; an app credential has no business granting one"},
	"/oauth/consent": {routeNoLeapMuxBearer, 0, "the POST half of /oauth/authorize, on the same session rule"},
	"/oauth/device":  {routeNoLeapMuxBearer, 0, "the device-flow consent page, on the same session rule"},
	"/oauth/device-authorization": {routeNoLeapMuxBearer, 0,
		"RFC 8628 leg 1; the app has no credential yet, which is what it is asking for"},
	"/oauth/token": {routeNoLeapMuxBearer, 0,
		"authenticates a code, a device code or a refresh secret -- never a bearer"},
	"/oauth/revoke": {routeNoLeapMuxBearer, 0,
		"the caller presents the very credential it is ending; demanding a scope would demand a permission to give up permissions"},
	"/oauth/register": {routeNoLeapMuxBearer, 0,
		"RFC 7591, anonymous by definition and behind the open_app_registration setting"},
	"/oauth/step-up": {routeNoLeapMuxBearer, 0,
		"the app presents its own credential to start the browser ceremony; the ceremony is what proves the factor"},
	"/oauth/apps/": {routeNoLeapMuxBearer, 0,
		"a verified app's icon, read by the consent page before any credential exists"},

	// --- Inbound sign-in providers: the hub is a CLIENT here, and the leg
	// carries a provider's state and code rather than anything of ours.
	"/auth/idp/": {routeNoLeapMuxBearer, 0, "the hub as an OAuth client of GitHub or an OIDC provider"},

	// --- The worker's own credential.
	"/worker/delegation-tokens/mint": {routeNoLeapMuxBearer, 0,
		"a worker auth_token, which the scope model does not describe"},
	"/worker/delegation-tokens/revoke": {routeNoLeapMuxBearer, 0,
		"a worker auth_token; see the mint beside it"},

	// --- The one application route under a Go subtree.
	//
	// Recorded by its CONSTANT spelling, which is how server.go mounts it: the
	// value must match the IdP callback's redirect, so it lives in one place
	// rather than as two literals that can drift.
	"service.IdPCompleteSignupPath": {routeNoLeapMuxBearer, 0,
		"the SPA page a provider sign-up lands on; the application authenticates afterwards, exactly as at \"/\""},

	// --- Operational and static.
	"/metrics": {routeNoLeapMuxBearer, 0, "the Prometheus endpoint; the deployment restricts it, not a scope"},
	"/version": {routeNoLeapMuxBearer, 0, "a build stamp, deliberately anonymous"},
	"/":        {routeNoLeapMuxBearer, 0, "the SPA bundle; the application it serves authenticates afterwards"},
}

// TestEveryHandMountedRouteRecordsItsCredentialRule is the bidirectional
// tripwire over the routes no descriptor walk reaches.
func TestEveryHandMountedRouteRecordsItsCredentialRule(t *testing.T) {
	mounted := mountedLiteralRoutes(t)

	for _, path := range mounted {
		entry, ok := handMountedRoutes[path]
		if !assert.Truef(t, ok,
			"route %q is mounted by hand but records no credential rule; the interceptor's scope rung never sees it, so add it to handMountedRoutes with either the scope it requires or why it carries no leapmux bearer", path) {
			continue
		}
		assert.NotEmptyf(t, entry.reason, "route %q records an empty reason", path)
		if entry.rule == routeRequiresScope {
			assert.Truef(t, authscope.IsGrantable(entry.scope),
				"route %q states %s, which no account can grant", path, entry.scope)
			continue
		}
		assert.Equalf(t, leapmuxv1.Scope_SCOPE_UNSPECIFIED, entry.scope,
			"route %q carries no leapmux bearer, so it must state no scope", path)
	}

	known := make(map[string]bool, len(mounted))
	for _, path := range mounted {
		known[path] = true
	}
	for path := range handMountedRoutes {
		assert.Truef(t, known[path],
			"route %q records a credential rule but nothing mounts it any more; remove the stale entry", path)
	}
}

// TestSPACompleteSignupOutranksTheIdPSubtree pins the one place a Go subtree
// route and an application route share an address.
//
// idpHandler owns `/auth/idp/` and answers 400 for any path that is not
// `<provider>/<action>`, so `/auth/idp/complete-signup` -- the page the
// provider callback redirects a NEW user to -- is swallowed unless an exact
// pattern outranks the subtree. It shipped swallowed: every identity-provider
// sign-up ended on `invalid identity-provider path`, and nothing said so,
// because no other flow visits that address.
//
// The test asserts BOTH halves. Serving the SPA there is the fix; still
// serving the provider legs is what makes the fix a mount rather than a hole
// in the subtree.
func TestSPACompleteSignupOutranksTheIdPSubtree(t *testing.T) {
	srv := startTestServer(t, &config.Config{})
	handler := srv.server.Handler

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	t.Run("the completion page is the application", func(t *testing.T) {
		rec := get(service.IdPCompleteSignupPath + "?token=whatever")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Header().Get("Content-Type"), "text/html",
			"the page a provider sign-up lands on must be served by the SPA, not by the IdP subtree")
		assert.NotContains(t, rec.Body.String(), "invalid identity-provider path")
	})

	t.Run("the provider legs still reach the IdP handler", func(t *testing.T) {
		// An unknown provider id, so the answer comes from the handler's own
		// lookup rather than from a configured row. What matters is that the
		// SPA fallback did NOT answer: a 200 text/html here would mean the
		// exact mount above widened into the subtree.
		rec := get("/auth/idp/no-such-provider/login")
		assert.NotEqual(t, http.StatusOK, rec.Code)
		assert.NotContains(t, rec.Header().Get("Content-Type"), "text/html")
	})

	t.Run("a stray path under the subtree is still the IdP handler's refusal", func(t *testing.T) {
		rec := get("/auth/idp/whatever-else")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid identity-provider path")
	})
}
