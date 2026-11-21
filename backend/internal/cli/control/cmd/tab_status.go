package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
				{Command: `leapmux control tab status set self "Running the tests"`},
				{Comment: "Report progress for a separate task.", Command: `leapmux control tab status set 87a19d5f "Migrating rows" --key migration --progress 40`},
				{Comment: "Mark the tab when the work fails.", Command: `leapmux control tab status set self "Tests failed" --tone error`},
			},
			Flags: func(fs *pflag.FlagSet) {
				addTabStatusKeyFlag(fs)
				fs.Int("progress", 0, "how far the work is, from 0 to 100 (default: no progress)")
				fs.String("tone", neutralToneToken, "the color of the status")
				cli.SetEnum(fs, "tone", tabStatusToneTokens()...)
			},
			Complete: completeTargets,
			Run:      runTabStatusSet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "clear <tab>",
			Short:   "Remove one status entry from a tab",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control tab status clear self"},
				{Command: "leapmux control tab status clear 87a19d5f --key migration"},
			},
			Flags:    addTabStatusKeyFlag,
			Complete: completeTargets,
			Run:      runTabStatusClear,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "list <tab>",
			Short: "List a tab's status entries, with the visible one first",
			Examples: []cli.Example{
				{Command: "leapmux control tab status list self"},
			},
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

// tabStatusResult is the result of `tab status set` and `tab status clear`.
// Status is null after a clear.
type tabStatusResult struct {
	ID     string        `json:"id"`
	Key    string        `json:"key"`
	Status *tabStatusRow `json:"status"`
}

// tabStatusRow is a tab's status in a result.
type tabStatusRow struct {
	Key             string  `json:"key,omitempty" table:"KEY"`
	Text            string  `json:"text" table:"STATUS"`
	ProgressPercent *uint32 `json:"progress_percent,omitempty" table:"PROGRESS %"`
	Tone            string  `json:"tone" table:"TONE"`
}

type tabStatusListResult struct {
	ID       string         `json:"id"`
	Statuses []tabStatusRow `json:"statuses"`
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
	return setTabStatus(inv, status)
}

// runTabStatusClear sends a request with no status, which clears it.
func runTabStatusClear(inv *cli.Invocation) (any, error) {
	return setTabStatus(inv, nil)
}

// setTabStatus resolves the <tab> argument, sends SetTabStatus to the Worker
// that hosts the tab, and returns the result: the tab and the status that the
// Worker stored. A nil status clears it, and the result then states no status.
func setTabStatus(inv *cli.Invocation, status *leapmuxv1.TabStatus) (tabStatusResult, error) {
	s, got, err := openTabStatusSession(inv)
	if err != nil {
		return tabStatusResult{}, err
	}
	defer s.Close()
	req := &leapmuxv1.SetTabStatusRequest{TabId: got.ID, Status: status}
	if inv.Changed("key") {
		req.Key = new(stringFlag(inv, "key"))
	}
	var resp leapmuxv1.SetTabStatusResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, req, &resp); err != nil {
		return tabStatusResult{}, err
	}
	return tabStatusResult{ID: got.ID, Key: resp.GetKey(), Status: tabStatusRowOf(resp.GetStatus())}, nil
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
