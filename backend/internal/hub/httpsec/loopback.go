package httpsec

// LoopbackHosts are the hosts a CLI login may redirect to.
//
// ONE list, because three places need the same answer and a comment saying
// they agree is not a mechanism:
//
//   - `isLoopbackURL` (hub/service) accepts exactly these as a `redirect_uri`.
//   - The CSP's `form-action` must admit the same set, or the browser blocks
//     the consent form's redirect hop and `leapmux control auth login` waits
//     until it times out. A browser matches `form-action` against EVERY hop of
//     a submission's redirect chain, so `'self'` alone is not enough.
//   - The CSP test asserted the set with a literal of its own.
//
// All three carried a comment claiming they matched, and no test connected
// them. A set that widens in one place and not the others is either a hole (the
// policy admits a host the redirect refuses) or an outage (the redirect offers
// a host the policy blocks), and neither shows up until a CLI login hangs.
//
// It lives here because `hub/frontend` and `hub/service` are siblings and this
// package is the leaf both already reach for.
var LoopbackHosts = []string{"127.0.0.1", "localhost", "::1"}
