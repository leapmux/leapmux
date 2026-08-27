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
//   - Each page held its own copy of the chrome, so a change to it landed
//     in one page and not the others.
//   - Escaping by hand is a rule a writer must remember at EVERY slot, on
//     pages that interpolate attacker-influenced text. device_name arrives
//     at an anonymous endpoint, and the activation page echoes it back. The
//     hand-written calls were all correct; the construction is what makes
//     the next one unpredictable.
//
// html/template escapes by CONTEXT -- differently inside an attribute, a URL
// and a text node -- and it cannot be skipped, so the mistake stops being
// reachable rather than merely being avoided.
//
// The style is inline and plain on purpose. The Go mux serves these pages,
// outside the SPA and its stylesheet, and they must render for somebody who
// arrived from a terminal with no session yet.

// pageChrome wraps each page's body, so the shell exists in one place.
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
<p>The leapmux control CLI on <strong>{{.DeviceName}}</strong> requests access to your account (<strong>{{.Username}}</strong>).</p>
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
// the browser is on a DIFFERENT machine from the machine to authorize.
//
// The scope is a CHECKBOX here rather than a hidden field, because the user
// may reach this page by typing the user code by hand -- the URL that carried
// the CLI's ask is then absent, so the user states the scope themselves.
// Nothing renders for a non-administrator: there is no scope for them to
// grant, and offering one that the hub refuses on submit reads as a bug.
//
// The two grant kinds identify their subject from different sources, and the
// template keeps them apart: a step-up renders .Credential, which the hub read
// from its own api_tokens row, and an issuance renders .DeviceName, which the
// requester chose. A step-up that fell back to .DeviceName would let a stolen
// credential label its own step-up with the owner's laptop name.
var activatePageTmpl = mustParsePage("activate", `{{define "body"}}
<h1>{{if .Elevating}}Verify a CLI credential{{else}}Authorize CLI device{{end}}</h1>
{{if .Elevating}}<p>This grants an existing command-line credential the right to make sensitive changes for the next two hours. It issues nothing new.</p>
{{with .Credential}}<p>The credential is <strong>{{.Name}}</strong>, added {{.Added}} (UTC).{{if .Revoked}} It was revoked, so verifying it grants nothing.{{end}}</p>
{{end}}{{else if .DeviceName}}<p>Requested by <strong>{{.DeviceName}}</strong>.</p>
{{end}}<p>Enter the code that the CLI shows:</p>
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
// carry hub administration. The page the user CLICKS states it, not only
// the CLI that asked, because the browser is where the consent actually
// happens.
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

// activateCredential identifies the credential a step-up grant elevates, from
// the hub's OWN record of it.
//
// The grant's device_name is what the REQUESTER sent, so it identifies nothing
// that a person can trust on a page which asks them to re-arm a credential.
// Every field here comes from the api_tokens row instead: the name the
// account's credential list shows, the date the account added it, and whether
// the account already revoked it.
type activateCredential struct {
	Name  string
	Added string
	// Revoked reports a credential the approval will refuse. The page says
	// so, rather than asking a person to verify something that grants
	// nothing.
	Revoked bool
}

// activatePageData fills activatePageTmpl.
type activatePageData struct {
	// DeviceName is the label the REQUESTER chose, and it is for the
	// ISSUANCE flow alone. It is empty when the code identifies no live
	// grant, and on a step-up, where Credential identifies the subject
	// instead.
	DeviceName        string
	UserCode          string
	AdminScope        bool
	ShowAdminCheckbox bool
	// Elevating re-labels the page for a grant that VERIFIES an existing
	// credential rather than issuing one. The two flows share this page
	// because they share the row and the ceremony; what they must not share
	// is the sentence, because a person who approves a step-up does not hand
	// out a new credential, and the page must not say that they do.
	Elevating bool
	// Credential identifies the credential a step-up verifies. Nil on an
	// issuance, and nil for a step-up whose credential is already gone.
	Credential *activateCredential
}

// writePage renders one page as the whole response.
//
// It renders into a buffer FIRST, because a template that fails halfway
// already wrote a partial document to the client, and the hub already sent
// the status line. Buffering keeps a failure recoverable: the client gets a
// plain error instead of half a consent form with a dangling <form> that the
// browser submits without complaint.
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
