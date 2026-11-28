package cmd

import (
	"cmp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// notifyLong is the help of `notify`.
const notifyLong = `Show a notification in every LeapMux window that you have open: a toast with
the title and the body. While a window is hidden, it also shows a desktop
notification, when "Desktop notifications" is on in Preferences.

The Worker identifies the sending tab when the command runs inside one.
Use --tab or --workspace to attach a destination the user can open.

With --tab, the notification goes through the destination's Worker. For
other notifications, the command uses the sending tab's Worker. Outside a
tab, pass --worker. A window hears a Worker while one of your tabs runs there.

The command returns the notification ID. Use "notification list" to read the
Worker's recent notifications, and "notification clear" to remove one by ID.
A window that connects later does not show an old toast.

The Worker refuses a title or a body that is too long, and states the limit.`

// newNotifyCommand builds the top-level `notify` shortcut for `notification send`.
func newNotifyCommand() *cobra.Command {
	return newNotifySendLeaf("notify <title>", "leapmux notify")
}

func newNotifySendLeaf(use, examplePrefix string) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     use,
		Short:   "Show a notification in every LeapMux window that you have open",
		Long:    notifyLong,
		Mutates: true,
		Args: map[string]string{
			"title": "A short title, such as \"Tests passed\".",
		},
		Examples: []cli.Example{
			{Command: examplePrefix + ` "Tests passed"`},
			{Comment: "Say what failed, with the last lines of the log.", Command: `tail -n 5 build.log | ` + examplePrefix + ` "Build failed" --body -`},
			{Comment: "Notify through another Worker from outside a tab.", Command: examplePrefix + ` "Deploy finished" --body "v1.4.2 is live" --worker name:build-box`},
			{Comment: "Attach the deployment terminal.", Command: examplePrefix + ` "Deploy finished" --tab title:Deployment`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("body", "", `more text below the title ("-" reads stdin)`)
			fs.String("tab", "", "open this tab from the notification")
			fs.String("workspace", "", "open this workspace from the notification")
			addWorkerFlag(fs)
		},
		Run: runNotify,
	})
}

func newNotificationCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("notification", "Send, list and clear recent Worker notifications",
		newNotifySendLeaf("send <title>", "leapmux notification send"),
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List the recent notifications on a Worker",
			Examples: []cli.Example{
				{Command: "leapmux notification list --worker name:build-box"},
			},
			Flags: addWorkerFlag,
			Run:   runNotificationList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "clear [notification-id]...",
			Aliases:     []string{"rm"},
			Short:       "Remove one or more notifications from a Worker and dismiss their toasts",
			Mutates:     true,
			Destructive: true,
			Args: map[string]string{
				"notification-id": "One or more IDs returned by notify or notification list (or pass --all).",
			},
			Examples: []cli.Example{
				{Command: "leapmux notification clear 7fe1879a7fe1879a7fe1879a7fe1879a7fe1879a7fe1879a --worker name:build-box"},
				{Comment: "Clear every recent notification on the Worker.", Command: "leapmux notification clear --all --worker name:build-box"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("all", false, "clear every recent notification on the Worker")
				addWorkerFlag(fs)
			},
			Complete: completeNotifications,
			Run:      runNotificationClear,
		}),
	), "notif")
}

// notifyResult is the result of `notify`.
type notifyResult struct {
	ID          string `json:"id"`
	WorkerID    string `json:"worker_id"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// runNotify sends the notification to the selected Worker and prints the
// notification that the Worker published. It refuses an empty title before it
// sends anything, as a usage error must. The Worker owns every other rule:
// the trim, which drops the line ending that a pipe leaves on --body -, and
// the length limits. The CLI keeps no copy of them.
func runNotify(inv *cli.Invocation) (any, error) {
	if inv.Changed("tab") && inv.Changed("workspace") {
		return nil, cli.Usagef("pass just one of --tab and --workspace")
	}
	title := inv.Arg("title")
	if strings.TrimSpace(title) == "" {
		return nil, cli.Usagef("the title is empty")
	}
	body := ""
	if inv.Changed("body") {
		body = stringFlag(inv, "body")
		if body == "-" {
			data, err := readStdinGuarded(inv.Stdin)
			if err != nil {
				return nil, err
			}
			body = string(data)
		}
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, destination, err := notificationDestination(s)
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	req := &leapmuxv1.NotifyRequest{Title: title, Body: body, Destination: destination}
	var resp leapmuxv1.NotifyResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, req, &resp); err != nil {
		return nil, err
	}
	if resp.GetId() == "" {
		return nil, cli.Newf(cli.CodeInternal, "the Worker returned no notification ID")
	}
	target := resp.GetDestination()
	return notifyResult{
		ID: resp.GetId(), WorkerID: workerID, Title: resp.GetTitle(), Body: resp.GetBody(),
		TabID: target.GetTab().GetTabId(), WorkspaceID: target.GetWorkspaceId(),
	}, nil
}

func notificationDestination(s *session) (string, *leapmuxv1.NotificationDestination, error) {
	if s.inv.Changed("tab") {
		tab, err := s.resolveTab(resolve.KindTab, stringFlag(s.inv, "tab"), resolve.Need{WorkerID: true, Want: resolve.Wants{TabType: true}})
		if err != nil {
			return "", nil, err
		}
		if s.inv.Changed(workerFlag) {
			workerID, err := s.workerScope()
			if err != nil {
				return "", nil, err
			}
			if workerID != tab.WorkerID {
				return "", nil, cli.Usagef("the selected Worker does not host the destination tab")
			}
		}
		destination := &leapmuxv1.NotificationDestination{
			Target: &leapmuxv1.NotificationDestination_Tab{Tab: &leapmuxv1.TabIdent{TabId: tab.ID, TabType: tab.Type}},
		}
		return tab.WorkerID, destination, nil
	}
	workerID, err := s.workerScope()
	if err != nil {
		return "", nil, err
	}
	if !s.inv.Changed("workspace") {
		return workerID, nil, nil
	}
	workspace, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
	if err != nil {
		return "", nil, err
	}
	return workerID, &leapmuxv1.NotificationDestination{Target: &leapmuxv1.NotificationDestination_WorkspaceId{WorkspaceId: workspace.ID}}, nil
}

type recentNotificationRow struct {
	ID          string `json:"id" table:"ID"`
	Title       string `json:"title" table:"TITLE"`
	Body        string `json:"body,omitempty"`
	SourceTabID string `json:"source_tab_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type notificationListResult struct {
	WorkerID      string                  `json:"worker_id"`
	Notifications []recentNotificationRow `json:"notifications"`
}

func (r notificationListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Notifications))
	for _, n := range r.Notifications {
		out = append(out, n.ID)
	}
	return out
}

func runNotificationList(inv *cli.Invocation) (any, error) {
	var response leapmuxv1.ListNotificationsResponse
	workerID, err := callOnWorkerWithID(inv, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, &leapmuxv1.ListNotificationsRequest{}, &response)
	if err != nil {
		return nil, err
	}
	rows := make([]recentNotificationRow, 0, len(response.GetNotifications()))
	for _, notice := range response.GetNotifications() {
		if notice.GetId() == "" || notice.GetTitle() == "" {
			return nil, cli.Newf(cli.CodeInternal, "the Worker returned an incomplete notification record")
		}
		destination := notice.GetDestination()
		rows = append(rows, recentNotificationRow{
			ID: notice.GetId(), Title: notice.GetTitle(), Body: notice.GetBody(), SourceTabID: notice.GetSourceTabId(),
			TabID: destination.GetTab().GetTabId(), WorkspaceID: destination.GetWorkspaceId(),
		})
	}
	return notificationListResult{WorkerID: workerID, Notifications: rows}, nil
}

type notificationClearResult struct {
	ID       string `json:"id"`
	WorkerID string `json:"worker_id"`
	Cleared  bool   `json:"cleared"`
}

type notificationClearBatchResult struct {
	WorkerID string   `json:"worker_id"`
	Cleared  []string `json:"cleared"`
}

func (r notificationClearBatchResult) QuietLines() []string { return r.Cleared }

func parseNotificationSelector(raw string) (value string, fullID bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, cli.Usagef("the notification ID is empty")
	}
	if rest, ok := strings.CutPrefix(trimmed, "id:"); ok {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return "", false, cli.Usagef(`"id:" needs a notification ID after the colon`)
		}
		return rest, resolve.IsGeneratedID(rest), nil
	}
	return trimmed, resolve.IsGeneratedID(trimmed), nil
}

func resolveNotificationID(items []*leapmuxv1.UserNotification, raw string) (string, error) {
	value, fullID, err := parseNotificationSelector(raw)
	if err != nil {
		return "", err
	}
	if fullID {
		return value, nil
	}
	matched, ambiguous, ok := lookupByIDPrefixMin(items, value, resolve.MinPrefixLength, (*leapmuxv1.UserNotification).GetId)
	if ok {
		return matched.GetId(), nil
	}
	if len(ambiguous) == 0 {
		return "", cli.Newf(cli.CodeNotFound, "no notification matches %q", raw).
			WithHint(`list recent notifications with "leapmux notification list"`)
	}
	e := cli.Newf(cli.CodeAmbiguousTarget, "%d notifications match %q", len(ambiguous), value).
		WithHint("type more characters of the notification ID, or the full ID")
	e.Candidates = make([]cli.Candidate, 0, len(ambiguous))
	for _, m := range ambiguous {
		e.Candidates = append(e.Candidates, cli.Candidate{ID: m.GetId(), Kind: "notification", Title: m.GetTitle()})
	}
	slices.SortFunc(e.Candidates, func(a, b cli.Candidate) int {
		return cmp.Compare(a.ID, b.ID)
	})
	return "", e
}

func runNotificationClear(inv *cli.Invocation) (any, error) {
	all := boolFlag(inv, "all")
	ids := inv.Variadic("notification-id")
	if all && len(ids) > 0 {
		return nil, cli.Usagef("--all cannot be combined with notification IDs")
	}
	if !all && len(ids) == 0 {
		return nil, cli.Usagef("pass one or more notification IDs, or --all")
	}
	needList := all
	for _, raw := range ids {
		_, exact, err := parseNotificationSelector(raw)
		if err != nil {
			return nil, err
		}
		needList = needList || !exact
	}
	s, workerID, err := openWorkerSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var (
		resolvedIDs  []string
		clearedFlags []bool
	)
	err = withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		var listed []*leapmuxv1.UserNotification
		if needList {
			var listResp leapmuxv1.ListNotificationsResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, &leapmuxv1.ListNotificationsRequest{}, &listResp); err != nil {
				return err
			}
			listed = listResp.GetNotifications()
		}
		seen := make(map[string]struct{}, len(ids))
		addID := func(nid string) {
			if nid == "" {
				return
			}
			if _, dup := seen[nid]; dup {
				return
			}
			seen[nid] = struct{}{}
			resolvedIDs = append(resolvedIDs, nid)
		}
		if all {
			for _, n := range listed {
				addID(n.GetId())
			}
		} else {
			for _, raw := range ids {
				nid, err := resolveNotificationID(listed, raw)
				if err != nil {
					return err
				}
				addID(nid)
			}
		}
		clearedFlags = make([]bool, len(resolvedIDs))
		g, gctx := errgroup.WithContext(s.ctx)
		g.SetLimit(detailFanoutLimit)
		for i, noticeID := range resolvedIDs {
			g.Go(func() error {
				var response leapmuxv1.ClearNotificationResponse
				if err := w.Call(gctx, leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure, &leapmuxv1.ClearNotificationRequest{Id: noticeID}, &response); err != nil {
					return err
				}
				clearedFlags[i] = response.GetCleared()
				return nil
			})
		}
		return g.Wait()
	})
	cleared := make([]string, 0, len(resolvedIDs))
	for i, ok := range clearedFlags {
		if ok {
			cleared = append(cleared, resolvedIDs[i])
		}
	}
	if err != nil {
		if len(cleared) > 0 {
			return nil, cli.FromError(err).WithHint("already cleared: %s", strings.Join(cleared, ", "))
		}
		return nil, err
	}
	if !all && len(ids) == 1 && len(resolvedIDs) == 1 {
		return notificationClearResult{
			ID:       resolvedIDs[0],
			WorkerID: workerID,
			Cleared:  clearedFlags[0],
		}, nil
	}
	return notificationClearBatchResult{WorkerID: workerID, Cleared: cleared}, nil
}
