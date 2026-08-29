package httpsec

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The four defined values plus the two shapes a real deployment produces: a
// browser that sends nothing, and a header with stray case or space.
func TestStartedByAnotherDocument(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		header string
		set    bool
		want   bool
	}{
		{name: "the app's own link", header: "same-origin", set: true, want: false},
		{name: "typed or bookmarked", header: "none", set: true, want: false},
		{name: "another site", header: "cross-site", set: true, want: true},
		{name: "a sibling subdomain", header: "same-site", set: true, want: true},
		{name: "a browser that sends nothing fails open", set: false, want: false},
		{name: "an empty value fails open", header: "", set: true, want: false},
		{name: "case and space do not matter", header: "  Cross-Site ", set: true, want: true},
		{name: "an unknown value fails open", header: "totally-new", set: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/auth/idp/gh/reauth", nil)
			if tc.set {
				r.Header.Set("Sec-Fetch-Site", tc.header)
			}
			assert.Equal(t, tc.want, StartedByAnotherDocument(r))
		})
	}
}
