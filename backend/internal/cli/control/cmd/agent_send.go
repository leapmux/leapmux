package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// The flags of `agent send` that shape the message itself: --attach, --at
// and --request-id. The command checks here what the CLI can check without
// the Worker, and the Worker checks the rest: the policy of the agent's
// provider refuses a file that the provider cannot read, and the refusal
// states the file.

// userMessage is one message that `agent send` puts in an agent's input
// queue.
type userMessage struct {
	text        string
	attachments []*leapmuxv1.Attachment
	// scheduledAt is the canonical UTC time before which the agent does not
	// read the message, or "" to let it read the message at once.
	scheduledAt string
}

// request builds the request that queues the message as inputID.
func (m userMessage) request(agentID, inputID string) *leapmuxv1.EnqueueAgentInputRequest {
	return &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: agentID, InputId: inputID, Text: m.text,
		Attachments: m.attachments, ScheduledAt: m.scheduledAt,
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, RequireFutureSchedule: m.scheduledAt != "",
	}
}

// sendSpec is what the command line of `agent send` gives besides the
// target and the wait.
type sendSpec struct {
	message           userMessage
	requestID         string
	explicitRequestID bool
}

// readSendSpec reads the message of `agent send` and its flags. now is the
// moment that a relative --at counts from.
//
// The text is optional when --attach gives a file, because a message may hold
// attachments alone. A text that the command line gives must not be empty.
func readSendSpec(inv *cli.Invocation, now time.Time) (sendSpec, error) {
	requestID, explicitRequestID, err := ensureRequestID(inv)
	if err != nil {
		return sendSpec{}, err
	}
	var scheduledAt string
	if inv.Changed("at") {
		at, relative, err := parseSendAt(stringFlag(inv, "at"), now)
		if err != nil {
			return sendSpec{}, err
		}
		if relative && explicitRequestID {
			return sendSpec{}, cli.Usagef("--request-id needs --at as an RFC 3339 time").
				WithHint("a duration from now gives another time on each run, so a retry would conflict with the first run")
		}
		if relative {
			// A relative --at cannot be idempotently retried with an auto-generated receipt.
			requestID = ""
		}
		if !explicitRequestID && !at.After(now) {
			return sendSpec{}, cli.Usagef("--at %s is not after now (%s)", timefmt.Format(at), timefmt.Format(now)).
				WithHint("omit --at to send the message at once")
		}
		scheduledAt = timefmt.Format(at)
	}
	paths := inv.StringArray("attach")
	if len(paths) > contracts.MaxAgentInputAttachmentsPerItem {
		return sendSpec{}, cli.Usagef("a message holds at most %d attachments; --attach gives %d", contracts.MaxAgentInputAttachmentsPerItem, len(paths))
	}
	text := ""
	if len(paths) == 0 || inv.HasArg("text") || inv.Changed("file") {
		if text, err = inputText(inv, "text"); err != nil {
			return sendSpec{}, err
		}
	}
	if len(text) > contracts.MaxAgentInputItemBytes {
		return sendSpec{}, cli.Usagef("the text holds %d bytes; one message holds at most %d bytes", len(text), contracts.MaxAgentInputItemBytes)
	}
	attachments, err := readAttachments(paths, int64(contracts.MaxAgentInputItemBytes-len(text)))
	if err != nil {
		return sendSpec{}, err
	}
	return sendSpec{message: userMessage{text: text, attachments: attachments, scheduledAt: scheduledAt}, requestID: requestID, explicitRequestID: explicitRequestID}, nil
}

// parseSendAt reads --at: an RFC 3339 time, which states its own offset, or
// "+" and a positive duration from now, such as +10m. It reports whether the
// value was relative. An absolute timestamp can be in the past when a
// request ID lets the Worker check whether it already accepted that schedule.
//
// The CLI never assumes a time zone. A time without an offset is refused,
// because the zone of the machine that runs the command and the zone of the
// Worker can differ.
func parseSendAt(raw string, now time.Time) (time.Time, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false, cli.Usagef("--at is empty")
	}
	var at time.Time
	relative := strings.HasPrefix(value, "+")
	if relative {
		d, err := time.ParseDuration(value[1:])
		if err != nil {
			return time.Time{}, false, cli.Usagef("--at %q is not a duration such as +10m or +1h30m", raw)
		}
		if d <= 0 {
			return time.Time{}, false, cli.Usagef("--at %q is not after now", raw).
				WithHint("omit --at to send the message at once")
		}
		at = now.Add(d)
	} else {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, false, cli.Usagef("--at %q is neither an RFC 3339 time with its offset nor a duration from now", raw).
				WithHint("write a time such as 2026-11-20T09:00:00+09:00 or 2026-11-20T00:00:00Z, or a duration such as +10m")
		}
		at = parsed
	}
	return at, relative, nil
}

// readAttachments reads the files of --attach, in order. budget is the count
// of bytes that the attachments may hold together: what remains of the limit
// of one message after its text. Each attachment keeps its file's name and
// states no media type, so the Worker infers the type with the classifier
// that its provider policies use.
func readAttachments(paths []string, budget int64) ([]*leapmuxv1.Attachment, error) {
	out := make([]*leapmuxv1.Attachment, 0, len(paths))
	for _, path := range paths {
		data, err := readAttachment(path, budget)
		if err != nil {
			return nil, err
		}
		budget -= int64(len(data))
		out = append(out, &leapmuxv1.Attachment{Filename: filepath.Base(path), Data: data})
	}
	return out, nil
}

// readAttachment reads one file of --attach, which must fit in budget bytes.
// It reads at most one byte more than the budget, so a large file, or a pipe
// with no end, cannot exhaust the memory of the CLI.
func readAttachment(path string, budget int64) ([]byte, error) {
	if path == "" {
		return nil, cli.Usagef(`--attach ""`).WithHint("give the path of a file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "read --attach")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "read --attach")
	}
	if info.IsDir() {
		return nil, cli.Usagef("--attach %s is a directory", path).WithHint("attach the files in it one by one")
	}
	if info.Mode().IsRegular() && info.Size() > budget {
		return nil, attachmentTooLarge(path)
	}
	data, err := io.ReadAll(io.LimitReader(f, budget+1))
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "read --attach "+path)
	}
	if int64(len(data)) > budget {
		return nil, attachmentTooLarge(path)
	}
	return data, nil
}

// attachmentTooLarge refuses the file at path, which does not fit in what
// remains of the limit of one message.
func attachmentTooLarge(path string) error {
	return cli.Usagef("--attach %s makes the message too large: its text and its attachments hold at most %d bytes together", path, contracts.MaxAgentInputItemBytes)
}
