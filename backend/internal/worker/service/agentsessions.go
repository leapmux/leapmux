package service

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/validate"
)

// maxListedSessions caps the ListAgentSessions response.
//
// The client shows the list behind a filter box, so a longer list costs the
// user nothing to scan; the cap is here so the response, the provider-store
// scan and the database read all stay bounded. It is deliberately NOT a
// contract value: no client asks for a count, so it is consumed on this side
// only.
const maxListedSessions = 50

// registerAgentSessionHandlers registers the resumable-session listing.
//
// OWNER-ONLY, for the reason ListAvailableProviders is: the answer describes
// machine-scoped state -- which sessions exist on this host, and what they are
// called -- so a non-owner channel, notably a delegation bearer minted for a
// different user, must not reach it.
func registerAgentSessionHandlers(d registrar, svc *Service) {
	registerOwnerGated(d, "ListAgentSessions", leapmuxv1.Scope_SCOPE_AGENT_READ, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, r *leapmuxv1.ListAgentSessionsRequest, sender channel.ResponseWriter) {
			workingDir, err := validate.SanitizePath(r.GetWorkingDir(), svc.HomeDir)
			if err != nil {
				sendInvalidArgument(sender, err.Error())
				return
			}

			rows, err := svc.Queries.ListSessionsForResume(ctx, db.ListSessionsForResumeParams{
				AgentProvider: r.GetAgentProvider(),
				WorkingDir:    workingDir,
			})
			if err != nil {
				slog.Error("failed to list worker sessions for resume",
					"working_dir", workingDir, "provider", r.GetAgentProvider(), "error", err)
				sendInternalError(sender, "failed to list sessions")
				return
			}

			stored := svc.readProviderSessions(ctx, r.GetAgentProvider(), workingDir)

			sendProtoResponse(sender, &leapmuxv1.ListAgentSessionsResponse{
				Sessions: mergeSessionSummaries(rows, stored, maxListedSessions),
			})
		})
}

// readProviderSessions asks the provider for the sessions its OWN storage
// holds, and turns every failure into the empty list.
//
// A provider store is another program's data on a disk this worker does not
// control, so a read of it fails for reasons that say nothing about whether
// LeapMux is working: the CLI is not installed, its store moved, its schema
// changed, a lock is held. None of those is a reason to fail a dialog that the
// worker's own database can already answer, so the failure is logged and the
// caller continues with what it knows.
func (svc *Service) readProviderSessions(ctx context.Context, provider leapmuxv1.AgentProvider, workingDir string) []agent.StoredSession {
	// Derived from the inbound context, so dismissing the dialog cancels a scan
	// in flight. The API timeout rather than the startup one: reading a session
	// store is file and index work, and must never take as long as an agent
	// handshake.
	ctx, cancel := context.WithTimeout(ctx, svc.agentAPITimeout())
	defer cancel()

	sessions, err := agent.ProviderFor(provider).ListStoredSessions(ctx, agent.StoredSessionQuery{
		WorkingDir: workingDir,
		HomeDir:    svc.HomeDir,
		Limit:      maxListedSessions,
	})
	if err != nil {
		slog.Warn("failed to read provider session store",
			"provider", provider, "working_dir", workingDir, "error", err)
		return nil
	}
	return sessions
}

// mergeSessionSummaries combines the worker's own records with the provider
// store's into the response list.
//
// Three rules, in this order.
//
// An OPEN session is excluded, and excluded from BOTH sources. A live process
// is attached to that handle, and resuming it into a second tab would run two
// processes against one session store -- which corrupts it for Claude
// (`--resume`) and for every ACP provider (`session/load`). The provider store
// lists that session too, so the exclusion has to survive the merge rather than
// happen inside the database query.
//
// A handle both sources carry appears ONCE, and the worker's record wins every
// field: it is the record LeapMux itself wrote, and its title is the tab title
// the user chose, which the provider's store does not know.
//
// What remains is ordered by last activity, newest first, and capped.
func mergeSessionSummaries(rows []db.ListSessionsForResumeRow, stored []agent.StoredSession, limit int) []*leapmuxv1.AgentSessionSummary {
	open := make(map[string]struct{}, len(rows))
	for i := range rows {
		if rows[i].ClosedAt.Valid {
			continue
		}
		open[rows[i].AgentSessionID] = struct{}{}
	}

	type entry struct {
		handle  string
		title   string
		updated time.Time
	}
	merged := make([]entry, 0, len(rows)+len(stored))
	seen := make(map[string]struct{}, len(rows)+len(stored))

	// The worker's records first, so the dedupe below keeps them over the
	// store's.
	for i := range rows {
		handle := strings.TrimSpace(rows[i].AgentSessionID)
		if handle == "" {
			continue
		}
		if _, live := open[handle]; live {
			continue
		}
		if _, dup := seen[handle]; dup {
			continue
		}
		seen[handle] = struct{}{}
		merged = append(merged, entry{
			handle:  handle,
			title:   rows[i].Title,
			updated: rows[i].LastActivity.Time,
		})
	}
	for _, s := range stored {
		handle := strings.TrimSpace(s.Handle)
		if handle == "" {
			continue
		}
		if _, live := open[handle]; live {
			continue
		}
		if _, dup := seen[handle]; dup {
			continue
		}
		seen[handle] = struct{}{}
		merged = append(merged, entry{
			handle:  handle,
			title:   s.Title,
			updated: s.UpdatedAt,
		})
	}

	// The handle breaks a timestamp tie, so two sessions recorded in the same
	// millisecond keep one order across calls rather than whichever the map
	// iteration produced.
	sort.SliceStable(merged, func(i, j int) bool {
		if !merged[i].updated.Equal(merged[j].updated) {
			return merged[i].updated.After(merged[j].updated)
		}
		return merged[i].handle < merged[j].handle
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}

	out := make([]*leapmuxv1.AgentSessionSummary, 0, len(merged))
	for _, e := range merged {
		summary := &leapmuxv1.AgentSessionSummary{
			SessionId: e.handle,
			Title:     e.title,
		}
		// An empty string, not the zero instant formatted: a client that reads
		// "0001-01-01T00:00:00Z" as a date would render a session from the year
		// one, where the empty value states plainly that the store recorded no
		// time.
		if !e.updated.IsZero() {
			summary.UpdatedAt = timefmt.Format(e.updated)
		}
		out = append(out, summary)
	}
	return out
}
