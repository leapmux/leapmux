package copilot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/procutil"
)

// Copilot CLI 1.0.103 removes `--no-remote` (remote control became opt-in) and
// renames `--no-remote-export` to `--sync-session=off`. LeapMux launches the
// native server with the new arguments, so an older CLI fails in its argument
// parser before any useful message appears. The start probes the version first
// and states the requirement in words a user can act on.

// copilotVersion is one release number.
type copilotVersion struct {
	major, minor, patch int
}

func (v copilotVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// less reports whether v is an earlier release than other.
func (v copilotVersion) less(other copilotVersion) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

// copilotMinimumVersion is the first Copilot CLI release that accepts the
// server arguments LeapMux uses.
var copilotMinimumVersion = copilotVersion{major: 1, minor: 0, patch: 103}

// copilotVersionPattern matches the first release number in the version output.
// Copilot CLI prints the number after its name (`GitHub Copilot CLI 1.0.103.`);
// a prerelease carries a suffix that the comparison ignores.
var copilotVersionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parseCopilotVersion reads the release number out of `copilot --version`.
func parseCopilotVersion(output string) (copilotVersion, bool) {
	match := copilotVersionPattern.FindStringSubmatch(output)
	if match == nil {
		return copilotVersion{}, false
	}
	var parts [3]int
	for i := range parts {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return copilotVersion{}, false
		}
		parts[i] = n
	}
	return copilotVersion{major: parts[0], minor: parts[1], patch: parts[2]}, true
}

// checkCopilotVersion refuses a release that predates the server arguments
// LeapMux uses, and states how to install a supported one.
func checkCopilotVersion(output string) (copilotVersion, error) {
	version, ok := parseCopilotVersion(output)
	if !ok {
		return copilotVersion{}, fmt.Errorf("`copilot --version` printed %q, which states no release number; "+
			"install GitHub Copilot CLI %s or later and make sure it is the `copilot` on PATH",
			firstLine(output), copilotMinimumVersion)
	}
	if version.less(copilotMinimumVersion) {
		return version, fmt.Errorf("`copilot --version` reports version %s; "+
			"install GitHub Copilot CLI %s or later and make sure it is the `copilot` on PATH",
			version, copilotMinimumVersion)
	}
	return version, nil
}

// copilotVersionProbeTimeout limits `copilot --version`, which starts a login
// shell and a Node process and prints one line.
const copilotVersionProbeTimeout = 60 * time.Second

// probeCopilotVersion runs `copilot --version` through the same resolved launch
// the server takes, so the version it checks is the program the server runs.
// COPILOT_AUTO_UPDATE selects the runtime in both invocations, so both must
// carry it.
func probeCopilotVersion(ctx context.Context, opts agent.Options, spec launch.Spec) (copilotVersion, error) {
	timeout := min(opts.EffectiveStartupTimeout(), copilotVersionProbeTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, delimiter, _ := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		BaseArgs:   []string{"--version"},
		SetEnv:     []string{copilotAutoUpdateEnv},
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	// A process that inherited stdout -- a background job that the login shell's
	// profile started -- keeps the pipe open after `copilot` exits. Without a
	// WaitDelay, Wait reads the pipe until that process exits too, and the timeout
	// above cannot end the wait: the program it would kill exited already.
	owner := procutil.PrepareProcess(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// ErrWaitDelay reports a program that exited with success while a descendant
	// held its pipe. The program's own output is complete, so it is read as usual.
	runErr := owner.Start()
	if runErr == nil {
		runErr = owner.Wait()
	}
	cleanupErr := owner.Close()
	if errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = nil
	}
	if err := errors.Join(runErr, cleanupErr); err != nil {
		if ctx.Err() != nil {
			return copilotVersion{}, fmt.Errorf("`copilot --version` did not answer within %s", timeout)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(string(providerkit.AfterPreamble(stdout.Bytes(), delimiter)))
		}
		return copilotVersion{}, fmt.Errorf("run `copilot --version`: %w: %s", err, firstLine(detail))
	}
	return checkCopilotVersion(string(providerkit.AfterPreamble(stdout.Bytes(), delimiter)))
}

// firstLine returns the first non-blank line of s, trimmed, for an error
// message that must not carry a whole help text.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
