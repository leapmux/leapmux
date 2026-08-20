package frontend

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genfrontend "github.com/leapmux/leapmux/internal/hub/generated/frontend"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
)

// sha256Source returns the CSP source expression a browser computes for body.
// The test derives it the same way a browser does rather than pasting a
// constant, because a pasted constant only proves that the code did not
// change.
func sha256Source(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestInlineScriptHashes(t *testing.T) {
	t.Parallel()

	t.Run("hashes the body of an inline script", func(t *testing.T) {
		const body = `window.manifest = {"a":"b"}`
		got, err := inlineScriptHashes(fstest.MapFS{
			"index.html": {Data: []byte("<html><head><script>" + body + "</script></head><body></body></html>")},
		}, "index.html")
		require.NoError(t, err)
		assert.Equal(t, []string{sha256Source(body)}, got)
	})

	// A browser hashes the script's text EXACTLY, whitespace included. A
	// parser that trimmed, or a pattern that stopped at the wrong byte, would
	// produce a valid-looking hash that the browser then refuses -- a blank
	// page, and no failure here.
	t.Run("hashes the body byte for byte, whitespace included", func(t *testing.T) {
		const body = "\n  let x = 1;\n  \n"
		got, err := inlineScriptHashes(fstest.MapFS{
			"index.html": {Data: []byte("<html><body><script>" + body + "</script></body></html>")},
		}, "index.html")
		require.NoError(t, err)
		assert.Equal(t, []string{sha256Source(body)}, got)
	})

	// A <script> is a RAW TEXT element, so its body is not markup: a `<`, a
	// `</div>` and an HTML entity inside it all reach the browser verbatim and
	// must reach the hash verbatim too. This is the case a naive parse gets
	// wrong.
	t.Run("keeps markup-like text in the body verbatim", func(t *testing.T) {
		const body = `if (a < b && c > d) { s = "</div>&amp;" }`
		got, err := inlineScriptHashes(fstest.MapFS{
			"index.html": {Data: []byte("<html><body><script>" + body + "</script></body></html>")},
		}, "index.html")
		require.NoError(t, err)
		assert.Equal(t, []string{sha256Source(body)}, got)
	})

	// A script with `src` runs the FILE; its body is dead text the browser
	// never executes, so hashing it would add a source that authorizes
	// nothing.
	t.Run("skips a script that carries src", func(t *testing.T) {
		got, err := inlineScriptHashes(fstest.MapFS{
			"index.html": {Data: []byte(
				`<html><body><script type="module" src="/a.js"></script></body></html>`)},
		}, "index.html")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("skips an empty script", func(t *testing.T) {
		got, err := inlineScriptHashes(fstest.MapFS{
			"index.html": {Data: []byte("<html><body><script></script></body></html>")},
		}, "index.html")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("hashes every inline script and reports each one once", func(t *testing.T) {
		got, err := inlineScriptHashes(fstest.MapFS{
			"index.html": {Data: []byte(
				"<html><head><script>let a = 1</script></head>" +
					`<body><script src="/x.js"></script>` +
					"<script>let b = 2</script>" +
					"<script>let a = 1</script></body></html>")},
		}, "index.html")
		require.NoError(t, err)
		assert.Equal(t, []string{sha256Source("let a = 1"), sha256Source("let b = 2")}, sortedCopy(got))
		assert.Len(t, got, 2, "two scripts with identical bodies share one hash")
	})

	// The order must not depend on Go's map iteration, or two hub processes
	// serving the same assets would send different header bytes.
	t.Run("returns the hashes in a stable order", func(t *testing.T) {
		doc := []byte("<html><body><script>zzz</script><script>aaa</script><script>mmm</script></body></html>")
		first, err := inlineScriptHashes(fstest.MapFS{"index.html": {Data: doc}}, "index.html")
		require.NoError(t, err)
		for range 20 {
			again, err := inlineScriptHashes(fstest.MapFS{"index.html": {Data: doc}}, "index.html")
			require.NoError(t, err)
			assert.Equal(t, first, again)
		}
		assert.Equal(t, sortedCopy(first), first, "the order is sorted, not insertion order")
	})

	t.Run("reports a missing file rather than returning an empty list", func(t *testing.T) {
		_, err := inlineScriptHashes(fstest.MapFS{}, "index.html")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "index.html")
	})
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// ONE POLICY covers every HTML document the hub serves, so the sources it
// authorizes must be the union over all of them.
//
// Hashing index.html alone made the policy correct for one document by
// accident: the others carry no inline script today. The day one does, the
// browser refuses it and that page breaks at runtime with a console error --
// the exact failure a derived hash exists to prevent, and the one a checked-in
// hash was rejected for.
func TestAllInlineScriptHashes(t *testing.T) {
	t.Parallel()

	doc := func(body string) []byte {
		return []byte("<html><head><script>" + body + "</script></head><body></body></html>")
	}

	t.Run("unions every document, not just index.html", func(t *testing.T) {
		got, err := allInlineScriptHashes(fstest.MapFS{
			"index.html":            {Data: doc("a")},
			"NOTICE.html":           {Data: doc("b")},
			"legal/attribution.htm": {Data: doc("c")},
		})
		require.NoError(t, err)
		assert.Equal(t, slices.Sorted(slices.Values([]string{
			sha256Source("a"), sha256Source("b"), sha256Source("c"),
		})), got, "a document outside index.html must contribute its own source")
	})

	t.Run("reports one source for the same script in two documents", func(t *testing.T) {
		got, err := allInlineScriptHashes(fstest.MapFS{
			"index.html":  {Data: doc("same")},
			"NOTICE.html": {Data: doc("same")},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{sha256Source("same")}, got)
	})

	t.Run("ignores what the browser does not parse as HTML", func(t *testing.T) {
		got, err := allInlineScriptHashes(fstest.MapFS{
			"index.html":       {Data: doc("a")},
			"assets/app.js":    {Data: []byte("<script>b</script>")},
			"README.md":        {Data: []byte("<script>c</script>")},
			"manifest.webmani": {Data: []byte("<script>d</script>")},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{sha256Source("a")}, got)
	})

	// A broken embed must not serve a policy derived from nothing: it would
	// authorize no script at all, which is a blank page for every user.
	t.Run("fails when the tree holds no HTML document", func(t *testing.T) {
		_, err := allInlineScriptHashes(fstest.MapFS{"assets/app.js": {Data: []byte("x")}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no HTML document")
	})

	// The real embedded tree, so the walk is proved against the documents the
	// hub actually ships rather than a fixture alone.
	t.Run("covers every document in the embedded frontend", func(t *testing.T) {
		publicFS := mustPublicFS(t)
		got, err := allInlineScriptHashes(publicFS)
		require.NoError(t, err)

		var docs []string
		require.NoError(t, fs.WalkDir(publicFS, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && isHTMLDocument(name) {
				docs = append(docs, name)
			}
			return nil
		}))
		require.NotEmpty(t, docs, "the embedded frontend must ship at least one HTML document")

		for _, name := range docs {
			perDoc, err := inlineScriptHashes(publicFS, name)
			require.NoErrorf(t, err, "%s must parse", name)
			for _, h := range perDoc {
				assert.Containsf(t, got, h, "%s carries an inline script the policy does not authorize", name)
			}
		}
	})
}

// Policy reads the EMBEDDED index.html -- the same bytes the handler beside it
// serves. That is the whole design: a hash written down by hand goes stale on
// every frontend build, and it fails at runtime rather than at build time.
func TestPolicy(t *testing.T) {
	t.Parallel()

	p := Policy()
	require.NotEmpty(t, p.CSP,
		"the embedded frontend must yield a policy; an empty one means the derivation failed and the app ships undefended")
	assert.False(t, p.ReportOnly, "the shipped policy is enforced, not advisory")
	assert.Equal(t, "Content-Security-Policy", p.Header())

	t.Run("authorizes the app's own scripts and nothing inline", func(t *testing.T) {
		scriptSrc := directive(t, p.CSP, "script-src")
		assert.Contains(t, scriptSrc, "'self'", "the external module chunk loads by src")
		assert.Contains(t, scriptSrc, "'sha256-",
			"the build emits an inline asset manifest, so at least one hash must be present")
		assert.NotContains(t, scriptSrc, "'unsafe-inline'",
			"'unsafe-inline' would discard the entire value of script-src")
	})

	// The ALTCHA captcha's SCRYPT and ARGON2ID solvers compile a WebAssembly
	// module, and CSP governs that under script-src. Without this source the
	// browser refuses the module and sign-up cannot solve its challenge -- an
	// outage, and one that only the captcha flow reaches. 'wasm-unsafe-eval'
	// is the narrow source: it permits WebAssembly and still refuses eval() of
	// JavaScript, which is the part 'unsafe-eval' would have handed over too.
	t.Run("permits WebAssembly without permitting eval", func(t *testing.T) {
		scriptSrc := directive(t, p.CSP, "script-src")
		assert.Contains(t, scriptSrc, "'wasm-unsafe-eval'",
			"the captcha's WASM solvers do not run without this source")
		assert.NotContains(t, scriptSrc, " 'unsafe-eval'",
			"WebAssembly needs 'wasm-unsafe-eval' only; 'unsafe-eval' would also open up eval()")
	})

	// An operator can select Turnstile or reCAPTCHA at RUNTIME, so the policy
	// authorizes both vendors whichever one is configured -- a policy derived
	// from the setting at startup would block the widget an operator just
	// switched to until someone restarted the hub.
	//
	// The Google sources must stay PATH-RESTRICTED. `https://www.google.com`
	// alone serves endpoints an attacker can turn into a script, which would
	// make the whole script-src directive porous.
	t.Run("authorizes both captcha vendors, by narrow path", func(t *testing.T) {
		scriptSrc := directive(t, p.CSP, "script-src")
		frameSrc := directive(t, p.CSP, "frame-src")

		assert.Contains(t, scriptSrc, "https://challenges.cloudflare.com")
		assert.Contains(t, scriptSrc, "https://www.google.com/recaptcha/")
		assert.Contains(t, scriptSrc, "https://www.gstatic.com/recaptcha/")
		assert.Contains(t, frameSrc, "https://challenges.cloudflare.com")
		assert.Contains(t, frameSrc, "https://www.google.com/recaptcha/")

		// connect-src carries them too, and NO TEST REACHES THIS. The E2E
		// specs replace window.turnstile and window.grecaptcha with fakes, so
		// the vendors' real traffic never runs in CI -- reCAPTCHA v3 scores a
		// request by calling home, and omitting the target would leave a hole
		// that only a real deployment finds, as a login that hangs.
		connectSrc := directive(t, p.CSP, "connect-src")
		assert.Contains(t, connectSrc, "'self'", "the app's own WebSockets and fetches are same-origin")
		assert.Contains(t, connectSrc, "https://challenges.cloudflare.com")
		assert.Contains(t, connectSrc, "https://www.google.com/recaptcha/")

		for _, src := range strings.Fields(scriptSrc) {
			assert.NotEqualf(t, "https://www.google.com", src,
				"an unrestricted google.com script source makes script-src porous; keep the /recaptcha/ path")
			assert.NotEqualf(t, "https://www.gstatic.com", src,
				"an unrestricted gstatic.com script source is the same hazard")
		}
	})

	// Every hash must be the hash of a script that is really in the document.
	// A stale or invented source would look right in this header and would
	// leave the browser refusing the app's own manifest.
	t.Run("every hash matches an inline script in the served index.html", func(t *testing.T) {
		want, err := inlineScriptHashes(mustPublicFS(t), "index.html")
		require.NoError(t, err)
		require.NotEmpty(t, want, "this assertion only bites while the build emits an inline script")

		scriptSrc := directive(t, p.CSP, "script-src")
		for _, h := range want {
			assert.Containsf(t, scriptSrc, h, "the policy must carry the hash of each inline script (%s)", h)
		}
		assert.Equal(t, len(want), strings.Count(scriptSrc, "'sha256-"),
			"the policy must carry no hash beyond the scripts the document holds")
	})

	t.Run("carries the directives that need no asset knowledge", func(t *testing.T) {
		for _, want := range []string{
			"default-src 'self'",
			"img-src 'self' data: blob:",
			"font-src 'self'",
			"worker-src 'self' blob:",
			"object-src 'none'",
			"base-uri 'self'",
			"frame-ancestors 'none'",
			"form-action 'self'",
		} {
			assert.Containsf(t, p.CSP, want, "the policy must carry %q", want)
		}
	})

	// The one directive that CANNOT be tightened. @xterm/xterm's DomRenderer
	// assigns a built stylesheet to a <style> element's textContent, and that
	// content changes at runtime, so no hash and no build-time nonce covers
	// it. Removing 'unsafe-inline' breaks the terminal renderer, so the test
	// states the constraint rather than leaving the next reader to discover it
	// from a bug report.
	t.Run("keeps style-src unsafe-inline, which the terminal renderer requires", func(t *testing.T) {
		assert.Contains(t, directive(t, p.CSP, "style-src"), "'unsafe-inline'")
	})

	t.Run("is computed once and returns the same value every call", func(t *testing.T) {
		assert.Equal(t, Policy(), Policy())
	})
}

func TestDevPolicy(t *testing.T) {
	t.Parallel()

	p := DevPolicy()
	// Report-only, because Vite's HMR client injects inline scripts and
	// evaluates source maps. An ENFORCED policy stops hot reload, and a
	// developer who cannot reload deletes the header rather than fixes one
	// directive.
	assert.True(t, p.ReportOnly)
	assert.Equal(t, "Content-Security-Policy-Report-Only", p.Header())
	assert.Contains(t, p.CSP, "'unsafe-eval'", "the dev policy must not fight the dev server")
	assert.Contains(t, p.CSP, "frame-ancestors 'none'")
}

// An injected frontend brings assets this package cannot read, so it gets NO
// policy. A guessed one is an outage: the first inline script it did not
// account for is a blank page whose cause is the header we added.
func TestUnknownAssetsPolicy(t *testing.T) {
	t.Parallel()

	assert.Empty(t, UnknownAssetsPolicy().CSP)
}

// directive returns the named directive from a policy, and fails when it is
// absent -- so a renamed or dropped directive reports itself rather than
// passing a `NotContains` assertion for the wrong reason.
func directive(t *testing.T, policy, name string) string {
	t.Helper()
	for _, d := range strings.Split(policy, ";") {
		d = strings.TrimSpace(d)
		if after, ok := strings.CutPrefix(d, name+" "); ok {
			return after
		}
	}
	require.Failf(t, "missing directive", "policy %q holds no %s directive", policy, name)
	return ""
}

// mustPublicFS opens the same embedded sub-FS the handler serves, so a test
// reads the exact bytes production reads.
func mustPublicFS(t *testing.T) fs.FS {
	t.Helper()
	publicFS, err := fs.Sub(genfrontend.PublicFS, "public")
	require.NoError(t, err)
	return publicFS
}

// The CLI consent form is the one form in the app whose submission leaves this
// origin, and `form-action` is matched against every hop of a submission's
// redirect chain. With `'self'` alone, Chromium and WebKit refuse the 302 to the
// loopback callback and `leapmux control auth login` waits until it times out.
func TestFormActionAllowsTheCliLoopbackCallback(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		policy string
	}{
		{"enforced", strings.Join(cspDirectives, "; ")},
		{"dev report-only", devCSP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var formAction string
			for _, directive := range strings.Split(tc.policy, "; ") {
				if strings.HasPrefix(directive, "form-action ") {
					formAction = directive
				}
			}
			require.NotEmpty(t, formAction, "the policy must state form-action")

			assert.Contains(t, formAction, "'self'",
				"the app's own forms must keep posting to this origin")
			// DERIVED from the same list `isLoopbackURL` reads, rather than a
			// third literal that claims to match it. A literal here asserted
			// only that the policy had not changed, never that it agreed with
			// the redirect -- the two could widen apart and this test would
			// keep passing on whichever one it had memorized.
			//
			// An IPv6 literal is the one host the two cannot share: CSP's
			// `host-source` grammar has no production for one, so a browser
			// reports `http://[::1]:*` as an invalid source and ignores the
			// entry. It is skipped HERE for the same reason the policy skips
			// it, and the case below pins that it stays out.
			for _, host := range httpsec.LoopbackHosts {
				if strings.Contains(host, ":") {
					continue
				}
				source := "http://" + host + ":*"
				assert.Containsf(t, formAction, source,
					"the CLI binds an ephemeral loopback port, so %q must be allowed", source)
			}
			// An entry the browser IGNORES is worse than none: it buys no
			// permission and logs a console error on every page load, which is
			// how this was found (tests/e2e/181-security-headers.spec.ts).
			assert.NotContains(t, formAction, "[",
				"CSP cannot express an IPv6 host, so no bracketed source may be stated")
			// Not a blanket relaxation: nothing off the loopback may receive a form.
			assert.NotContains(t, formAction, "*.",
				"form-action must not admit a wildcard host")
		})
	}
}
