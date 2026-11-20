package eventjournal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"
	"strconv"

	"github.com/leapmux/leapmux/internal/util/userid"
)

const cursorVersion = 1
const cursorPositionSize = 1 + 8

type cursorIdentity struct {
	workerID, journalID string
	owner               userid.UserID
	key                 []byte
}

// The token carries only the version and position. The signature binds the
// rest to the identity established by this Worker and the tab it admitted.
func (c cursorIdentity) sign(tab Tab, streamID string, sequence int64) []byte {
	position := make([]byte, cursorPositionSize, cursorPositionSize+sha256.Size)
	position[0] = cursorVersion
	binary.BigEndian.PutUint64(position[1:], uint64(sequence))
	sum := c.digest("leapmux semantic tab cursor", strconv.FormatInt(int64(tab.Type), 10), tab.ID, streamID, string(position))
	return append(position, sum...)
}

// RequestInstance derives a journal-local identity from a live claim. The
// journal can correlate a request and its end without retaining the claim.
func (j *Journal) RequestInstance(agentID, claim string) []byte {
	if agentID == "" || claim == "" {
		return nil
	}
	return j.identity.digest("leapmux control request instance", agentID, claim)
}

func (c cursorIdentity) digest(purpose string, fields ...string) []byte {
	mac := hmac.New(sha256.New, c.key)
	for _, field := range append([]string{purpose, c.workerID, c.owner.String(), c.journalID}, fields...) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = mac.Write(length[:])
		_, _ = io.WriteString(mac, field)
	}
	return mac.Sum(nil)
}

func (c cursorIdentity) position(tab Tab, streamID string, cursor []byte) (int64, error) {
	if len(cursor) != cursorPositionSize+sha256.Size || cursor[0] != cursorVersion {
		return 0, ErrCursor
	}
	sequence := binary.BigEndian.Uint64(cursor[1:cursorPositionSize])
	if sequence > math.MaxInt64 || !hmac.Equal(cursor, c.sign(tab, streamID, int64(sequence))) {
		return 0, ErrCursor
	}
	return int64(sequence), nil
}
