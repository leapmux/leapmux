package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

func TestLeapmuxStateClassifier_MapsAllKnownTokens(t *testing.T) {
	cases := []struct {
		token string
		want  cli.StateTone
	}{
		{"idle", cli.StateToneGreen},
		{"ready", cli.StateToneGreen},
		{"active", cli.StateToneGreen},
		{"online", cli.StateToneGreen},
		{"ok", cli.StateToneGreen},
		{"pass", cli.StateToneGreen},
		{"running", cli.StateToneGreen},
		{"completed", cli.StateToneGreen},
		{"working", cli.StateToneYellow},
		{"starting", cli.StateToneYellow},
		{"pending", cli.StateToneYellow},
		{"warn", cli.StateToneYellow},
		{"queued", cli.StateToneYellow},
		{"dispatching", cli.StateToneYellow},
		{"waiting_for_user", cli.StateToneMagenta},
		{"error", cli.StateToneRed},
		{"exited", cli.StateToneRed},
		{"startup_failed", cli.StateToneRed},
		{"failed", cli.StateToneRed},
		{"offline", cli.StateToneRed},
		{"fail", cli.StateToneRed},
		{"closed", cli.StateToneRed},
		{"canceled", cli.StateToneRed},
		{"  IDLE  ", cli.StateToneGreen},
		{"Working", cli.StateToneYellow},
		{"WAITING_FOR_USER", cli.StateToneMagenta},
		{"Startup_Failed", cli.StateToneRed},
		{"", cli.StateToneNone},
		{"   ", cli.StateToneNone},
		{"unknown-token", cli.StateToneNone},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, leapmuxStateClassifier(tc.token), "token %q", tc.token)
	}
}

func TestAttach_InstallsDefaultStateClassifier(t *testing.T) {
	result := struct {
		Rows []queueItemRow `json:"items"`
	}{
		Rows: []queueItemRow{{ID: "q-1", Kind: "user_message", State: "failed", Text: "x"}},
	}

	root := &cobra.Command{Use: "leapmux"}
	Attach(root, nil)
	root.AddCommand(cli.NewLeaf(cli.Leaf{
		Use:   "state-probe",
		Short: "Probe state styling",
		Run:   func(*cli.Invocation) (any, error) { return result, nil },
	}))

	var buf bytes.Buffer
	err := cli.Run(context.Background(), root, []string{"state-probe", "--color", "always"}, strings.NewReader(""), &buf, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "\x1b[31mfailed\x1b[0m")

	// A separate command tree without Attach does not inherit LeapMux's classifier.
	plainRoot := cli.Group("other", "Other", cli.NewLeaf(cli.Leaf{
		Use:   "state-probe",
		Short: "Probe state styling",
		Run:   func(*cli.Invocation) (any, error) { return result, nil },
	}))
	cli.AddOutputFlags(plainRoot)
	var plainBuf bytes.Buffer
	err = cli.Run(context.Background(), plainRoot, []string{"state-probe", "--color", "always"}, strings.NewReader(""), &plainBuf, &bytes.Buffer{})
	require.NoError(t, err)
	assert.NotContains(t, plainBuf.String(), "\x1b[31mfailed\x1b[0m")
}

func TestTableTags_StateAndDimStylingWithDefaultClassifier(t *testing.T) {
	var buf bytes.Buffer
	p := &cli.Printer{
		Out:             &buf,
		Err:             &bytes.Buffer{},
		Color:           cli.ColorAlways,
		StateClassifier: leapmuxStateClassifier,
	}
	err := p.Result(struct {
		Rows []queueItemRow `json:"items"`
	}{
		Rows: []queueItemRow{{
			ID:          "q-1234567890",
			Kind:        "user_message",
			State:       "queued",
			ScheduledAt: "2026-01-02T03:04:05Z",
			Text:        "hello",
		}},
	})
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "\x1b[33mqueued\x1b[0m", "STATE column must color queued yellow via leapmuxStateClassifier")
	assert.Contains(t, out, "\x1b[2m2026-01-02T03:04:05Z\x1b[0m", "SCHEDULED column must render dim")
}

func TestDestructiveLeaves_MarkedConsistently(t *testing.T) {
	root := NewCommand()
	paths := [][]string{
		{"agent", "clear"},
		{"git", "branch", "delete"},
		{"notification", "clear"},
		{"tab", "status", "clear"},
		{"worker", "pins", "remove"},
	}
	for _, path := range paths {
		leaf, _, err := root.Find(path)
		require.NoError(t, err, "find %v", path)
		assert.True(t, cli.IsDestructive(leaf), "leaf %v must be marked Destructive", path)
	}
}
