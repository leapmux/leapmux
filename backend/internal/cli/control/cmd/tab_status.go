package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// tabStatusSetLong is the help of `tab status set`.
const tabStatusSetLong = `Show a short status beside a tab's title, in the tab bar and in the sidebar of
every LeapMux window that you have open. A window that you open later shows it
too. The most recently set status is visible.

Give each reporter a --key to keep its status separate. Setting a key again
replaces its previous status. Without --key, the command uses "default".

--progress adds a progress ring, from 0 to 100. Without it, the status shows
no progress. --tone colors the status: neutral (the default), success,
warning or error.

The status stays until "tab status clear" removes its key, the tab closes, or
the tab's Worker stops. Use "tab status list" to see all keys. The Worker
reports its limits on the status text and keys when it refuses a request.`

// maxTabStatusProgress is the progress of finished work. TabStatus states its
// progress from 0 to 100, so this bound is the meaning of a percentage rather
// than a policy that the Worker could change.
const maxTabStatusProgress = 100

// neutralToneToken is the token of the tone that TabStatus states as
// unspecified. enumToken prints nothing for a zero value, and the neutral tone
// is a choice that a caller makes, so it gets a word of its own.
const neutralToneToken = "neutral"

// newTabStatusCommand builds the commands for a tab's status entries.
func newTabStatusCommand() *cobra.Command {
	return cli.Group("status", "Report and inspect status entries on a tab",
		cli.NewLeaf(cli.Leaf{
			Use:     "set <tab> <text>",
			Short:   "Show a status beside a tab's title, with an optional progress and tone",
			Long:    tabStatusSetLong,
			Mutates: true,
			Args: map[string]string{
				"text": `A short text, such as "Running the tests", or "-" to read it from standard input.`,
			},
			Examples: []cli.Example{
				{Command: `leapmux tab status set self "Running the tests"`},
				{Comment: "Report progress for a separate task.", Command: `leapmux tab status set 87a19d5f "Migrating rows" --key migration --progress 40`},
				{Comment: "Mark the tab when the work fails.", Command: `leapmux tab status set self "Tests failed" --tone error`},
			},
			Flags: func(fs *pflag.FlagSet) {
				addTabStatusKeyFlag(fs)
				fs.Int("progress", 0, "how far the work is, from 0 to 100 (default: no progress)")
				fs.String("tone", neutralToneToken, "the color of the status")
				cli.SetEnum(fs, "tone", tabStatusToneTokens()...)
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runTabStatusSet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "clear <tab> [key]...",
			Short:       "Remove one or more status entries from a tab",
			Mutates:     true,
			Destructive: true,
			Args: map[string]string{
				"key": "One or more status keys to clear (default: the default key, or --all for every key).",
			},
			Examples: []cli.Example{
				{Command: "leapmux tab status clear self"},
				{Command: "leapmux tab status clear 87a19d5f --key migration"},
				{Comment: "Remove every status entry on the tab.", Command: "leapmux tab status clear self --all"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addTabStatusKeyFlag(fs)
				fs.Bool("all", false, "remove every status entry on the tab")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runTabStatusClear,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "list [tab]",
			Aliases: []string{"ls"},
			Short:   "List a tab's status entries, with the visible one first (default: this tab)",
			Examples: []cli.Example{
				{Command: "leapmux tab status list"},
				{Command: "leapmux tab status list 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runTabStatusList,
		}),
	)
}

func addTabStatusKeyFlag(fs *pflag.FlagSet) {
	fs.String("key", "", "which status entry to change (default: default)")
}

// tabStatusToneTokens lists the tones that --tone accepts: the neutral tone,
// then each tone that TabStatusTone declares.
func tabStatusToneTokens() []string {
	return append([]string{neutralToneToken}, enumTokens(leapmuxv1.TabStatusTone_TAB_STATUS_TONE_UNSPECIFIED)...)
}

// parseTabStatusTone returns the tone of a --tone token.
func parseTabStatusTone(token string) (leapmuxv1.TabStatusTone, error) {
	if strings.EqualFold(token, neutralToneToken) {
		return leapmuxv1.TabStatusTone_TAB_STATUS_TONE_UNSPECIFIED, nil
	}
	if n, ok := parseEnumToken(leapmuxv1.TabStatusTone_TAB_STATUS_TONE_UNSPECIFIED, token); ok {
		return leapmuxv1.TabStatusTone(n), nil
	}
	return leapmuxv1.TabStatusTone_TAB_STATUS_TONE_UNSPECIFIED,
		cli.Usagef("--tone must be one of %s; got %q", strings.Join(tabStatusToneTokens(), ", "), token)
}

// tabStatusToneToken is the token that a result prints for a tone, the one
// that --tone accepts back.
func tabStatusToneToken(tone leapmuxv1.TabStatusTone) string {
	if tone == leapmuxv1.TabStatusTone_TAB_STATUS_TONE_UNSPECIFIED {
		return neutralToneToken
	}
	return enumToken(tone)
}

// tabStatusResult is the result of `tab status set`.
type tabStatusResult struct {
	ID     string        `json:"id"`
	Key    string        `json:"key"`
	Status *tabStatusRow `json:"status"`
}

// tabStatusClearResult is the result of clearing a single status entry.
// Status stays null in JSON, and QuietLines prints the cleared status key so
// single-key, multi-key, and --all clears produce consistent -q output.
type tabStatusClearResult struct {
	ID     string        `json:"id"`
	Key    string        `json:"key"`
	Status *tabStatusRow `json:"status"`
}

func (r tabStatusClearResult) QuietLines() []string {
	if r.Key == "" {
		return nil
	}
	return []string{r.Key}
}

// tabStatusRow is a tab's status in a result.
type tabStatusRow struct {
	Key             string  `json:"key,omitempty" table:"KEY"`
	Text            string  `json:"text" table:"STATUS,state"`
	ProgressPercent *uint32 `json:"progress_percent,omitempty" table:"PROGRESS %"`
	Tone            string  `json:"tone" table:"TONE"`
}

type tabStatusListResult struct {
	ID       string         `json:"id"`
	Statuses []tabStatusRow `json:"statuses"`
}

// QuietLines prints the status keys on the tab so scripts can iterate keys for
// `tab status clear`.
func (r tabStatusListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Statuses))
	for _, s := range r.Statuses {
		out = append(out, s.Key)
	}
	return out
}

// runTabStatusSet builds the status from the command line, refusing what it
// can check without the Worker, sends it to the tab's Worker, and prints the
// status that the Worker stored. The Worker owns the trim and the length
// limit, so the CLI sends the text as given.
func runTabStatusSet(inv *cli.Invocation) (any, error) {
	text, err := inputText(inv, "text")
	if err != nil {
		return nil, err
	}
	status := &leapmuxv1.TabStatus{Text: text}
	if inv.Changed("progress") {
		progress, err := inv.Command.Flags().GetInt("progress")
		if err != nil {
			return nil, cli.Wrap(cli.CodeUsage, err, "--progress")
		}
		if progress < 0 || progress > maxTabStatusProgress {
			return nil, cli.Usagef("--progress must be a whole number from 0 to %d; got %d", maxTabStatusProgress, progress)
		}
		percent := uint32(progress)
		status.ProgressPercent = &percent
	}
	tone, err := parseTabStatusTone(stringFlag(inv, "tone"))
	if err != nil {
		return nil, err
	}
	status.Tone = tone
	s, got, err := openTabStatusSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	req := &leapmuxv1.SetTabStatusRequest{TabId: got.ID, Status: status}
	if inv.Changed("key") {
		req.Key = new(stringFlag(inv, "key"))
	}
	var resp leapmuxv1.SetTabStatusResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, req, &resp); err != nil {
		return nil, err
	}
	return tabStatusResult{ID: got.ID, Key: resp.GetKey(), Status: tabStatusRowOf(resp.GetStatus())}, nil
}

type tabStatusClearBatchResult struct {
	ID      string   `json:"id"`
	Cleared []string `json:"cleared"`
}

func (r tabStatusClearBatchResult) QuietLines() []string { return r.Cleared }

// runTabStatusClear clears one key, multiple positional keys, or --all keys
// over a single E2EE channel to the tab's Worker.
func runTabStatusClear(inv *cli.Invocation) (any, error) {
	all := boolFlag(inv, "all")
	rawKeys := inv.Variadic("key")
	hasKeyFlag := inv.Changed("key")
	if all && (hasKeyFlag || len(rawKeys) > 0) {
		return nil, cli.Usagef("--all cannot be combined with --key or positional keys")
	}
	if hasKeyFlag && len(rawKeys) > 0 {
		return nil, cli.Usagef("pass --key or positional keys, not both")
	}

	var keyPtrs []*string
	if !all {
		switch {
		case len(rawKeys) > 0:
			keyPtrs = make([]*string, 0, len(rawKeys))
			for _, k := range rawKeys {
				k = strings.TrimSpace(k)
				if k == "" {
					return nil, cli.Usagef("status key is empty")
				}
				keyPtrs = append(keyPtrs, new(k))
			}
		case hasKeyFlag:
			k := strings.TrimSpace(stringFlag(inv, "key"))
			if k == "" {
				return nil, cli.Usagef("status key is empty")
			}
			keyPtrs = []*string{new(k)}
		default:
			// An unset Key clears the default status entry.
			keyPtrs = []*string{nil}
		}
	}

	s, got, err := openTabStatusSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var cleared []string
	err = withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		if all {
			var listResp leapmuxv1.ListTabStatusesResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure, &leapmuxv1.ListTabStatusesRequest{TabId: got.ID}, &listResp); err != nil {
				return err
			}
			for _, entry := range listResp.GetStatuses() {
				if k := strings.TrimSpace(entry.GetKey()); k != "" {
					keyPtrs = append(keyPtrs, new(k))
				}
			}
			if len(keyPtrs) == 0 {
				cleared = []string{}
				return nil
			}
		}

		cleared = make([]string, len(keyPtrs))
		if len(keyPtrs) == 1 {
			var resp leapmuxv1.SetTabStatusResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, &leapmuxv1.SetTabStatusRequest{TabId: got.ID, Key: keyPtrs[0]}, &resp); err != nil {
				return err
			}
			cleared[0] = resp.GetKey()
			return nil
		}

		g, gctx := errgroup.WithContext(s.ctx)
		g.SetLimit(detailFanoutLimit)
		for i, keyPtr := range keyPtrs {
			g.Go(func() error {
				var resp leapmuxv1.SetTabStatusResponse
				if err := w.Call(gctx, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, &leapmuxv1.SetTabStatusRequest{TabId: got.ID, Key: keyPtr}, &resp); err != nil {
					return err
				}
				cleared[i] = resp.GetKey()
				return nil
			})
		}
		return g.Wait()
	})
	if err != nil {
		return nil, err
	}
	if !all && len(cleared) == 1 {
		return tabStatusClearResult{ID: got.ID, Key: cleared[0]}, nil
	}
	return tabStatusClearBatchResult{ID: got.ID, Cleared: cleared}, nil
}

func runTabStatusList(inv *cli.Invocation) (any, error) {
	s, got, err := openTabStatusSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	req := &leapmuxv1.ListTabStatusesRequest{TabId: got.ID}
	var resp leapmuxv1.ListTabStatusesResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure, req, &resp); err != nil {
		return nil, err
	}
	result := tabStatusListResult{ID: got.ID, Statuses: make([]tabStatusRow, 0, len(resp.GetStatuses()))}
	for _, entry := range resp.GetStatuses() {
		row := tabStatusRowOf(entry.GetStatus())
		if row == nil || entry.GetKey() == "" {
			return nil, cli.Newf(cli.CodeInternal, "the Worker returned an incomplete tab status entry")
		}
		row.Key = entry.GetKey()
		result.Statuses = append(result.Statuses, *row)
	}
	return result, nil
}

func openTabStatusSession(inv *cli.Invocation) (*session, tabTarget, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, tabTarget{}, err
	}
	got, err := s.resolveTab(resolve.KindTab, inv.Arg("tab"), resolve.Need{WorkerID: true})
	if err == nil {
		err = maybePreflightWorker(s.ctx, s.c, got.WorkerID)
	}
	if err != nil {
		s.Close()
		return nil, tabTarget{}, err
	}
	return s, got, nil
}

// tabStatusRowOf is the result row of a status that the Worker stored, or nil
// when it stores none.
func tabStatusRowOf(status *leapmuxv1.TabStatus) *tabStatusRow {
	if status == nil {
		return nil
	}
	return &tabStatusRow{
		Text:            status.GetText(),
		ProgressPercent: status.ProgressPercent,
		Tone:            tabStatusToneToken(status.GetTone()),
	}
}
