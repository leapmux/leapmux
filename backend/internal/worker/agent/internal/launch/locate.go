package launch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// Spec says how to invoke a provider's agent program.
//
// It exists for a provider that a bare PATH name cannot describe: ZCode ships no
// executable of its own, only a `zcode.cjs` script inside its desktop bundle, which
// a Node interpreter has to be handed.
type Spec struct {
	// Program is the shell word that starts the process: a bare name the shell
	// resolves through PATH, or an absolute path. Wrap quotes it.
	Program string
	// PrefixArgs precede the provider's own BaseArgs -- the script path an
	// interpreter must receive before the script's own arguments.
	PrefixArgs []string
	// Env are extra KEY=VALUE entries the launched program needs, pinned onto the
	// spawned environment (ELECTRON_RUN_AS_NODE=1 for ZCode's Electron-as-Node
	// fallback). Empty for a plain interpreter.
	Env []string
}

// Resolution is the three-state answer a launch resolver gives. It is the
// launch-side counterpart of ProbeResult, and it exists for the same reason: the
// availability scan (agent.Registry.ListAvailable) must distinguish "the probe ran and the provider is not
// installed" from "no probe could run", or a broken shell reports an authoritative
// empty provider list that never retries. The two stay separate types because they
// answer different questions -- "is this program present" against "how do I start this
// provider" -- and a resolver that returned ProbeYes would say nothing about the spec.
//
// Unknown is the ZERO value on purpose: a resolver that falls off the end of
// its own logic then reports "nothing established", which is retryable, rather than
// an authoritative absence.
type Resolution int

const (
	// Unknown: no probe established anything. The caller must treat the scan
	// as incomplete and retryable.
	Unknown Resolution = iota
	// Found: the returned Spec is usable.
	Found
	// Missing: every probe ran and the provider is not usable on this machine.
	Missing
)

// ResolverFunc discovers how to launch a provider. The shell path and
// login-shell flag are the same values probeBinary uses, so a resolver that wants
// the PATH answer can delegate to CheckBinary.
//
// A resolver MUST return Unknown whenever it could not establish an answer --
// a probe killed by the context, a shell that would not start, an interpreter it
// could not run. Reporting Missing there would freeze a transient failure as
// "not installed" for the worker's lifetime.
type ResolverFunc func(ctx context.Context, shellPath string, loginShell bool) (Spec, Resolution)

// resolveBinaryName returns the first binary from candidates that is
// available in the user's shell environment. If none are found, the first
// candidate is returned so that invocation produces a meaningful
// "command not found" error rather than silently picking an alias.
func resolveBinaryName(ctx context.Context, shellPath string, loginShell bool, candidates []string) string {
	for _, c := range candidates {
		if CheckBinary(ctx, shellPath, loginShell, c) == ProbeYes {
			return c
		}
	}
	return candidates[0]
}

// binaryAvailabilityCache memoizes the result of a login-shell binary probe.
// Each probe spawns a (possibly login) shell that sources user profiles —
// commonly hundreds of milliseconds — so repeat calls from the availability
// scan (Locator.Available) and the launch (Locator.Resolve) share results for
// the worker's lifetime. Installed binaries don't appear or disappear within
// a session, so no TTL is needed — but only a probe that RAN TO COMPLETION
// establishes anything: one killed by an expired context proves neither
// presence nor absence, and caching its result would freeze a load-induced
// timeout as "not installed" for the rest of the session (the new-agent
// button then stays disabled no matter how idle the machine becomes).
var (
	binaryAvailabilityCache   sync.Map // binaryAvailabilityKey -> ProbeResult
	binaryAvailabilityMutexes sync.Map // binaryAvailabilityKey -> *sync.Mutex
)

type binaryAvailabilityKey struct {
	shellPath  string
	loginShell bool
	binaryName string
}

// CheckBinary answers whether one binary resolves, and whether
// that answer ESTABLISHES anything.
//
// The second result is not optional bookkeeping: a caller that reports an
// authoritative "not installed" needs to know the difference between a
// shell that ran and said no, and a shell that never ran at all. A cached
// answer is conclusive by construction, because only a conclusive probe
// is ever cached.
func CheckBinary(ctx context.Context, shellPath string, loginShell bool, binaryName string) ProbeResult {
	key := binaryAvailabilityKey{shellPath, loginShell, binaryName}
	if v, ok := binaryAvailabilityCache.Load(key); ok {
		return v.(ProbeResult)
	}
	// Single-flight per key. A mutex rather than the previous sync.Once:
	// once an attempt ends without caching (deadline-killed probe), the
	// next caller must be able to probe again, and a spent Once cannot.
	muAny, _ := binaryAvailabilityMutexes.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if v, ok := binaryAvailabilityCache.Load(key); ok {
		return v.(ProbeResult)
	}
	res := probeBinary(ctx, shellPath, loginShell, binaryName)
	// An unknown result is this call's best answer but never a cached one — the next
	// caller re-probes with its own deadline.
	if res.Settled() {
		binaryAvailabilityCache.Store(key, res)
	}
	return res
}

// ProbeResult is the answer every environment probe in this package gives: does the
// thing exist, and did the probe ESTABLISH anything at all?
//
// It replaces the `(found, conclusive bool)` pair those probes used to return. The pair
// could spell `(true, false)` -- "it works, but nothing was established" -- which means
// nothing, and a caller that read only the first boolean silently treated an
// unestablished answer as a real one. The enum cannot express the contradiction.
//
// ProbeUnknown is the ZERO value on purpose, for the same reason Unknown is: a
// probe that falls off the end of its own logic then reports "nothing established",
// which is retryable, rather than an authoritative absence.
type ProbeResult int

const (
	// ProbeUnknown: the probe established nothing. The caller must treat the answer as
	// incomplete and must not cache it.
	ProbeUnknown ProbeResult = iota
	// ProbeYes: the thing is present and usable.
	ProbeYes
	// ProbeNo: the probe ran and the thing is not present.
	ProbeNo
)

// Settled reports whether the probe established anything, so a caller can tell an
// authoritative absence from a probe that never ran.
func (r ProbeResult) Settled() bool { return r != ProbeUnknown }

// probeReachedPresent / probeReachedAbsent are printed by the inner
// command so a login profile that exits before the probe cannot be
// cached as "binary absent". Exit status alone cannot tell those apart:
// both are ExitError.
const (
	probeReachedPresent = "__LEAPMUX_PROBE_REACHED__present"
	probeReachedAbsent  = "__LEAPMUX_PROBE_REACHED__absent"
)

// probeBinary asks the shell whether binaryName resolves, and reports
// whether the answer ESTABLISHES anything.
//
// The two are not the same, and conflating them is what froze a broken
// environment as "not installed" for the worker's lifetime. Presence is
// the inner command's marker on stdout, not the shell's exit status:
//
//   - the inner command printed probeReachedPresent or Absent — conclusive;
//   - the shell could not START at all (a $SHELL that is not executable, a
//     missing interpreter, EACCES, fork failure under load) — proves
//     nothing about the binary;
//   - a login profile exited before the inner command — no marker, likewise;
//   - ctx expired and CommandContext killed the process — likewise.
//
// $SHELL reaches here unvalidated (terminal.ResolveDefaultShell does no
// LookPath), so the start-failure case is reachable, not theoretical.
func probeBinary(ctx context.Context, shellPath string, loginShell bool, binaryName string) ProbeResult {
	shellName := terminal.ShellBaseName(shellPath)
	quoted := posixQuote(binaryName)

	var inner, flag string
	switch {
	case terminal.IsPwsh(shellName):
		inner = fmt.Sprintf(
			"if (Get-Command %s -ErrorAction SilentlyContinue) { Write-Output '%s' } else { Write-Output '%s' }",
			pwshQuote(binaryName), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-Command"
	case shellName == "nu":
		inner = fmt.Sprintf(
			"if (which %s | is-not-empty) { echo '%s' } else { echo '%s' }",
			nuQuote(binaryName), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	case shellName == "tcsh" || shellName == "csh":
		inner = fmt.Sprintf(
			"which %s >& /dev/null && printf '%%s\\n' '%s' || printf '%%s\\n' '%s'",
			quoted, probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	default:
		inner = fmt.Sprintf(
			"if command -v %s >/dev/null 2>&1; then printf '%%s\\n' '%s'; else printf '%%s\\n' '%s'; fi",
			quoted, probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	}

	args := terminal.CommandArgs(shellPath, loginShell, flag, inner)

	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.Dir = os.TempDir()
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	if ctx.Err() != nil {
		return ProbeUnknown
	}
	out := stdout.String()
	if strings.Contains(out, probeReachedPresent) {
		return ProbeYes
	}
	if strings.Contains(out, probeReachedAbsent) {
		return ProbeNo
	}
	return ProbeUnknown
}

// ProgramPath asks the user's shell for the ABSOLUTE path of a bare program name, and
// reports whether the answer establishes anything.
//
// It is the path-returning sibling of probeBinary and uses the same reached-markers for the
// same reason: a login profile that exits before the inner command must not be read as "the
// program is absent". ZCode needs the path (rather than probeBinary's boolean) so a resolved
// interpreter can be probed for node:sqlite directly, without a second shell.
func ProgramPath(ctx context.Context, shellPath string, loginShell bool, name string) (string, ProbeResult) {
	shellName := terminal.ShellBaseName(shellPath)
	quoted := posixQuote(name)

	var inner, flag string
	switch {
	case terminal.IsPwsh(shellName):
		inner = fmt.Sprintf(
			"$c = Get-Command %s -ErrorAction SilentlyContinue; if ($c) { Write-Output '%s'; Write-Output $c.Source } else { Write-Output '%s' }",
			pwshQuote(name), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-Command"
	case shellName == "nu":
		inner = fmt.Sprintf(
			"let p = (which %s); if ($p | is-not-empty) { echo '%s'; echo ($p | get 0.path) } else { echo '%s' }",
			nuQuote(name), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	case shellName == "tcsh" || shellName == "csh":
		inner = fmt.Sprintf(
			"which %s >& /dev/null && printf '%%s\\n' '%s' && which %s || printf '%%s\\n' '%s'",
			quoted, probeReachedPresent, quoted, probeReachedAbsent,
		)
		flag = "-c"
	default:
		inner = fmt.Sprintf(
			"if p=$(command -v %s 2>/dev/null); then printf '%%s\\n%%s\\n' '%s' \"$p\"; else printf '%%s\\n' '%s'; fi",
			quoted, probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	}

	args := terminal.CommandArgs(shellPath, loginShell, flag, inner)
	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.Dir = os.TempDir()
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	if ctx.Err() != nil {
		return "", ProbeUnknown
	}
	return parseProgramPathProbe(stdout.String())
}

// parseProgramPathProbe extracts the path that follows the reached-present marker.
//
// A present marker with no path after it is INCONCLUSIVE, not "absent": the shell reached
// the inner command and said the program resolves, so reporting absence would contradict
// the only evidence there is. A path that is not absolute is likewise inconclusive -- a
// shell builtin or function name cannot be executed directly.
func parseProgramPathProbe(out string) (string, ProbeResult) {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case probeReachedPresent:
			for _, rest := range lines[i+1:] {
				p := strings.TrimSpace(rest)
				if p == "" {
					continue
				}
				if !filepath.IsAbs(p) {
					return "", ProbeUnknown
				}
				return p, ProbeYes
			}
			return "", ProbeUnknown
		case probeReachedAbsent:
			return "", ProbeNo
		}
	}
	return "", ProbeUnknown
}

// Locator says how to find a provider's program: by probing bare names in
// the user's shell, or by a resolver of the provider's own. It holds exactly one
// of the two; Binaries and Custom are its only constructors, so a
// locator that states both, or neither, cannot be built outside this file.
//
// Both the availability scan and the launch read ONE locator, so the two can
// never disagree about which program a provider runs.
type Locator struct {
	// binaries lists the executable names to probe, the preferred one first.
	binaries []string
	// resolver replaces the binaries probe for a provider whose program is not a
	// bare name the login shell can resolve (see ResolverFunc).
	resolver ResolverFunc
}

// Binaries finds a program by probing bare names in the user's shell,
// the preferred name first.
func Binaries(names ...string) Locator {
	return Locator{binaries: append([]string(nil), names...)}
}

// Custom finds a program through a resolver of the provider's own.
func Custom(fn ResolverFunc) Locator {
	return Locator{resolver: fn}
}

// Valid reports whether the locator states exactly one way to find the program.
func (l Locator) Valid() bool {
	return (len(l.binaries) > 0) != (l.resolver != nil)
}

// Resolve says how to start the program at spawn time. displayName names the
// provider in the error a user sees.
//
// A binaries locator never fails: resolveBinaryName falls back to the preferred
// name, so the failure surfaces as "command not found" from the shell. A resolver
// that reports Missing or Unknown is an error, because neither can
// start a process; the two differ for the availability scan, not here.
func (l Locator) Resolve(ctx context.Context, shellPath string, loginShell bool, displayName string) (Spec, error) {
	if l.resolver != nil {
		spec, res := l.resolver(ctx, shellPath, loginShell)
		switch res {
		case Found:
			return spec, nil
		case Missing:
			return Spec{}, fmt.Errorf("%s is not installed on this machine", displayName)
		default:
			return Spec{}, fmt.Errorf("could not determine how to launch %s (the probe did not complete)", displayName)
		}
	}
	if len(l.binaries) == 0 {
		return Spec{}, fmt.Errorf("no binary candidates registered for %s", displayName)
	}
	return Spec{Program: resolveBinaryName(ctx, shellPath, loginShell, l.binaries)}, nil
}

// Available answers whether the program is present, in the same three states a
// resolver gives. A binaries locator is Found when any name probes present,
// Missing when every probe ran and answered absent, and Unknown when a
// probe established nothing and no name was found. A locator that states no way
// to find the program is Missing: nothing can start it.
func (l Locator) Available(ctx context.Context, shellPath string, loginShell bool) Resolution {
	if l.resolver != nil {
		_, res := l.resolver(ctx, shellPath, loginShell)
		return res
	}
	settled := true
	for _, name := range l.binaries {
		res := CheckBinary(ctx, shellPath, loginShell, name)
		if res == ProbeYes {
			return Found
		}
		if !res.Settled() {
			settled = false
		}
	}
	if !settled {
		return Unknown
	}
	return Missing
}
