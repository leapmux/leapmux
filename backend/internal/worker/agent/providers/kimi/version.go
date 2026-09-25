package kimi

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

// Two different programs install a `kimi` binary. Kimi Code 2.x is the
// TypeScript rewrite this provider speaks, and its `web` command starts the
// kap-server. The legacy Python kimi-cli (1.x and earlier) has no kap-server,
// and its `web` command is a different program that neither states the ready
// line nor answers the REST routes. A worker that launched it would wait for a
// line that never comes, so the start checks the version first and states the
// reason in words a user can act on.

// kimiVersion is one release number.
type kimiVersion struct {
	major, minor, patch int
}

func (v kimiVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// less reports whether v is an earlier release than other.
func (v kimiVersion) less(other kimiVersion) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

// kimiVersionPattern matches the first release number in the version output.
// Kimi Code prints the bare number (`2.0.2`); the legacy CLI prints it after
// its name (`kimi, version 1.5.0`), and a pre-release carries a suffix
// (`2.1.0-beta.1`) that the comparison ignores.
var kimiVersionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parseKimiVersion reads the release number out of `kimi --version`.
func parseKimiVersion(output string) (kimiVersion, bool) {
	match := kimiVersionPattern.FindStringSubmatch(output)
	if match == nil {
		return kimiVersion{}, false
	}
	var parts [3]int
	for i := range parts {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return kimiVersion{}, false
		}
		parts[i] = n
	}
	return kimiVersion{major: parts[0], minor: parts[1], patch: parts[2]}, true
}

// errKimiLegacyCLI reports a `kimi` that is not Kimi Code 2.x.
var errKimiLegacyCLI = errors.New("the `kimi` on this machine is not Kimi Code 2.0 or later")

// The two sources of a version that checkKimiVersion reads.
const (
	kimiVersionFromCLI    = "`kimi --version` printed"
	kimiVersionFromServer = "the server stated the version"
)

// checkKimiVersion refuses a version that is not Kimi Code 2.0 or later. source
// states where the version came from, for the error message.
func checkKimiVersion(source, output string) (kimiVersion, error) {
	version, ok := parseKimiVersion(output)
	if !ok {
		return kimiVersion{}, fmt.Errorf("%w: %s %q, which states no release number; "+
			"the legacy Python kimi-cli also installs a `kimi` program, so install Kimi Code 2.0 or later and "+
			"make sure it comes first on PATH", errKimiLegacyCLI, source, firstLine(output))
	}
	if version.less(kimiMinimumVersion) {
		return version, fmt.Errorf("%w: it reports version %s, which is the legacy Python kimi-cli; "+
			"install Kimi Code %s or later and make sure it comes first on PATH",
			errKimiLegacyCLI, version, kimiMinimumVersion)
	}
	return version, nil
}

// probeKimiVersion runs `kimi --version` through the same shell launch the
// server takes, so the version it checks is the program the server runs.
func probeKimiVersion(ctx context.Context, opts agent.Options, spec launch.Spec, timeout time.Duration) (kimiVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, delimiter, _ := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		BaseArgs:   []string{"--version"},
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	// A process that inherited stdout -- a background job that the login shell's
	// profile started -- keeps the pipe open after `kimi` exits. Without a
	// WaitDelay, Run reads the pipe until that process exits too, and the timeout
	// above cannot end the wait: the program it would kill exited already.
	procutil.GracefulGroupCancel(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// ErrWaitDelay reports a program that exited with success while a descendant
	// held its pipe. The program's own output is complete, so it is read as usual.
	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if ctx.Err() != nil {
			return kimiVersion{}, fmt.Errorf("`kimi --version` did not answer within %s", timeout)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(afterDelimiter(stdout.String(), delimiter))
		}
		return kimiVersion{}, fmt.Errorf("run `kimi --version`: %w: %s", err, firstLine(detail))
	}
	return checkKimiVersion(kimiVersionFromCLI, afterDelimiter(stdout.String(), delimiter))
}

// afterDelimiter returns what the program printed after the shell wrapper's
// preamble, which ends at the delimiter line.
func afterDelimiter(output, delimiter string) string {
	if delimiter == "" {
		return output
	}
	if _, rest, found := strings.Cut(output, delimiter); found {
		return rest
	}
	return output
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
