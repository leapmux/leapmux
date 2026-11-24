package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	defaultAgentSearchLimit     = 50
	maxAgentSearchLimit         = 200
	agentSearchPageSize         = 200
	maxMessagesScannedPerAgent  = 5000
	maxTotalMessagesScanned     = 50_000
	maxAgentSearchIDs           = 500
	agentSearchTimeout          = 30 * time.Second
	agentSearchSnippetMaxRunes  = 200
	agentSearchSnippetHalfRunes = 80
)

func registerAgentSearchHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, dispatchPlain, svc.handleSearchAgentMessages)
}

type searchAgentRow struct {
	id       string
	title    string
	provider leapmuxv1.AgentProvider
}

func (svc *Service) handleSearchAgentMessages(ctx context.Context, _ channel.Caller, r *leapmuxv1.SearchAgentMessagesRequest, sender channel.ResponseWriter) {
	ctx, cancel := context.WithTimeout(ctx, agentSearchTimeout)
	defer cancel()
	if len(r.GetAgentIds()) > maxAgentSearchIDs {
		sendInvalidArgument(sender, fmt.Sprintf("too many agent_ids (maximum %d)", maxAgentSearchIDs))
		return
	}
	query := strings.TrimSpace(r.GetQuery())
	if query == "" {
		sendInvalidArgument(sender, "query is required")
		return
	}
	if r.GetLimit() < 0 {
		sendInvalidArgument(sender, "limit must not be negative")
		return
	}
	limit := int(r.GetLimit())
	if limit == 0 {
		limit = defaultAgentSearchLimit
	}
	if limit > maxAgentSearchLimit {
		limit = maxAgentSearchLimit
	}

	var findMatch func(string) (int, int, bool)
	if r.GetRegex() {
		expr := query
		if !r.GetCaseSensitive() {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			sendInvalidArgument(sender, fmt.Sprintf("invalid regular expression: %v", err))
			return
		}
		findMatch = func(text string) (int, int, bool) {
			loc := re.FindStringIndex(text)
			if loc == nil {
				return 0, 0, false
			}
			return loc[0], loc[1], true
		}
	} else if r.GetCaseSensitive() {
		findMatch = func(text string) (int, int, bool) {
			idx := strings.Index(text, query)
			if idx < 0 {
				return 0, 0, false
			}
			return idx, idx + len(query), true
		}
	} else {
		// Use RE2 (?i) with QuoteMeta so matched byte offsets ALWAYS correspond
		// to the original string even under UTF-8 case-folding length changes
		// (e.g. 'İ' U+0130 or 'K' U+212A).
		re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
		if err != nil {
			sendInvalidArgument(sender, fmt.Sprintf("invalid query: %v", err))
			return
		}
		findMatch = func(text string) (int, int, bool) {
			loc := re.FindStringIndex(text)
			if loc == nil {
				return 0, 0, false
			}
			return loc[0], loc[1], true
		}
	}

	var since time.Time
	hasSince := false
	if raw := strings.TrimSpace(r.GetSince()); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			sendInvalidArgument(sender, "since must be an RFC 3339 timestamp")
			return
		}
		since = parsed.UTC()
		hasSince = true
	}

	agents, err := svc.listSearchableAgents(ctx, r.GetAgentIds(), r.GetProvider())
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Error("failed to list agents for transcript search", "error", err)
		sendInternalError(sender, "failed to list agents for search")
		return
	}

	var hits []*leapmuxv1.AgentMessageSearchHit
	truncated := false
	totalScanned := 0

	// Scan each agent up to limit newest matches (bounded by a global scan
	// budget) so a newly created agent cannot starve older agents, trimming
	// hits periodically so memory stays O(limit).
	for _, ag := range agents {
		if ctx.Err() != nil {
			return
		}
		if totalScanned >= maxTotalMessagesScanned {
			truncated = true
			break
		}
		scanned := 0
		agentHits := 0
		var cursorSeq int64
		firstPage := true
		for scanned < maxMessagesScannedPerAgent && totalScanned < maxTotalMessagesScanned {
			if ctx.Err() != nil {
				return
			}
			var rows []db.Message
			var qerr error
			if firstPage {
				rows, qerr = svc.Queries.ListLatestMessagesByAgentID(ctx, db.ListLatestMessagesByAgentIDParams{
					AgentID: ag.id,
					Limit:   agentSearchPageSize,
				})
				firstPage = false
			} else {
				rows, qerr = svc.Queries.ListMessagesByAgentIDReverse(ctx, db.ListMessagesByAgentIDReverseParams{
					AgentID: ag.id,
					Seq:     cursorSeq,
					Limit:   agentSearchPageSize,
				})
			}
			if qerr != nil {
				slog.Warn("failed to scan agent messages for search", "agent_id", ag.id, "error", qerr)
				break
			}
			if len(rows) == 0 {
				break
			}
			reachedSinceCutoff := false
			reachedAgentCap := false
			for i := range rows {
				if totalScanned >= maxTotalMessagesScanned {
					truncated = true
					reachedAgentCap = true
					break
				}
				m := &rows[i]
				scanned++
				totalScanned++
				cursorSeq = m.Seq
				if hasSince && !m.CreatedAt.IsZero() && m.CreatedAt.Before(since) {
					reachedSinceCutoff = true
					break
				}
				searchable := svc.searchableMessageText(m, ag.provider)
				if searchable == "" {
					continue
				}
				start, end, matched := findMatch(searchable)
				if !matched {
					continue
				}
				if agentHits >= limit {
					truncated = true
					reachedAgentCap = true
					break
				}
				createdAt := ""
				if !m.CreatedAt.IsZero() {
					createdAt = timefmt.Format(m.CreatedAt.Time)
				}
				hits = append(hits, &leapmuxv1.AgentMessageSearchHit{
					AgentId:    ag.id,
					AgentTitle: ag.title,
					Provider:   ag.provider,
					Seq:        m.Seq,
					Source:     m.Source,
					Snippet:    buildSearchSnippet(searchable, start, end),
					CreatedAt:  createdAt,
				})
				agentHits++
			}
			if reachedAgentCap || reachedSinceCutoff || len(rows) < agentSearchPageSize {
				break
			}
			if scanned >= maxMessagesScannedPerAgent {
				truncated = true
				break
			}
		}
		if len(hits) > limit*2 {
			sortAgentSearchHits(hits)
			hits = hits[:limit]
			truncated = true
		}
	}

	sortAgentSearchHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
		truncated = true
	}

	sendProtoResponse(sender, &leapmuxv1.SearchAgentMessagesResponse{
		Hits:      hits,
		Truncated: truncated,
	})
}

func sortAgentSearchHits(hits []*leapmuxv1.AgentMessageSearchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].GetCreatedAt() != hits[j].GetCreatedAt() {
			return hits[i].GetCreatedAt() > hits[j].GetCreatedAt()
		}
		return hits[i].GetSeq() > hits[j].GetSeq()
	})
}

func (svc *Service) listSearchableAgents(ctx context.Context, agentIDs []string, provider leapmuxv1.AgentProvider) ([]searchAgentRow, error) {
	if len(agentIDs) > 0 {
		rows, err := svc.Queries.ListAgentsByIDs(ctx, agentIDs)
		if err != nil {
			return nil, err
		}
		out := make([]searchAgentRow, 0, len(rows))
		for i := range rows {
			if provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED && rows[i].AgentProvider != provider {
				continue
			}
			out = append(out, searchAgentRow{
				id:       rows[i].ID,
				title:    rows[i].Title,
				provider: rows[i].AgentProvider,
			})
		}
		return out, nil
	}
	if svc.DB == nil {
		return nil, nil
	}
	sqlRows, err := svc.DB.QueryContext(ctx, "SELECT id, title, agent_provider FROM agents WHERE closed_at IS NULL ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = sqlRows.Close() }()

	var out []searchAgentRow
	for sqlRows.Next() {
		var row searchAgentRow
		if err := sqlRows.Scan(&row.id, &row.title, &row.provider); err != nil {
			return nil, err
		}
		if provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED && row.provider != provider {
			continue
		}
		out = append(out, row)
	}
	if err := sqlRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (svc *Service) searchableMessageText(m *db.Message, provider leapmuxv1.AgentProvider) string {
	if m.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		if protoMsg, err := messageToProto(m, provider); err == nil {
			if reply := svc.replyTextOf(protoMsg); reply != nil && strings.TrimSpace(*reply) != "" {
				return strings.ToValidUTF8(*reply, "\uFFFD")
			}
		}
	}
	raw, err := msgcodec.Decompress(m.Content, m.ContentCompression)
	if err != nil || len(raw) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(strings.ToValidUTF8(string(raw), "\uFFFD"))
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]any
		if json.Unmarshal(raw, &obj) == nil {
			for _, key := range []string{"text", "message", "content", "prompt"} {
				if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
					return strings.ToValidUTF8(v, "\uFFFD")
				}
			}
			// Avoid matching structural JSON keys on raw agent frames that
			// carried no human-readable reply text (e.g. tool_use/thinking frames).
			if m.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
				return ""
			}
		}
	}
	return trimmed
}

func buildSearchSnippet(text string, matchStart, matchEnd int) string {
	flat := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(flat) <= agentSearchSnippetMaxRunes {
		return flat
	}
	if matchStart < 0 {
		matchStart = 0
	}
	if matchStart > len(text) {
		matchStart = len(text)
	}
	if matchEnd < matchStart {
		matchEnd = matchStart
	}
	if matchEnd > len(text) {
		matchEnd = len(text)
	}
	prefixRunes := utf8.RuneCountInString(text[:matchStart])
	matchRunes := utf8.RuneCountInString(text[matchStart:matchEnd])
	all := []rune(text)
	lo := prefixRunes - agentSearchSnippetHalfRunes
	if lo < 0 {
		lo = 0
	}
	hi := prefixRunes + matchRunes + agentSearchSnippetHalfRunes
	if hi-lo > agentSearchSnippetMaxRunes {
		hi = lo + agentSearchSnippetMaxRunes
	}
	if hi > len(all) {
		hi = len(all)
	}
	snippet := strings.Join(strings.Fields(string(all[lo:hi])), " ")
	if utf8.RuneCountInString(snippet) > agentSearchSnippetMaxRunes {
		snippet = string([]rune(snippet)[:agentSearchSnippetMaxRunes])
		hi = len(all) - 1 // ensure trailing ellipsis
	}
	if lo > 0 {
		snippet = "…" + snippet
	}
	if hi < len(all) {
		snippet += "…"
	}
	return snippet
}
