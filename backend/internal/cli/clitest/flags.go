package clitest

import (
	"bytes"
	"context"
	"strings"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

// FlagsHelp renders the CLI help output of a leaf command whose flags are
// registered by register.
func FlagsHelp(t require.TestingT, register func(*pflag.FlagSet)) string {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	leaf := cli.NewLeaf(cli.Leaf{
		Use:   "probe",
		Short: "Probe the flags",
		Flags: register,
		Run:   func(*cli.Invocation) (any, error) { return nil, nil },
	})
	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), leaf, []string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, cli.ExitOK, code, stderr.String())
	return stdout.String()
}
