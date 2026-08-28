package service

import "testing"

// A percent-encoded path spelling must not match a plain registration. The
// match rule refuses normalization: url.Parse decodes %62 into Path, so
// comparing Path would equate "/call%62ack" with "/callback" and hand the
// code to a spelling the registrant never wrote.
func TestLoopbackMatchRefusesEncodedPathSpellings(t *testing.T) {
	registered := "http://127.0.0.1/callback"

	for _, presented := range []string{
		"http://127.0.0.1:5555/callback", // the RFC 8252 port exception
	} {
		if _, ok := MatchRedirectURI([]string{registered}, presented); !ok {
			t.Errorf("presented %q should match %q", presented, registered)
		}
	}
	for _, presented := range []string{
		"http://127.0.0.1:5555/call%62ack", // decodes to /callback
		"http://127.0.0.1:5555/callbac%6B", // decodes to /callback
		"http://127.0.0.1:5555/other",
	} {
		if _, ok := MatchRedirectURI([]string{registered}, presented); ok {
			t.Errorf("presented %q must not match %q", presented, registered)
		}
	}
}
