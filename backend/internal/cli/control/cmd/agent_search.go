package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

func newAgentSearchCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "search <query>",
		Short: "Search agent transcripts locally on workers over E2EE",
		Long: `Search agent conversation transcripts across one or more workers.

Plaintext transcripts stay on each worker and are searched locally over E2EE;
the hub never sees transcript contents. Omit --worker and --agent to fan out
across every online worker in parallel.`,
		Examples: []cli.Example{
			{Command: `leapmux agent search "race condition"`},
			{Command: `leapmux agent search "v[0-9]+\.[0-9]+" --regex --provider claude --since 24h`},
			{Command: `leapmux agent search "timeout" --agent 87a19d5f --limit 20`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("agent", "", `search only this agent (id, prefix, title, or "self")`)
			fs.String("provider", "", "search only agents using this provider (such as claude, codex)")
			fs.String("since", "", "include only messages at or after this RFC 3339 time or lookback duration (such as 24h, 30m)")
			fs.String("workspace", "", "search only agents belonging to this workspace")
			addWorkerFlag(fs)
			for _, name := range []string{"agent", "provider", "since", "workspace", "worker"} {
				cli.SetFlagGroup(fs, name, "Filter")
			}
			fs.Bool("regex", false, "treat <query> as an RE2 regular expression")
			fs.Bool("case-sensitive", false, "match case-sensitively (default is case-insensitive)")
			fs.Int("limit", 50, "maximum number of hits to return (max 200)")
			for _, name := range []string{"regex", "case-sensitive", "limit"} {
				cli.SetFlagGroup(fs, name, "Match")
			}
		},
		Run: runAgentSearch,
	})
}

type agentSearchHit struct {
	AgentID   string `json:"agent_id" table:"AGENT,short"`
	Title     string `json:"title,omitempty" table:"TITLE"`
	Provider  string `json:"provider,omitempty" table:"PROVIDER"`
	WorkerID  string `json:"worker_id,omitempty"`
	Seq       int64  `json:"seq" table:"SEQ"`
	Source    string `json:"source" table:"SOURCE"`
	CreatedAt string `json:"created_at,omitempty" table:"TIME,dim"`
	Snippet   string `json:"snippet" table:"SNIPPET"`
}

type agentSearchResult struct {
	Query     string           `json:"query"`
	Hits      []agentSearchHit `json:"hits"`
	Truncated bool             `json:"truncated,omitempty"`
}

func (r agentSearchResult) QuietLines() []string {
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, fmt.Sprintf("%s:%d", h.AgentID, h.Seq))
	}
	return out
}

func parseSinceFlag(raw string, now time.Time) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return timefmt.Format(t.UTC()), nil
	}
	durRaw := strings.TrimPrefix(v, "-")
	if d, err := time.ParseDuration(durRaw); err == nil && d > 0 {
		return timefmt.Format(now.Add(-d).UTC()), nil
	}
	return "", cli.Usagef("--since %q must be an RFC 3339 timestamp or positive duration (such as 24h or 30m)", raw)
}

func runAgentSearch(inv *cli.Invocation) (any, error) {
	query := strings.TrimSpace(inv.Arg("query"))
	if query == "" {
		return nil, cli.Usagef("query is empty")
	}
	limit, err := inv.Command.Flags().GetInt("limit")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		return nil, cli.Usagef("--limit must be between 1 and 200")
	}
	since, err := parseSinceFlag(stringFlag(inv, "since"), time.Now())
	if err != nil {
		return nil, err
	}
	var provider leapmuxv1.AgentProvider
	if raw := strings.TrimSpace(stringFlag(inv, "provider")); raw != "" {
		p, ok := agentlabels.ParseProvider(raw)
		if !ok {
			return nil, cli.Usagef("unknown --provider %q", raw).
				WithHint("accepted values: %s", strings.Join(allProviderAliases(), ", "))
		}
		provider = p
	}
	regexMode := boolFlag(inv, "regex")
	caseSensitive := boolFlag(inv, "case-sensitive")

	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	// Build map of workerID -> optional agentIDs filter.
	targets := map[string][]string{}
	switch {
	case inv.Changed("agent"):
		got, err := s.resolveTab(resolve.KindAgent, stringFlag(inv, "agent"), resolve.Need{WorkerID: true})
		if err != nil {
			return nil, err
		}
		if inv.Changed(workerFlag) {
			w, err := s.resolveOne(resolve.KindWorker, stringFlag(inv, workerFlag))
			if err != nil {
				return nil, err
			}
			if got.WorkerID != w.ID {
				return agentSearchResult{Query: query, Hits: []agentSearchHit{}}, nil
			}
		}
		targets[got.WorkerID] = []string{got.ID}
	case inv.Changed("workspace"):
		tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_AGENT)
		if err != nil {
			return nil, err
		}
		filterWorker := ""
		if inv.Changed(workerFlag) {
			w, err := s.resolveOne(resolve.KindWorker, stringFlag(inv, workerFlag))
			if err != nil {
				return nil, err
			}
			filterWorker = w.ID
		}
		for _, t := range tabs {
			wid := t.GetWorkerId()
			if wid == "" || (filterWorker != "" && wid != filterWorker) {
				continue
			}
			targets[wid] = append(targets[wid], t.GetTab().GetTabId())
		}
	case inv.Changed(workerFlag):
		wid, err := s.workerScope()
		if err != nil {
			return nil, err
		}
		targets[wid] = nil
	default:
		// Fan out across all online workers (paginated), falling back to the
		// local IPC worker only if listing workers is unavailable.
		workers, err := listAllWorkers(s.ctx, s.c)
		if err != nil {
			if _, isIPC := s.c.(*control.WorkerIPCClient); isIPC {
				if wid, werr := s.workerScope(); werr == nil && wid != "" {
					targets[wid] = nil
				}
			}
			if len(targets) == 0 {
				return nil, err
			}
		} else {
			for _, w := range workers {
				if w.GetOnline() {
					targets[w.GetId()] = nil
				}
			}
		}
	}

	if len(targets) == 0 {
		return agentSearchResult{Query: query, Hits: []agentSearchHit{}}, nil
	}

	var mu sync.Mutex
	var hits []agentSearchHit
	var failedWorkers []string
	var lastErr error
	succeeded := 0
	truncated := false
	g, gctx := errgroup.WithContext(s.ctx)
	g.SetLimit(detailFanoutLimit)

	for wid, agentIDs := range targets {
		wid, agentIDs := wid, agentIDs
		g.Go(func() error {
			req := &leapmuxv1.SearchAgentMessagesRequest{
				Query:         query,
				AgentIds:      agentIDs,
				Provider:      provider,
				Since:         since,
				Limit:         clampInt32(limit),
				Regex:         regexMode,
				CaseSensitive: caseSensitive,
			}
			var resp leapmuxv1.SearchAgentMessagesResponse
			if err := callInnerRPC(gctx, s.c, wid, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, req, &resp); err != nil {
				// Single-worker searches fail immediately; multi-worker fan-out
				// records failures and warns unless every worker fails.
				if len(targets) == 1 {
					return err
				}
				mu.Lock()
				failedWorkers = append(failedWorkers, fmt.Sprintf("%s (%v)", cli.ShortID(wid), err))
				lastErr = err
				mu.Unlock()
				return nil
			}
			mu.Lock()
			succeeded++
			if resp.GetTruncated() {
				truncated = true
			}
			for _, h := range resp.GetHits() {
				hits = append(hits, agentSearchHit{
					AgentID:   h.GetAgentId(),
					Title:     h.GetAgentTitle(),
					Provider:  agentProviderName(h.GetProvider()),
					WorkerID:  wid,
					Seq:       h.GetSeq(),
					Source:    enumToken(h.GetSource()),
					CreatedAt: h.GetCreatedAt(),
					Snippet:   h.GetSnippet(),
				})
			}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if len(failedWorkers) > 0 {
		if succeeded == 0 {
			e := cli.FromError(lastErr)
			if e.Code == cli.CodeUsage || e.Code == cli.CodeInvalidArgument || e.Code == cli.CodePermissionDenied {
				return nil, lastErr
			}
			return nil, cli.Wrap(cli.CodeWorkerOffline, lastErr, fmt.Sprintf("agent search failed on all %d workers", len(failedWorkers)))
		}
		sort.Strings(failedWorkers)
		if inv.Printer.Format == cli.FormatText {
			inv.Printer.Notice("warning: skipped unreachable worker(s): %s", strings.Join(failedWorkers, "; "))
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].CreatedAt != hits[j].CreatedAt {
			return hits[i].CreatedAt > hits[j].CreatedAt
		}
		return hits[i].Seq > hits[j].Seq
	})
	if len(hits) > limit {
		hits = hits[:limit]
		truncated = true
	}
	if hits == nil {
		hits = []agentSearchHit{}
	}
	return agentSearchResult{
		Query:     query,
		Hits:      hits,
		Truncated: truncated,
	}, nil
}

// Keep context import used cleanly for future extensions if needed.
var _ context.Context
