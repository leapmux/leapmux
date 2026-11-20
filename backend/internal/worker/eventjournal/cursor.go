package eventjournal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"

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
func (c cursorIdentity) sign(tabID, streamID string, sequence int64) []byte {
	position := make([]byte, cursorPositionSize, cursorPositionSize+sha256.Size)
	position[0] = cursorVersion
	binary.BigEndian.PutUint64(position[1:], uint64(sequence))
	mac := hmac.New(sha256.New, c.key)
	_, _ = io.WriteString(mac, "leapmux semantic terminal cursor\x00")
	for _, field := range []string{c.workerID, c.owner.String(), c.journalID, tabID, streamID} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = mac.Write(length[:])
		_, _ = io.WriteString(mac, field)
	}
	_, _ = mac.Write(position)
	return mac.Sum(position)
}

func (c cursorIdentity) position(tabID, streamID string, cursor []byte) (int64, error) {
	if len(cursor) != cursorPositionSize+sha256.Size || cursor[0] != cursorVersion {
		return 0, ErrCursor
	}
	sequence := binary.BigEndian.Uint64(cursor[1:cursorPositionSize])
	if sequence > math.MaxInt64 || !hmac.Equal(cursor, c.sign(tabID, streamID, int64(sequence))) {
		return 0, ErrCursor
	}
	return int64(sequence), nil
}
