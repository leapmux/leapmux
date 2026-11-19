package cmd

import (
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

// notifyLong is the help of `notify`.
const notifyLong = `Show a notification in every LeapMux window that you have open: a toast with
the title and the body. While a window is hidden, it also shows a desktop
notification, when "Desktop notifications" is on in Preferences.

The notification names the tab that runs the command, so you see which agent
or terminal sent it. Nothing stores a notification: a window that you open
later does not show it.

The command goes to the Worker of the tab that runs it. Outside a tab, pass
--worker. A window hears a Worker only while one of your tabs runs on that
Worker. A notification sent from outside a tab names no tab.

The Worker refuses a title or a body that is too long, and states the limit.`

// newNotifyCommand builds `notify`, which asks every open frontend of the
// user to show a notification.
//
// It addresses a Worker rather than a tab, as `terminal quake` does: the
// Worker relays the request on its private-event stream, which reaches every
// frontend that has a tab on it, and it stamps the calling tab from the
// control token, so the CLI sends no tab at all.
func newNotifyCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "notify <title>",
		Short:   "Show a notification in every LeapMux window that you have open",
		Long:    notifyLong,
		Mutates: true,
		Args: map[string]string{
			"title": "A short title, such as \"Tests passed\".",
		},
		Examples: []cli.Example{
			{Command: `leapmux control notify "Tests passed"`},
			{Comment: "Say what failed, with the last lines of the log.", Command: `tail -n 5 build.log | leapmux control notify "Build failed" --body -`},
			{Comment: "Notify through another Worker from outside a tab.", Command: `leapmux control notify "Deploy finished" --body "v1.4.2 is live" --worker name:build-box`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("body", "", `more text below the title ("-" reads stdin)`)
			addWorkerFlag(fs)
		},
		Run: runNotify,
	})
}

// notifyResult is the result of `notify`.
type notifyResult struct {
	WorkerID string `json:"worker_id"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
}

// runNotify sends the notification to the selected Worker and prints the
// notification that the Worker published. It refuses an empty title before it
// sends anything, as a usage error must. The Worker owns every other rule:
// the trim, which drops the line ending that a pipe leaves on --body -, and
// the length limits. The CLI keeps no copy of them.
func runNotify(inv *cli.Invocation) (any, error) {
	title := inv.Arg("title")
	if strings.TrimSpace(title) == "" {
		return nil, cli.Usagef("the title is empty")
	}
	body := ""
	if inv.Changed("body") {
		body = stringFlag(inv, "body")
		if body == "-" {
			data, err := io.ReadAll(inv.Stdin)
			if err != nil {
				return nil, cli.Wrap(cli.CodeInternal, err, "read stdin")
			}
			body = string(data)
		}
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	req := &leapmuxv1.NotifyRequest{Title: title, Body: body}
	var resp leapmuxv1.NotifyResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, req, &resp); err != nil {
		return nil, err
	}
	return notifyResult{WorkerID: workerID, Title: resp.GetTitle(), Body: resp.GetBody()}, nil
}
