package launch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// shellEnvPrefix begins each line of the ShellEnv probe that states one value.
const shellEnvPrefix = "__LEAPMUX_ENV__"

// ShellEnv reads the values of names in the environment that the user's shell
// sets up, and reports whether the probe established anything.
//
// It exists for a provider that must read a file the way its program will: the
// program starts inside the same shell, after the profile runs, and a profile
// can export a variable that the worker's own environment does not hold. The
// desktop app is the common case, because it starts the worker with no profile.
//
// An unset variable and an empty one both read as "". The probe prints each
// value on one line, so a value that holds a newline keeps only its first line;
// the paths that a caller reads hold none. Each name must be a plain
// identifier, because the probe writes it into shell code unquoted; ShellEnv
// panics on any other name, as Wrap does for a gate. It uses the reached marker
// of probeBinary, for the same reason: a profile that exits before the probe
// must not read as a set of empty values.
func ShellEnv(ctx context.Context, shellPath string, loginShell bool, names []string) (map[string]string, ProbeResult) {
	for _, name := range names {
		if !shellIdentifier.MatchString(name) {
			panic(fmt.Sprintf("shell env probe: variable %q is not a plain identifier", name))
		}
	}
	shellName := terminal.ShellBaseName(shellPath)
	var inner, flag string
	switch {
	case terminal.IsPwsh(shellName):
		inner, flag = pwshEnvProbe(names), "-Command"
	case shellName == "nu":
		inner, flag = nuEnvProbe(names), "-c"
	case shellName == "tcsh" || shellName == "csh":
		inner, flag = cshEnvProbe(names), "-c"
	default:
		inner, flag = posixEnvProbe(names), "-c"
	}

	cmd := exec.CommandContext(ctx, shellPath, terminal.CommandArgs(shellPath, loginShell, flag, inner)...)
	cmd.Dir = os.TempDir()
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	if ctx.Err() != nil {
		return nil, ProbeUnknown
	}
	return parseShellEnvProbe(stdout.String(), names)
}

// posixEnvProbe prints the reached marker, then one line for each name.
// printf repeats its format for each argument, and fish reads the same form.
func posixEnvProbe(names []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "printf '%%s\\n' '%s'", probeReachedPresent)
	for _, name := range names {
		fmt.Fprintf(&b, ` "%s%s=$%s"`, shellEnvPrefix, name, name)
	}
	return b.String()
}

// cshEnvProbe prints the reached marker, then one line for each name that is
// set. csh fails on an unset name, and a guard and the read it guards cannot
// share one line (see cshEnvSeed).
func cshEnvProbe(names []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "printf '%%s\\n' '%s'\n", probeReachedPresent)
	for _, name := range names {
		fmt.Fprintf(&b, "if ( $?%s ) then\n", name)
		fmt.Fprintf(&b, "printf '%%s\\n' \"%s%s=$%s\"\n", shellEnvPrefix, name, name)
		b.WriteString("endif\n")
	}
	return b.String()
}

// nuEnvProbe prints the reached marker, then one line for each name.
func nuEnvProbe(names []string) string {
	parts := []string{fmt.Sprintf("echo '%s'", probeReachedPresent)}
	for _, name := range names {
		parts = append(parts, fmt.Sprintf(`echo $"%s%s=($env | get -i %s | default '')"`, shellEnvPrefix, name, name))
	}
	return strings.Join(parts, "; ")
}

// pwshEnvProbe prints the reached marker, then one line for each name.
func pwshEnvProbe(names []string) string {
	parts := []string{fmt.Sprintf("Write-Output '%s'", probeReachedPresent)}
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("Write-Output ('%s%s=' + $env:%s)", shellEnvPrefix, name, name))
	}
	return strings.Join(parts, "; ")
}

// parseShellEnvProbe reads the values that follow the reached marker. A name
// that no line states reads as "". No marker establishes nothing.
func parseShellEnvProbe(out string, names []string) (map[string]string, ProbeResult) {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != probeReachedPresent {
			continue
		}
		values := make(map[string]string, len(names))
		for _, name := range names {
			values[name] = ""
		}
		for _, rest := range lines[i+1:] {
			entry, ok := strings.CutPrefix(strings.TrimRight(rest, "\r"), shellEnvPrefix)
			if !ok {
				continue
			}
			name, value, ok := strings.Cut(entry, "=")
			if _, wanted := values[name]; ok && wanted {
				values[name] = value
			}
		}
		return values, ProbeYes
	}
	return nil, ProbeUnknown
}
