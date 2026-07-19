package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/solo"
	"github.com/leapmux/leapmux/util/version"
)

func runSolo(args []string, soloMode bool) error {
	for _, a := range args {
		if a == "-version" || a == "--version" {
			fmt.Println(version.Format())
			return nil
		}
	}

	modeName := "solo"
	defaultListen := "127.0.0.1:4327"
	if !soloMode {
		modeName = "dev"
		defaultListen = ":4327"
	}
	configDir := "~/.config/leapmux/" + modeName
	configFile := configDir + "/" + modeName + ".yaml"

	cliFlags := []string{
		"listen", "data-dir", "dev-frontend",
		"storage-sqlite-max-conns",
		"api-timeout-seconds", "agent-startup-timeout-seconds", "worktree-create-timeout-seconds",
		"log-level", "use-login-shell",
	}
	if !soloMode {
		cliFlags = append(cliFlags, "public-url")
	}
	// Worker-scoped knobs for the embedded worker; see solo.defaultExtraFlags for
	// why max-incomplete-chunked is an extra rather than a hub flag. These mirror
	// solo.defaultExtraFlags (the desktop-oriented default) so a `leapmux solo`/
	// `leapmux dev` invocation exposes the same worker knobs the desktop launcher
	// does -- without this, --use-login-shell=false is parsed by neither list and
	// is silently dropped on the floor (the worker keeps wrapping claude in the
	// login shell against the explicit flag).
	extraFlags := []hubconfig.ExtraFlagDef{
		{Name: "encryption-mode", KoanfKey: "encryption_mode", Usage: "encryption mode (classic, post-quantum)", StrDefault: "post-quantum"},
		{Name: "use-login-shell", KoanfKey: "use_login_shell", Usage: "wrap claude invocation in user's login shell", StrDefault: "true"},
		{Name: "max-incomplete-chunked", KoanfKey: "max_incomplete_chunked", Usage: "maximum in-flight chunked sequences per channel for the embedded worker (default 4)", StrDefault: "0", Category: "Timeout and limit options"},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	inst, err := solo.Start(ctx, solo.Config{
		Listen:     defaultListen,
		ConfigDir:  configDir,
		ConfigFile: configFile,
		Args:       args,
		CLIFlags:   cliFlags,
		ExtraFlags: extraFlags,
		DevMode:    !soloMode,
	})
	if err != nil {
		return err
	}
	return waitSolo(ctx, inst)
}

// soloInstance is the slice of *solo.Instance waitSolo needs, named so the wait
// logic is testable without standing up a Hub.
type soloInstance interface {
	// Wait blocks until the Hub's serve loop ends and returns its terminal error.
	Wait() error
	// Stop cancels the instance, drains it, and returns the same terminal error
	// Wait reports.
	Stop() error
}

// waitSolo blocks until the user signals or the Hub's serve loop ends, then shuts
// the instance down and reports the Hub's terminal error, if any.
//
// The deferred Stop is the SINGLE reporter of that error. Instance.Stop ends in
// Wait(), and hubErr is assigned exactly once, so Stop hands back the identical
// value the hubExited arm already observed -- returning it from the arm as well
// would join the error with itself and print the message twice to the user (main's
// handleRunError does a plain Fprintln of the joined error). Hence both select arms
// return nil: they choose WHEN to stop, never WHAT to report.
func waitSolo(ctx context.Context, inst soloInstance) (retErr error) {
	defer func() {
		stopErr := inst.Stop()
		// Belt-and-braces: a clean shutdown's http.ErrServerClosed cannot actually
		// reach here (Serve's recordListenerResult drops it before it is recorded as
		// hubErr), but filtering it in the one place that surfaces hubErr costs
		// nothing and keeps a normal Ctrl-C from ever being reported as a failure.
		if errors.Is(stopErr, http.ErrServerClosed) {
			stopErr = nil
		}
		retErr = errors.Join(retErr, stopErr)
	}()

	// Exit when the user signals or the Hub errors out.
	hubExited := make(chan error, 1)
	go func() { hubExited <- inst.Wait() }()
	select {
	case <-ctx.Done():
		return nil
	case <-hubExited:
		// The error is deliberately dropped here: the deferred Stop re-reads the very
		// same hubErr and reports it.
		return nil
	}
}
