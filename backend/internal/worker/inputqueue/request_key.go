package inputqueue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"

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

// InputRequestKeyPrefix distinguishes accepted queue input from provider rows.
const InputRequestKeyPrefix = "input:"

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
	h := sha256.New()
	var lenBuf [8]byte
	writeUint64 := func(v uint64) {
		binary.BigEndian.PutUint64(lenBuf[:], v)
		_, _ = h.Write(lenBuf[:])
	}
	writeString := func(s string) {
		writeUint64(uint64(len(s)))
		_, _ = h.Write([]byte(s))
	}
	writeBytes := func(b []byte) {
		writeUint64(uint64(len(b)))
		if len(b) > 0 {
			_, _ = h.Write(b)
		}
	}
	writeBool := func(v bool) {
		if v {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}
	}
	writeHashHeader(h)
	writeString(input.ID)
	writeString(input.AgentID)
	writeUint64(uint64(input.Kind))
	if input.Sender != nil {
		writeBool(true)
		writeString(input.Sender.TabID)
		writeUint64(uint64(input.Sender.Provider))
	} else {
		writeBool(false)
	}
	writeString(input.Text)
	writeString(input.TargetMode)
	writeBool(input.PrepareContext)
	writeBool(input.ReclassifyOnEdit)
	if input.ScheduledAt != nil {
		writeBool(true)
		writeString(timefmt.Format(*input.ScheduledAt))
	} else {
		writeBool(false)
	}
	writeBytes(input.SourcePayload)
	writeUint64(uint64(len(input.Attachments)))
	for i := range input.Attachments {
		writeString(input.Attachments[i].Filename)
		writeString(input.Attachments[i].MimeType)
		writeBytes(input.Attachments[i].Data)
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return InputRequestKeyPrefix + hex.EncodeToString(sum[:])
}

func writeHashHeader(h hash.Hash) {
	_, _ = h.Write([]byte("leapmux.input.idempotency.v1\x00"))
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
		// The live queue row.
		`SELECT agent_id, idempotency_key, 0 FROM agent_input_queue_items WHERE id = ?`,
		// The tombstone of an input that someone removed from the queue before
		// delivery: a retry of the request that queued it matches it too.
		`SELECT agent_id, idempotency_key, 0 FROM agent_input_queue_removed WHERE id = ?`,
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
