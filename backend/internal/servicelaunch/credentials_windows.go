//go:build windows

package servicelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

// readAccountPassword uses the console's protected input mode. The native
// installer receives the value in memory; no process argument carries it.
func readAccountPassword(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("configure startup interactively to enter the password for %s", account)
	}
	if _, err := fmt.Fprintf(os.Stdout, "Password for %s (LeapMux does not store it): ", account); err != nil {
		return nil, err
	}
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, outputErr := fmt.Fprintln(os.Stdout)
	if err != nil || outputErr != nil || ctx.Err() != nil {
		clear(password)
		return nil, errors.Join(err, outputErr, ctx.Err())
	}
	return password, nil
}
