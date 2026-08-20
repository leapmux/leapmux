package frontend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDevPolicyCoversEveryShippedDirective is the guard the drift needed.
//
// The dev policy is REPORT-ONLY, so its whole value is that a violation in the
// console means something. It used to be a hand-written second list, and it had
// already fallen four directives behind the shipped one -- `font-src`,
// `worker-src`, `frame-src` and the captcha connect sources. An omitted
// directive falls back to `default-src 'self'`, so each of those reported a
// violation for something production ALLOWS, and a developer who learns to
// ignore the console stops reading the real report too.
func TestDevPolicyCoversEveryShippedDirective(t *testing.T) {
	t.Parallel()

	dev := DevPolicy()
	require.True(t, dev.ReportOnly, "an enforced policy would stop Vite's hot reload")

	named := func(policy string) map[string]string {
		out := map[string]string{}
		for _, d := range strings.Split(policy, "; ") {
			name, sources, _ := strings.Cut(strings.TrimSpace(d), " ")
			out[name] = sources
		}
		return out
	}

	devByName := named(dev.CSP)
	for _, directive := range cspDirectives {
		name, sources, _ := strings.Cut(directive, " ")
		got, ok := devByName[name]
		require.Truef(t, ok, "the dev policy omits %q, so it reports what production allows", name)
		// The two directives the dev server must relax are the only ones
		// allowed to differ, and each must be a SUPERSET of the shipped one.
		if name == "connect-src" {
			for _, source := range strings.Fields(sources) {
				assert.Containsf(t, got, source,
					"connect-src may add ws:/wss:, but must keep %q", source)
			}
			continue
		}
		assert.Equalf(t, sources, got, "%q must match the shipped policy", name)
	}

	// The two deliberate relaxations.
	assert.Contains(t, devByName["script-src"], "'unsafe-eval'", "Vite evaluates source maps")
	assert.Contains(t, devByName["connect-src"], "ws:", "the HMR client opens a WebSocket")
}

// withDirective must not write into the caller's backing array: cspDirectives is
// package state the SHIPPED policy reads, and a mutation there would edit the
// enforced header from the dev one.
func TestWithDirectiveLeavesTheInputAlone(t *testing.T) {
	t.Parallel()

	before := strings.Join(cspDirectives, "; ")
	_ = withDirective(cspDirectives, "script-src", "'unsafe-eval'")
	_ = withDirective(cspDirectives, "connect-src", "ws:")
	assert.Equal(t, before, strings.Join(cspDirectives, "; "))

	// It replaces in place when the directive exists, and appends when it does not.
	replaced := withDirective([]string{"a 1", "b 2"}, "b", "9")
	assert.Equal(t, []string{"a 1", "b 9"}, replaced)
	appended := withDirective([]string{"a 1"}, "c", "3")
	assert.Equal(t, []string{"a 1", "c 3"}, appended)
}
