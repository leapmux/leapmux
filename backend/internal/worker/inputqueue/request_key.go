package inputqueue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// A client that repeats a request to queue an input, such as `leapmux control
// agent send --request-id` after a lost answer, sends the input id of the
// first request again. The Worker takes the repeat as the first request only
// when it carries the same content. The idempotency key of a request states
// that content (inputIdempotencyKey), and the Worker stores the key of the
// request that queued an input once, when it inserts the queue row
// (insertQueuedItem):
//
//   - The queue row keeps it through every change of the input: an edit, a
//     reclassification, a move, a new schedule, a return to the queue. A
//     retry then still matches the input after someone edited it.
//   - The transcript row of the accepted input takes it over
//     (insertInputTranscript).
//
// A retry compares its own key with the stored key (storedRequest), never
// with the content that the input holds now.

const inputIdempotencyKeyPrefix = "input:"

// inputIdempotencyKey returns the key of a request that queues input, after
// its admission. The policy for adding a new input does not change the key.
// Two requests have one key exactly when they state the same
// input:
//
//   - The input id and the agent.
//   - The kind that the admission settled.
//   - The sender: an input that another agent's tab sends is another input.
//     The key holds the sender's tab and provider, not its title, so a retry
//     from a tab that the user renamed meanwhile is the same request.
//   - The text, the target mode, the preparation of the context, and whether
//     an edit reclassifies the input.
//   - The schedule, as an instant at the millisecond precision at which the
//     queue stores it. The same instant in another time zone is the same
//     schedule, and the same input id sent again with another schedule is
//     another request.
//   - The source payload of an automatic retry.
//   - The attachments, with their data. An attachment without data counts the
//     same whether its data is nil or empty, as bytes.Equal compares them.
func inputIdempotencyKey(input NewItem) string {
	type idempotencyAttachment struct {
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		Data     []byte `json:"data"`
	}
	type idempotencySender struct {
		TabID    string                  `json:"tab_id"`
		Provider leapmuxv1.AgentProvider `json:"provider"`
	}
	type idempotencyPayload struct {
		ID               string                   `json:"id"`
		AgentID          string                   `json:"agent_id"`
		Kind             leapmuxv1.AgentInputKind `json:"kind"`
		Sender           *idempotencySender       `json:"sender,omitempty"`
		Text             string                   `json:"text"`
		TargetMode       string                   `json:"target_mode"`
		PrepareContext   bool                     `json:"prepare_context"`
		ReclassifyOnEdit bool                     `json:"reclassify_on_edit"`
		ScheduledAt      *string                  `json:"scheduled_at,omitempty"`
		SourcePayload    []byte                   `json:"source_payload,omitempty"`
		Attachments      []idempotencyAttachment  `json:"attachments"`
	}
	attachments := make([]idempotencyAttachment, len(input.Attachments))
	for index := range input.Attachments {
		attachments[index] = idempotencyAttachment(input.Attachments[index])
		if len(attachments[index].Data) == 0 {
			// encoding/json writes a nil slice as null and an empty one as "".
			attachments[index].Data = nil
		}
	}
	var scheduled *string
	if input.ScheduledAt != nil {
		text := timefmt.Format(*input.ScheduledAt)
		scheduled = &text
	}
	var sender *idempotencySender
	if input.Sender != nil {
		sender = &idempotencySender{TabID: input.Sender.TabID, Provider: input.Sender.Provider}
	}
	data, err := json.Marshal(idempotencyPayload{
		ID: input.ID, AgentID: input.AgentID, Kind: input.Kind, Sender: sender, Text: input.Text,
		TargetMode: input.TargetMode, PrepareContext: input.PrepareContext,
		ReclassifyOnEdit: input.ReclassifyOnEdit, ScheduledAt: scheduled,
		SourcePayload: input.SourcePayload, Attachments: attachments,
	})
	if err != nil {
		panic(fmt.Sprintf("marshal queued input idempotency key: %v", err))
	}
	sum := sha256.Sum256(data)
	return inputIdempotencyKeyPrefix + hex.EncodeToString(sum[:])
}

type storedInputRequest struct {
	key         string
	acceptedSeq int64
}

// storedRequest reads a queued or accepted input's request key. It reads the
// sequence with the key when the input already has a transcript row. Input
// IDs are unique across the Worker, so a row of another agent conflicts.
func storedRequest(ctx context.Context, tx db.DBTX, agentID, inputID string) (storedInputRequest, bool, error) {
	statements := [...]string{
		// The queue row, whether the queue holds the input or someone removed
		// it: a retry of the request that queued a removed input matches it
		// too, so the statement takes both values of removed_at on purpose.
		`SELECT agent_id, idempotency_key, 0 FROM agent_input_queue_items WHERE id = ? AND (removed_at IS NULL OR removed_at IS NOT NULL)`,
		// The transcript row of the accepted input, whose message id is the
		// input id.
		`SELECT agent_id, idempotency_key, seq FROM messages WHERE id = ?`,
	}
	for _, statement := range statements {
		var owner string
		var request storedInputRequest
		err := tx.QueryRowContext(ctx, statement, inputID).Scan(&owner, &request.key, &request.acceptedSeq)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return storedInputRequest{}, false, err
		}
		if owner != agentID {
			return storedInputRequest{}, false, ErrConflict
		}
		return request, true, nil
	}
	return storedInputRequest{}, false, nil
}
