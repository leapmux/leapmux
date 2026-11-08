package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/lifecycle"
)

// printModeBanner prints the LeapMux banner of a mode to stderr. Every
// `leapmux <mode>` command prints it once, after its flags parse. A test
// replaces it to observe which commands print it.
var printModeBanner = logging.PrintBanner

// openLifecycleCommand parses a lifecycle command and prints the banner. It
// reports done when the command needs no further work: --version printed the
// version, or the flags failed to parse. The caller then returns err.
//
// A parse failure and --help print no banner. Neither does `status --json`,
// because a script reads its output.
func openLifecycleCommand(mode, verb string, args []string) (configuration *lifecycleConfiguration, options lifecycleOptions, done bool, err error) {
	configuration, options, err = loadLifecycleCommand(mode, verb, args)
	if handleVersionQuery(err) {
		return nil, options, true, nil
	}
	if err != nil {
		return nil, options, true, err
	}
	if !options.json {
		printModeBanner(mode)
	}
	return configuration, options, false, nil
}

// instanceSelection identifies one instance in a printed command: the
// program that runs the command, and the configuration file and the data
// directory that select the instance.
type instanceSelection struct {
	mode          string
	executable    string
	configFile    string
	dataDirectory string
}

// serviceSelection selects the instance of a service specification: the
// executable that the service runs and the configuration file that it reads.
// A later start with an explicit --config replaces the configuration file of
// the service, so a printed command must name the file that is in use.
func serviceSelection(spec lifecycle.ServiceSpec) instanceSelection {
	return instanceSelection{
		mode:          spec.Mode.String(),
		executable:    spec.Executable,
		configFile:    spec.ConfigFile,
		dataDirectory: spec.DataDirectory,
	}
}

// commandSelection selects the instance that a command acted on. A saved
// service specification wins, as serviceSelection explains. Without one, the
// command's own configuration file and the running executable select it.
// configured reports whether a saved specification exists. A specification
// that cannot be read counts as none: the selection serves guidance after a
// command that already succeeded, and `status` reports the unreadable file.
func commandSelection(mode, configFile, dataDirectory string, newController nativeControllerFactory) (selection instanceSelection, configured bool, err error) {
	if controller, controllerErr := newController(mode, dataDirectory); controllerErr == nil {
		if spec, readErr := controller.ReadSpec(); readErr == nil {
			return serviceSelection(spec), true, nil
		}
	}
	executable, err := currentExecutable()
	if err != nil {
		return instanceSelection{}, false, err
	}
	return instanceSelection{mode: mode, executable: executable, configFile: configFile, dataDirectory: dataDirectory}, false, nil
}

// currentExecutable is the path of the running LeapMux executable, as a
// service definition records it.
func currentExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := exec.LookPath(executable); err == nil {
		executable = resolved
	}
	return executable, nil
}

// printNextCommands prints the commands that act on the instance next, for
// the shell of this platform. It follows a command that already succeeded,
// so it is best effort, as printInstanceCommands explains.
func printNextCommands(selection instanceSelection, verbs ...string) {
	printInstanceCommands(os.Stdout, selection, shellquote.ForOS(runtime.GOOS), exec.LookPath, verbs...)
}

// printSelectedNextCommands prints the next commands for the instance that a
// command selected. A saved service adds the verbs in serviceVerbs. A failed
// lookup of the executable prints nothing, because the command already
// succeeded.
func printSelectedNextCommands(mode, configFile, dataDirectory string, newController nativeControllerFactory, verbs, serviceVerbs []string) {
	selection, configured, err := commandSelection(mode, configFile, dataDirectory, newController)
	if err != nil {
		return
	}
	if configured {
		verbs = append(append([]string(nil), verbs...), serviceVerbs...)
	}
	printNextCommands(selection, verbs...)
}

// printInstanceCommands prints the exact commands that act on one instance,
// one for each verb. Each selects the instance by its configuration file and
// its data directory, so it acts on this instance whatever the working
// directory, the defaults, or the environment of the shell that runs it
// later. The commands go through LeapMux, not through the native service
// manager, so they keep its validation and its readiness wait. The command
// that prints them already succeeded, so a failed write is no failure of it.
func printInstanceCommands(w io.Writer, selection instanceSelection, shell shellquote.Shell, lookPath func(string) (string, error), verbs ...string) {
	if len(verbs) == 0 {
		return
	}
	program := instanceCommandProgram(selection.executable, lookPath)
	width := 0
	for _, verb := range verbs {
		width = max(width, len(verb)+1)
	}
	var b strings.Builder
	b.WriteString("\nCommands for this instance:\n")
	for _, verb := range verbs {
		fmt.Fprintf(&b, "  %-*s %s\n", width, capitalize(verb)+":",
			shell.Command(program, selection.mode, verb, "--config", selection.configFile, "--data-dir", selection.dataDirectory))
	}
	_, _ = io.WriteString(w, b.String())
}

// instanceCommandProgram names the program in a printed command: `leapmux`
// when that name on PATH runs the same executable, and the executable's path
// otherwise, so the command never runs another copy of LeapMux.
func instanceCommandProgram(executable string, lookPath func(string) (string, error)) string {
	found, err := lookPath("leapmux")
	if err != nil {
		return executable
	}
	foundInfo, err := os.Stat(found)
	if err != nil {
		return executable
	}
	executableInfo, err := os.Stat(executable)
	if err != nil || !os.SameFile(foundInfo, executableInfo) {
		return executable
	}
	return "leapmux"
}

// printAccessHint states where to open a ready instance. A new dev instance
// waits for its first administrator, whom the browser creates there. A
// Worker, and an instance without a TCP address, report no URL.
func printAccessHint(w io.Writer, status lifecycle.Status) {
	if status.URL == nil {
		return
	}
	if status.Readiness != nil && *status.Readiness == lifecycle.ReadinessReadyForSetup {
		_, _ = fmt.Fprintf(w, "Open %s in your browser to create the first administrator.\n", *status.URL)
		return
	}
	_, _ = fmt.Fprintf(w, "Open %s in your browser.\n", *status.URL)
}

// statusNextCommands suggests what to run after a status report: start for
// an instance that does not run, and stop or restart for one that runs or
// changes state. serviceVerbs adds disable when a service exists.
func statusNextCommands(status lifecycle.Status) (verbs, serviceVerbs []string) {
	switch status.Process.State {
	case lifecycle.ProcessStopped, lifecycle.ProcessFailed:
		return []string{"start"}, []string{"disable"}
	default:
		return []string{"stop", "restart"}, nil
	}
}
