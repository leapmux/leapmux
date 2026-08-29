package service

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// Every page the authorization server renders, in one place.
//
// They stay Go html/template pages rather than SPA routes, for three reasons
// and each one is independent:
//
//   - Almost every slot is ATTACKER-INFLUENCED. After open registration the app
//     name, the icon, the redirect URI and the scope string all are.
//     html/template escapes per CONTEXT -- differently inside an attribute, a
//     URL and a text node -- and it cannot be skipped, so the mistake stops
//     being reachable rather than merely being avoided.
//   - They must render for somebody who arrived from a terminal with no session
//     and no bundle.
//   - An SPA route at /oauth/authorize would flip isServerRoute() to the client
//     branch, which is the exact 404-mid-ceremony failure
//     tests/e2e/143-app-consent.spec.ts exists to catch.
//
// The style is inline and plain on purpose: the Go mux serves these outside the
// SPA and its stylesheet.

// pageChrome wraps each page's body, so the shell exists in one place.
const pageChrome = `<!doctype html><html><body style="font-family:sans-serif;max-width:520px;margin:48px auto;line-height:1.5;">
{{block "body" .}}{{end}}
</body></html>`

// consentPageTmpl is the authorization-code consent form.
//
// Three deliberate choices in the markup, and each one is a defence:
//
//   - AN UNVERIFIED NAME NEVER ENTERS THE HEADING. The heading states the
//     verdict ("Authorize an unverified app?") and the chosen name appears
//     afterwards, inside a paragraph, in quotation marks -- so an app named
//     "LeapMux Official Security Check" cannot borrow the heading's authority.
//   - DENY COMES FIRST IN DOM ORDER, so it takes the first tab stop. Neither
//     button is autofocused, so a stray Enter grants nothing.
//   - THE REDIRECT IS A LABEL, never a URI. See redirectLabel.
//
// The hidden fields carry the whole request back to handleConsent, which
// re-validates every one of them: the form is attacker-writable, and having
// rendered a page makes nothing that comes back trustworthy.
var consentPageTmpl = mustParsePage("consent", `{{define "body"}}
{{if .App.Verified}}<h1>Authorize {{.App.Name}}?</h1>
{{else}}<h1>Authorize an unverified app?</h1>
<p style="padding:8px 12px;border:1px solid #c00;border-radius:4px;">Nobody verified this app on this hub. It says its name is &ldquo;{{.App.Name}}&rdquo;. Continue only if you started this yourself.</p>
{{end}}
{{template "appIdentity" .App}}
<p>It asks to use your account (<strong>{{.Username}}</strong>) and will return to {{.RedirectLabel}}.</p>
{{if .Permissions}}<p>It would be able to:</p>
<ul>{{range .Permissions}}<li>{{.}}</li>{{end}}</ul>
{{else}}<p>It asks for no permissions at all, so it would be able to do nothing with your account.</p>
{{end}}
<form method="POST" action="/oauth/consent">
  <input type="hidden" name="client_id" value="{{.ClientID}}"/>
  <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}"/>
  <input type="hidden" name="response_type" value="code"/>
  <input type="hidden" name="code_challenge_method" value="S256"/>
  <input type="hidden" name="state" value="{{.State}}"/>
  <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}"/>
  <input type="hidden" name="scope" value="{{.Scope}}"/>
  <input type="hidden" name="installation_name" value="{{.InstallationName}}"/>
  <button type="submit" name="decision" value="deny" style="padding:10px 16px;font-size:14px;">Deny</button>
  <button type="submit" name="decision" value="allow" style="padding:10px 16px;font-size:14px;margin-left:8px;">Allow</button>
</form>
{{end}}`)

// appIdentityTmpl renders the app's icon and name.
//
// A VERIFIED app shows its stored icon, served from this origin so the page's
// img-src stays 'self'. An UNVERIFIED one shows a MONOGRAM -- the first letter
// of its name in a neutral square -- which fetches nothing at all, so an
// unverified registrant can neither borrow a well-known icon nor learn when the
// consent page rendered and from which address.
const appIdentityTmpl = `{{define "appIdentity"}}
<p>{{if .HasIcon}}<img src="/oauth/apps/{{.ClientID}}/icon" alt="" width="48" height="48" style="vertical-align:middle;border-radius:8px;"/>{{else}}<span aria-hidden="true" style="display:inline-block;width:48px;height:48px;line-height:48px;text-align:center;border-radius:8px;background:#e5e5e5;font-size:24px;vertical-align:middle;">{{.Monogram}}</span>{{end}}
<strong style="margin-left:12px;font-size:18px;">{{.Name}}</strong></p>
{{end}}`

// devicePageTmpl is the device-code entry form, for the flow that runs when the
// browser is on a DIFFERENT machine from the one to authorize.
//
// The two grant kinds identify their subject from different sources, and the
// template keeps them apart: a step-up renders .Credential, which the hub read
// from its own api_tokens row, and an issuance renders .App, which the
// registration supplies. A step-up that fell back to a requester-chosen label
// would let a stolen credential label its own step-up with the owner's laptop
// name.
var devicePageTmpl = mustParsePage("device", `{{define "body"}}
<h1>{{if .Elevating}}Verify an app credential{{else}}Authorize an app{{end}}</h1>
{{if .Elevating}}<p>This grants an existing app credential the right to make sensitive changes for the next two hours. It issues nothing new.</p>
{{with .Credential}}<p>The credential is <strong>{{.Name}}</strong>, added {{.Added}} (UTC).{{if .Revoked}} It was revoked, so verifying it grants nothing.{{end}}</p>
{{end}}{{else if .App}}{{if not .App.Verified}}<p style="padding:8px 12px;border:1px solid #c00;border-radius:4px;">Nobody verified this app on this hub. It says its name is &ldquo;{{.App.Name}}&rdquo;.</p>
{{end}}{{template "appIdentity" .App}}
{{if .ConfirmAdmin}}<p style="padding:8px 12px;border:1px solid #c00;border-radius:4px;">This code asks for <strong>hub administration</strong>. Authorizing it grants the app administrator authority over this hub. Continue only if you started this request yourself and meant to grant it.</p>
{{end}}
{{if .Permissions}}<p>It would be able to:</p>
<ul>{{range .Permissions}}<li>{{.}}</li>{{end}}</ul>
{{end}}{{end}}<p>Enter the code that the app shows:</p>
<form method="POST" action="/oauth/device">
<input name="user_code" value="{{.UserCode}}" pattern="[A-Za-z0-9-]{6,8}" autofocus required style="font-size:24px;letter-spacing:2px;text-align:center;width:100%;padding:8px;"/>
{{if .ConfirmAdmin}}<input type="hidden" name="admin_confirmed" value="1"/>
{{end}}
<p style="margin-top:16px;"><button type="submit" name="decision" value="deny" style="padding:10px 16px;">Deny</button>
<button type="submit" name="decision" value="allow" style="padding:10px 16px;margin-left:8px;">Authorize</button></p>
</form>
{{end}}`)

// deviceDonePageTmpl is the end of the device flow. It says nothing about WHAT
// was authorized: the program on the other machine is where the result belongs.
var deviceDonePageTmpl = mustParsePage("deviceDone", `{{define "body"}}
<h1>{{if .Denied}}Refused{{else if .Elevating}}Credential verified{{else}}App authorized{{end}}</h1>
<p>You can close this window and return to the program that asked.</p>
{{end}}`)

// deviceDonePageData fills deviceDonePageTmpl.
type deviceDonePageData struct {
	Elevating bool
	Denied    bool
}

// elevationRequiredPageTmpl is the refusal a consent endpoint writes when the
// session proved no factor recently. See writeElevationRequiredPage.
var elevationRequiredPageTmpl = mustParsePage("elevationRequired", `{{define "body"}}
<h1>Verify your identity</h1>
<p>{{.}}</p>
{{end}}`)

// invalidRequestPageTmpl answers a request whose client or redirect URI could
// not be verified, so no redirect is allowed.
//
// Its one slot is a sentence the HUB wrote; see invalidRequestSentence.
var invalidRequestPageTmpl = mustParsePage("invalidRequest", `{{define "body"}}
<h1>This authorization request is not valid</h1>
<p>{{.Reason}}</p>
<p>LeapMux will not send you back to the address the request asked for, because that address is not one it can verify.</p>
{{end}}`)

type invalidRequestPageData struct {
	Reason string
}

// mustParsePage builds one page from the shared chrome plus its own body.
//
// It panics on a parse failure, which is right for a template literal: the
// text is a constant, so a failure is a programming error present at every
// start rather than a condition a request can reach.
func mustParsePage(name, body string) *template.Template {
	return template.Must(template.New(name).Parse(pageChrome + appIdentityTmpl + body))
}

// appDisplayData is what a page says about one app. Every field comes from the
// REGISTRATION; nothing here is chosen by whoever built the request URL.
type appDisplayData struct {
	ClientID string
	Name     string
	Verified bool
	HasIcon  bool
	Monogram string
}

// appDisplay projects a registration for a page.
//
// The icon is shown only for a VERIFIED app. An unverified registrant supplies
// the bytes, so an icon from one would let an app that nobody vouched for wear
// a familiar face on the one screen where a person decides.
func appDisplay(app *store.OAuthClient) appDisplayData {
	name := strings.TrimSpace(app.ClientName)
	if name == "" {
		name = "an unnamed app"
	}
	return appDisplayData{
		ClientID: app.ClientID,
		Name:     name,
		Verified: app.IsVerified(),
		HasIcon:  app.IsVerified() && app.HasIcon,
		Monogram: monogramOf(name),
	}
}

// monogramOf is the first LETTER of a name, upper-cased, for the neutral square
// an unverified app renders instead of an icon.
//
// It skips anything that is not a letter, so a name starting with an emoji or a
// quotation mark still yields a readable initial rather than a box glyph. A
// name with no letter at all yields "?", which is honest.
func monogramOf(name string) string {
	for _, r := range name {
		if unicode.IsLetter(r) {
			return strings.ToUpper(string(r))
		}
	}
	return "?"
}

// consentPageData fills consentPageTmpl.
type consentPageData struct {
	App              appDisplayData
	Username         string
	RedirectLabel    string
	Permissions      []string
	RedirectURI      string
	ClientID         string
	State            string
	CodeChallenge    string
	Scope            string
	InstallationName string
}

// deviceCredential identifies the credential a step-up grant elevates, from the
// hub's OWN record of it.
//
// The grant's requester-supplied label identifies nothing a person can trust on
// a page which asks them to re-arm a credential. Every field here comes from the
// api_tokens row instead: the name the account's connected-app list shows, the
// date the account added it, and whether the account already revoked it.
type deviceCredential struct {
	Name  string
	Added string
	// Revoked reports a credential the approval will refuse. The page says so,
	// rather than asking a person to verify something that grants nothing.
	Revoked bool
}

// devicePageData fills devicePageTmpl.
type devicePageData struct {
	UserCode    string
	App         *appDisplayData
	Permissions []string
	// ConfirmAdmin re-renders the page as the SECOND step of an
	// admin-reaching ask: the first Allow returned this page with the admin
	// sentences stated beside a caution, and the form now carries
	// admin_confirmed so the next Allow binds. The device flow is the
	// phishing classic -- a code typed on a different machine from the one
	// it authorizes -- and one click on a trusted app name is all a phished
	// administrator ever needs to hand away. A deliberate second stop, never
	// a silent narrowing: the page the person confirms shows exactly what
	// will bind.
	ConfirmAdmin bool
	// Elevating re-labels the page for a grant that VERIFIES an existing
	// credential rather than issuing one. The two flows share this page because
	// they share the row and the ceremony; what they must not share is the
	// sentence, because a person who approves a step-up does not hand out a new
	// credential, and the page must not say that they do.
	Elevating bool
	// Credential identifies the credential a step-up verifies. Nil on an
	// issuance, and nil for a step-up whose credential is already gone.
	Credential *deviceCredential
}

// scopeSentences is what each permission means in a sentence a person can act
// on. It is the consent screen's whole vocabulary.
//
// A scope with no sentence would render as its wire token, which tells a reader
// nothing; TestEveryGrantableScopeHasASentence fails the suite instead.
var scopeSentences = map[leapmuxv1.Scope]string{
	leapmuxv1.Scope_SCOPE_ACCOUNT_READ:    "Read your profile: your username, your email address and whether you are an administrator.",
	leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE:   "Change your profile and your account settings, including your password.",
	leapmuxv1.Scope_SCOPE_WORKSPACE_READ:  "Read your workspaces, your tabs and your layout.",
	leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE: "Create, rename, move and close your workspaces and tabs.",
	leapmuxv1.Scope_SCOPE_WORKER_READ:     "List your workers and connect to one.",
	leapmuxv1.Scope_SCOPE_WORKER_ADMIN:    "Rename and deregister your workers, and manage the keys that let a machine join.",
	leapmuxv1.Scope_SCOPE_AGENT_READ:      "Read your coding-agent sessions, including every message in them.",
	leapmuxv1.Scope_SCOPE_AGENT_WRITE:     "Send prompts to your coding agents and answer their permission requests.",
	leapmuxv1.Scope_SCOPE_TERMINAL_READ:   "Read the output of your terminals.",
	leapmuxv1.Scope_SCOPE_TERMINAL_WRITE:  "Type into your terminals, which runs any command on your machine.",
	leapmuxv1.Scope_SCOPE_FILE_READ:       "Browse and read files on your machines.",
	leapmuxv1.Scope_SCOPE_GIT_READ:        "Read the git state of your repositories: status, branches, diffs and history.",
	leapmuxv1.Scope_SCOPE_GIT_WRITE:       "Commit, push, and create or delete branches in your repositories.",
	leapmuxv1.Scope_SCOPE_TUNNEL_OPEN:     "Open network connections from inside your private network to any address it can reach.",
	leapmuxv1.Scope_SCOPE_ADMIN_READ:      "Read this hub's administration: every account, setting, worker and credential.",
	leapmuxv1.Scope_SCOPE_ADMIN_USERS:     "Administer every account on this hub, including resetting passwords.",
	leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS:  "Change this hub's settings, including its security policy and its sign-in providers.",
	leapmuxv1.Scope_SCOPE_ADMIN_WORKERS:   "Administer every worker on this hub.",
	leapmuxv1.Scope_SCOPE_ADMIN_APPS:      "Register, edit, vouch, retire and delete the hub's app registrations.",
}

// describeScopes renders a grant as the sentences a consent screen lists.
//
// The order is the SET's canonical order, so two apps asking for the same
// permissions show the same list. A scope with no sentence is dropped rather
// than rendered as a token: the suite already fails for a missing one, so
// reaching this branch means the vocabulary grew between a release and a
// deployment, and a bullet a person cannot read is worse than one fewer.
func describeScopes(set authscope.ScopeSet) []string {
	scopes := set.Scopes()
	if set.IsUnscoped() {
		// No app ever holds an unscoped grant, but a page that rendered one as
		// "SCOPE_ALL" would be a silent lie about what was asked for.
		scopes = authscope.Grantable()
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if sentence, ok := scopeSentences[scope]; ok {
			out = append(out, sentence)
		}
	}
	return out
}

// writePage renders one page as the whole response, with the security headers
// every page here needs.
//
// It renders into a buffer FIRST, because a template that fails halfway already
// wrote a partial document to the client, and the hub already sent the status
// line. Buffering keeps a failure recoverable: the client gets a plain error
// instead of half a consent form with a dangling <form> that the browser
// submits without complaint.
//
// The headers are set HERE rather than at each call site, so a new page cannot
// ship without them:
//
//   - A policy of its own, tighter than the app's. These pages load no script,
//     no font and no frame, so everything but the two they do use is 'none'.
//     `frame-ancestors 'none'` is the clickjacking defence for a page whose one
//     button grants authority, and `base-uri 'none'` stops an injected <base>
//     from re-pointing the form.
//   - `form-action` lists 'self' plus THIS grant's redirect origin, and nothing
//     else. A browser matches form-action against every hop of a submission's
//     redirect chain, so the consent POST's redirect to the app needs its
//     origin stated -- and stating one origin per request is narrower than the
//     wildcard loopback set the global policy used to carry for every page.
//   - `Referrer-Policy: no-referrer`, because this URL carries `state` and
//     `code_challenge` and the redirect would otherwise hand them to the app's
//     origin.
//   - `Cache-Control: no-store`, because a consent page is about one request.
func writePage(w http.ResponseWriter, status int, tmpl *template.Template, data any, formActionSource string) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		slog.Error("render authorization page", "template", tmpl.Name(), "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	formAction := "'self'"
	if formActionSource != "" {
		formAction += " " + formActionSource
	}
	w.Header().Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'none'",
		"style-src 'unsafe-inline'",
		"img-src 'self' data:",
		"form-action " + formAction,
		"frame-ancestors 'none'",
		"base-uri 'none'",
	}, "; "))
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// credentialAddedLayout prints the date a credential was added. UTC and
// numeric: the Go mux serves the page outside the SPA, so it has no locale and
// no time zone of the reader to print in, and an ISO date cannot be read two
// ways.
const credentialAddedLayout = "2006-01-02"
