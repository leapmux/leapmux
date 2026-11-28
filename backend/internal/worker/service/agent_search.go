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
	"github.com/leapmux/leapmux/internal/worker/agent"
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
	agentSearchSnippetMaxRunes  = 200
	agentSearchSnippetHalfRunes = 80
)

var agentSearchTimeout = 30 * time.Second

func registerAgentSearchHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, dispatchPlain, svc.handleSearchAgentMessages)
}

func (svc *Service) handleSearchAgentMessages(parentCtx context.Context, _ channel.Caller, r *leapmuxv1.SearchAgentMessagesRequest, sender channel.ResponseWriter) {
	ctx, cancel := context.WithTimeout(parentCtx, agentSearchTimeout)
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
	limit, err := resolveSearchLimit(r.GetLimit(), defaultAgentSearchLimit, maxAgentSearchLimit)
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}

	var findMatch func(string) (int, bool)
	if !r.GetRegex() && r.GetCaseSensitive() {
		findMatch = func(text string) (int, bool) {
			idx := strings.Index(text, query)
			if idx < 0 {
				return 0, false
			}
			return idx, true
		}
	} else {
		// For case-insensitive literal queries, use RE2 (?i) with QuoteMeta so
		// matched byte offsets always correspond to the original string even
		// under UTF-8 case-folding length changes (e.g. 'İ' U+0130 or 'K' U+212A).
		expr := query
		if !r.GetRegex() {
			expr = regexp.QuoteMeta(expr)
		}
		if !r.GetCaseSensitive() {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			sendInvalidArgument(sender, fmt.Sprintf("invalid regular expression: %v", err))
			return
		}
		findMatch = func(text string) (int, bool) {
			loc := re.FindStringIndex(text)
			if loc == nil {
				return 0, false
			}
			return loc[0], true
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
		if sendSearchContextError(parentCtx, ctx, sender, "agent message search timed out") {
			return
		}
		slog.Error("failed to list agents for transcript search", "error", err)
		sendInternalError(sender, "failed to list agents for search")
		return
	}

	var hits []*leapmuxv1.AgentMessageSearchHit
	truncated := false
	totalScanned := 0

	// Scan each agent up to limit newest matches (capped by a global scan
	// budget) so a newly created agent cannot starve older agents, trimming
	// hits periodically so memory stays O(limit).
	for _, ag := range agents {
		if sendSearchContextError(parentCtx, ctx, sender, "agent message search timed out") {
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
			if sendSearchContextError(parentCtx, ctx, sender, "agent message search timed out") {
				return
			}
			var rows []db.Message
			var qerr error
			if firstPage {
				rows, qerr = svc.Queries.ListLatestMessagesByAgentID(ctx, db.ListLatestMessagesByAgentIDParams{
					AgentID: ag.ID,
					Limit:   agentSearchPageSize,
				})
				firstPage = false
			} else {
				rows, qerr = svc.Queries.ListMessagesByAgentIDReverse(ctx, db.ListMessagesByAgentIDReverseParams{
					AgentID: ag.ID,
					Seq:     cursorSeq,
					Limit:   agentSearchPageSize,
				})
			}
			if qerr != nil {
				if sendSearchContextError(parentCtx, ctx, sender, "agent message search timed out") {
					return
				}
				slog.Error("failed to scan agent messages for search", "agent_id", ag.ID, "error", qerr)
				sendInternalError(sender, "failed to search agent messages")
				return
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
				searchable := svc.searchableMessageText(m, ag.AgentProvider)
				if searchable == "" {
					continue
				}
				start, matched := findMatch(searchable)
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
					AgentId:    ag.ID,
					AgentTitle: ag.Title,
					Provider:   ag.AgentProvider,
					Seq:        m.Seq,
					Source:     m.Source,
					Snippet:    buildSearchSnippet(searchable, start),
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

	if sendSearchContextError(parentCtx, ctx, sender, "agent message search timed out") {
		return
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

func (svc *Service) listSearchableAgents(ctx context.Context, agentIDs []string, provider leapmuxv1.AgentProvider) ([]db.ListOpenAgentsForSearchRow, error) {
	if svc.Queries == nil {
		return nil, nil
	}
	var all []db.ListOpenAgentsForSearchRow
	if len(agentIDs) > 0 {
		rows, err := svc.Queries.ListOpenAgentsForSearchByIDs(ctx, agentIDs)
		if err != nil {
			return nil, err
		}
		all = make([]db.ListOpenAgentsForSearchRow, len(rows))
		for i, row := range rows {
			all[i] = db.ListOpenAgentsForSearchRow(row)
		}
	} else {
		rows, err := svc.Queries.ListOpenAgentsForSearch(ctx)
		if err != nil {
			return nil, err
		}
		all = rows
	}
	if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return all, nil
	}
	out := make([]db.ListOpenAgentsForSearchRow, 0, len(all))
	for _, row := range all {
		if row.AgentProvider == provider {
			out = append(out, row)
		}
	}
	return out, nil
}

// searchableMessageText extracts searchable text by message source without
// guessing provider wire shapes: agent rows use Provider.ReplyText exclusively,
// and user rows use the Worker's {"content": ...} envelope (or plain text).
func (svc *Service) searchableMessageText(m *db.Message, provider leapmuxv1.AgentProvider) string {
	switch m.Source {
	case leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT:
		if svc.Agents == nil {
			return ""
		}
		content, err := decodeStoredRow(*m, "agent search message")
		if err != nil {
			return ""
		}
		reply, ok := agent.ReplyText(svc.Agents.Registry().Plugin(provider), content)
		if !ok || strings.TrimSpace(reply) == "" {
			return ""
		}
		return strings.ToValidUTF8(reply, "\uFFFD")

	case leapmuxv1.MessageSource_MESSAGE_SOURCE_USER:
		raw, err := msgcodec.Decompress(m.Content, m.ContentCompression)
		if err != nil || len(raw) == 0 {
			return ""
		}
		trimmed := strings.TrimSpace(strings.ToValidUTF8(string(raw), "\uFFFD"))
		if trimmed == "" {
			return ""
		}
		if strings.HasPrefix(trimmed, "{") {
			var envelope struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(raw, &envelope) == nil {
				if strings.TrimSpace(envelope.Content) == "" {
					return ""
				}
				return strings.ToValidUTF8(envelope.Content, "\uFFFD")
			}
		}
		return trimmed

	default:
		return ""
	}
}

func buildSearchSnippet(text string, matchStart int) string {
	// Fast check: scan at most agentSearchSnippetMaxRunes+1 runes before
	// deciding whether the entire message fits without windowing.
	if exceedsRuneCount(text, agentSearchSnippetMaxRunes) {
		return buildWindowedSearchSnippet(text, matchStart)
	}
	return strings.Join(strings.Fields(text), " ")
}

func exceedsRuneCount(s string, maxRunes int) bool {
	count := 0
	for range s {
		count++
		if count > maxRunes {
			return true
		}
	}
	return false
}

func buildWindowedSearchSnippet(text string, matchStart int) string {
	if matchStart < 0 {
		matchStart = 0
	}
	if matchStart > len(text) {
		matchStart = len(text)
	}
	// Walk backward at most agentSearchSnippetHalfRunes runes from matchStart.
	lo := matchStart
	for runes := 0; lo > 0 && runes < agentSearchSnippetHalfRunes; runes++ {
		_, size := utf8.DecodeLastRuneInString(text[:lo])
		lo -= size
	}
	// Walk forward from lo up to agentSearchSnippetMaxRunes runes.
	hi := lo
	for runes := 0; hi < len(text) && runes < agentSearchSnippetMaxRunes; runes++ {
		_, size := utf8.DecodeRuneInString(text[hi:])
		hi += size
	}
	snippet := strings.Join(strings.Fields(text[lo:hi]), " ")
	truncatedEnd := hi < len(text)
	if exceedsRuneCount(snippet, agentSearchSnippetMaxRunes) {
		snippet = sliceRunes(snippet, agentSearchSnippetMaxRunes)
		truncatedEnd = true
	}
	if lo > 0 {
		snippet = "…" + snippet
	}
	if truncatedEnd {
		snippet += "…"
	}
	return snippet
}

func sliceRunes(s string, maxRunes int) string {
	idx := 0
	for runes := 0; idx < len(s) && runes < maxRunes; runes++ {
		_, size := utf8.DecodeRuneInString(s[idx:])
		idx += size
	}
	return s[:idx]
}
