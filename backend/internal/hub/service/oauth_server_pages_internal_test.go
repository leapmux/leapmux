package service

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// payloadRaw breaks out of a text node, an attribute, and a quoted attribute
// value alike, so one string covers every slot shape these pages have.
const payloadRaw = `"><script>alert(1)</script>`

// The whole point of these pages being html/template: escaping stops being a
// rule the next writer has to remember at every slot.
//
// The pages used fmt.Sprintf and hand-placed html.EscapeString calls. Every one
// of them was correct -- but with open registration the app NAME, the redirect
// URI and the scope string are all attacker-chosen, so one omitted call is
// script injection on the page where a user grants a year-long credential.
func TestConsentPagesEscapeEveryInterpolatedValue(t *testing.T) {
	t.Parallel()

	const payload = payloadRaw

	t.Run("consent page", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 200, consentPageTmpl, consentPageData{
			App:              appDisplayData{ClientID: payload, Name: payload, Monogram: payload},
			Username:         payload,
			RedirectLabel:    payload,
			Permissions:      []scopeCategoryView{{Label: payload, Entries: []scopeEntryView{{Token: payload, Sentence: payload}}}},
			RedirectURI:      payload,
			ClientID:         payload,
			State:            payload,
			CodeChallenge:    payload,
			Scope:            payload,
			InstallationName: payload,
		}, "")
		assertNoInjection(t, w.Body.String())
		// The value still reaches the page -- escaped, not dropped. A page that
		// silently lost the state or the challenge would break the flow rather
		// than secure it.
		assert.Contains(t, w.Body.String(), "&lt;script&gt;")
	})

	t.Run("device page", func(t *testing.T) {
		w := httptest.NewRecorder()
		app := appDisplayData{ClientID: payload, Name: payload, Monogram: payload}
		writePage(w, 200, devicePageTmpl, devicePageData{
			UserCode:    payload,
			App:         &app,
			Permissions: []scopeCategoryView{{Label: payload, Entries: []scopeEntryView{{Token: payload, Sentence: payload}}}},
		}, "")
		assertNoInjection(t, w.Body.String())
	})

	// The step-up branch of the same page. Its slots come from the api_tokens
	// row rather than from the grant, and whoever ran the app that minted the
	// credential chooses the installation label -- so it is attacker-influenced
	// text on the page where a user re-arms that credential.
	t.Run("device page for a step-up", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 200, devicePageTmpl, devicePageData{
			UserCode:   payload,
			Elevating:  true,
			Credential: &deviceCredential{Name: payload, Added: payload, Revoked: true},
		}, "")
		assertNoInjection(t, w.Body.String())
		assert.Contains(t, w.Body.String(), "&lt;script&gt;")
		assert.NotContains(t, w.Body.String(), "It is asking to use your account",
			"a step-up must never read as an authorization that issues something")
	})

	t.Run("elevation required page", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 403, elevationRequiredPageTmpl, payload, "")
		assertNoInjection(t, w.Body.String())
	})

	t.Run("invalid request page", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 400, invalidRequestPageTmpl, invalidRequestPageData{Reason: payload}, "")
		assertNoInjection(t, w.Body.String())
	})
}

// assertNoInjection fails when a payload escaped its slot. It checks the
// rendered document rather than the escaping call, so it holds whatever
// mechanism does the escaping.
//
// The test is that the RAW payload is absent, not that its text is: an escaped
// `alert(1)` is inert prose and must still reach the page, because a renderer
// that dropped the value would break the flow instead of securing it. What must
// never appear is an unescaped tag or a quote that closes an attribute.
func assertNoInjection(t *testing.T, page string) {
	t.Helper()
	assert.NotContains(t, page, payloadRaw, "the raw payload reached the document")
	assert.NotContains(t, page, "<script", "a payload opened a tag")
	// The chrome must still be intact: a payload that closed <body> early would
	// leave a document that renders but is no longer the page.
	assert.True(t, strings.HasPrefix(page, "<!doctype html>"))
	assert.True(t, strings.HasSuffix(strings.TrimSpace(page), "</body></html>"))
}

// TestConsentPageKeepsAnUnverifiedNameOutOfTheHeading is the phishing defence
// that the markup alone provides.
//
// An unverified app supplies its own name. If that name entered the <h1>, an
// app called "LeapMux Official Security Check" would borrow the heading's
// authority -- so the heading states the VERDICT, and the chosen name appears
// afterwards, inside a paragraph, in quotation marks, after a sentence that
// already said nobody verified it.
func TestConsentPageKeepsAnUnverifiedNameOutOfTheHeading(t *testing.T) {
	t.Parallel()

	const claimed = "LeapMux Official Security Check"
	w := httptest.NewRecorder()
	writePage(w, 200, consentPageTmpl, consentPageData{
		App:      appDisplayData{ClientID: "c1", Name: claimed, Verified: false, Monogram: "L"},
		Username: "alice",
	}, "")
	page := w.Body.String()

	heading := between(t, page, "<h1>", "</h1>")
	assert.Equal(t, "Authorize an unverified app?", heading)
	assert.NotContains(t, heading, claimed, "an unverified name must never enter the heading")
	assert.Contains(t, page, "Nobody verified this app on this hub")
	assert.Contains(t, page, "&ldquo;"+claimed+"&rdquo;",
		"the chosen name still appears, in quotation marks, after the verdict")

	// A VERIFIED app is the other branch: an administrator vouched, so its name
	// carries the heading.
	verified := httptest.NewRecorder()
	writePage(verified, 200, consentPageTmpl, consentPageData{
		App:      appDisplayData{ClientID: "c1", Name: "Acme Deploy", Verified: true, Monogram: "A"},
		Username: "alice",
	}, "")
	assert.Equal(t, "Authorize Acme Deploy?", between(t, verified.Body.String(), "<h1>", "</h1>"))
	assert.NotContains(t, verified.Body.String(), "Nobody has verified")
}

// TestConsentPagePutsDenyFirstAndAutofocusesNeither pins the two properties of
// the markup that decide what a stray keypress does.
//
// Deny first in DOM ORDER means Deny takes the first tab stop. Neither button
// is autofocused, so an Enter pressed on the way to reading the page grants
// nothing.
func TestConsentPagePutsDenyFirstAndAutofocusesNeither(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	writePage(w, 200, consentPageTmpl, consentPageData{
		App:      appDisplayData{ClientID: "c1", Name: "Acme", Verified: true, Monogram: "A"},
		Username: "alice",
	}, "")
	page := w.Body.String()

	deny := strings.Index(page, `value="deny"`)
	allow := strings.Index(page, `value="allow"`)
	require.Positive(t, deny, "the page must offer Deny at all")
	require.Positive(t, allow)
	assert.Less(t, deny, allow, "Deny must come first, so it takes the first tab stop")
	assert.NotContains(t, page, "autofocus", "a stray Enter must not grant")
}

// TestConsentPageFetchesNothingForAnUnverifiedApp pins the reason a remote logo
// URL was rejected.
//
// An icon fetched from the registrant's own server is a beacon: it reports when
// the consent page rendered and from which IP, and its bytes are chosen by the
// registrant, so nothing stops an unverified app serving a well-known icon. A
// verified app's icon is stored and served from this origin; an unverified one
// renders a monogram, which fetches nothing at all.
func TestConsentPageFetchesNothingForAnUnverifiedApp(t *testing.T) {
	t.Parallel()

	unverified := httptest.NewRecorder()
	writePage(unverified, 200, consentPageTmpl, consentPageData{
		App:      appDisplay(clientPtr(testClient{name: "Acme", verified: false, icon: true})),
		Username: "alice",
	}, "")
	assert.NotContains(t, unverified.Body.String(), "<img",
		"an unverified app must fetch nothing; it renders a monogram")
	assert.Contains(t, unverified.Body.String(), ">A<", "the monogram is the first letter of the chosen name")

	verified := httptest.NewRecorder()
	writePage(verified, 200, consentPageTmpl, consentPageData{
		App:      appDisplay(clientPtr(testClient{name: "Acme", verified: true, icon: true})),
		Username: "alice",
	}, "")
	assert.Contains(t, verified.Body.String(), `src="/oauth/apps/c1/icon"`,
		"a verified icon is served SAME ORIGIN, so the page's img-src stays 'self'")
}

// TestWritePageCarriesItsSecurityHeaders pins the headers every page here needs.
//
// They are set inside writePage rather than at each call site, so a new page
// cannot ship without them. `frame-ancestors 'none'` is the clickjacking
// defence for a page whose one button grants authority, and it was missing
// entirely before.
func TestWritePageCarriesItsSecurityHeaders(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	writePage(w, 200, elevationRequiredPageTmpl, "x", "https://app.example.com")
	h := w.Header()

	csp := h.Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'none'")
	assert.Contains(t, csp, "frame-ancestors 'none'")
	assert.Contains(t, csp, "base-uri 'none'")
	assert.Contains(t, csp, "img-src 'self' data:")
	assert.Contains(t, csp, "form-action 'self' https://app.example.com",
		"the consent POST redirects to the app, and a browser matches form-action against every hop")
	assert.NotContains(t, csp, "script-src", "these pages load no script; default-src 'none' covers it")

	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"),
		"this URL carries state and code_challenge; the redirect must not hand them to the app's origin")
	assert.Equal(t, "no-store", h.Get("Cache-Control"))
	assert.Equal(t, "text/html; charset=utf-8", h.Get("Content-Type"))
}

// TestWritePageOmitsTheRedirectSourceWhenThereIsNone pins that a page with no
// redirect target keeps the tightest policy, rather than widening to a
// placeholder.
func TestWritePageOmitsTheRedirectSourceWhenThereIsNone(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	writePage(w, 200, elevationRequiredPageTmpl, "x", "")
	assert.Contains(t, w.Header().Get("Content-Security-Policy"), "form-action 'self';")
}

// TestEveryPageSharesOneChrome is the other half of the change: the pages used
// to hold their own copy of the document shell, so a change to it landed in
// some of them and not the rest.
func TestEveryPageSharesOneChrome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		tmpl *template.Template
		data any
	}{
		{"consent", consentPageTmpl, consentPageData{}},
		{"device", devicePageTmpl, devicePageData{}},
		{"deviceDone", deviceDonePageTmpl, deviceDonePageData{}},
		{"elevationRequired", elevationRequiredPageTmpl, "x"},
		{"invalidRequest", invalidRequestPageTmpl, invalidRequestPageData{}},
	} {
		w := httptest.NewRecorder()
		writePage(w, 200, tc.tmpl, tc.data, "")
		page := w.Body.String()
		// The chrome prefix now carries the shared <style> block; the suffix
		// still closes the card, body and html the chrome opens.
		assert.Truef(t, strings.HasPrefix(page, "<!doctype html><html><head>"), "page %q", tc.name)
		assert.Truef(t, strings.Contains(page, "<style>"), "page %q carries the one stylesheet", tc.name)
		assert.Truef(t, strings.HasSuffix(strings.TrimSpace(page), "</main>\n</body></html>"), "page %q", tc.name)
	}
}

// TestEveryGrantableScopeHasASentence is the tripwire on the consent
// vocabulary: a scope with no sentence would render as its wire token, which
// tells a reader deciding whether to grant it nothing at all.
func TestEveryGrantableScopeHasASentence(t *testing.T) {
	t.Parallel()

	for _, scope := range authscope.Grantable() {
		sentence, ok := scopeSentences[scope]
		assert.Truef(t, ok, "%s has no consent-screen sentence", scope)
		assert.NotEmptyf(t, sentence, "%s has an empty consent-screen sentence", scope)
		assert.Truef(t, strings.HasSuffix(sentence, "."), "%s's sentence must be a sentence", scope)
	}
	for scope := range scopeSentences {
		assert.Truef(t, authscope.IsGrantable(scope),
			"%s has a consent sentence but no account can grant it", scope)
	}
}

// TestScopeCategoriesCoverEveryGrantableScope is the catalogue's membership
// pin: a scope added to the proto fails here until somebody states its
// family, and one removed fails here rather than lingering as a row the page
// renders for a permission no account can grant.
func TestScopeCategoriesCoverEveryGrantableScope(t *testing.T) {
	t.Parallel()

	seen := map[leapmuxv1.Scope]int{}
	for _, category := range scopeCategories {
		assert.NotEmptyf(t, category.label, "a category carries a label")
		for _, scope := range category.scopes {
			seen[scope]++
			_, hasToken := authscope.Token(scope)
			assert.Truef(t, hasToken, "%s has no wire token", scope)
			assert.NotEmptyf(t, scopeSentences[scope], "%s has no consent-screen sentence", scope)
		}
	}
	for _, scope := range authscope.Grantable() {
		assert.Equalf(t, 1, seen[scope], "%s must appear in exactly one category", scope)
	}
}

// TestDescribeScopeCatalogue pins the two properties the page leans on: the
// asked-for permissions are the ticked ones, and a family with nothing
// granted is skipped rather than rendered as a wall of dimmed rows.
func TestDescribeScopeCatalogue(t *testing.T) {
	t.Parallel()

	set := authscope.MustNew(
		leapmuxv1.Scope_SCOPE_GIT_READ,
		leapmuxv1.Scope_SCOPE_ACCOUNT_READ,
	)
	out := describeScopeCatalogue(set)

	// Family order is the catalogue's own, and only the two touched families
	// render.
	require.Len(t, out, 2)
	assert.Equal(t, "Account", out[0].Label)
	assert.Equal(t, "Git", out[1].Label)

	accountRead := out[0].Entries[0]
	assert.True(t, accountRead.Granted)
	assert.Equal(t, "account:read", accountRead.Token)
	assert.Equal(t, scopeSentences[leapmuxv1.Scope_SCOPE_ACCOUNT_READ], accountRead.Sentence)
	assert.False(t, out[0].Entries[1].Granted,
		"the write half of the family is the NOT-GRANTED half the page dims")
	assert.True(t, out[1].Entries[0].Granted)

	assert.Empty(t, describeScopeCatalogue(authscope.ScopeSet{}),
		"an empty grant lists nothing, and the page says so in prose instead")
}

// TestMonogramOfSkipsNonLetters keeps the neutral square readable for a name
// that starts with an emoji or a quotation mark, which is exactly what an app
// trying to look official would choose.
func TestMonogramOfSkipsNonLetters(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "A", monogramOf("Acme"))
	assert.Equal(t, "A", monogramOf(`"Acme"`))
	assert.Equal(t, "A", monogramOf("🚀 Acme"))
	assert.Equal(t, "?", monogramOf("🚀🚀"))
	assert.Equal(t, "?", monogramOf(""))
}

// between extracts the text between two markers, so a heading assertion reads
// the heading rather than the whole document.
func between(t *testing.T, page, open, close string) string {
	t.Helper()
	start := strings.Index(page, open)
	require.GreaterOrEqual(t, start, 0, "marker %q not found", open)
	rest := page[start+len(open):]
	end := strings.Index(rest, close)
	require.GreaterOrEqual(t, end, 0, "marker %q not found", close)
	return rest[:end]
}

// testClient builds a minimal registration for a page test, so a case states
// only the two facts it cares about rather than a whole store row.
type testClient struct {
	name     string
	verified bool
	icon     bool
}

func (c testClient) build() store.OAuthClient {
	out := store.OAuthClient{ClientID: "c1", ClientName: c.name}
	if c.verified {
		at := time.Unix(0, 0).UTC()
		out.VerifiedAt = &at
		out.VerifiedBy = "u-admin"
	}
	if c.icon {
		out.HasIcon = true
	}
	return out
}

// clientPtr builds the registration and hands back a pointer, because
// appDisplay takes one.
func clientPtr(c testClient) *store.OAuthClient {
	out := c.build()
	return &out
}

// TestInvalidRequestSentencesAreAClosedSet pins the page's whole vocabulary.
//
// invalidRequestPageData carries ONE field, and it is filled from this
// function alone -- so the closed set here is the closed set the page can
// render. RFC 6749 section 4.1.2.1 forbids redirecting an error to an
// unregistered address, which makes this page the last place attacker-chosen
// text could reach a browser, and a `default` arm that passed the error code
// through would be exactly that.
//
// The assertion is over the OUTPUT rather than over the arms: a code the
// switch does not name must still produce a hub-authored sentence, and the
// arbitrary codes below are what proves the default arm answers rather than
// echoes.
func TestInvalidRequestSentencesAreAClosedSet(t *testing.T) {
	t.Parallel()

	authored := map[string]bool{
		"That app is not registered on this hub, or it is not available to your account.": true,
		"The hub could not read this app's registration.":                                 true,
		"The address this app asked the hub to return to is not one it registered.":       true,
	}
	for _, code := range []string{
		// The two the switch names.
		"invalid_client", "server_error",
		// The ones it does not, including values a caller could put in a link.
		"invalid_request", "invalid_scope", "unauthorized_client", "",
		"<script>alert(1)</script>", "https://evil.example.com/callback",
	} {
		sentence := invalidRequestSentence(code)
		assert.Truef(t, authored[sentence],
			"invalidRequestSentence(%q) produced %q, which is not one of the hub's own sentences", code, sentence)
		if code != "" {
			assert.NotContainsf(t, sentence, code,
				"the sentence for %q must not echo the code back", code)
		}
	}
}
