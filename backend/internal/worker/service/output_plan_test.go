package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/msgcodec"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// readAllNotifications returns every persisted LEAPMUX message body for an
// agent, decoded and unwrapped from any notification-thread envelope. Used by
// tests to assert that updatePlan emitted (or didn't emit) `plan_updated`
// notifications with the expected payload.
func readAllNotifications(t *testing.T, queries *db.Queries, agentID string) []map[string]interface{} {
	t.Helper()
	rows, err := queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   100,
	})
	require.NoError(t, err)

	var out []map[string]interface{}
	for _, row := range rows {
		body, err := msgcodec.Decompress(row.Content, row.ContentCompression)
		require.NoError(t, err)
		// notifThreadWrapper has shape {messages: [...]}; fall back to a
		// bare payload if the body doesn't match.
		var wrapper struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Messages) > 0 {
			for _, m := range wrapper.Messages {
				var inner map[string]interface{}
				require.NoError(t, json.Unmarshal(m, &inner))
				out = append(out, inner)
			}
			continue
		}
		var inner map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &inner))
		out = append(out, inner)
	}
	return out
}

// findNotificationsByType filters notifications down to ones with the given
// `type` field.
func findNotificationsByType(notifs []map[string]interface{}, kind string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, n := range notifs {
		if n["type"] == kind {
			out = append(out, n)
		}
	}
	return out
}

// updatePlanHelper takes raw content bytes and forwards them through the same
// compress/decompress pipeline the production callers use.
func updatePlanHelper(t *testing.T, h *OutputHandler, agentID, title string, content []byte) {
	t.Helper()
	compressed, compression := msgcodec.Compress(content)
	h.updatePlan(agentID, compressed, compression, title)
}

func newPlanHandler(t *testing.T) (*OutputHandler, *db.Queries, string) {
	t.Helper()
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	h := NewOutputHandler(queries, NewWatcherManager(), nil, nil)
	h.DataDir = dataDir
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 0, 0, 0, time.UTC) }
	return h, queries, dataDir
}

func createTestAgent(t *testing.T, queries *db.Queries, agentID, title string) {
	t.Helper()
	require.NoError(t, queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       title,
	}))
}

func TestUpdatePlan_FirstWrite_StoresCanonicalPathWithAgentID(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	content := []byte("# Rendering fixes\n\n- item\n")
	updatePlanHelper(t, h, "agent-1", "Rendering fixes", content)

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)

	wantPath := filepath.Join(dataDir, "plans", "2026", "04", "Rendering fixes.agent-1.md")
	assert.Equal(t, wantPath, row.PlanFilePath)
	assert.Equal(t, "Rendering fixes", row.PlanTitle)

	got, err := os.ReadFile(row.PlanFilePath)
	require.NoError(t, err)
	assert.Equal(t, string(content), string(got))
}

func TestUpdatePlan_FirstWrite_TriggersAutoRenameForPlaceholderTitle(t *testing.T) {
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "Rendering fixes", []byte("# Rendering fixes\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "Rendering fixes", row.Title, "auto-rename should overwrite the placeholder agent title")
}

func TestUpdatePlan_FirstWrite_PreservesUserSetAgentTitle(t *testing.T) {
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "My custom title")

	updatePlanHelper(t, h, "agent-1", "Rendering fixes", []byte("# Rendering fixes\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "My custom title", row.Title, "auto-rename must not overwrite a manually set title")
}

func TestUpdatePlan_RewriteSameTitleAndContent_IsNoOp(t *testing.T) {
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	content := []byte("# Rendering fixes\n\n- item\n")
	updatePlanHelper(t, h, "agent-1", "Rendering fixes", content)
	rowBefore, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)

	// Second call with byte-identical content and the same title — must not
	// archive, must not write a new file, and must not emit a notification.
	updatePlanHelper(t, h, "agent-1", "Rendering fixes", content)

	rowAfter, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, rowBefore.PlanFilePath, rowAfter.PlanFilePath)

	dirEntries, err := os.ReadDir(filepath.Dir(rowAfter.PlanFilePath))
	require.NoError(t, err)
	assert.Equal(t, 1, len(dirEntries), "no archive file should exist after a no-op rewrite")
}

func TestUpdatePlan_RewriteSameTitleNewContent_ArchivesPriorAndWritesCanonical(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	v1 := []byte("# Rendering fixes\n\n- item one\n")
	updatePlanHelper(t, h, "agent-1", "Rendering fixes", v1)

	// Advance the clock so the archive timestamp is distinguishable.
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 30, 0, 0, time.UTC) }
	v2 := []byte("# Rendering fixes\n\n- item one\n- item two\n")
	updatePlanHelper(t, h, "agent-1", "Rendering fixes", v2)

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	canonicalPath := filepath.Join(dataDir, "plans", "2026", "04", "Rendering fixes.agent-1.md")
	assert.Equal(t, canonicalPath, row.PlanFilePath)

	current, err := os.ReadFile(canonicalPath)
	require.NoError(t, err)
	assert.Equal(t, string(v2), string(current))

	// Exactly one archive file should sit beside the canonical one, carrying
	// the v1 content. Its name embeds the timestamp from the second write.
	entries, err := os.ReadDir(filepath.Dir(canonicalPath))
	require.NoError(t, err)
	require.Equal(t, 2, len(entries))
	var archiveName string
	for _, e := range entries {
		if e.Name() != "Rendering fixes.agent-1.md" {
			archiveName = e.Name()
		}
	}
	assert.True(t, strings.HasPrefix(archiveName, "Rendering fixes.agent-1."), "archive name should preserve the original stem: %s", archiveName)
	assert.True(t, strings.HasSuffix(archiveName, ".md"), "archive name should end in .md: %s", archiveName)

	archivedContent, err := os.ReadFile(filepath.Join(filepath.Dir(canonicalPath), archiveName))
	require.NoError(t, err)
	assert.Equal(t, string(v1), string(archivedContent))
}

func TestUpdatePlan_RewriteWithDifferentTitle_ArchivesUnderOldNameAndWritesNew(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "Original Title", []byte("# Original Title\n"))

	h.now = func() time.Time { return time.Date(2026, time.April, 14, 10, 0, 0, 0, time.UTC) }
	updatePlanHelper(t, h, "agent-1", "Renamed", []byte("# Renamed\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)

	expectedNewPath := filepath.Join(dataDir, "plans", "2026", "04", "Renamed.agent-1.md")
	assert.Equal(t, expectedNewPath, row.PlanFilePath)
	assert.Equal(t, "Renamed", row.PlanTitle)

	entries, err := os.ReadDir(filepath.Dir(expectedNewPath))
	require.NoError(t, err)
	require.Equal(t, 2, len(entries))
	var archiveName string
	for _, e := range entries {
		if e.Name() != "Renamed.agent-1.md" {
			archiveName = e.Name()
		}
	}
	assert.True(t, strings.HasPrefix(archiveName, "Original Title.agent-1."), "archive should keep the prior title in its name: %s", archiveName)
}

func TestUpdatePlan_FirstWriteWins_DirectoryStaysWithFirstMonth(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "Rendering fixes", []byte("# Rendering fixes\n"))

	// Cross a month boundary on the second write. Versions stay together
	// in the original month directory rather than fragmenting.
	h.now = func() time.Time { return time.Date(2026, time.May, 3, 9, 0, 0, 0, time.UTC) }
	updatePlanHelper(t, h, "agent-1", "Rendering fixes", []byte("# Rendering fixes\n\n- new line\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)

	wantDir := filepath.Join(dataDir, "plans", "2026", "04")
	assert.Equal(t, wantDir, filepath.Dir(row.PlanFilePath))

	entries, err := os.ReadDir(wantDir)
	require.NoError(t, err)
	assert.Equal(t, 2, len(entries), "v1 archive and v2 canonical should both live in the original month directory")

	mayDir := filepath.Join(dataDir, "plans", "2026", "05")
	_, err = os.Stat(mayDir)
	assert.True(t, os.IsNotExist(err), "May directory should not exist for plan that started in April")
}

func TestUpdatePlan_EmptyTitleFallsBackToUntitled(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "", []byte("- item\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dataDir, "plans", "2026", "04", "Untitled Plan.agent-1.md"), row.PlanFilePath)
}

func TestUpdatePlan_TwoAgentsSameTitleDoNotCollide(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-A", "Agent Alice")
	createTestAgent(t, queries, "agent-B", "Agent Bob")

	updatePlanHelper(t, h, "agent-A", "Shared Title", []byte("agent A content"))
	updatePlanHelper(t, h, "agent-B", "Shared Title", []byte("agent B content"))

	rowA, err := queries.GetAgentByID(context.Background(), "agent-A")
	require.NoError(t, err)
	rowB, err := queries.GetAgentByID(context.Background(), "agent-B")
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dataDir, "plans", "2026", "04", "Shared Title.agent-A.md"), rowA.PlanFilePath)
	assert.Equal(t, filepath.Join(dataDir, "plans", "2026", "04", "Shared Title.agent-B.md"), rowB.PlanFilePath)

	contentA, err := os.ReadFile(rowA.PlanFilePath)
	require.NoError(t, err)
	contentB, err := os.ReadFile(rowB.PlanFilePath)
	require.NoError(t, err)
	assert.Equal(t, "agent A content", string(contentA))
	assert.Equal(t, "agent B content", string(contentB))
}

func TestUpdatePlan_NotificationEmittedWithoutAutoRename(t *testing.T) {
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "User Set Title")

	updatePlanHelper(t, h, "agent-1", "Plan Title", []byte("# Plan Title\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)

	notifs := findNotificationsByType(readAllNotifications(t, queries, "agent-1"), "plan_updated")
	require.Equal(t, 1, len(notifs), "exactly one plan_updated notification expected")
	assert.Equal(t, "Plan Title", notifs[0]["plan_title"])
	assert.Equal(t, row.PlanFilePath, notifs[0]["plan_file_path"])
	_, hasFlag := notifs[0]["update_agent_title"]
	assert.False(t, hasFlag, "update_agent_title must be omitted when auto-rename did not fire")
}

func TestUpdatePlan_NotificationEmittedWithUpdateAgentTitleFlag(t *testing.T) {
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "Auth Refactor", []byte("# Auth Refactor\n"))

	notifs := findNotificationsByType(readAllNotifications(t, queries, "agent-1"), "plan_updated")
	require.Equal(t, 1, len(notifs))
	assert.Equal(t, "Auth Refactor", notifs[0]["plan_title"])
	assert.Equal(t, true, notifs[0]["update_agent_title"], "auto-rename branch must set update_agent_title:true")
}

func TestUpdatePlan_ContentOnlyChange_DoesNotEmitNotification(t *testing.T) {
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "My Plan", []byte("# My Plan\n\n- step 1\n"))
	beforeCount := len(findNotificationsByType(readAllNotifications(t, queries, "agent-1"), "plan_updated"))

	// Same title (extracted from same first line), different body.
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 30, 0, 0, time.UTC) }
	updatePlanHelper(t, h, "agent-1", "My Plan", []byte("# My Plan\n\n- step 1\n- step 2\n"))

	afterCount := len(findNotificationsByType(readAllNotifications(t, queries, "agent-1"), "plan_updated"))
	assert.Equal(t, beforeCount, afterCount, "content-only changes must not emit additional plan_updated notifications")
}

func TestUpdatePlan_EmptyContentReturnsEarlyWithoutDBOrFileWrites(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	// Empty compressed payload — caller had nothing to materialize.
	h.updatePlan("agent-1", nil, 0, "Some Title")

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "", row.PlanFilePath, "row must not be updated when content is empty")
	assert.Equal(t, "", row.PlanTitle)

	_, err = os.Stat(filepath.Join(dataDir, "plans"))
	assert.True(t, os.IsNotExist(err), "plans directory must not be created")
	notifs := findNotificationsByType(readAllNotifications(t, queries, "agent-1"), "plan_updated")
	assert.Empty(t, notifs)
}

func TestUpdatePlan_EmptyTitlePreservesExistingPlanTitle(t *testing.T) {
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	// Seed: write a plan so PlanTitle is populated.
	updatePlanHelper(t, h, "agent-1", "First Title", []byte("# First Title\n"))
	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	require.Equal(t, "First Title", row.PlanTitle)

	// Second call with title="" — preserve the prior plan_title.
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 30, 0, 0, time.UTC) }
	updatePlanHelper(t, h, "agent-1", "", []byte("# Different first line\n"))

	row, err = queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "First Title", row.PlanTitle, "empty title from caller must preserve existing plan_title")
}

func TestUpdatePlan_StalePriorPathDoesNotCrashRecovery(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	// Manually point the agent at a non-existent prior path. This simulates
	// the recovery scenario where the previous write succeeded in DB but the
	// file was later deleted/corrupted out of band.
	stale := filepath.Join(dataDir, "plans", "2026", "04", "stale.agent-1.md")
	require.NoError(t, queries.UpdateAgentPlan(context.Background(), db.UpdateAgentPlanParams{
		PlanFilePath: stale,
		PlanTitle:    "stale",
		ID:           "agent-1",
	}))

	updatePlanHelper(t, h, "agent-1", "Recovered", []byte("# Recovered\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	want := filepath.Join(dataDir, "plans", "2026", "04", "Recovered.agent-1.md")
	assert.Equal(t, want, row.PlanFilePath)
	got, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, "# Recovered\n", string(got))

	// Stale path must NOT have been recreated as an archive — there was
	// nothing to archive.
	_, err = os.Stat(stale)
	assert.True(t, os.IsNotExist(err))
}

func TestUpdatePlan_ArchiveCollisionAppendsCounterSuffix(t *testing.T) {
	h, queries, dataDir := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	// Two updates within the same millisecond — archive timestamps collide,
	// so the second archive must fall back to a counter suffix.
	updatePlanHelper(t, h, "agent-1", "Plan", []byte("v1\n"))
	updatePlanHelper(t, h, "agent-1", "Plan", []byte("v2\n"))
	updatePlanHelper(t, h, "agent-1", "Plan", []byte("v3\n"))

	dir := filepath.Join(dataDir, "plans", "2026", "04")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, 3, len(entries), "one canonical + two archives expected")

	canonicalCount := 0
	archiveCount := 0
	for _, e := range entries {
		if e.Name() == "Plan.agent-1.md" {
			canonicalCount++
		} else if strings.HasPrefix(e.Name(), "Plan.agent-1.") && strings.HasSuffix(e.Name(), ".md") {
			archiveCount++
		}
	}
	assert.Equal(t, 1, canonicalCount)
	assert.Equal(t, 2, archiveCount)
}

func TestUpdatePlan_AutoRename_TitleEqualsPlanTitle(t *testing.T) {
	// After a prior auto-rename, agentRow.Title == agentRow.PlanTitle. The
	// next plan-title change should auto-rename again (the user has not
	// manually set the tab title since the last plan).
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "First", []byte("# First\n"))
	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	require.Equal(t, "First", row.Title, "first call should auto-rename via placeholder branch")
	require.Equal(t, "First", row.PlanTitle)

	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 30, 0, 0, time.UTC) }
	updatePlanHelper(t, h, "agent-1", "Second", []byte("# Second\n"))
	row, err = queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "Second", row.Title, "second call should auto-rename via Title==PlanTitle branch")
	assert.Equal(t, "Second", row.PlanTitle)
}

func TestUpdatePlan_AutoRename_RespectsManualOverride(t *testing.T) {
	// Once the user manually renames the tab to something other than
	// PlanTitle, subsequent plan-title changes must NOT clobber it.
	h, queries, _ := newPlanHandler(t)
	createTestAgent(t, queries, "agent-1", "Agent Olivia")

	updatePlanHelper(t, h, "agent-1", "First", []byte("# First\n"))
	// Simulate manual rename.
	_, err := queries.RenameAgent(context.Background(), db.RenameAgentParams{
		Title: "User Set Title",
		ID:    "agent-1",
	})
	require.NoError(t, err)

	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 30, 0, 0, time.UTC) }
	updatePlanHelper(t, h, "agent-1", "Second", []byte("# Second\n"))

	row, err := queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "User Set Title", row.Title, "manual override must survive plan-title changes")
	assert.Equal(t, "Second", row.PlanTitle)

	notifs := findNotificationsByType(readAllNotifications(t, queries, "agent-1"), "plan_updated")
	require.GreaterOrEqual(t, len(notifs), 1)
	last := notifs[len(notifs)-1]
	_, hasFlag := last["update_agent_title"]
	assert.False(t, hasFlag, "update_agent_title must be omitted when auto-rename was suppressed")
}
