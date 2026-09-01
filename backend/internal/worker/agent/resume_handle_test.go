package agent

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// resumeHandleErr is ResolveResumeHandle's error half, for the cases that ask
// only whether a handle is acceptable. A case that cares about the value the
// caller must SEND calls ResolveResumeHandle directly and reads both returns --
// see TestResolveResumeHandleReturnsWhatReachesArgv.
func resumeHandleErr(p Provider, handle, homeDir string) error {
	_, err := p.ResolveResumeHandle(handle, homeDir)
	return err
}

// TestResolveResumeHandleIsTheProvidersOwnRule pins the split a shared rule
// could not carry: a resume handle is a TOKEN for every provider but Pi, whose
// handle is a session file PATH.
//
// One rule for both refused a legitimate Pi resume. The token class bans `\`,
// which every Windows path holds, and caps at 128 bytes, which a deep path
// passes -- so `leapmux` refused to resume a session Pi had written, with
// "session ID contains invalid characters" as the only explanation.
func TestResolveResumeHandleIsTheProvidersOwnRule(t *testing.T) {
	t.Parallel()

	tokenProviders := []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		// ZCode is here although it inherits nothing: it OVERRIDES the method and
		// delegates to the same validator, so the whole token contract is asserted
		// against the override rather than only the backslash the registry sweep below
		// covers.
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}

	t.Run("a token provider keeps the token rule", func(t *testing.T) {
		for _, p := range tokenProviders {
			plugin := ProviderFor(p)
			assert.NoErrorf(t, resumeHandleErr(plugin, "01JAV8Q3ZP9K2M4N6R8T0W2Y4B", ""), "%v", p)
			assert.NoErrorf(t, resumeHandleErr(plugin, "", ""), "%v: the empty handle means no resume", p)
			// The guard the token rule exists for: one argv element is enough
			// to reach a permission-skipping flag.
			assert.Errorf(t, resumeHandleErr(plugin, "--dangerously-skip-permissions", ""), "%v", p)
			assert.Errorf(t, resumeHandleErr(plugin, strings.Repeat("a", 129), ""), "%v", p)
		}
	})

	t.Run("pi takes a session file path the token rule refuses", func(t *testing.T) {
		pi := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)

		// The shape Pi actually issues, per OS. Both are far past the token
		// byte cap, and the Windows one holds the backslash the token class
		// bans -- so every one of these was refused before.
		var handle string
		if runtime.GOOS == "windows" {
			handle = `C:\Users\pi\AppData\Roaming\pi\sessions\` + strings.Repeat("a", 120) + ".jsonl"
		} else {
			handle = "/home/pi/.local/share/pi/sessions/" + strings.Repeat("a", 120) + ".jsonl"
		}
		require.Greater(t, len(handle), 128, "the case is only meaningful past the token cap")
		assert.NoError(t, resumeHandleErr(pi, handle, "/home/pi"))

		// Still not unchecked. A path rule answers the questions a PATH raises.
		assert.NoError(t, resumeHandleErr(pi, "", ""), "the empty handle means no resume")
		assert.Error(t, resumeHandleErr(pi, "relative/path.jsonl", ""), "a relative path is refused")
		assert.Error(t, resumeHandleErr(pi, "   ", ""), "whitespace is neither shape")
	})

	// The other half of Pi's rule, and the half that refused a legitimate
	// resume in the opposite direction: `pi --session` resolves a bare session
	// ID against this working directory's sessions, so the identifier Pi shows
	// in its own interface is a handle. The PATH rule refused it -- an ID is
	// relative by construction -- with "path must be absolute".
	t.Run("pi takes a session ID as well, under the token rule", func(t *testing.T) {
		pi := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)

		assert.NoError(t, resumeHandleErr(pi, "018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b", ""))
		assert.NoError(t, resumeHandleErr(pi, "01JAV8Q3ZP9K2M4N6R8T0W2Y4B", ""))
		// The ID half is the token rule itself, not a copy of it, so every
		// guard that rule states applies here too.
		assert.Error(t, resumeHandleErr(pi, "--dangerously-skip-permissions", ""))
		assert.Error(t, resumeHandleErr(pi, strings.Repeat("a", 129), ""))
		assert.Error(t, resumeHandleErr(pi, "bad\x00id", ""))
	})

	// The shape test decides which rule runs, so it decides which refusal a
	// user reads. It must answer the same way Pi's own resolver does: a
	// separator anywhere, or the `.jsonl` suffix.
	t.Run("pi picks the rule by shape", func(t *testing.T) {
		assert.True(t, piResumeHandleIsFilePath("/tmp/s.jsonl"))
		assert.True(t, piResumeHandleIsFilePath(`C:\pi\s.jsonl`))
		assert.True(t, piResumeHandleIsFilePath("s.jsonl"), "the suffix alone makes it a path")
		assert.True(t, piResumeHandleIsFilePath("~/s"), "the separator alone makes it a path")
		assert.False(t, piResumeHandleIsFilePath("018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b"))
		assert.False(t, piResumeHandleIsFilePath("~"), "a bare tilde holds no separator")
	})
}

// TestPiIsTheOnlyProviderOffTheTokenRule keeps the carve-out honest.
//
// Every other provider inherits noopProvider's token rule by saying nothing, so
// a provider added later is covered by default and only one whose handle is a
// different shape has to override. This fails the day a second provider does,
// which is the day somebody should look at whether the split still reads.
func TestPiIsTheOnlyProviderOffTheTokenRule(t *testing.T) {
	t.Parallel()

	// A handle the TOKEN rule refuses and a path rule accepts.
	const pathLike = "/tmp/pi/sessions/" + `a\b` + ".jsonl"

	providerMu.RLock()
	defer providerMu.RUnlock()
	for id, plugin := range providerRegistry {
		err := resumeHandleErr(plugin, pathLike, "")
		if id == leapmuxv1.AgentProvider_AGENT_PROVIDER_PI {
			continue
		}
		assert.Errorf(t, err, "%v must keep the token rule, which refuses a backslash", id)
	}
}

// TestResolveResumeHandleReturnsWhatReachesArgv pins the reason the method
// returns the handle rather than only judging it.
//
// SanitizePath NORMALIZES before it judges: it drops control characters, trims
// edge whitespace and cleans the path. The rule therefore approved one string
// while the caller sent another, and Pi's SessionManager.open does not require
// the file to exist -- so `--session` opened an EMPTY session at a filename
// nobody typed and the user's conversation was silently gone.
func TestResolveResumeHandleReturnsWhatReachesArgv(t *testing.T) {
	t.Parallel()
	pi := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)

	// Per-OS, because filepath.IsAbs wants a volume on Windows.
	root := "/tmp/pi/sessions"
	home := "/home/pi"
	if runtime.GOOS == "windows" {
		root = `C:\pi\sessions`
		home = `C:\Users\pi`
	}

	t.Run("a control character is removed from what is sent, not only from what is checked", func(t *testing.T) {
		typed := root + string(filepath.Separator) + "a\x07b.jsonl"
		resolved, err := pi.ResolveResumeHandle(typed, home)
		require.NoError(t, err, "SanitizePath strips the control character and then accepts")
		assert.NotContains(t, resolved, "\x07", "the sent handle must not carry it either")
		assert.NotEqual(t, typed, resolved, "the checked string and the typed string differ here")
	})

	t.Run("edge whitespace is removed from what is sent", func(t *testing.T) {
		typed := "  " + root + string(filepath.Separator) + "s.jsonl  "
		resolved, err := pi.ResolveResumeHandle(typed, home)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(typed), resolved)
	})

	t.Run("an invisible format character is REFUSED, because SanitizePath keeps it", func(t *testing.T) {
		// U+200B is Cf, not a control character, so it survives SanitizePath
		// untouched and would reach Pi inside the filename. The token shape
		// refuses the same class, and both shapes arrive in one field.
		_, err := pi.ResolveResumeHandle(root+string(filepath.Separator)+"a\u200Bb.jsonl", home)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invisible")
	})

	t.Run("a tilde path is sent expanded, and Pi accepts either spelling", func(t *testing.T) {
		// Pi's normalizePath defaults expandTilde to true, so `~/x` and the
		// expanded form name the same session. SanitizePath expands it to
		// judge, and the expanded form is what this returns -- either spelling
		// resumes, and the expanded one carries no dependence on Pi's own home.
		resolved, err := pi.ResolveResumeHandle("~/.pi/agent/sessions/p/s.jsonl", home)
		require.NoError(t, err)
		assert.NotContains(t, resolved, "~", "the value the worker sends is already expanded")
		assert.Contains(t, resolved, "s.jsonl")
	})

	t.Run("a token comes back untouched", func(t *testing.T) {
		const id = "018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b"
		resolved, err := pi.ResolveResumeHandle(id, home)
		require.NoError(t, err)
		assert.Equal(t, id, resolved, "the token rule refuses rather than normalizes")
	})

	t.Run("every token provider returns the handle unchanged", func(t *testing.T) {
		for _, p := range []leapmuxv1.AgentProvider{
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		} {
			const id = "01JAV8Q3ZP9K2M4N6R8T0W2Y4B"
			resolved, err := ProviderFor(p).ResolveResumeHandle(id, home)
			require.NoErrorf(t, err, "%v", p)
			assert.Equalf(t, id, resolved, "%v", p)
		}
	})

	t.Run("a refused handle returns the empty string, never the input", func(t *testing.T) {
		resolved, err := pi.ResolveResumeHandle("relative/path.jsonl", home)
		require.Error(t, err)
		assert.Empty(t, resolved, "a caller that ignores the error must not get a usable value")
	})
}
