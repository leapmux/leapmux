package inputqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// SteeredInput is what the Worker still holds of an input that the queue
// steered into a running turn. A provider that drops the steer hands it back,
// and the Worker reads this to queue the input again (see Store.SteeredInput).
type SteeredInput struct {
	// Sender is the agent tab that sent an agent message, and nil for every
	// other input. Read from a transcript row, it holds no title, because the
	// row states the sender as an AgentMessageSender, which has none.
	Sender *AgentSender
	// Text is the text of the input as its writer wrote it, without anything
	// that the Worker adds for the provider, such as the header of an agent
	// message.
	Text string
	// Held reports that the queue keeps the input itself: it holds the input
	// for a later delivery, or it keeps the row of an input that the user
	// removed. Sender and Text are then empty, because a hand-back of a held
	// input queues nothing.
	Held bool
}

// SteeredInput reads the input inputID of agentID after a steer gave it to the
// provider. The input is in one of two places, and one read transaction reads
// both, so it finds the input in the place that holds it at that instant:
//
//   - The queue row, while the steer is in flight. The provider took the
//     input, and Accept has not yet written its transcript row. A provider can
//     drop the steer in that window, for example at an interrupt that follows
//     the steer at once. The row holds the sender with its title.
//   - The transcript row, once Accept ran. Accept writes the row with the
//     input id as its message id, and deletes the queue row, in one
//     transaction. The row holds the sender without its title.
//
// A queue row in a state that no steer holds, such as QUEUED, and the row of an
// input that the user removed, answer Held. An input that neither place holds
// fails with ErrNotFound.
func (s *Store) SteeredInput(ctx context.Context, agentID, inputID string) (SteeredInput, error) {
	if !validateIdentity(agentID) || !validateIdentity(inputID) {
		return SteeredInput{}, fmt.Errorf("%w: agent and input IDs are required", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SteeredInput{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, _, found, err := getItemWithEphemeral(ctx, tx, agentID, inputID, false, nil)
	if err != nil {
		return SteeredInput{}, err
	}
	if found {
		if !lockedInputState(item.State) {
			return SteeredInput{Held: true}, nil
		}
		return SteeredInput{Sender: item.Sender, Text: item.Text}, nil
	}
	var removed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_input_queue_removed WHERE agent_id = ? AND id = ?)`,
		agentID, inputID).Scan(&removed); err != nil {
		return SteeredInput{}, err
	}
	if removed {
		return SteeredInput{Held: true}, nil
	}
	return readInputTranscript(ctx, tx, agentID, inputID)
}

// readInputTranscript reads the transcript row that Accept wrote for the input
// inputID of agentID. It reads only a user row accepted from the input queue,
// so a message from any other source or producer never reads as a queued input.
func readInputTranscript(ctx context.Context, tx db.DBTX, agentID, inputID string) (SteeredInput, error) {
	var content, supplemental []byte
	var contentCompression, supplementalCompression int32
	err := tx.QueryRowContext(ctx, `SELECT content, content_compression, supplemental_content, supplemental_content_compression
		FROM messages WHERE id = ? AND agent_id = ? AND source = ? AND from_input_queue = 1`,
		inputID, agentID, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER).
		Scan(&content, &contentCompression, &supplemental, &supplementalCompression)
	if errors.Is(err, sql.ErrNoRows) {
		return SteeredInput{}, ErrNotFound
	}
	if err != nil {
		return SteeredInput{}, err
	}
	content, err = msgcodec.Decompress(content, leapmuxv1.ContentCompression(contentCompression))
	if err != nil {
		return SteeredInput{}, fmt.Errorf("decompress the content of the input: %w", err)
	}
	text, err := transcriptText(content)
	if err != nil {
		return SteeredInput{}, err
	}
	supplemental, err = msgcodec.Decompress(supplemental, leapmuxv1.ContentCompression(supplementalCompression))
	if err != nil {
		return SteeredInput{}, fmt.Errorf("decompress the supplement of the input: %w", err)
	}
	sender, err := agentMessageSenderOf(supplemental)
	if err != nil {
		return SteeredInput{}, err
	}
	return SteeredInput{Sender: sender, Text: text}, nil
}

// SteeredInput reads what the Worker still holds of an input that the queue
// steered (see Store.SteeredInput). It takes no coordinator lock: Accept moves
// an input from its queue row to its transcript row in one transaction, so the
// read finds the input in one place without the lock that serializes Accept.
func (m *Manager) SteeredInput(ctx context.Context, agentID, inputID string) (SteeredInput, error) {
	if !m.beginActivity() {
		return SteeredInput{}, ErrManagerStopped
	}
	defer m.endActivity()
	return m.store.SteeredInput(ctx, agentID, inputID)
}
