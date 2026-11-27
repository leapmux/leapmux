package cmd

import (
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
	if !strings.HasPrefix(v, "+") {
		durRaw := strings.TrimPrefix(v, "-")
		if !strings.HasPrefix(durRaw, "+") {
			if d, err := time.ParseDuration(durRaw); err == nil && d > 0 {
				return timefmt.Format(now.Add(-d).UTC()), nil
			}
		}
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
	provider, err := parseProviderFlag(stringFlag(inv, "provider"))
	if err != nil {
		return nil, err
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
	var failures []workerSearchFailure
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
				failures = append(failures, workerSearchFailure{workerID: wid, err: err})
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
					Provider:  agentlabels.CLIAlias(h.GetProvider()),
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
	if len(failures) > 0 {
		sort.Slice(failures, func(i, j int) bool {
			return failures[i].workerID < failures[j].workerID
		})
		if succeeded == 0 {
			return nil, selectSearchFailure(failures)
		}
		if inv.Printer.Format == cli.FormatText {
			labels := make([]string, 0, len(failures))
			for _, f := range failures {
				labels = append(labels, fmt.Sprintf("%s (%v)", cli.ShortID(f.workerID), f.err))
			}
			_ = inv.Printer.Notice("warning: skipped unreachable worker(s): %s", strings.Join(labels, "; "))
		}
	}

	sortAgentSearchHits(hits)
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

type workerSearchFailure struct {
	workerID string
	err      error
}

// selectSearchFailure picks the returned error deterministically when every
// worker in a multi-worker search failed. Failures are ordered by worker ID,
// and client/argument/permission refusals take precedence over offline/transient
// failures so goroutine scheduling never changes the reported error code.
func selectSearchFailure(failures []workerSearchFailure) error {
	ordered := append([]workerSearchFailure(nil), failures...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].workerID < ordered[j].workerID
	})
	for _, f := range ordered {
		code := cli.FromError(f.err).Code
		if code == cli.CodeUsage || code == cli.CodeInvalidArgument || code == cli.CodePermissionDenied {
			return f.err
		}
	}
	return cli.Wrap(cli.CodeWorkerOffline, ordered[0].err, fmt.Sprintf("agent search failed on all %d workers", len(ordered)))
}

// sortAgentSearchHits orders search hits newest first using parsed RFC 3339
// instants, falling back to string order for unparseable timestamps, with
// deterministic tie-breakers so concurrent worker responses produce stable
// pagination under --limit.
func sortAgentSearchHits(hits []agentSearchHit) {
	type parsedHit struct {
		hit   agentSearchHit
		ts    time.Time
		valid bool
	}
	parsed := make([]parsedHit, len(hits))
	for i, h := range hits {
		ts, err := time.Parse(time.RFC3339Nano, h.CreatedAt)
		parsed[i] = parsedHit{hit: h, ts: ts, valid: err == nil}
	}
	sort.Slice(parsed, func(i, j int) bool {
		a, b := parsed[i], parsed[j]
		if a.valid != b.valid {
			return a.valid
		}
		if a.valid {
			if !a.ts.Equal(b.ts) {
				return a.ts.After(b.ts)
			}
		} else if a.hit.CreatedAt != b.hit.CreatedAt {
			return a.hit.CreatedAt > b.hit.CreatedAt
		}
		if a.hit.Seq != b.hit.Seq {
			return a.hit.Seq > b.hit.Seq
		}
		if a.hit.AgentID != b.hit.AgentID {
			return a.hit.AgentID < b.hit.AgentID
		}
		return a.hit.WorkerID < b.hit.WorkerID
	})
	for i, p := range parsed {
		hits[i] = p.hit
	}
}
