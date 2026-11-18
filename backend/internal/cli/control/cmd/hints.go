package cmd

import (
	"errors"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"

	"github.com/leapmux/leapmux/internal/cli"
)

// addHubHints makes every leaf under root add the next step of a control
// command to a call that the hub refused: sign in again after a refused
// credential, and compare the versions after a call that the hub does not
// implement.
//
// The hints belong to this tree and not to cli.FromError, which classifies
// the failures of every command: `leapmux worker register` reaches the hub
// with a registration key, and "auth login" cannot fix a refused key.
func addHubHints(root *cobra.Command) {
	if root.Runnable() && !root.HasSubCommands() {
		run := root.RunE
		root.RunE = func(cmd *cobra.Command, args []string) error {
			return withHubHint(run(cmd, args))
		}
	}
	for _, child := range root.Commands() {
		addHubHints(child)
	}
}

// withHubHint returns err with the next step that a control command takes
// after a refused call. An error that a command classified itself keeps its
// own hints.
func withHubHint(err error) error {
	var classified *cli.Error
	var connectErr *connect.Error
	if err == nil || errors.As(err, &classified) || !errors.As(err, &connectErr) {
		return err
	}
	out := cli.FromError(err)
	switch out.Code {
	case cli.CodeNotLoggedIn:
		return out.WithHint(`run "leapmux control auth login --hub <url>" to sign in again`)
	case cli.CodeVersionMismatch:
		return out.WithHint(`run "leapmux control version --hub <url>" to compare the CLI with the hub, and upgrade the older one`)
	default:
		return out
	}
}
