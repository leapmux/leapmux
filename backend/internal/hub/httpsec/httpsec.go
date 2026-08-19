// Package httpsec carries the response headers that tell a browser what a
// LeapMux document may do. It holds no policy of its own: the caller supplies
// the Content-Security-Policy, because only the caller knows which assets are
// mounted, and the script hash is derived from those exact bytes.
package httpsec

import "net/http"

// Policy is the Content-Security-Policy for one mounted frontend.
//
// ReportOnly picks the header name. A report-only policy is advisory: the
// browser reports a violation and runs the page anyway. That is the correct
// mode for the Vite dev server, whose HMR client injects inline scripts and
// evaluates source maps -- an enforced policy there breaks hot reload, and a
// developer who cannot reload turns the whole header off rather than fix one
// directive.
type Policy struct {
	// CSP is the header value. An empty value sends no CSP header at all,
	// which is what a caller that cannot know its assets must ask for --
	// sending a policy that the document then violates is worse than sending
	// none, because it breaks the page.
	CSP string
	// ReportOnly sends Content-Security-Policy-Report-Only instead of
	// Content-Security-Policy.
	ReportOnly bool
}

// Header returns the header name for the policy's mode.
func (p Policy) Header() string {
	if p.ReportOnly {
		return "Content-Security-Policy-Report-Only"
	}
	return "Content-Security-Policy"
}

// Middleware sets the security headers on every response and then calls next.
//
// It wraps the WHOLE mux rather than the frontend handler alone. Two of the
// three headers protect every response and not only the app document: nosniff
// governs a JSON body a browser was tricked into loading as a script, and
// Referrer-Policy governs each request the browser makes next. The hub also
// renders HTML outside the frontend handler (the device-code and PKCE
// callback pages), and those pages deserve the same treatment as the app.
//
// The headers are set BEFORE next runs, so a handler that writes its status
// immediately still carries them. A handler that sets its own value for one of
// these keeps it, because Add-vs-Set matters here: Set replaces, and a
// deliberate per-route override must win over this default.
func Middleware(p Policy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if p.CSP != "" {
			h.Set(p.Header(), p.CSP)
		}
		// Refuse to run a response through the browser's content sniffer. A
		// JSON body served to a <script> tag is inert with this header and is
		// executable without it.
		h.Set("X-Content-Type-Options", "nosniff")
		// A LeapMux URL can carry a workspace or an agent id, and no external
		// site needs it. This value still sends the full referrer on a
		// same-origin request, which is where the app makes its own.
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
