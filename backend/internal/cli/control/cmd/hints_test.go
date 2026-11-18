package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

// runHinted runs a leaf that fails with err under a tree that addHubHints
// decorated, and returns the error of the command.
func runHinted(t *testing.T, err error) error {
	t.Helper()
	root := cli.Group("control", "Control",
		cli.NewLeaf(cli.Leaf{
			Use:   "probe",
			Short: "Probe",
			Run:   func(*cli.Invocation) (any, error) { return nil, err },
		}))
	addHubHints(root)
	return cli.Run(context.Background(), root, []string{"probe"}, strings.NewReader(""), io.Discard, io.Discard)
}

func TestAddHubHints(t *testing.T) {
	t.Run("hints the login after a refused credential", func(t *testing.T) {
		got := cli.FromError(runHinted(t, fmt.Errorf("list tabs: %w", connect.NewError(connect.CodeUnauthenticated, "expired"))))
		assert.Equal(t, cli.CodeNotLoggedIn, got.Code)
		assert.Equal(t, "list tabs: expired", got.Message)
		assert.Equal(t, []string{`run "leapmux control auth login --hub <url>" to sign in again`}, got.Hints)
	})

	t.Run("hints the version comparison after an unimplemented call", func(t *testing.T) {
		got := cli.FromError(runHinted(t, connect.NewError(connect.CodeUnimplemented, "no such method")))
		assert.Equal(t, cli.CodeVersionMismatch, got.Code)
		require.Len(t, got.Hints, 1)
		assert.Contains(t, got.Hints[0], "leapmux control version")
	})

	t.Run("adds nothing to another refusal", func(t *testing.T) {
		got := cli.FromError(runHinted(t, connect.NewError(connect.CodeNotFound, "no such tab")))
		assert.Equal(t, cli.CodeNotFound, got.Code)
		assert.Empty(t, got.Hints)
	})

	t.Run("keeps the hints of an error that the command classified", func(t *testing.T) {
		own := cli.Wrap(cli.CodeNotLoggedIn, connect.NewError(connect.CodeUnauthenticated, "refused"), "the hub refused this credential").
			WithHint("its own hint")
		got := cli.FromError(runHinted(t, own))
		assert.Equal(t, []string{"its own hint"}, got.Hints)
	})

	t.Run("passes an error that is not a refused call", func(t *testing.T) {
		plain := errors.New("disk full")
		err := runHinted(t, plain)
		require.ErrorIs(t, err, plain)
		assert.Empty(t, cli.FromError(err).Hints)
	})

	t.Run("passes a success", func(t *testing.T) {
		require.NoError(t, runHinted(t, nil))
	})
}

// A shipped control command adds the hint too, so NewCommand decorates its
// tree. The fake hub does not implement GetUser.
func TestNewCommand_AddsTheHubHints(t *testing.T) {
	url := startAdminHub(t, &fakeAdminHub{})
	f := runControl(t, "admin", "user", "get", "--hub", url, "alice").requireFailure(t, cli.CodeVersionMismatch)
	require.Len(t, f.Hints, 1)
	assert.Contains(t, f.Hints[0], "leapmux control version --hub <url>")
}
