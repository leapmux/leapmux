package letta

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeLettaTestTaskLog(t *testing.T, root, childID, report string) string {
	t.Helper()
	directory := filepath.Join(root, "letta-background-test")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "task_1.log")
	content := "[Task started: Read a file]\n[subagent_type: general-purpose]\n\n" +
		"subagent_type=general-purpose subagent_id=" + childID +
		" subagent_status=success agent_id=agent-child conversation_id=default\n\n" +
		report + "\n\n[Task completed]\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestReadLettaTaskReportKeepsExactContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const report = "  Keep indentation.\n\nKeep the last spaces.  "
	path := writeLettaTestTaskLog(t, root, lettaTestChildID, report)

	actual, err := readLettaTaskReport(root, false, path, "task_1", lettaTestChildID, "completed")
	require.NoError(t, err)
	assert.Equal(t, report, actual)
}

func TestReadLettaTaskReportRejectsWrongIdentityAndLocation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := writeLettaTestTaskLog(t, root, lettaTestChildID, "safe report")
	for _, tc := range []struct {
		name, path, taskID, childID string
	}{
		{name: "wrong task", path: path, taskID: "task_2", childID: lettaTestChildID},
		{name: "wrong child", path: path, taskID: "task_1", childID: "subagent-other"},
		{name: "path traversal", path: filepath.Join(t.TempDir(), "task_1.log"), taskID: "task_1", childID: lettaTestChildID},
		{name: "invalid task id", path: path, taskID: "task_../1", childID: lettaTestChildID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readLettaTaskReport(root, false, tc.path, tc.taskID, tc.childID, "completed")
			require.Error(t, err)
		})
	}
}

func TestReadLettaTaskReportRejectsASymlinkedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := writeLettaTestTaskLog(t, root, lettaTestChildID, "safe report")
	outside := filepath.Join(t.TempDir(), "outside.log")
	require.NoError(t, os.Rename(path, outside))
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, err := readLettaTaskReport(root, false, path, "task_1", lettaTestChildID, "completed")
	require.ErrorContains(t, err, "not a regular file")
}

func TestReadLettaTaskReportRejectsASymlinkedDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := writeLettaTestTaskLog(t, root, lettaTestChildID, "safe report")
	directory := filepath.Dir(path)
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.Rename(directory, outside))
	if err := os.Symlink(outside, directory); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, err := readLettaTaskReport(root, false, path, "task_1", lettaTestChildID, "completed")
	require.ErrorContains(t, err, "not a regular directory")
}

func TestReadLettaTaskReportRejectsAnOversizedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := writeLettaTestTaskLog(t, root, lettaTestChildID, "safe report")
	file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(lettaTaskLogReadLimit+1))
	require.NoError(t, file.Close())
	_, err = readLettaTaskReport(root, false, path, "task_1", lettaTestChildID, "completed")
	require.ErrorContains(t, err, "size cap")
}

func TestReadLettaTaskReportAcceptsAConfiguredScratchpad(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "task_1.log")
	nested := writeLettaTestTaskLog(t, root, lettaTestChildID, "scratchpad report")
	require.NoError(t, os.Rename(nested, path))
	selectedRoot, direct := lettaTaskLogRoot([]string{"LETTA_SCRATCHPAD=" + root})
	assert.Equal(t, root, selectedRoot)
	assert.True(t, direct)
	report, err := readLettaTaskReport(selectedRoot, direct, path, "task_1", lettaTestChildID, "completed")
	require.NoError(t, err)
	assert.Equal(t, "scratchpad report", report)
}

func TestLettaReportHeaderRejectsDuplicateOrConflictingFields(t *testing.T) {
	t.Parallel()
	for _, header := range []string{
		"subagent_id=" + lettaTestChildID + " subagent_id=subagent-other subagent_status=success",
		"subagent_id=subagent-other subagent_id=" + lettaTestChildID + " subagent_status=success",
		"subagent_id=" + lettaTestChildID + " subagent_status=success subagent_status=error",
		"subagent_id=" + lettaTestChildID + " subagent_status=error subagent_status=success",
	} {
		assert.False(t, lettaReportIdentifiesChild(header, lettaTestChildID, "completed"), header)
	}
	assert.True(t, lettaReportIdentifiesChild("subagent_id="+lettaTestChildID+" subagent_status=success", lettaTestChildID, "completed"))
}

func TestLettaTaskLogRootUsesTheLastConfiguredValue(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	selected, direct := lettaTaskLogRoot([]string{"LETTA_SCRATCHPAD=/old", "OTHER=x", "LETTA_SCRATCHPAD=" + root})
	assert.Equal(t, root, selected)
	assert.True(t, direct)
	selected, direct = lettaTaskLogRoot([]string{"LETTA_SCRATCHPAD=relative/path"})
	assert.Equal(t, os.TempDir(), selected)
	assert.False(t, direct)
}

func TestParseLettaTaskLogRejectsAnInterruptedEnding(t *testing.T) {
	t.Parallel()
	content := "subagent_type=general-purpose subagent_id=" + lettaTestChildID +
		" subagent_status=success\n\npartial report"
	_, err := parseLettaTaskLog(content, lettaTestChildID, "completed")
	require.ErrorContains(t, err, "ending")
}
