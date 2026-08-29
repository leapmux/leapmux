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
//
// WHAT the style says is the app's own look, restated: the token values below
// are a copy of LeapMux's DEFAULT palette pair (frontend
// src/styles/themes/default.ts), and the rules are Oat's component shapes in
// the subset of CSS a script-free page needs. The palette cannot be LINKED
// here -- `default-src 'none'` is the defence that makes these pages safe to
// render for a stranger arriving from a terminal -- so it is stated in the
// document, and a reader's chosen variant cannot follow them: that choice
// lives in the SPA's per-account storage, which these pages have no script to
// read. The page follows the DEFAULT pair and the system's light/dark
// preference, which is as close as a server-rendered page can come. When the
// default palette changes, change the copy with it.

// pageCSS is every page's stylesheet: the default palette as custom
// properties (light, then dark under the system preference) and the shared
// component rules. One copy, inside the chrome, so no page can drift.
const pageCSS = `
:root {
  color-scheme: light dark;
  --background: rgb(255 254 252);
  --foreground: rgb(34 32 30);
  --card: rgb(247 245 242);
  --border: rgb(221 217 211);
  --input: rgb(213 209 203);
  --muted: rgb(237 235 231);
  --muted-foreground: rgb(120 117 111);
  --primary: rgb(13 148 136);
  --primary-foreground: rgb(255 255 255);
  --accent: rgb(222 235 225);
  --danger: rgb(220 74 68);
  --danger-subtle: rgb(253 235 233);
}
@media (prefers-color-scheme: dark) {
  :root {
    --background: rgb(26 25 23);
    --foreground: rgb(232 230 225);
    --card: rgb(42 40 38);
    --border: rgb(61 58 54);
    --input: rgb(61 58 54);
    --muted: rgb(46 43 40);
    --muted-foreground: rgb(138 134 128);
    --primary: rgb(20 184 166);
    --primary-foreground: rgb(12 12 11);
    --accent: rgb(45 62 50);
    --danger: rgb(239 83 80);
    --danger-subtle: rgb(50 30 28);
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  padding: 48px 16px;
  background: var(--background);
  color: var(--foreground);
  font: 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif;
}
main { max-width: 520px; margin: 0 auto; }
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 24px;
}
h1 { font-size: 20px; font-weight: 600; margin: 0 0 16px; }
p { margin: 0 0 12px; }
ul { margin: 0 0 12px; padding-left: 24px; }
.alert {
  border: 1px solid var(--danger);
  background: var(--danger-subtle);
  color: var(--danger);
  padding: 8px 12px;
  border-radius: 6px;
  margin: 0 0 12px;
}
.identity { display: flex; align-items: center; gap: 12px; margin: 0 0 12px; }
.identity img, .monogram { width: 48px; height: 48px; border-radius: 8px; flex: none; }
.monogram {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: var(--muted);
  color: var(--muted-foreground);
  font-size: 24px;
}
.identity strong { font-size: 18px; }
.actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 24px; }
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 8px 16px;
  font: 500 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif;
  background: var(--primary);
  color: var(--primary-foreground);
  border: 1px solid transparent;
  border-radius: 6px;
  cursor: pointer;
}
.btn:hover { background: color-mix(in srgb, var(--primary), white 25%); }
.btn-outline { background: transparent; color: var(--foreground); border-color: var(--border); }
.btn-outline:hover { background: var(--accent); }
.code-input {
  width: 100%;
  font: 24px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  letter-spacing: 2px;
  text-align: center;
  padding: 8px;
  background: var(--background);
  color: var(--foreground);
  border: 1px solid var(--input);
  border-radius: 6px;
}
main.wide { max-width: 680px; }
.scopes { list-style: none; margin: 0 0 12px; padding: 0; display: grid; gap: 10px; }
.scope-category { display: block; font-weight: 600; margin-bottom: 4px; }
.scopes ul { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
/*
 * The ENTRY row alone, never the family <li> around it. A ".scopes li"
 * selector styled both levels as one flex row, and a narrow family (File,
 * one short row) then laid that row BESIDE its label while wide families
 * wrapped theirs under -- the label must always stand on its own line.
 *
 * NOWRAP is what keeps a description beside its token: with flex-wrap, a
 * browser places items by their FULL content width before shrinking ever
 * applies, so a long sentence moved to a line of its own however small its
 * min-width. Unwrapped, the sentence is the one item that may shrink, and
 * its text wraps INSIDE its own box, always starting beside the token.
 */
.scopes ul > li { display: flex; align-items: flex-start; gap: 8px; }
/*
 * The tick mark, restating Oat's own checkbox rule piece for piece -- 1rem
 * square, radius-small corners, primary fill, and the check drawn through
 * the same SVG mask in the primary foreground -- so the mark a consent page
 * draws is pixel-identical to the checkbox the Preferences dialog ticks.
 * A native checkbox could not be used: the disabled state that makes it a
 * mark instead of a control is the one state every browser paints in its
 * own grey, accent-color or not, and a dimmed tick contradicted the legend.
 */
.tick {
  flex: none;
  width: 1rem;
  height: 1rem;
  margin-top: 2px;
  position: relative;
  background-color: var(--background);
  border: 1px solid var(--input);
  border-radius: 0.125rem;
}
.granted > .tick { background-color: var(--primary); border-color: var(--primary); }
.granted > .tick::after {
  content: "";
  position: absolute;
  inset: 0;
  background-color: var(--primary-foreground);
  mask-position: center;
  mask-repeat: no-repeat;
  mask-size: 100%;
  mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='4'%3E%3Cpolyline points='20 6 9 17 4 12'/%3E%3C/svg%3E");
}
.scope-token { flex: none; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13px; }
.scope-sentence { flex: 1 1 0; min-width: 0; color: var(--muted-foreground); font-size: 13px; }
.not-granted { opacity: 0.55; }
/* The screen-reader spelling of the tick, for the mark itself is decorative. */
.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}
.scope-note { color: var(--muted-foreground); font-size: 12px; }
`

// pageChrome wraps each page's body, so the shell exists in one place.
const pageChrome = `<!doctype html><html><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>LeapMux</title>
<style>` + pageCSS + `</style>
</head><body>
<main class="card {{block "pageClass" .}}{{end}}">
{{block "body" .}}{{end}}
</main>
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
var consentPageTmpl = mustParsePage("consent", `{{define "pageClass"}} wide{{end}}{{define "body"}}
{{if .App.Verified}}<h1>Authorize {{.App.Name}}?</h1>
{{else}}<h1>Authorize an unverified app?</h1>
<p class="alert">Nobody verified this app on this hub. It says its name is &ldquo;{{.App.Name}}&rdquo;. Continue only if you started this yourself.</p>
{{end}}
{{template "appIdentity" .App}}
<p>It asks to use your account (<strong>{{.Username}}</strong>) and will return to {{.RedirectLabel}}.</p>
{{if .Permissions}}{{template "permissions" .Permissions}}
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
  <div class="actions">
    <button type="submit" name="decision" value="deny" class="btn btn-outline">Deny</button>
    <button type="submit" name="decision" value="allow" class="btn">Allow</button>
  </div>
</form>
{{end}}`)

// appIdentityTmpl renders the app's icon and name.
//
// A VERIFIED app shows its stored icon, served from this origin so the page's
// img-src stays 'self'. An UNVERIFIED one shows a MONOGRAM -- the first letter
// of its name in a neutral square -- which fetches nothing at all, so an
// unverified registrant can neither borrow a well-known icon nor learn when
// the consent page rendered and from which address.
const appIdentityTmpl = `{{define "appIdentity"}}
<p class="identity">{{if .HasIcon}}<img src="/oauth/apps/{{.ClientID}}/icon" alt="" width="48" height="48"/>{{else}}<span aria-hidden="true" class="monogram">{{.Monogram}}</span>{{end}}
<strong>{{.Name}}</strong></p>
{{end}}`

// permissionsTmpl renders a grant as the WHOLE grantable vocabulary grouped
// by family, the same shape the Preferences dialog's "Permissions this app
// may ask for" list uses: the asked-for permissions ticked, the rest dimmed.
//
// The tick is a CSS-DRAWN mark, not a disabled native checkbox: browsers
// paint disabled controls in their own grey whatever accent-color says, and
// a tick that reads dimmed contradicts the legend beneath it. The mark is
// decorative (aria-hidden); the granted/not-granted fact reaches a screen
// reader as visually-hidden words beside the sentence.
const permissionsTmpl = `{{define "permissions"}}
<p>It would be able to:</p>
<ul class="scopes">{{range .}}
<li><span class="scope-category">{{.Label}}</span><ul>{{range .Entries}}
<li class="{{if .Granted}}granted{{else}}not-granted{{end}}"><span class="tick" aria-hidden="true"></span><span class="scope-token">{{.Token}}</span><span class="scope-sentence">{{.Sentence}}<span class="visually-hidden"> ({{if .Granted}}granted{{else}}not granted{{end}})</span></span></li>{{end}}
</ul></li>{{end}}
</ul>
<p class="scope-note">The app asks only for the ticked permissions. The dimmed permissions are not granted.</p>
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
var devicePageTmpl = mustParsePage("device", `{{define "pageClass"}} wide{{end}}{{define "body"}}
<h1>{{if .Elevating}}Verify an app credential{{else}}Authorize an app{{end}}</h1>
{{if .Elevating}}<p>This grants an existing app credential the right to make sensitive changes for the next two hours. It issues nothing new.</p>
{{with .Credential}}<p>The credential is <strong>{{.Name}}</strong>, added {{.Added}} (UTC).{{if .Revoked}} It was revoked, so verifying it grants nothing.{{end}}</p>
{{end}}{{else if .App}}{{if not .App.Verified}}<p class="alert">Nobody verified this app on this hub. It says its name is &ldquo;{{.App.Name}}&rdquo;.</p>
{{end}}{{template "appIdentity" .App}}
{{if .ConfirmAdmin}}<p class="alert">This code asks for <strong>hub administration</strong>. Authorizing it grants the app administrator authority over this hub. Continue only if you started this request yourself and meant to grant it.</p>
{{end}}
{{if .Permissions}}{{template "permissions" .Permissions}}
{{else}}<p>It asks for no permissions at all, so it would be able to do nothing with your account.</p>
{{end}}{{end}}<p>Enter the code that the app shows:</p>
<form method="POST" action="/oauth/device">
<input name="user_code" value="{{.UserCode}}" pattern="[A-Za-z0-9-]{6,8}" autofocus required class="code-input"/>
{{if .ConfirmAdmin}}<input type="hidden" name="admin_confirmed" value="1"/>
{{end}}
<div class="actions">
<button type="submit" name="decision" value="deny" class="btn btn-outline">Deny</button>
<button type="submit" name="decision" value="allow" class="btn">Authorize</button>
</div>
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
	return template.Must(template.New(name).Parse(pageChrome + appIdentityTmpl + permissionsTmpl + body))
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
	Permissions      []scopeCategoryView
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
	Permissions []scopeCategoryView
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

// scopeCategories groups the grantable vocabulary the way scope.proto's own
// sections do -- the same families the Preferences dialog's "Permissions this
// app may ask for" list renders. A consent screen that grouped by anything
// else would answer a question nobody asked.
//
// ORDER is render order, and the membership is pinned by test: every
// grantable scope appears exactly once, so a scope added to the proto fails
// the suite until somebody writes its family here.
var scopeCategories = []struct {
	label  string
	scopes []leapmuxv1.Scope
}{
	{"Account", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_ACCOUNT_READ,
		leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE,
	}},
	{"Workspace", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE,
	}},
	{"Worker", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_WORKER_READ,
		leapmuxv1.Scope_SCOPE_WORKER_ADMIN,
	}},
	{"Agent", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_AGENT_READ,
		leapmuxv1.Scope_SCOPE_AGENT_WRITE,
	}},
	{"Terminal", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_TERMINAL_READ,
		leapmuxv1.Scope_SCOPE_TERMINAL_WRITE,
	}},
	{"File", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_FILE_READ,
	}},
	{"Git", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_GIT_READ,
		leapmuxv1.Scope_SCOPE_GIT_WRITE,
	}},
	{"Tunnel", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_TUNNEL_OPEN,
	}},
	{"Hub administration", []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_ADMIN_READ,
		leapmuxv1.Scope_SCOPE_ADMIN_USERS,
		leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS,
		leapmuxv1.Scope_SCOPE_ADMIN_WORKERS,
		leapmuxv1.Scope_SCOPE_ADMIN_APPS,
	}},
}

// scopeEntryView is one permission on a decision page: the wire token a
// developer knows, the sentence a person acts on, and whether THIS grant
// carries it.
type scopeEntryView struct {
	Token    string
	Sentence string
	Granted  bool
}

// scopeCategoryView is one family of permissions, as the page renders it.
type scopeCategoryView struct {
	Label   string
	Entries []scopeEntryView
}

// describeScopeCatalogue renders a grant as the whole grantable vocabulary,
// grouped by family, with the asked-for permissions ticked and the rest
// dimmed.
//
// The WHOLE catalogue, not only the ask, because "which permission is NOT
// granted" is half of what a reader deciding needs: a list of granted
// sentences answers "what does it do" while leaving "does it also administer
// my hub" to the imagination. The dimmed rows close that question without a
// sentence of prose per negative.
//
// Categories with nothing granted are SKIPPED: a consent screen that listed
// "Tunnel -- none" for an app that asked to read files would spend the
// reader's attention on families that carry no decision. The empty GRANT is
// still the caller's to state in prose (the page says "no permissions at
// all" rather than rendering nineteen dimmed rows).
func describeScopeCatalogue(set authscope.ScopeSet) []scopeCategoryView {
	granted := map[leapmuxv1.Scope]bool{}
	// No app ever holds an unscoped grant, but a page that rendered one as
	// "no permissions at all" would be a silent lie about what was asked for:
	// unscoped is the absence of a limit, not the absence of permissions.
	if set.IsUnscoped() {
		for _, scope := range authscope.Grantable() {
			granted[scope] = true
		}
	}
	for _, scope := range set.Scopes() {
		granted[scope] = true
	}
	out := make([]scopeCategoryView, 0, len(scopeCategories))
	for _, category := range scopeCategories {
		entries := make([]scopeEntryView, 0, len(category.scopes))
		familyGranted := false
		for _, scope := range category.scopes {
			sentence := scopeSentences[scope]
			isGranted := granted[scope]
			familyGranted = familyGranted || isGranted
			// The catalogue's membership test pins every entry to a grantable
			// scope, so Token always answers here; the enum-name fallback is
			// what a future drift renders while that test fails the build.
			token, ok := authscope.Token(scope)
			if !ok {
				token = scope.String()
			}
			entries = append(entries, scopeEntryView{
				Token:    token,
				Sentence: sentence,
				Granted:  isGranted,
			})
		}
		if !familyGranted {
			continue
		}
		out = append(out, scopeCategoryView{Label: category.label, Entries: entries})
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
