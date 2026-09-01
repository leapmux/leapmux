package service

import (
	"context"
	"log/slog"
	"slices"
	"strings"

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
// user nothing to scan; the cap is here so the response and the provider-store
// scan stay within a limit. It is deliberately NOT a contract value: no client
// asks for a count, so it is consumed on this side only.
//
// The DATABASE read is deliberately outside that limit. ListSessionsForResume
// carries no LIMIT because the same rows supply the open-handle exclusion set,
// and a cut there would drop an open handle whose last activity falls outside
// the window -- after which the provider's own store re-admits that live handle
// to the picker. What limits that read instead is the (provider, working
// directory) filter, the index that serves it, and the hourly retention sweep,
// which deletes an agent closed more than cleanupRetention ago.
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
			// The SAME default OpenAgent applies. A caller that omits the
			// provider would otherwise be told this directory has nothing to
			// resume, and would then successfully open a Claude Code agent that
			// could have resumed any of the sessions this call hid.
			provider := agent.ProviderOrDefault(r.GetAgentProvider())

			// The two reads touch unrelated stores, so they overlap. The
			// database read is fast; the provider scan walks another program's
			// files and is what the user waits for, so paying the sum of the
			// two rather than the larger of them is latency for nothing on the
			// path of a dialog that opens on every "New agent" click.
			var stored []agent.StoredSession
			storeDone := make(chan struct{})
			go func() {
				defer close(storeDone)
				stored = svc.readProviderSessions(ctx, provider, workingDir)
			}()

			rows, err := svc.Queries.ListSessionsForResume(ctx, db.ListSessionsForResumeParams{
				AgentProvider: provider,
				WorkingDir:    workingDir,
			})
			<-storeDone
			if err != nil {
				slog.Error("failed to list worker sessions for resume",
					"working_dir", workingDir, "provider", provider, "error", err)
				sendInternalError(sender, "failed to list sessions")
				return
			}

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
// LeapMux works: the CLI is not installed, its store moved, its schema changed,
// a lock is held. None of those is a reason to fail a dialog that the worker's
// own database can already answer, so the failure is logged and the caller
// continues with what it knows.
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

// workerRowsAsSessions projects the worker's own records into the shape the
// provider readers already return, so the merge below has ONE record type and
// therefore one loop rather than two that can drift apart.
func workerRowsAsSessions(rows []db.ListSessionsForResumeRow) []agent.StoredSession {
	out := make([]agent.StoredSession, 0, len(rows))
	for i := range rows {
		out = append(out, agent.StoredSession{
			Handle:    rows[i].AgentSessionID,
			Title:     rows[i].Title,
			UpdatedAt: rows[i].LastActivity.Time,
		})
	}
	return out
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
// happen inside the database query. Both the set and the lookups use the
// TRIMMED handle: a key stored one way and read the other never matches, and
// the failure is exactly the corruption this rule prevents.
//
// A handle both sources carry appears ONCE, and the worker's record wins every
// field: it is the record LeapMux itself wrote, and its title is the tab title
// the user chose, which the provider's store does not know. The worker's rows
// come first in the concatenation, and the dedupe keeps the first.
//
// What remains is ordered by last activity, newest first, and capped -- through
// the SAME agent.SortAndCapSessions the provider readers use, so the response
// cannot order its rows by a different rule than the records arrived in.
func mergeSessionSummaries(rows []db.ListSessionsForResumeRow, stored []agent.StoredSession, limit int) []*leapmuxv1.AgentSessionSummary {
	open := make(map[string]struct{}, len(rows))
	for i := range rows {
		if rows[i].ClosedAt.Valid {
			continue
		}
		if handle := strings.TrimSpace(rows[i].AgentSessionID); handle != "" {
			open[handle] = struct{}{}
		}
	}

	candidates := slices.Concat(workerRowsAsSessions(rows), stored)
	merged := make([]agent.StoredSession, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		handle := strings.TrimSpace(candidate.Handle)
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
		candidate.Handle = handle
		merged = append(merged, candidate)
	}
	merged = agent.SortAndCapSessions(merged, limit)

	out := make([]*leapmuxv1.AgentSessionSummary, 0, len(merged))
	for _, e := range merged {
		summary := &leapmuxv1.AgentSessionSummary{
			SessionId: e.Handle,
			Title:     e.Title,
		}
		// An empty string, not the zero instant formatted: a client that reads
		// "0001-01-01T00:00:00Z" as a date would render a session from the year
		// one, where the empty value states plainly that the store recorded no
		// time.
		if !e.UpdatedAt.IsZero() {
			summary.UpdatedAt = timefmt.Format(e.UpdatedAt)
		}
		out = append(out, summary)
	}
	return out
}
