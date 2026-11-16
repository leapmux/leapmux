package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeProjectedMessagesFollowOpaqueCursor(t *testing.T) {
	t.Parallel()
	var pages atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/session/ses_owner/message", r.URL.Path)
		assert.Equal(t, "200", r.URL.Query().Get("limit"))
		if pages.Add(1) == 1 {
			assert.Equal(t, "asc", r.URL.Query().Get("order"))
			assert.False(t, r.URL.Query().Has("cursor"))
			_, err := fmt.Fprint(w, `{"data":[{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"zero 0 and false"}]}],"cursor":{"next":"opaque/+?= 값"}}`)
			assert.NoError(t, err)
		} else {
			assert.Equal(t, "opaque/+?= 값", r.URL.Query().Get("cursor"))
			assert.False(t, r.URL.Query().Has("order"))
			_, err := fmt.Fprint(w, `{"data":[{"id":"msg_2","type":"idle","future":{"count":0,"enabled":false}}],"cursor":{"previous":"unused"}}`)
			assert.NoError(t, err)
		}
	})
	messages, err := collectOpenCodeMessagesForTest(t.Context(), rpc, "ses_owner")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "msg_1", messages[0].ID)
	assert.Equal(t, json.RawMessage(`{"id":"msg_2","type":"idle","future":{"count":0,"enabled":false}}`), messages[1].Raw)
	assert.Equal(t, int32(2), pages.Load())
}

func TestOpenCodeProjectedMessagesRejectMalformedRecovery(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"data":[],"cursor":null}`, `{"data":null,"cursor":{}}`, `{"data":[{"type":"assistant"}],"cursor":{}}`, `{"data":[{"id":"msg_1"}],"cursor":{}}`, `{"data":[{"id":"msg_1","type":"user"},{"id":"msg_1","type":"user"}],"cursor":{}}`, `{"data":[],"cursor":{"next":"cycle"}}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, err := fmt.Fprint(w, body)
				assert.NoError(t, err)
			})
			_, err := collectOpenCodeMessagesForTest(t.Context(), rpc, "ses_1")
			require.Error(t, err)
			assert.LessOrEqual(t, calls.Load(), int32(2))
		})
	}
}

func TestOpenCodeProjectedMessagesEmptyAndHTTPError(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session/ses_empty/message" {
			_, err := fmt.Fprint(w, `{"data":[],"cursor":{}}`)
			assert.NoError(t, err)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
	messages, err := collectOpenCodeMessagesForTest(t.Context(), rpc, "ses_empty")
	require.NoError(t, err)
	assert.NotNil(t, messages)
	assert.Empty(t, messages)
	_, err = collectOpenCodeMessagesForTest(t.Context(), rpc, "ses_refused")
	require.Error(t, err)
	_, err = collectOpenCodeMessagesForTest(t.Context(), rpc, "../other")
	require.Error(t, err)
}

func TestOpenCodeActualActivitySnapshot(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"data":{}}`, `{"data":{"ses_1":{"type":"running"},"ses_2":{"type":"running"}}}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/session/active", r.URL.Path)
				_, err := fmt.Fprint(w, body)
				assert.NoError(t, err)
			})
			active, err := rpc.activeSessions(t.Context())
			require.NoError(t, err)
			assert.NotNil(t, active)
			for _, running := range active {
				assert.True(t, running)
			}
		})
	}
}

func TestOpenCodeActualActivityRejectsInvalidSnapshot(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{"../other":{"type":"running"}}}`, `{"data":{"ses_1":{"type":"idle"}}}`, `{"data":{"ses_1":null}}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, err := fmt.Fprint(w, body); assert.NoError(t, err) })
			_, err := rpc.activeSessions(t.Context())
			require.Error(t, err)
		})
	}
}

func collectOpenCodeMessagesForTest(ctx context.Context, rpc openCodeRPC, sessionID string) ([]openCodeStoredMessage, error) {
	messages := []openCodeStoredMessage{}
	err := rpc.visitMessages(ctx, sessionID, func(message openCodeStoredMessage) error { messages = append(messages, message); return nil })
	return messages, err
}

func TestOpenCodePendingInboxValidatesNativeOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		valid      bool
		count      int
	}{
		{"empty", `{"data":[]}`, true, 0},
		{"all input types", `{"data":[{"id":"msg_1","sessionID":"ses_1","type":"user"},{"id":"msg_2","sessionID":"ses_1","type":"synthetic"},{"id":"msg_3","sessionID":"ses_1","type":"compaction"},{"id":"msg_4","sessionID":"ses_1","type":"move"}]}`, true, 4},
		{"absent data", `{}`, false, 0},
		{"null data", `{"data":null}`, false, 0},
		{"missing ID", `{"data":[{"sessionID":"ses_1","type":"user"}]}`, false, 0},
		{"invalid ID", `{"data":[{"id":"../foreign","sessionID":"ses_1","type":"user"}]}`, false, 0},
		{"foreign owner", `{"data":[{"id":"msg_1","sessionID":"ses_other","type":"user"}]}`, false, 0},
		{"duplicate ID", `{"data":[{"id":"msg_1","sessionID":"ses_1","type":"user"},{"id":"msg_1","sessionID":"ses_1","type":"user"}]}`, false, 0},
		{"missing type", `{"data":[{"id":"msg_1","sessionID":"ses_1"}]}`, false, 0},
		{"unknown type", `{"data":[{"id":"msg_1","sessionID":"ses_1","type":"future"}]}`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/api/session/ses_1/inbox", r.URL.Path)
				_, err := fmt.Fprint(w, tc.body)
				assert.NoError(t, err)
			})
			items, err := rpc.pendingInbox(t.Context(), "ses_1")
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, items, tc.count)
		})
	}
}

func TestOpenCodeProjectedMessagesReduceOversizedMediaPages(t *testing.T) {
	for _, prefix := range []int{0, 200} {
		t.Run(fmt.Sprintf("small-prefix=%d", prefix), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			t.Cleanup(cancel)
			media := "data:image/jpeg;base64," + strings.Repeat("A", 4*((1<<20)+2)/3)
			suffix := `"type":"user","text":"attachment","files":[{"uri":"` + media + `","name":"image.jpg"}],"time":{"created":0}}`
			var callsMu sync.Mutex
			var limits, offsets []int
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
				if !assert.NoError(t, err) || !assert.Greater(t, limit, 0) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				offset := 0
				if cursor := r.URL.Query().Get("cursor"); cursor != "" {
					offset, err = strconv.Atoi(strings.TrimPrefix(cursor, "cursor:"))
					if !assert.NoError(t, err) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.False(t, r.URL.Query().Has("order"))
				} else {
					assert.Equal(t, "asc", r.URL.Query().Get("order"))
				}
				callsMu.Lock()
				limits = append(limits, limit)
				offsets = append(offsets, offset)
				callsMu.Unlock()
				end := min(prefix+50, offset+limit)
				if _, err = io.WriteString(w, `{"data":[`); err != nil {
					return
				}
				for i := offset; i < end; i++ {
					if i > offset {
						if _, err = io.WriteString(w, ","); err != nil {
							return
						}
					}
					if i < prefix {
						_, err = fmt.Fprintf(w, `{"id":"msg_%d","type":"user","text":"small"}`, i)
					} else {
						if _, err = fmt.Fprintf(w, `{"id":"msg_%d",`, i); err != nil {
							return
						}
						_, err = io.WriteString(w, suffix)
					}
					if err != nil {
						return
					}
				}
				next := ""
				if end < prefix+50 {
					next = fmt.Sprintf("cursor:%d", end)
				}
				_, _ = fmt.Fprintf(w, `],"cursor":{"next":%q}}`, next)
			})
			visited := 0
			err := rpc.visitMessages(ctx, "ses_1", func(message openCodeStoredMessage) error {
				i := visited
				assert.Equal(t, fmt.Sprintf("msg_%d", i), message.ID)
				if i >= prefix {
					var payload struct {
						Files []openCodePromptFile `json:"files"`
					}
					if !assert.NoError(t, json.Unmarshal(message.Raw, &payload)) {
						return fmt.Errorf("decode message %d", i)
					}
					if !assert.Len(t, payload.Files, 1) {
						return fmt.Errorf("message %d has no file", i)
					}
					assert.Equal(t, media, payload.Files[0].URI)
				}
				visited++
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, prefix+50, visited)
			callsMu.Lock()
			defer callsMu.Unlock()
			assert.Equal(t, 200, limits[0])
			assert.Contains(t, offsets, prefix)
			assert.Less(t, limits[len(limits)-1], 200)
		})
	}
}

func TestOpenCodeProjectedMessagesVisitBeforeFetchingTheNextPage(t *testing.T) {
	t.Parallel()
	for _, stop := range []string{"none", "visitor error", "cancellation"} {
		t.Run(stop, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			var pages, visits atomic.Int32
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				if pages.Add(1) == 1 {
					_, _ = fmt.Fprint(w, `{"data":[{"id":"msg_1","type":"user"},{"id":"msg_2","type":"user"}],"cursor":{"next":"two"}}`)
					return
				}
				assert.EqualValues(t, 2, visits.Load(), "the previous page must finish before the next request")
				_, _ = fmt.Fprint(w, `{"data":[{"id":"msg_3","type":"idle"}],"cursor":{}}`)
			})
			failure := errors.New("the visitor could not persist a message")
			var ids []string
			err := rpc.visitMessages(ctx, "ses_1", func(message openCodeStoredMessage) error {
				visits.Add(1)
				ids = append(ids, message.ID)
				if stop == "visitor error" {
					return failure
				}
				if stop == "cancellation" {
					cancel()
				}
				return nil
			})
			switch stop {
			case "none":
				require.NoError(t, err)
				assert.Equal(t, []string{"msg_1", "msg_2", "msg_3"}, ids)
				assert.EqualValues(t, 2, pages.Load())
			case "visitor error":
				require.ErrorIs(t, err, failure)
				assert.Equal(t, []string{"msg_1"}, ids)
				assert.EqualValues(t, 1, pages.Load())
			case "cancellation":
				require.ErrorIs(t, err, context.Canceled)
				assert.Equal(t, []string{"msg_1"}, ids)
				assert.EqualValues(t, 1, pages.Load())
			}
		})
	}
}

func TestOpenCodeProjectedMessagesValidateTheWholePageBeforeVisiting(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"msg_1","type":"user"},{"id":"msg_1","type":"user"}],"cursor":{}}`)
	})
	visits := 0
	err := rpc.visitMessages(t.Context(), "ses_1", func(openCodeStoredMessage) error { visits++; return nil })
	require.ErrorContains(t, err, "repeated message")
	assert.Zero(t, visits)
	require.Error(t, rpc.visitMessages(t.Context(), "ses_1", nil))
}

func TestOpenCodeProjectedMessagesRefuseOneOversizedMessage(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var callsMu sync.Mutex
	var limits []int
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		assert.NoError(t, err)
		callsMu.Lock()
		limits = append(limits, limit)
		callsMu.Unlock()
		if _, err = io.WriteString(w, `{"data":[{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"`); err != nil {
			return
		}
		chunk := strings.Repeat("x", 1<<20)
		for size := 0; size <= providerkit.MaxEndpointResponseBytes; size += len(chunk) {
			if _, err = io.WriteString(w, chunk); err != nil {
				return
			}
		}
		_, _ = io.WriteString(w, `"}]}],"cursor":{}}`)
	})
	visits := 0
	err := rpc.visitMessages(ctx, "ses_1", func(openCodeStoredMessage) error { visits++; return nil })
	require.ErrorIs(t, err, providerkit.ErrEndpointResponseTooLarge)
	assert.Zero(t, visits)
	callsMu.Lock()
	defer callsMu.Unlock()
	require.NotEmpty(t, limits)
	assert.Equal(t, 1, limits[len(limits)-1])
	assert.LessOrEqual(t, len(limits), 9, "page reduction must stop after trying one record")
}
