package channel

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSendStreamPreservesCompleteMessagesForOneCorrelation(t *testing.T) {
	t.Parallel()
	for _, sameID := range []bool{true, false} {
		t.Run(map[bool]string{true: "same correlation", false: "different correlations"}[sameID], func(t *testing.T) {
			t.Parallel()
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
			select {
			case <-firstFrame:
			case <-time.After(30 * time.Second):
				t.Fatal("the first stream frame did not start")
			}
			smallID := uint64(2)
			if sameID {
				smallID = 1
			}
			smallDone := make(chan error, 1)
			go func() { smallDone <- cs.sendStream(smallID, &leapmuxv1.InnerStreamMessage{Payload: []byte("small")}) }()
			require.Eventually(t, func() bool { return cs.gate.SendWaiters() == 1 }, 30*time.Second, time.Millisecond,
				"the second message must wait before the first frame returns")
			release.Do(func() { close(releaseFirst) })
			for _, done := range []chan error{largeDone, smallDone} {
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(30 * time.Second):
					t.Fatal("the stream send did not finish")
				}
			}
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
	}
}
