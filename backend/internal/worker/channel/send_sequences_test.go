package channel

import (
	"bytes"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// A large message holds its correlation id's sequence permit across all of its
// chunks, so a second message on the SAME id follows it. A message on ANOTHER id
// takes only the frame permit, so it overtakes the large message between its
// chunks.
//
// Each case runs in a synctest bubble, so the test releases the first frame only
// after synctest.Wait sees the second send parked on its permit. A wait on
// SendWaiters alone races: acquire counts a waiter BEFORE it parks, and a frame
// permit released inside that window goes back to the large message, which then
// finishes first and breaks the order the different-correlation case asserts.
func TestSendStreamPreservesCompleteMessagesForOneCorrelation(t *testing.T) {
	t.Parallel()
	for _, sameID := range []bool{true, false} {
		t.Run(map[bool]string{true: "same correlation", false: "different correlations"}[sameID], func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				workerSession, clientSession := setupTestSessions(t)
				firstFrame := make(chan struct{})
				releaseFirst := make(chan struct{})
				var release sync.Once
				t.Cleanup(func() { release.Do(func() { close(releaseFirst) }) })
				collector := newCollectSender()
				cs := &channelSender{
					channelID: "test-ch", session: workerSession, maxReassembled: 1 << 20,
					sendFn: func(msg *leapmuxv1.ConnectRequest) error {
						if len(collector.messages()) == 0 {
							close(firstFrame)
							<-releaseFirst
						}
						return collector.send(msg)
					},
				}
				large := bytes.Repeat([]byte("x"), contracts.MaxPlaintextPerChunk+100)
				largeDone := make(chan error, 1)
				go func() { largeDone <- cs.sendStream(1, &leapmuxv1.InnerStreamMessage{Payload: large}) }()
				// A first frame that never starts deadlocks the bubble, which fails the test.
				<-firstFrame
				smallID := uint64(2)
				if sameID {
					smallID = 1
				}
				smallDone := make(chan error, 1)
				go func() { smallDone <- cs.sendStream(smallID, &leapmuxv1.InnerStreamMessage{Payload: []byte("small")}) }()
				synctest.Wait()
				require.Equal(t, 1, cs.gate.SendWaiters(), "the second message must wait before the first frame returns")
				release.Do(func() { close(releaseFirst) })
				require.NoError(t, <-largeDone)
				require.NoError(t, <-smallDone)
				parts := map[uint64][]byte{}
				var messages []*leapmuxv1.InnerStreamMessage
				for _, msg := range collector.messages() {
					frame := msg.GetChannelMessageResp()
					plain, err := clientSession.Decrypt(frame.GetCiphertext())
					require.NoError(t, err)
					id := frame.GetCorrelationId()
					parts[id] = append(parts[id], plain...)
					if frame.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE {
						continue
					}
					var envelope leapmuxv1.InnerMessage
					require.NoError(t, proto.Unmarshal(parts[id], &envelope), "each final frame must complete one valid envelope")
					messages = append(messages, envelope.GetStream())
					delete(parts, id)
				}
				require.Len(t, messages, 2)
				assert.Empty(t, parts)
				if sameID {
					assert.Equal(t, large, messages[0].GetPayload())
					assert.Equal(t, []byte("small"), messages[1].GetPayload())
				} else {
					assert.Equal(t, []byte("small"), messages[0].GetPayload(), "another correlation can overtake a large message")
					assert.Equal(t, large, messages[1].GetPayload())
				}
			})
		})
	}
}
