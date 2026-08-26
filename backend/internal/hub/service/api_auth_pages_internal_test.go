package service

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// payloadRaw breaks out of a text node, an attribute, and a quoted attribute
// value alike, so one string covers every slot shape these pages have.
const payloadRaw = `"><script>alert(1)</script>`

// The whole point of moving these pages onto html/template: escaping stops
// being a rule the next writer has to remember at every slot.
//
// The four pages were built with fmt.Sprintf and hand-placed
// html.EscapeString calls. Every one of them was correct -- but device_name
// reaches the activation page from an ANONYMOUS endpoint, so one omitted call
// is script injection on the page where a user grants a year-long credential.
// These cases would have caught the omission; nothing in the old shape could.
func TestConsentPagesEscapeEveryInterpolatedValue(t *testing.T) {
	t.Parallel()

	const payload = payloadRaw

	t.Run("consent page", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 200, consentPageTmpl, consentPageData{
			DeviceName:    payload,
			Username:      payload,
			RedirectURI:   payload,
			State:         payload,
			CodeChallenge: payload,
			AdminScope:    true,
		})
		assertNoInjection(t, w.Body.String())
		// The value still reaches the page -- escaped, not dropped. A page
		// that silently lost the state or the challenge would break the
		// flow rather than secure it.
		assert.Contains(t, w.Body.String(), "&lt;script&gt;")
		assert.Contains(t, w.Body.String(), "administer the hub",
			"the admin notice renders when the grant carries the scope")
	})

	t.Run("activate page", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 200, activatePageTmpl, activatePageData{
			DeviceName:        payload,
			UserCode:          payload,
			AdminScope:        true,
			ShowAdminCheckbox: true,
		})
		assertNoInjection(t, w.Body.String())
	})

	t.Run("elevation required page", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 403, elevationRequiredPageTmpl, payload)
		assertNoInjection(t, w.Body.String())
	})
}

// assertNoInjection fails when a payload escaped its slot. It checks the
// rendered document rather than the escaping call, so it holds whatever
// mechanism does the escaping.
//
// The test is that the RAW payload is absent, not that its text is: an
// escaped `alert(1)` is inert prose and must still reach the page, because a
// renderer that dropped the value would break the flow instead of securing
// it. What must never appear is an unescaped tag or a quote that closes an
// attribute.
func assertNoInjection(t *testing.T, page string) {
	t.Helper()
	assert.NotContains(t, page, payloadRaw, "the raw payload reached the document")
	assert.NotContains(t, page, "<script", "a payload opened a tag")
	// There is deliberately no check for a quote-then-tag sequence: the
	// pages own markup contains one legitimately, in `<p style="...">
	// <label>`, so it would fail against correct output. The raw-payload
	// check above is what covers the attribute breakout, and it is exact.
	// The chrome must still be intact: a payload that closed <body> early
	// would leave a document that renders but is no longer the page.
	assert.True(t, strings.HasPrefix(page, "<!doctype html>"))
	assert.True(t, strings.HasSuffix(strings.TrimSpace(page), "</body></html>"))
}

// The pages that DEPEND on a conditional must render both arms, or a branch
// that never fires is a branch nobody notices is broken.
func TestConsentPagesRenderTheirConditionalArms(t *testing.T) {
	t.Parallel()

	t.Run("no admin notice without the scope", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 200, consentPageTmpl, consentPageData{DeviceName: "laptop", Username: "alice"})
		assert.NotContains(t, w.Body.String(), "administer the hub")
		assert.Contains(t, w.Body.String(), `name="admin" value="0"`,
			"the hidden field still posts the answer back, and requestsAdminScope reads 0 as not granted")
	})

	t.Run("no checkbox for a non-administrator", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 200, activatePageTmpl, activatePageData{UserCode: "ABCD-1234"})
		assert.NotContains(t, w.Body.String(), "checkbox",
			"there is no scope for them to grant, and offering one that is refused reads as a bug")
	})

	t.Run("no device notice for a code that names no live grant", func(t *testing.T) {
		w := httptest.NewRecorder()
		writePage(w, 200, activatePageTmpl, activatePageData{UserCode: "ABCD-1234"})
		assert.NotContains(t, w.Body.String(), "Requested by")
	})

	t.Run("the checkbox is pre-ticked only when the CLI asked", func(t *testing.T) {
		asked := httptest.NewRecorder()
		writePage(asked, 200, activatePageTmpl, activatePageData{ShowAdminCheckbox: true, AdminScope: true})
		assert.Contains(t, asked.Body.String(), "checked")

		typed := httptest.NewRecorder()
		writePage(typed, 200, activatePageTmpl, activatePageData{ShowAdminCheckbox: true})
		assert.NotContains(t, typed.Body.String(), "checked")
	})
}

// Every page shares one chrome, which is the other half of the change: it
// used to be copied into four fmt.Sprintf literals, so a change to it landed
// in some of them and not the rest.
func TestEveryPageSharesOneChrome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		tmpl *template.Template
		data any
	}{
		{"consent", consentPageTmpl, consentPageData{}},
		{"activate", activatePageTmpl, activatePageData{}},
		{"activated", activatedPageTmpl, nil},
		{"elevationRequired", elevationRequiredPageTmpl, "x"},
	} {
		w := httptest.NewRecorder()
		writePage(w, 200, tc.tmpl, tc.data)
		page := w.Body.String()
		assert.Truef(t, strings.HasPrefix(page, "<!doctype html><html><body style="), "page %q", tc.name)
		assert.Truef(t, strings.HasSuffix(strings.TrimSpace(page), "</body></html>"), "page %q", tc.name)
		assert.Equalf(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"), "page %q", tc.name)
	}
}
