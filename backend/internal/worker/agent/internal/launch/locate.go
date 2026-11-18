package launch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

type launchResolutionKey struct{}

func pathFallbackAllowed(ctx context.Context) bool {
	return ctx.Value(launchResolutionKey{}) != nil
}

// RefreshProbeCommandPath prepares a direct executable probe. Discovery needs
// a fresh PATH. A launch can use the last successful read if a refresh fails.
func RefreshProbeCommandPath(ctx context.Context, cmd *exec.Cmd) error {
	err := RefreshCommandPath(cmd)
	if pathFallbackAllowed(ctx) {
		return nil
	}
	return err
}

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

// ResolverFunc discovers how to launch a provider in the configured shell.
// A resolver can use CheckBinary for a command lookup. Return Unknown when a
// probe fails to establish an answer so a caller can retry.
type ResolverFunc func(ctx context.Context, shellPath string, loginShell bool) (Spec, Resolution)

// resolveBinaryName returns the first binary from candidates that is
// available in the user's shell environment. If none are found, the first
// candidate is returned so that invocation produces a meaningful
// "command not found" error rather than silently picking an alias.
func resolveBinaryName(ctx context.Context, shellPath string, loginShell bool, candidates []string) string {
	if len(candidates) == 1 {
		return candidates[0]
	}
	results := checkBinaries(ctx, shellPath, loginShell, candidates)
	for _, candidate := range candidates {
		if results[candidate] == ProbeYes {
			return candidate
		}
	}
	return candidates[0]
}

// CheckBinary reports whether the command resolves in a fresh shell.
func CheckBinary(ctx context.Context, shellPath string, loginShell bool, binaryName string) ProbeResult {
	return checkBinaries(ctx, shellPath, loginShell, []string{binaryName})[binaryName]
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
	// ProbeUnknown: the probe established nothing. The caller must treat the answer as incomplete.
	ProbeUnknown ProbeResult = iota
	// ProbeYes: the thing is present and usable.
	ProbeYes
	// ProbeNo: the probe ran and the thing is not present.
	ProbeNo
)

// Settled reports whether the probe established anything, so a caller can tell an
// authoritative absence from a probe that never ran.
func (r ProbeResult) Settled() bool { return r != ProbeUnknown }

// A result marker confirms that the shell reached the lookup.
const (
	probeReachedPresent = "__LEAPMUX_PROBE_REACHED__present"
	probeReachedAbsent  = "__LEAPMUX_PROBE_REACHED__absent"
)

// checkBinaries runs one fresh shell so all names are resolved against the same
// PATH and profile. A profile can exit before running the checks, so only a
// result marker establishes presence or absence.
func checkBinaries(ctx context.Context, shellPath string, loginShell bool, names []string) map[string]ProbeResult {
	if len(names) == 0 || ctx.Err() != nil {
		return nil
	}
	shellName := terminal.ShellBaseName(shellPath)
	type answer struct {
		name   string
		result ProbeResult
	}
	markers := make(map[string]answer, 2*len(names))
	var script strings.Builder
	for index, name := range names {
		present := fmt.Sprintf("%s:%d", probeReachedPresent, index)
		absent := fmt.Sprintf("%s:%d", probeReachedAbsent, index)
		markers[present] = answer{name: name, result: ProbeYes}
		markers[absent] = answer{name: name, result: ProbeNo}
		script.WriteString(binaryCheckCommand(shellName, name, present, absent))
		script.WriteByte('\n')
	}

	output := runShellProbe(ctx, shellPath, loginShell, script.String())
	results := make(map[string]ProbeResult, len(names))
	for line := range strings.SplitSeq(output, "\n") {
		if marker, ok := markers[strings.TrimSpace(line)]; ok {
			results[marker.name] = marker.result
		}
	}
	return results
}

func binaryCheckCommand(shellName, name, present, absent string) string {
	quoted := shellquote.POSIX.Quote(name)
	switch {
	case terminal.IsPwsh(shellName):
		return fmt.Sprintf(
			"Write-Output ''; if (Get-Command %s -ErrorAction SilentlyContinue) { Write-Output '%s' } else { Write-Output '%s' }",
			shellquote.PowerShell.Quote(name), present, absent,
		)
	case shellName == "nu":
		return fmt.Sprintf(
			"print ''; if (which %s | is-not-empty) { print '%s' } else { print '%s' }",
			nuQuote(name), present, absent,
		)
	case shellName == "tcsh" || shellName == "csh":
		return fmt.Sprintf(
			"echo ''; which %s >& /dev/null && echo '%s' || echo '%s'",
			quoted, present, absent,
		)
	case shellName == "fish":
		return fmt.Sprintf(
			"if command -v %s >/dev/null 2>&1; printf '\\n%%s\\n' '%s'; else; printf '\\n%%s\\n' '%s'; end",
			quoted, present, absent,
		)
	default:
		return fmt.Sprintf(
			"if command -v %s >/dev/null 2>&1; then printf '\\n%%s\\n' '%s'; else printf '\\n%%s\\n' '%s'; fi",
			quoted, present, absent,
		)
	}
}

// ProgramPath returns the absolute path of a command in the configured shell.
func ProgramPath(ctx context.Context, shellPath string, loginShell bool, name string) (string, ProbeResult) {
	shellName := terminal.ShellBaseName(shellPath)
	quoted := shellquote.POSIX.Quote(name)

	var lookup string
	switch {
	case terminal.IsPwsh(shellName):
		lookup = fmt.Sprintf(
			"Write-Output ''; $c = Get-Command %s -ErrorAction SilentlyContinue; if ($c) { Write-Output '%s'; Write-Output $c.Source } else { Write-Output '%s' }",
			shellquote.PowerShell.Quote(name), probeReachedPresent, probeReachedAbsent,
		)
	case shellName == "nu":
		lookup = fmt.Sprintf(
			"print ''; let p = (which %s); if ($p | is-not-empty) { print '%s'; print ($p | get 0.path) } else { print '%s' }",
			nuQuote(name), probeReachedPresent, probeReachedAbsent,
		)
	case shellName == "tcsh" || shellName == "csh":
		lookup = fmt.Sprintf(
			"echo ''; which %s >& /dev/null && echo '%s' && which %s || echo '%s'",
			quoted, probeReachedPresent, quoted, probeReachedAbsent,
		)
	case shellName == "fish":
		lookup = fmt.Sprintf(
			"if command -q %s; printf '\\n%%s\\n' '%s'; command -s %s; else; printf '\\n%%s\\n' '%s'; end",
			quoted, probeReachedPresent, quoted, probeReachedAbsent,
		)
	default:
		nativePath := ""
		if runtime.GOOS == "windows" {
			// Git Bash can return /c/Tools/node.exe for a native executable.
			nativePath = "case \"$p\" in /*) if [ -x /usr/bin/cygpath ]; then p=$(/usr/bin/cygpath -w -- \"$p\") || exit 1; fi;; esac; "
		}
		lookup = fmt.Sprintf(
			"if p=$(command -v %s 2>/dev/null); then %sprintf '\\n%%s\\n%%s\\n' '%s' \"$p\"; else printf '\\n%%s\\n' '%s'; fi",
			quoted, nativePath, probeReachedPresent, probeReachedAbsent,
		)
	}

	return parseProgramPathProbe(runShellProbe(ctx, shellPath, loginShell, lookup))
}

func runShellProbe(ctx context.Context, shellPath string, loginShell bool, script string) string {
	if name := terminal.ShellBaseName(shellPath); name == "tcsh" || name == "csh" {
		// The builtins still work when the PATH being checked is empty.
		script = "unalias echo\nunalias which\n" + script
	}
	cmd, err := newShellCommand(ctx, shellPath, loginShell, script, os.TempDir())
	if err != nil && !pathFallbackAllowed(ctx) {
		return ""
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// A shell profile can leave a subprocess holding stdout open.
	owner := procutil.PrepareProcess(cmd)
	cmd.WaitDelay = time.Second
	if startErr := owner.Start(); startErr != nil {
		_ = owner.Close()
		return ""
	}
	_ = owner.Wait()
	_ = owner.Close()
	if ctx.Err() != nil {
		return ""
	}
	return stdout.String()
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
// Discovery and launch share the registered candidate list or resolver.
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
	ctx = context.WithValue(ctx, launchResolutionKey{}, struct{}{})
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

func (l Locator) binaryAvailability(results map[string]ProbeResult) Resolution {
	settled := true
	for _, name := range l.binaries {
		switch results[name] {
		case ProbeYes:
			return Found
		case ProbeUnknown:
			settled = false
		case ProbeNo:
			continue
		}
	}
	if !settled {
		return Unknown
	}
	return Missing
}

// ScanAvailability checks ordinary locators together in one shell. Custom
// resolvers run concurrently and use the same resolution logic as launch.
func ScanAvailability(ctx context.Context, shellPath string, loginShell bool, locators []Locator) []Resolution {
	results := make([]Resolution, len(locators))
	names := make([]string, 0, len(locators))
	seen := make(map[string]struct{}, len(locators))
	var pending sync.WaitGroup
	for index, locator := range locators {
		if locator.resolver != nil {
			pending.Go(func() {
				_, results[index] = locator.resolver(ctx, shellPath, loginShell)
			})
			continue
		}
		for _, name := range locator.binaries {
			if _, duplicate := seen[name]; !duplicate {
				names = append(names, name)
				seen[name] = struct{}{}
			}
		}
	}
	binaries := checkBinaries(ctx, shellPath, loginShell, names)
	for index, locator := range locators {
		if locator.resolver == nil {
			results[index] = locator.binaryAvailability(binaries)
		}
	}
	pending.Wait()
	return results
}
