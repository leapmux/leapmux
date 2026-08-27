package service

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/locallisten"
)

// RFC 8414 (authorization server metadata) and RFC 9728 (protected resource
// metadata).
//
// They exist so an off-the-shelf client library works against a LeapMux hub
// with no LeapMux-specific code: it fetches one document and learns every
// address, every grant type and every scope name. Without them each client
// hard-codes the paths, and the day one moves, every client breaks silently.
//
// Both are ANONYMOUS by design. A client fetches them before it holds anything,
// and everything in them is already discoverable by reading the source.

// authorizationServerMetadata is the RFC 8414 document.
//
// The field names are the RFC's, so they are wire identifiers rather than Go
// style. `registration_endpoint` is a POINTER because RFC 8414 makes its
// presence meaningful: absent means the server does not accept dynamic
// registration, which is exactly what an operator who leaves the setting off is
// saying. Publishing it with the endpoint refusing every request would tell a
// client library to try, and to report the refusal as a hub failure.
type authorizationServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	DeviceAuthorizationEndpoint                string   `json:"device_authorization_endpoint"`
	RevocationEndpoint                         string   `json:"revocation_endpoint"`
	RegistrationEndpoint                       *string  `json:"registration_endpoint,omitempty"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethodsSupported     []string `json:"revocation_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	ServiceDocumentation                       string   `json:"service_documentation,omitempty"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

// protectedResourceMetadata is the RFC 9728 document. It tells a client which
// authorization server guards this API, which for LeapMux is the same hub.
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// serviceDocumentationURL points a developer at the wire contract.
const serviceDocumentationURL = "https://leapmux.com/docs/reference/oauth-api/"

// metadataBase is the address every endpoint in both documents is built from,
// or a refusal.
//
// It REFUSES rather than publishing what it has, and that is the whole reason
// it exists. A hub that cannot state its own address -- no public_url, and no
// TCP listener to derive one from -- produced "http://", which the trim below
// then cut to "http:". Every endpoint in the document became "http:/oauth/..."
// and a conformant client failed somewhere unrelated, on a request it built
// correctly from a document that was wrong.
//
// The refusal names the setting, and it lands at DISCOVERY, which is the first
// thing any client fetches -- so an operator meets it before an app does.
//
// TrimSuffix, not TrimRight: TrimRight cuts a RUN of slashes, which is what
// turned "http://" into "http:" instead of leaving it recognisably empty.
func (h *OAuthServerHandler) metadataBase(w http.ResponseWriter) (string, bool) {
	base := strings.TrimSuffix(h.hubURL(), "/")
	if u, err := url.Parse(base); err != nil || u.Scheme == "" || u.Host == "" {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error",
			"this hub cannot state its own address; set the public_url setting")
		return "", false
	}
	return base, true
}

func (h *OAuthServerHandler) handleAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if !isMetadataRead(w, r) {
		return
	}
	base, ok := h.metadataBase(w)
	if !ok {
		return
	}
	doc := authorizationServerMetadata{
		Issuer:                      base,
		AuthorizationEndpoint:       locallisten.JoinPath(base, "/oauth/authorize"),
		TokenEndpoint:               locallisten.JoinPath(base, "/oauth/token"),
		DeviceAuthorizationEndpoint: locallisten.JoinPath(base, "/oauth/device-authorization"),
		RevocationEndpoint:          locallisten.JoinPath(base, "/oauth/revoke"),
		ScopesSupported:             metadataScopeTokens(),
		// OAuth 2.1 removed the implicit grant, so `code` is the only value.
		ResponseTypesSupported: []string{"code"},
		GrantTypesSupported: []string{
			GrantTypeAuthorizationCode,
			GrantTypeRefreshToken,
			GrantTypeDeviceCode,
		},
		// `none` is the PUBLIC client, which every native app and the control
		// CLI are; the two secret-bearing methods serve a confidential one.
		TokenEndpointAuthMethodsSupported:      []string{"none", "client_secret_basic", "client_secret_post"},
		RevocationEndpointAuthMethodsSupported: []string{"none", "client_secret_basic", "client_secret_post"},
		// S256 alone. `plain` is not a challenge, and OAuth 2.1 requires PKCE
		// of every client, so offering it would advertise a downgrade.
		CodeChallengeMethodsSupported: []string{"S256"},
		ServiceDocumentation:          serviceDocumentationURL,
		// The hub does not yet return `iss` on the authorization response
		// (RFC 9207). Saying so is what stops a client from requiring it.
		AuthorizationResponseIssParameterSupported: false,
	}
	if settings.KeyOpenAppRegistration.Of(h.snapshot(r.Context())) {
		endpoint := locallisten.JoinPath(base, "/oauth/register")
		doc.RegistrationEndpoint = &endpoint
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *OAuthServerHandler) handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if !isMetadataRead(w, r) {
		return
	}
	base, ok := h.metadataBase(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, protectedResourceMetadata{
		Resource:             base,
		AuthorizationServers: []string{base},
		ScopesSupported:      metadataScopeTokens(),
		// The Authorization header and nothing else. LeapMux never accepts a
		// bearer in a query parameter (it lands in logs and referrers) or in a
		// form body.
		BearerMethodsSupported: []string{"header"},
		ResourceDocumentation:  serviceDocumentationURL,
	})
}

// isMetadataRead restricts both documents to a read. It answers 405 for
// anything else rather than serving the document, so a client that POSTs to a
// well-known address learns it guessed wrong.
func isMetadataRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// metadataScopeTokens lists every GRANTABLE scope, sorted, which is what a
// client library shows a developer picking permissions.
//
// It is derived from authscope rather than written out here, so a scope added
// to scope.proto appears in the document the day it lands.
func metadataScopeTokens() []string {
	return SortedScopeTokens(authscope.EveryGrantableScope())
}
