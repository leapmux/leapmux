package service

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
)

// Every page the CLI authorization flow renders, in one place.
//
// The four pages were four fmt.Sprintf calls in three files, each repeating
// the same document chrome verbatim and each escaping its own slots by hand
// with html.EscapeString. Both halves of that were a problem:
//
//   - The chrome was copied, so a change to it landed in one page and not
//     the others.
//   - Escaping by hand is a rule a writer must remember at EVERY slot, on
//     pages that interpolate attacker-influenced text. device_name arrives
//     at an anonymous endpoint, and the activation page echoes it back. The
//     hand-written calls were all correct; the construction is what makes
//     the next one a coin toss.
//
// html/template escapes by CONTEXT -- differently inside an attribute, a URL
// and a text node -- and it cannot be skipped, so the mistake stops being
// reachable rather than merely being avoided.
//
// The style is inline and plain on purpose. These pages are served by the Go
// mux, outside the SPA and its stylesheet, and they must render for somebody
// who arrived from a terminal with no session yet.

// pageChrome wraps each page's body, so the shell is written once.
const pageChrome = `<!doctype html><html><body style="font-family:sans-serif;max-width:480px;margin:48px auto;">
{{block "body" .}}{{end}}
</body></html>`

// consentPageTmpl is the local-redirect consent form: the CLI printed a URL,
// the browser opened it, and this is where the user allows the grant.
//
// The hidden fields carry the PKCE challenge, the state and the redirect URI
// back to handleAuthorize unchanged. They are hidden rather than re-derived
// because the leg that reads them refuses to bounce, so nothing may be lost
// between the two.
var consentPageTmpl = mustParsePage("consent", `{{define "body"}}
<h1>Authorize CLI access?</h1>
<p>The leapmux control CLI on <strong>{{.DeviceName}}</strong> is requesting access to your account (<strong>{{.Username}}</strong>).</p>
{{template "adminNotice" .AdminScope}}
<form method="POST" action="/auth/cli/authorize">
  <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}"/>
  <input type="hidden" name="state" value="{{.State}}"/>
  <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}"/>
  <input type="hidden" name="device_name" value="{{.DeviceName}}"/>
  <input type="hidden" name="admin" value="{{if .AdminScope}}1{{else}}0{{end}}"/>
  <button type="submit" style="padding:10px 16px;font-size:14px;">Allow</button>
</form>
{{end}}`)

// activatePageTmpl is the device-code entry form, for the flow that runs when
// the browser is on a DIFFERENT machine from the one being authorized.
//
// The scope is a CHECKBOX here rather than a hidden field, because this page
// may be reached by typing the user code by hand -- the URL that carried the
// CLI's ask is then absent, so the user states the scope themselves. Nothing
// renders for a non-administrator: there is no scope for them to grant, and
// offering one that is refused on submit reads as a bug.
var activatePageTmpl = mustParsePage("activate", `{{define "body"}}
<h1>{{if .Elevating}}Verify a CLI credential{{else}}Authorize CLI device{{end}}</h1>
{{if .Elevating}}<p>This grants an existing command-line credential the right to make sensitive changes for the next two hours. It issues nothing new.</p>{{end}}
{{if .DeviceName}}<p>Requested by <strong>{{.DeviceName}}</strong>.</p>{{end}}
<p>Enter the code displayed by the CLI:</p>
<form method="POST" action="/auth/cli/activate">
<input name="user_code" value="{{.UserCode}}" pattern="[A-Z0-9-]{6,8}" autofocus required style="font-size:24px;letter-spacing:2px;text-align:center;width:100%;padding:8px;"/>
{{if .ShowAdminCheckbox}}<p style="margin-top:16px;"><label><input type="checkbox" name="admin" value="1"{{if .AdminScope}} checked{{end}}/> Also allow this device to <strong>administer the hub</strong> (manage every user, worker, and setting).</label></p>
{{end}}<p><button type="submit" style="margin-top:16px;padding:10px 16px;">Authorize</button></p>
</form>
{{end}}`)

// activatedPageTmpl is the end of the device flow. It carries no data, and
// says nothing about WHAT was authorized: the CLI on the other machine is
// where the result belongs.
var activatedPageTmpl = mustParsePage("activated", `{{define "body"}}
<h1>{{if .Elevating}}Credential verified{{else}}Device authorized{{end}}</h1>
<p>You can close this window and return to the CLI.</p>
{{end}}`)

// activatedPageData fills activatedPageTmpl. It carries the one bit that
// changes the sentence, and nothing about WHAT was authorized: the CLI on the
// other machine is where the result belongs.
type activatedPageData struct {
	Elevating bool
}

// elevationRequiredPageTmpl is the refusal a consent leg writes when the
// session proved no factor recently. See writeElevationRequiredPage.
var elevationRequiredPageTmpl = mustParsePage("elevationRequired", `{{define "body"}}
<h1>Verify your identity</h1>
<p>{{.}}</p>
{{end}}`)

// adminNoticeTmpl is the warning the consent page shows when the grant would
// carry hub administration. It is stated on the page the user CLICKS, not
// only in the CLI that asked, because the browser is where the consent
// actually happens.
const adminNoticeTmpl = `{{define "adminNotice"}}{{if .}}<p style="padding:8px 12px;border:1px solid #c00;border-radius:4px;">This credential will also be able to <strong>administer the hub</strong>: manage every user, worker, and setting.</p>
{{end}}{{end}}`

// mustParsePage builds one page from the shared chrome plus its own body.
//
// It panics on a parse failure, which is right for a template literal: the
// text is a constant, so a failure is a programming error present at every
// start rather than a condition a request can reach.
func mustParsePage(name, body string) *template.Template {
	return template.Must(template.New(name).Parse(pageChrome + adminNoticeTmpl + body))
}

// consentPageData fills consentPageTmpl.
type consentPageData struct {
	DeviceName    string
	Username      string
	RedirectURI   string
	State         string
	CodeChallenge string
	AdminScope    bool
}

// activatePageData fills activatePageTmpl. DeviceName is empty when the code
// names no live grant, and the notice then renders nothing.
type activatePageData struct {
	DeviceName        string
	UserCode          string
	AdminScope        bool
	ShowAdminCheckbox bool
	// Elevating re-labels the page for a grant that VERIFIES an existing
	// credential rather than issuing one. The two flows share this page
	// because they share the row and the ceremony; what they must not share
	// is the sentence, because a person approving a step-up is not handing
	// out a new credential and must not be told they are.
	Elevating bool
}

// writePage renders one page as the whole response.
//
// It renders into a buffer FIRST, because a template that fails halfway has
// already written a partial document to the client and the status line is
// long gone. Buffering keeps a failure recoverable: the client gets a plain
// error instead of half a consent form with a dangling <form> the browser
// will happily submit.
func writePage(w http.ResponseWriter, status int, tmpl *template.Template, data any) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		slog.Error("render CLI authorization page", "template", tmpl.Name(), "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
