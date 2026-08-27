package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/validate"
)

// RFC 7591 dynamic client registration.
//
// It is OFF by default and an administrator turns it on. Open registration
// means any anonymous caller can create a row that appears on a consent screen,
// which is a phishing surface as much as a convenience -- so the setting is the
// operator's decision and the metadata document reflects it: with the setting
// off, `registration_endpoint` is absent and a conformant client does not try.
//
// A dynamically registered app is HUB-WIDE and UNVERIFIED. It has no owner to
// scope it to (the request carries no account), and nobody vouched for it, so
// the consent page states that in its heading and shows a monogram rather than
// an icon.

// clientNameByteLimit caps a registered app's display name.
//
// The narrowest column that holds one is MySQL's VARCHAR(255), which counts
// CHARACTERS, so 128 BYTES can never overflow it in any dialect. It is also far
// more than a name anybody reads on a consent screen.
const clientNameByteLimit = 128

// clientURIByteLimit caps the app's home page. It reaches no <a href> today --
// the consent page renders no link -- but it is stored and shown in the app
// list, and an unbounded value is a column overflow waiting on MySQL.
const clientURIByteLimit = 512

// registrationRequest is the RFC 7591 section 2 client metadata this server
// accepts. Anything else in the document is ignored, which section 3.1 allows.
type registrationRequest struct {
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	Scope        string   `json:"scope"`
	ClientURI    string   `json:"client_uri"`
	// TokenEndpointAuthMethod decides whether the hub mints a secret.
	// "none" is a PUBLIC client; anything else the hub supports is
	// confidential. An unknown value is refused rather than defaulted, because
	// defaulting either way is a decision the registrant did not make.
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
}

// registrationResponse is RFC 7591 section 3.2.1.
//
// client_secret appears ONCE, here, and is never readable again: the store
// keeps only its hash. `client_secret_expires_at` is 0, which the RFC defines
// as "does not expire" -- a rotation verb would be the honest alternative, and
// there is none yet.
type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	Scope                   string   `json:"scope"`
	ClientURI               string   `json:"client_uri,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// registrationRequestByteLimit caps the document this endpoint reads.
//
// The endpoint is anonymous when the setting is on, so without a cap an
// unauthenticated caller can make the hub buffer an arbitrary body. 16 KiB is
// far more than the seven fields above ever need.
const registrationRequestByteLimit = 16 << 10

func (h *OAuthServerHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The setting read follows the route-level throttle that anonymousLeg
	// applies, so a hub with registration off does spend a probing caller's
	// budget. That is the budget working as designed: the alternative ordering
	// -- the setting checked first -- was a property only this handler stated,
	// and it is what let the throttle stay a per-handler line that the next
	// anonymous leg could forget.
	if !settings.KeyOpenAppRegistration.Of(h.snapshot(r.Context())) {
		writeOAuthError(w, http.StatusForbidden, "access_denied",
			"this hub does not accept open app registration; ask an administrator to register the app")
		return
	}
	var req registrationRequest
	if err := decodeJSONBody(w, r, registrationRequestByteLimit, &req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}
	params, secret, method, body := h.buildRegistration(req, store.OAuthClientSourceDynamic, "", "")
	if body != nil {
		writeOAuthErrorBody(w, http.StatusBadRequest, *body)
		return
	}
	if err := h.store.OAuthClients().Create(r.Context(), params); err != nil {
		writeInternalError(w, "app registration failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, registrationResponseFor(params, secret, method, h.now()))
}

// appRegistrationSpec is the caller-agnostic form of one registration: the
// three surfaces that register an app -- RFC 7591, a user's own registration,
// and an administrator's -- read different wire types but validate the same
// fields, and stating them once is what keeps every rule below a property of
// a REGISTRATION rather than of the caller that happened to submit it.
type appRegistrationSpec struct {
	name             string
	clientURI        string
	redirectURIs     []string
	grantTypes       []string
	scopes           authscope.ScopeSet
	confidential     bool
	elevationAllowed bool
	icon             []byte
	iconMediaType    string
}

// registrationError carries the RFC 7591 error code a refusal maps to, so the
// ONE validation core can serve both error shapes its callers answer with:
// the OAuth endpoint reads the code, and the Connect surface maps every
// refusal to InvalidArgument with the same text.
type registrationError struct {
	code string
	err  error
}

func (e *registrationError) Error() string { return e.err.Error() }
func (e *registrationError) Unwrap() error { return e.err }

// ErrRegistrationRedirectCode marks a redirect-list refusal, which RFC 7591
// section 3.2.2 answers with its own code rather than invalid_client_metadata.
const ErrRegistrationRedirectCode = "invalid_redirect_uri"

// authorizationCodeNeedsRedirect states the dependency both a registration and
// an EDIT must hold: a grant type that redirects needs somewhere to redirect
// TO. One helper, because the register surfaces and UpdateApp each stated it
// inline -- three copies of the one rule that most needs to agree.
func authorizationCodeNeedsRedirect(grantTypes []string, redirectURIs []string) bool {
	return containsAny(grantTypes, GrantTypeAuthorizationCode) && len(redirectURIs) == 0
}

// buildOAuthClientRegistration validates one registration and derives the row
// it writes. It is the ONE builder for the three surfaces that register an
// app; each caller translates its wire type into an appRegistrationSpec and
// maps the returned error to its own shape, and nothing else.
//
// The rules, in the order they run:
//
//   - A name, cleaned and capped. A consent screen states it, so an empty or
//     unbounded one is not display metadata.
//   - A valid redirect list (ValidateRedirectURIs states the rules).
//   - Grant types from the closed supported set.
//   - authorization_code needs at least one redirect address -- refusing here
//     is what turns "my app never gets past the consent screen" into a
//     message at registration time.
//   - The ceiling is CLOSED at registration for the same reason a grant is:
//     a consent may then be a plain subset test, and the app list shows the
//     same set the consent screen will.
//   - A capped client_uri, an icon within the closed media-type set, and a
//     secret minted for a confidential registration.
//
// `owner` empty registers a HUB-WIDE app. The caller decides which callers
// may state one; the RFC 7591 surface additionally refuses an admin-family
// ceiling before it gets here, because an anonymous registrant cannot state
// one (see buildRegistration).
func buildOAuthClientRegistration(
	v *auth.TokenValidator, spec appRegistrationSpec, source, owner, createdBy string,
) (store.CreateOAuthClientParams, string, error) {
	name := validate.CleanNameTo(spec.name, clientNameByteLimit)
	if name == "" {
		return store.CreateOAuthClientParams{}, "", registrationErr("invalid_client_metadata", errors.New("client_name is required"))
	}
	if err := ValidateRedirectURIs(spec.redirectURIs); err != nil {
		return store.CreateOAuthClientParams{}, "", registrationErr(ErrRegistrationRedirectCode, err)
	}
	grantTypes, err := normalizeGrantTypes(spec.grantTypes)
	if err != nil {
		return store.CreateOAuthClientParams{}, "", registrationErr("invalid_client_metadata", err)
	}
	if authorizationCodeNeedsRedirect(grantTypes, spec.redirectURIs) {
		return store.CreateOAuthClientParams{}, "", registrationErr(ErrRegistrationRedirectCode,
			errors.New("the authorization_code grant needs at least one redirect URI"))
	}
	stored, err := spec.scopes.Close().Storable()
	if err != nil {
		return store.CreateOAuthClientParams{}, "", registrationErr("invalid_client_metadata", err)
	}
	clientURI := strings.TrimSpace(spec.clientURI)
	if len(clientURI) > clientURIByteLimit {
		return store.CreateOAuthClientParams{}, "", registrationErr("invalid_client_metadata", errors.New("client_uri is too long"))
	}
	icon, iconMediaType, err := validateIcon(spec.icon, spec.iconMediaType)
	if err != nil {
		return store.CreateOAuthClientParams{}, "", err
	}

	var secretHash []byte
	var secret string
	if spec.confidential {
		secret = id.Generate()
		secretHash = v.HashSecret(secret)
	}
	return store.CreateOAuthClientParams{
		// A generated id, never a caller-supplied one. RFC 7591 section 3.2.1
		// makes the server the issuer of client_id, and a registrant who could
		// choose it would choose one that already exists.
		ClientID:      id.Generate(),
		OwnerUserID:   owner,
		CreatedBy:     createdBy,
		SecretHash:    secretHash,
		ClientName:    name,
		IconBlob:      icon,
		IconMediaType: iconMediaType,
		ClientURI:     clientURI,
		RedirectURIs:  JoinRedirectURIs(spec.redirectURIs),
		Scopes:        stored,
		GrantTypes:    strings.Join(grantTypes, " "),
		// The registrant asks, and the OWNER decides. For a private app those
		// are the same person, so the request is honored; for a hub-wide one
		// the registrant is an administrator, which is the same answer.
		ElevationAllowed:   spec.elevationAllowed,
		RegistrationSource: source,
	}, secret, nil
}

func registrationErr(code string, err error) error {
	return &registrationError{code: code, err: err}
}

// buildRegistration validates one RFC 7591 registration. It translates the
// wire document into an appRegistrationSpec and maps the core's refusals to
// their OAuth error bodies; the admin-family refusal runs here because only
// this surface is anonymous -- the named surfaces state their owner and let
// RegisterApp refuse the same ask to a non-administrator.
//
// The third return value is the effective token_endpoint_auth_method to echo
// back: the row stores no method (the token endpoint accepts both spellings a
// confidential registration may choose), so the response states the one the
// registrant asked for, which is what RFC 7591 section 3.2.1 requires the
// server to echo.
func (h *OAuthServerHandler) buildRegistration(
	req registrationRequest, source, owner, createdBy string,
) (store.CreateOAuthClientParams, string, string, *oauthErrorResponse) {
	method := "none"
	confidential := false
	switch req.TokenEndpointAuthMethod {
	case "", "none":
		// A PUBLIC client. The empty string defaults to public deliberately:
		// a native app or a browser app cannot keep a secret, and handing one
		// to a registrant who did not ask would let them believe they had a
		// confidential client while the secret sat in a distributed binary.
	case "client_secret_basic", "client_secret_post":
		confidential = true
		method = req.TokenEndpointAuthMethod
	default:
		body := oauthErrorBody("invalid_client_metadata",
			"token_endpoint_auth_method must be none, client_secret_basic or client_secret_post")
		return store.CreateOAuthClientParams{}, "", "", &body
	}

	scopes, err := authscope.Parse(req.Scope)
	if err != nil {
		body := oauthErrorBody("invalid_client_metadata", err.Error())
		return store.CreateOAuthClientParams{}, "", "", &body
	}
	// An anonymous registrant cannot state a ceiling that reaches hub
	// administration. The named registration surfaces refuse the same ask to a
	// non-administrator (see refuseAdminCeilingToNonAdmin); dynamic
	// registration is strictly less trusted -- it is anonymous -- yet without
	// this refusal it accepted strictly more, and one elevated consent click
	// handed an anonymous registrant's app the admin bullets. An
	// administrator who wants an admin app registers it through the catalogue,
	// where the owner is known.
	for _, scope := range adminScopeList {
		if scopes.Allows(scope) {
			token, _ := authscope.Token(scope)
			body := oauthErrorBody("invalid_client_metadata",
				fmt.Sprintf("an open registration cannot ask for %s; an administrator registers an app that needs it", token))
			return store.CreateOAuthClientParams{}, "", "", &body
		}
	}

	params, secret, err := buildOAuthClientRegistration(h.validator, appRegistrationSpec{
		name:         req.ClientName,
		clientURI:    req.ClientURI,
		redirectURIs: req.RedirectURIs,
		grantTypes:   req.GrantTypes,
		scopes:       scopes,
		confidential: confidential,
		// NEVER on for an open registration. An app that may re-arm an
		// elevation window can make the account's most sensitive changes, and
		// that is its owner's decision rather than a default an anonymous
		// registrant ticks.
		elevationAllowed: false,
	}, source, owner, createdBy)
	if err != nil {
		code := "invalid_client_metadata"
		var regErr *registrationError
		if errors.As(err, &regErr) {
			code = regErr.code
		}
		body := oauthErrorBody(code, err.Error())
		return store.CreateOAuthClientParams{}, "", "", &body
	}
	return params, secret, method, nil
}

// supportedGrantTypes is the closed set an app may register for.
//
// refresh_token is included because RFC 7591 requires a client to declare it to
// use it; the hub issues a refresh token on every consent regardless, so
// declaring it is a formality that a conformant library performs.
var supportedGrantTypes = []string{
	GrantTypeAuthorizationCode,
	GrantTypeRefreshToken,
	GrantTypeDeviceCode,
}

// normalizeGrantTypes validates a requested set and returns it deduplicated in
// a stable order.
//
// An EMPTY request takes the RFC 7591 section 2 default -- authorization_code
// plus refresh_token -- which is what an app that states nothing means.
func normalizeGrantTypes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return []string{GrantTypeAuthorizationCode, GrantTypeRefreshToken}, nil
	}
	seen := make(map[string]bool, len(requested))
	out := make([]string, 0, len(requested))
	for _, want := range supportedGrantTypes {
		for _, got := range requested {
			if got == want && !seen[want] {
				seen[want] = true
				out = append(out, want)
			}
		}
	}
	for _, got := range requested {
		if !seen[got] {
			return nil, errors.New("unsupported grant_type " + got)
		}
	}
	return out, nil
}

func containsAny(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// registrationResponseFor renders the one-time response, secret included.
// method is the effective token_endpoint_auth_method buildRegistration
// resolved: the registrant's own choice, which the endpoint honours, rather
// than a hardcoded spelling that contradicts what was asked.
func registrationResponseFor(p store.CreateOAuthClientParams, secret, method string, now time.Time) registrationResponse {
	return registrationResponse{
		ClientID:                p.ClientID,
		ClientSecret:            secret,
		ClientIDIssuedAt:        now.Unix(),
		ClientName:              p.ClientName,
		RedirectURIs:            ParseRedirectURIs(p.RedirectURIs),
		GrantTypes:              strings.Fields(p.GrantTypes),
		Scope:                   p.Scopes,
		ClientURI:               p.ClientURI,
		TokenEndpointAuthMethod: method,
	}
}

// --- App assets ---

// handleAppAsset serves /oauth/apps/<client_id>/icon.
//
// SAME ORIGIN, deliberately. A registration could have carried a logo URL
// instead, and that would be a beacon: it reports to the app operator when the
// consent page rendered and from which IP, and its bytes are chosen by the
// registrant, so nothing would stop an unverified app serving a well-known
// icon. Storing the bytes and serving them from here keeps the consent page's
// `img-src 'self'` and takes both problems away.
//
// It serves a VERIFIED app's icon only, matching what the consent page renders.
// An unverified app's stored bytes are never served, so uploading one gains a
// registrant nothing.
func (h *OAuthServerHandler) handleAppAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/oauth/apps/")
	clientID, asset, found := strings.Cut(rest, "/")
	if !found || asset != "icon" || clientID == "" {
		http.NotFound(w, r)
		return
	}
	icon, err := h.appIcon(r.Context(), clientID)
	if err != nil {
		writeInternalError(w, "app icon lookup failed", err)
		return
	}
	if icon == nil {
		http.NotFound(w, r)
		return
	}
	// A closed set of media types, and the STORED value is checked against it
	// rather than trusted: a Content-Type the registrant chose is a sniffing
	// surface, and `image/svg+xml` in particular is a script-execution one.
	if !isAllowedIconMediaType(icon.IconMediaType) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", icon.IconMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// An icon changes only when its owner replaces it, and the consent page is
	// the one reader. A short cache keeps a repeat consent cheap without making
	// a replaced icon linger.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(icon.IconBlob)
}

// appIcon loads an app whose icon may be served: live, verified, and holding
// bytes. Anything else answers nil, which the caller renders as a 404 --
// one answer for every reason, so this endpoint discloses nothing about which
// client ids exist.
//
// It reads the NARROW icon projection: the bytes, the media type, and the two
// facts that gate serving them. The full-row Get carries only whether an icon
// exists, so it cannot answer this question at all.
func (h *OAuthServerHandler) appIcon(ctx context.Context, clientID string) (*store.OAuthClientIcon, error) {
	icon, err := h.store.OAuthClients().GetIcon(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if icon.RevokedAt != nil || !store.ClientIsVerified(icon.RegistrationSource, icon.VerifiedAt) || len(icon.IconBlob) == 0 {
		return nil, nil
	}
	return icon, nil
}

// allowedIconMediaTypes is the closed set an app icon may declare.
//
// SVG is absent on purpose: an SVG is a document that can carry script, and
// although the response's own sandboxed policy would contain it, a media type
// that needs a second control to be safe does not belong on a list a consent
// page reads from.
var allowedIconMediaTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

// IsAllowedIconMediaType reports whether an app icon may declare this type. It
// is exported so the registration surfaces refuse an upload at intake, rather
// than storing bytes that this endpoint will then never serve.
func IsAllowedIconMediaType(mediaType string) bool { return isAllowedIconMediaType(mediaType) }

func isAllowedIconMediaType(mediaType string) bool {
	for _, allowed := range allowedIconMediaTypes {
		if mediaType == allowed {
			return true
		}
	}
	return false
}

// AllowedIconMediaTypes lists the accepted types, for an error message and for
// a test that pins the set.
func AllowedIconMediaTypes() []string {
	return append([]string(nil), allowedIconMediaTypes...)
}

// maxIconBytes caps a stored icon.
//
// The consent page renders it at 48 pixels square, so a large file buys the
// reader nothing and costs the store. It is also a bound on what an
// authenticated registrant can make the hub hold.
const maxIconBytes = 64 << 10

// MaxIconBytes is the cap the registration surfaces enforce.
const MaxIconBytes = maxIconBytes

// assertAppOwner reports whether a caller may write this app's registration.
//
// A HUB-WIDE app needs an administrator; an OWNED one needs its owner. The same
// predicate travels into the store statement, so this is the early refusal with
// a message rather than the enforcement -- see UpdateOAuthClientParams.
func assertAppOwner(app *store.OAuthClient, user *auth.UserInfo) bool {
	if user == nil {
		return false
	}
	if app.IsHubWide() {
		return user.IsAdmin
	}
	return user.ID.Matches(app.OwnerUserID)
}

// decodeJSONBody reads a JSON document with a hard byte cap.
//
// The cap is the point: this endpoint is anonymous when open registration is
// on, so without one an unauthenticated caller can make the hub buffer an
// arbitrary body. DisallowUnknownFields is deliberately NOT set -- RFC 7591
// section 3.1 tells a server to ignore metadata it does not understand, so a
// client library that sends its own extensions must still register.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, into any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("the request body is not a JSON object this endpoint understands")
	}
	return nil
}
