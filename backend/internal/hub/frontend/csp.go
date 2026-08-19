package frontend

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"golang.org/x/net/html"

	genfrontend "github.com/leapmux/leapmux/internal/hub/generated/frontend"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
)

// The captcha vendors LeapMux can be configured to use. An operator selects
// one with `admin captcha set --provider`, and the field then loads that
// vendor's script and renders its iframe -- so the policy has to authorize
// both origins or the sign-up page cannot show its challenge.
//
// THE SOURCES ARE UNCONDITIONAL, and that is a deliberate trade. The captcha
// provider is a HOT setting: an operator switches it at runtime, and a policy
// computed from the setting at startup would leave the new widget blocked
// until someone restarts the hub -- an outage caused by this header, which is
// the one failure mode worth ruling out by construction. Listing both origins
// costs a narrow, PATH-RESTRICTED script source on a hub that uses neither,
// which is what each vendor's own CSP guidance recommends. `https://www.google.com`
// unrestricted would be a real weakening (that origin serves endpoints an
// attacker can turn into a script); `https://www.google.com/recaptcha/` is not.
var (
	captchaScriptSources = []string{
		"https://challenges.cloudflare.com",
		"https://www.google.com/recaptcha/",
		"https://www.gstatic.com/recaptcha/",
	}
	captchaFrameSources = []string{
		"https://challenges.cloudflare.com",
		"https://www.google.com/recaptcha/",
	}
	// reCAPTCHA v3 scores a request by calling home, and Turnstile's platform
	// does the same on some configurations.
	//
	// These are listed although NO TEST REACHES THEM. The E2E specs replace
	// `window.turnstile` and `window.grecaptcha` with fakes, so they prove
	// that the vendor SCRIPT may load and stop there -- the vendors' real
	// runtime traffic never runs in CI, and it cannot without shipping a live
	// account's keys into the test suite. Omitting these would leave a hole
	// that only a real deployment discovers, as a login that hangs. The cost
	// of listing them is one path-restricted connect target per vendor.
	captchaConnectSources = []string{
		"https://challenges.cloudflare.com",
		"https://www.google.com/recaptcha/",
	}
)

// cspDirectives holds every directive except script-src, which is built from
// the assets themselves.
//
// Each one is here because the app does not need what it forbids:
//
//   - connect-src 'self' plus the two captcha vendors: both WebSockets are
//     same-origin (`${wsProtocol}//${window.location.host}/ws/channel`) and
//     every fetch of the app's own goes to the hub that served the page, but
//     reCAPTCHA v3 scores a request by calling home. See captchaConnectSources.
//   - style-src keeps 'unsafe-inline', and it CANNOT be tightened today.
//     @xterm/xterm's DomRenderer builds a stylesheet as text and assigns it to
//     a <style> element's textContent, which CSP governs and whose content
//     changes at runtime -- so neither a hash nor a build-time nonce applies.
//     Tightening this directive breaks the terminal renderer. It also means
//     CSP is NOT a second line of defence for a CSS-injection escape: the
//     escaping in frontend/src/lib/fontStack.ts is the only one. Removing
//     'unsafe-inline' needs a patch to xterm that sets styleElement.nonce.
//   - img-src allows data: and blob: because the app renders pasted and
//     generated images from memory. The markdown pipeline already refuses a
//     remote image, so no host is listed.
//   - font-src 'self': Hack NF ships with the app.
//   - worker-src 'self' blob:. The app's own workers (markdown, Shiki, the two
//     lazily loaded ALTCHA solvers) are same-origin asset URLs, but the ALTCHA
//     WIDGET builds its default solver worker from a Blob and runs it through
//     createObjectURL -- see createObjectURL/new Worker in
//     node_modules/altcha/dist/main/altcha.js, bundled into the captchaForm
//     chunk. Without blob: the captcha never solves, so LOGIN AND SIGN-UP HANG
//     on every hub, which is the default configuration. blob: costs little
//     here: a blob worker runs code the page itself assembled, so an attacker
//     needs script execution first, and script-src already governs that.
//     Stated rather than left to the default-src fallback, so a later change
//     to default-src cannot silently stop the workers.
//   - object-src 'none': the app embeds no plugin, so it is dead surface.
//   - frame-src lists the two captcha vendors and nothing else. Turnstile and
//     reCAPTCHA each render their challenge in an iframe of their own origin;
//     the app frames nothing else.
//   - base-uri 'self': a <base> tag injected into the document would otherwise
//     re-point every relative asset URL at another origin.
//   - frame-ancestors 'none': nothing may frame LeapMux. This replaces
//     X-Frame-Options, which therefore needs no header of its own.
//   - form-action 'self': a form the app never wrote cannot post elsewhere.
var cspDirectives = []string{
	"default-src 'self'",
	"connect-src " + strings.Join(append([]string{"'self'"}, captchaConnectSources...), " "),
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data: blob:",
	"font-src 'self'",
	"worker-src 'self' blob:",
	"object-src 'none'",
	"frame-src " + strings.Join(captchaFrameSources, " "),
	"base-uri 'self'",
	"frame-ancestors 'none'",
	"form-action 'self'",
}

// devCSP is the policy for the Vite dev server, and it is REPORT-ONLY.
//
// Vite's HMR client injects inline scripts and evaluates source maps, so an
// enforced policy stops hot reload. Report-only still surfaces a violation in
// the console, which is where a developer wants to learn that a new dependency
// reaches for something the shipped policy refuses -- before it ships, not
// after.
var devCSP = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data: blob:",
	"connect-src 'self' ws: wss:",
	"object-src 'none'",
	"base-uri 'self'",
	"frame-ancestors 'none'",
	"form-action 'self'",
}, "; ")

// DevPolicy returns the report-only policy for the dev proxy.
func DevPolicy() httpsec.Policy {
	return httpsec.Policy{CSP: devCSP, ReportOnly: true}
}

// UnknownAssetsPolicy returns the policy for a frontend whose assets this
// package cannot read -- one an embedder injects with hub.WithFrontendHandler.
//
// It sends NO Content-Security-Policy. A policy guessed for assets we have not
// seen is worse than none: the first inline script it did not account for
// leaves the caller with a blank page and a console error, and the header we
// added is the cause. Whoever mounts their own assets owns their policy.
func UnknownAssetsPolicy() httpsec.Policy {
	return httpsec.Policy{}
}

// Policy returns the enforced Content-Security-Policy for the EMBEDDED
// frontend, with one 'sha256-...' source for each inline script in index.html.
//
// THE HASH IS DERIVED, NEVER WRITTEN DOWN. The one inline script the build
// emits is an 18 KB asset manifest carrying the hashed chunk file names, so
// its bytes change on every frontend build that touches any source file. A
// hash checked into Go source would be stale before it was reviewed, and a
// stale hash fails at RUNTIME as a blank page with a console error -- never at
// build time. Reading the bytes that this same package is about to serve is
// what makes the two impossible to disagree.
//
// The work runs once for the life of the process. The input is an embedded
// file, so there is nothing to invalidate.
func Policy() httpsec.Policy { return embeddedPolicy() }

var embeddedPolicy = sync.OnceValue(func() httpsec.Policy {
	publicFS, err := fs.Sub(genfrontend.PublicFS, "public")
	if err != nil {
		return failedPolicy(fmt.Errorf("open the embedded frontend: %w", err))
	}
	hashes, err := inlineScriptHashes(publicFS, "index.html")
	if err != nil {
		return failedPolicy(err)
	}
	// 'self' covers the external module the document loads by src, and the
	// hashes cover the inline manifest.
	//
	// 'wasm-unsafe-eval' is what lets the ALTCHA captcha run. Its memory-hard
	// solvers (altcha/workers/scrypt.js and argon2id.js, loaded lazily by
	// frontend/src/lib/altchaSolvers.ts) compile a WebAssembly module, and CSP
	// governs that compilation under script-src: without a source for it the
	// browser refuses the module and sign-up cannot solve its challenge.
	// 'wasm-unsafe-eval' is the NARROW source for exactly this -- it permits
	// WebAssembly compilation and still refuses eval() of JavaScript, which is
	// what 'unsafe-eval' would have opened up as well.
	//
	// The captcha vendors' own bundles load from their origins; see
	// captchaScriptSources for why they are listed unconditionally.
	//
	// Nothing else may run.
	scriptSrc := append([]string{"script-src", "'self'", "'wasm-unsafe-eval'"}, hashes...)
	scriptSrc = append(scriptSrc, captchaScriptSources...)
	directives := append([]string{strings.Join(scriptSrc, " ")}, cspDirectives...)
	return httpsec.Policy{CSP: strings.Join(directives, "; ")}
})

// failedPolicy is what a hub serves when the hashes cannot be derived.
//
// It sends NO policy, and it says so loudly. The alternative -- shipping the
// directive list without the script hashes -- serves an app whose own script
// the browser then refuses, which is a blank page for every user. A missing
// header is a missing defence; a wrong header is an outage.
func failedPolicy(err error) httpsec.Policy {
	slog.Error("content security policy disabled: cannot derive the inline script hashes",
		"error", err)
	return httpsec.Policy{}
}

// inlineScriptHashes returns a sorted CSP source expression for each inline
// script in the named HTML file.
//
// It parses with x/net/html rather than matching a pattern, because a <script>
// is a RAW TEXT element: the parser hands back its body byte for byte, and CSP
// hashes exactly those bytes. A pattern that guesses where the body ends
// produces a hash that looks valid and is wrong, and the failure lands on the
// user rather than on this function.
//
// A script that carries a `src` attribute is skipped. Its body is dead text
// that the browser never runs, so a hash of it would authorize nothing.
func inlineScriptHashes(fsys fs.FS, name string) ([]string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()

	doc, err := html.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	seen := make(map[string]struct{})
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" && !hasAttr(n, "src") {
			if body := rawText(n); body != "" {
				sum := sha256.Sum256([]byte(body))
				seen["'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'"] = struct{}{}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	// Sorted, so the header is byte-identical across processes and a test can
	// compare it. Go randomizes map iteration order deliberately.
	hashes := make([]string, 0, len(seen))
	for h := range seen {
		hashes = append(hashes, h)
	}
	slices.Sort(hashes)
	return hashes, nil
}

func hasAttr(n *html.Node, name string) bool {
	return slices.ContainsFunc(n.Attr, func(a html.Attribute) bool { return a.Key == name })
}

// rawText concatenates the text children of a raw-text element. A <script>
// holds exactly one text node in practice; the loop makes that a property of
// the output rather than an assumption about the parser.
func rawText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}
