package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	internalconfig "github.com/leapmux/leapmux/internal/config"
)

func TestInvocationDurationReadsExtendedAndStandardDurationFlags(t *testing.T) {
	t.Parallel()

	var extended time.Duration
	var gotExtended, gotStandard time.Duration
	leaf := cli.NewLeaf(cli.Leaf{
		Use:   "probe",
		Short: "Probe duration flags",
		Flags: func(fs *pflag.FlagSet) {
			fs.Var(internalconfig.NewDurationFlag(&extended, 7*24*time.Hour), "ttl", "extended duration")
			fs.Duration("timeout", 10*time.Second, "standard duration")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			gotExtended = inv.Duration("ttl")
			gotStandard = inv.Duration("timeout")
			return nil, nil
		},
	})

	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), leaf, []string{"--ttl", "3d", "--timeout", "45s"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, cli.ExitOK, code, stderr.String())
	assert.Equal(t, 72*time.Hour, gotExtended)
	assert.Equal(t, 45*time.Second, gotStandard)
}
