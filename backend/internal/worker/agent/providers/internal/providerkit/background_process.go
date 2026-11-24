package providerkit

import (
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const defaultBackgroundProcessTitle = "Background process"

// BackgroundProcessSnapshot describes one provider-normalized background shell
// process entry passed to ReconcileBackgroundProcesses.
type BackgroundProcessSnapshot struct {
	RowKey         string
	Title          string
	TitleIsCommand bool
	Status         bgtask.Status
}

// BackgroundProcessOp is one registry mutation produced by
// ReconcileBackgroundProcesses. When Upsert is non-nil, the caller upserts that
// shell row; otherwise the caller closes RowKey with CloseStatus.
type BackgroundProcessOp struct {
	Upsert      *bgtask.Upsert
	RowKey      string
	CloseStatus bgtask.Status
}

// BackgroundProcessStatus maps a provider-reported status word and optional
// exit code to a registry status.
func BackgroundProcessStatus(status string, exitCode *int) bgtask.Status {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "running", "started", "starting", "active":
		return bgtask.StatusRunning
	case "pending", "queued":
		return bgtask.StatusPending
	case "completed", "succeeded", "exited", "done":
		if exitCode != nil && *exitCode != 0 {
			return bgtask.StatusFailed
		}
		return bgtask.StatusSucceeded
	case "failed", "error":
		return bgtask.StatusFailed
	case "killed", "stopped", "cancelled", "canceled", "terminated":
		return bgtask.StatusStopped
	default:
		return bgtask.StatusRunning
	}
}

// BackgroundProcessTitle chooses a display title from optional description,
// label, and command fields, reporting whether the returned title is a shell
// command.
func BackgroundProcessTitle(description, label, command string) (title string, isCommand bool) {
	if title = strings.TrimSpace(description); title != "" {
		return title, false
	}
	if title = strings.TrimSpace(label); title != "" {
		return title, false
	}
	if title = strings.TrimSpace(command); title != "" {
		return title, true
	}
	return defaultBackgroundProcessTitle, false
}

// ReconcileBackgroundProcesses diffs processes against tracked row keys,
// updates tracked in place, and returns the registry operations to apply:
// snapshot upserts and explicit closes in input order, followed by evicted
// tracked keys in sorted order with StatusEndedWithUnknownOutcome.
func ReconcileBackgroundProcesses(
	tracked *map[string]struct{},
	parentAgentID string,
	processes []BackgroundProcessSnapshot,
) []BackgroundProcessOp {
	var local map[string]struct{}
	if tracked == nil {
		tracked = &local
	}
	if len(*tracked) == 0 && len(processes) == 0 {
		return nil
	}
	if *tracked == nil {
		*tracked = make(map[string]struct{})
	}
	var ops []BackgroundProcessOp
	active := make(map[string]struct{}, len(processes))
	handled := make(map[string]struct{}, len(processes))
	for _, proc := range processes {
		if proc.RowKey == "" {
			continue
		}
		status := proc.Status
		if status == bgtask.StatusUnspecified {
			status = bgtask.StatusRunning
		}
		if status.IsFinished() {
			delete(active, proc.RowKey)
			handled[proc.RowKey] = struct{}{}
			if _, wasTracked := (*tracked)[proc.RowKey]; wasTracked {
				delete(*tracked, proc.RowKey)
				ops = append(ops, BackgroundProcessOp{
					RowKey:      proc.RowKey,
					CloseStatus: status,
				})
			}
			continue
		}
		title := strings.TrimSpace(proc.Title)
		titleIsCommand := proc.TitleIsCommand
		if title == "" {
			title = defaultBackgroundProcessTitle
			titleIsCommand = false
		}
		upsert := bgtask.Upsert{
			RowKey:         proc.RowKey,
			Kind:           bgtask.KindShell,
			ParentAgentID:  parentAgentID,
			Title:          title,
			TitleIsCommand: titleIsCommand,
			Status:         status,
		}
		ops = append(ops, BackgroundProcessOp{Upsert: &upsert})
		active[proc.RowKey] = struct{}{}
		handled[proc.RowKey] = struct{}{}
		(*tracked)[proc.RowKey] = struct{}{}
	}
	var evicted []string
	for rowKey := range *tracked {
		if _, stillActive := active[rowKey]; !stillActive {
			if _, alreadyHandled := handled[rowKey]; !alreadyHandled {
				evicted = append(evicted, rowKey)
			}
		}
	}
	slices.Sort(evicted)
	for _, rowKey := range evicted {
		delete(*tracked, rowKey)
		ops = append(ops, BackgroundProcessOp{
			RowKey:      rowKey,
			CloseStatus: bgtask.StatusEndedWithUnknownOutcome,
		})
	}
	return ops
}
