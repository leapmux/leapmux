package procutil

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDarwinProcessTablePreservesBirthAndParent(t *testing.T) {
	t.Parallel()
	identity, present := IdentifyProcess(os.Getpid())
	require.True(t, present)
	table, err := SnapshotProcessTable(t.Context())
	require.NoError(t, err)
	record := table.records[identity.PID]
	assert.Equal(t, ProcessRecord{PID: identity.PID, PPID: os.Getppid(), StartTime: identity.StartTime}, record)
	assert.NotEmpty(t, table.Name(t.Context(), identity.PID))
}
