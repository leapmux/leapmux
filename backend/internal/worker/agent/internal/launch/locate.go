package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/id"
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

// Spec describes the target of a provider launch. Ordinary locators supply
// ordered commands. A custom resolver can supply a program and its interpreter arguments.
type Spec struct {
	candidates []string
	// Program is an explicit path or program word. An ordered command spec
	// leaves Program empty and lets the shell choose among its candidates.
	Program string
	// PrefixArgs precede the provider's own BaseArgs -- the script path an
	// interpreter must receive before the script's own arguments.
	PrefixArgs []string
	// Env supplies provider defaults to the shell before the profile runs.
	Env []string
	// RequiredEnv states interpreter requirements that must survive the profile.
	// It takes precedence over other post-profile assignments for the same key.
	RequiredEnv []string
}

// ShellCommands prepares an ordered choice without consulting the profile.
func ShellCommands(names ...string) Spec {
	return Spec{candidates: slices.Clone(names)}
}

func validCandidates(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !providerCommandName.MatchString(name) {
			return false
		}
	}
	return true
}

// ErrProviderMissing reports a conclusive absence from a custom resolver.
var ErrProviderMissing = errors.New("is not installed on this machine")

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

// checkBinaries runs one fresh shell so all candidates see the same profile and PATH.
func checkBinaries(ctx context.Context, shellPath string, loginShell bool, names []string) map[string]ProbeResult {
	results, _ := probeCommands(ctx, shellPath, loginShell, names, false)
	return results
}

// ProgramPath finds a native executable even when a shell callable shadows it.
func ProgramPath(ctx context.Context, shellPath string, loginShell bool, name string) (string, ProbeResult) {
	results, path := probeCommands(ctx, shellPath, loginShell, []string{name}, true)
	return path, results[name]
}

func probeCommands(ctx context.Context, shellPath string, loginShell bool, names []string, nativeOnly bool) (map[string]ProbeResult, string) {
	validNames := validCandidates(names)
	if nativeOnly {
		validNames = len(names) == 1 && validNativeProgramName(names[0])
	}
	if !validNames || ctx.Err() != nil {
		return nil, ""
	}
	shell := terminal.ShellBaseName(shellPath)
	if shell == "cmd" {
		return nil, ""
	}
	nonce := "__LEAPMUX_PROBE_" + id.Short() + "__"
	output := runShellProbe(ctx, shellPath, loginShell, renderCommandProbe(shell, nonce, names, nativeOnly))
	return parseCommandProbe(output, nonce, names, nativeOnly)
}

func validNativeProgramName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}

const maxCommandProbeOutput = 1 << 20

func runShellProbe(ctx context.Context, shellPath string, loginShell bool, script string) string {
	if isCsh(terminal.ShellBaseName(shellPath)) {
		scriptFiles := &PreparedLaunch{}
		var err error
		script, err = scriptFiles.writeCshInvocation(script)
		if err != nil {
			return ""
		}
		defer func() { _ = scriptFiles.Close() }()
	}
	cmd, err := newShellCommand(ctx, shellPath, loginShell, script, os.TempDir(), nil)
	if err != nil && !pathFallbackAllowed(ctx) {
		return ""
	}
	stdout := LimitedBuffer{Limit: maxCommandProbeOutput}
	cmd.Stdout = &stdout
	owner := procutil.PrepareProcess(cmd)
	cmd.WaitDelay = time.Second
	if startErr := owner.Start(); startErr != nil {
		_ = owner.Close()
		return ""
	}
	waitErr := owner.Wait()
	cleanupErr := owner.Close()
	if waitErr != nil || cleanupErr != nil || ctx.Err() != nil || stdout.Truncated {
		return ""
	}
	return stdout.String()
}

// parseCommandProbe rejects an incomplete batch, including a duplicated or
// out-of-order record. Profiles can write before the frame and in exit hooks.
func parseCommandProbe(output, nonce string, names []string, nativeOnly bool) (map[string]ProbeResult, string) {
	results := make(map[string]ProbeResult, len(names))
	started, complete := false, false
	index := 0
	path := ""
	for len(output) != 0 {
		line, rest, hasNewline := strings.Cut(output, "\n")
		output = rest
		line = strings.TrimSuffix(line, "\r")
		if complete {
			if strings.HasPrefix(line, nonce) {
				return nil, ""
			}
			continue
		}
		if !hasNewline {
			return nil, ""
		}
		if !started {
			started = line == nonce+":start"
			continue
		}
		if line == "" {
			continue
		}
		if line == nonce+":complete" && index == len(names) {
			complete = true
			continue
		}
		if index >= len(names) {
			return nil, ""
		}
		record, ok := strings.CutPrefix(line, fmt.Sprintf("%s:%d:", nonce, index))
		if !ok {
			return nil, ""
		}
		switch {
		case record == "no":
			results[names[index]] = ProbeNo
		case record == "yes" && !nativeOnly:
			results[names[index]] = ProbeYes
		case nativeOnly && strings.HasPrefix(record, "yes:"):
			path = strings.TrimPrefix(record, "yes:")
			if !filepath.IsAbs(path) {
				return nil, ""
			}
			results[names[index]] = ProbeYes
		default:
			return nil, ""
		}
		index++
	}
	if !complete {
		return nil, ""
	}
	return results, path
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
	return (len(l.binaries) == 0 && l.resolver != nil) || (l.resolver == nil && validCandidates(l.binaries))
}

// Resolve says how to start the program at spawn time. displayName names the
// provider in the error a user sees.
//
// An ordinary locator carries the ordered names into the launch shell.
func (l Locator) Resolve(ctx context.Context, shellPath string, loginShell bool, displayName string) (Spec, error) {
	ctx = context.WithValue(ctx, launchResolutionKey{}, struct{}{})
	if l.resolver != nil {
		spec, res := l.resolver(ctx, shellPath, loginShell)
		switch res {
		case Found:
			return spec, nil
		case Missing:
			return Spec{}, fmt.Errorf("%s %w", displayName, ErrProviderMissing)
		default:
			return Spec{}, fmt.Errorf("could not determine how to launch %s (the probe did not complete)", displayName)
		}
	}
	if !validCandidates(l.binaries) {
		return Spec{}, fmt.Errorf("no valid provider commands registered for %s", displayName)
	}
	return ShellCommands(l.binaries...), nil
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
