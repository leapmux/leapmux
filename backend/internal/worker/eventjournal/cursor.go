package eventjournal

import (
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/seqcursor"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type cursorIdentity struct {
	workerID, journalID string
	owner               userid.UserID
	key                 []byte
}

// The token carries only the version and position. The signature binds the
// rest to the identity established by this Worker and the tab it admitted.
func (c cursorIdentity) tabSigner(tab Tab, streamID string) seqcursor.Signer {
	return seqcursor.NewSigner(
		c.key,
		"leapmux semantic tab cursor",
		c.workerID, c.owner.String(), c.journalID,
		strconv.FormatInt(int64(tab.Type), 10), tab.ID, streamID,
	)
}

func (c cursorIdentity) feedSigner(tabType leapmuxv1.TabType) seqcursor.Signer {
	return seqcursor.NewSigner(
		c.key,
		"leapmux semantic Worker cursor",
		c.workerID, c.owner.String(), c.journalID,
		strconv.FormatInt(int64(tabType), 10),
	)
}

func (c cursorIdentity) sign(tab Tab, streamID string, sequence int64) []byte {
	signer := c.tabSigner(tab, streamID)
	return signer.Sign(sequence)
}

func (c cursorIdentity) signFeedPosition(tabType leapmuxv1.TabType, sequence int64) []byte {
	signer := c.feedSigner(tabType)
	return signer.Sign(sequence)
}

// RequestInstance derives a journal-local identity from a live claim. The
// journal can correlate a request and its end without retaining the claim.
func (j *Journal) RequestInstance(agentID, claim string) []byte {
	if agentID == "" || claim == "" {
		return nil
	}
	return seqcursor.Digest(
		j.identity.key,
		"leapmux control request instance",
		j.identity.workerID, j.identity.owner.String(), j.identity.journalID,
		agentID, claim,
	)
}

func (c cursorIdentity) position(tab Tab, streamID string, cursor []byte) (int64, error) {
	signer := c.tabSigner(tab, streamID)
	sequence, ok := signer.Verify(cursor)
	if !ok {
		return 0, ErrCursor
	}
	return sequence, nil
}

func (c cursorIdentity) feedPosition(tabType leapmuxv1.TabType, cursor []byte) (int64, error) {
	signer := c.feedSigner(tabType)
	sequence, ok := signer.Verify(cursor)
	if !ok {
		return 0, ErrFeedCursor
	}
	return sequence, nil
}
