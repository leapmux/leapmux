package httpsec

import (
	"net/http"
	"strings"
)

// StartedByAnotherDocument reports whether some other page caused this
// request.
//
// The name states the RULE and not one of the header values it reads, because
// the rule spans two of them: "same-site" and "cross-site" both mean another
// document started this, and a caller that read a name quoting only
// "cross-site" would expect the sibling subdomain to be admitted.
//
// It reads Sec-Fetch-Site, the Fetch Metadata header a browser sets itself and
// a page cannot forge. The specification defines four values, and only two of
// them describe a request some other site caused:
//
//   - "same-origin" -- the app's own link or form. Allowed.
//   - "none" -- the user caused it directly: typed, bookmarked, or opened from
//     outside a page. Allowed, and it must be, or a bookmarked hub address
//     would stop working.
//   - "same-site" and "cross-site" -- another document started it. Refused.
//
// ABSENT means allowed, deliberately. The header is a defence in depth on top
// of SameSite cookies, not a replacement for one: a browser old enough to omit
// it omits it for the app's own links too, so refusing would break that user
// entirely while an attacker gains nothing -- a page cannot suppress the
// header on somebody else's browser. Fail-open here gives the modern majority a
// real block and costs the rest nothing.
//
// It answers about the INITIATOR and not about the request's mode, so it is
// true of a cross-site subresource load as well as of a navigation. That is
// the answer every caller here wants: a mux route that changes state should
// refuse both. A Connect RPC needs no such check at all -- the browser will
// not send one cross-site without a CORS preflight, and the hub grants none.
func StartedByAnotherDocument(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "same-site", "cross-site":
		return true
	default:
		return false
	}
}
