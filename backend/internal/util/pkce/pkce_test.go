package pkce

import "testing"

func TestValidVerifierBounds(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"one char", "a", false},
		{"two chars", "ab", false},
		{"42 chars", repeat("a", 42), false},
		{"43 chars", repeat("a", 43), true},
		{"128 chars", repeat("a", 128), true},
		{"129 chars", repeat("a", 129), false},
		{"unreserved set", "abcXYZ012-._~" + repeat("a", 30), true},
		{"plus is rejected", repeat("a", 42) + "+", false},
		{"space is rejected", repeat("a", 42) + " ", false},
		{"percent is rejected", repeat("a", 42) + "%", false},
	}
	for _, tc := range cases {
		if got := ValidVerifier(tc.in); got != tc.want {
			t.Errorf("%s: ValidVerifier = %v, want %v", tc.name, got, tc.want)
		}
		if got := ValidChallenge(tc.in); got != tc.want {
			t.Errorf("%s: ValidChallenge = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestS256Stable(t *testing.T) {
	// The transform itself is unchanged; this pins the value so a refactor of
	// the bounds above cannot silently alter it.
	if got := S256("test"); got != "n4bQgYhMfWWaL-qgxVrQFaO_TxsrC4Is0V1sFbDwCgg" {
		t.Errorf("S256(test) = %q", got)
	}
}
