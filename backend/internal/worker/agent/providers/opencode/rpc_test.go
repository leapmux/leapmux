package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openCodeTestRPC(t *testing.T, handle http.HandlerFunc) openCodeRPC {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !assert.True(t, ok) || !assert.Equal(t, "opencode", user) || !assert.Equal(t, "private-test-password", password) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handle(w, r)
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BasicAuth("opencode", "private-test-password"))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return openCodeRPC{endpoint: endpoint, timeout: 30 * time.Second}
}

func TestOpenCodeRouteID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"ses_abc-123", "form_01ABC", strings.Repeat("a", 512)} {
		assert.NoError(t, openCodeRouteID(id))
	}
	for _, id := range []string{"", "../ses_other", "ses_1/permission", "ses_1?resume=true", "ses_1#fragment", "ses_%2f", "ses_\x00", "ses_中", strings.Repeat("a", 513)} {
		assert.Error(t, openCodeRouteID(id), "%q", id)
	}
}

func TestOpenCodeRPCRequiresDataEnvelope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		succeeds   bool
	}{
		{"zero", `{"data":0}`, true},
		{"absent", `{}`, false},
		{"null", `{"data":null}`, false},
		{"empty", "", false},
		{"malformed", `{"data":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/model", r.URL.Path)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := openCodeData[int](context.Background(), rpc, http.MethodGet, "/api/model", nil)
			if tc.succeeds {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestOpenCodeRPCSessionAndPromptRequests(t *testing.T) {
	t.Parallel()
	var prompts atomic.Int64
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/session":
			var body openCodeSessionCreate
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "/workspace with spaces", body.Location.Directory)
			assert.Equal(t, &openCodeModelRef{ProviderID: "provider", ID: "model/name", Variant: "high"}, body.Model)
			assert.Equal(t, []openCodePermissionRule{{Action: "*", Resource: "*", Effect: "ask"}}, body.Permissions)
			_, _ = w.Write([]byte(`{"data":{"id":"ses_owner","agent":"build","model":{"providerID":"provider","id":"model/name","variant":"high"}}}`))
		case "GET /api/session/ses_owner":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_owner"}}`))
		case "POST /api/session/ses_owner/prompt":
			var body openCodePrompt
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Empty(t, body.Text)
			assert.Equal(t, []openCodePromptFile{{URI: "file:///workspace/image.png", Name: "image.png"}}, body.Files)
			assert.Contains(t, []string{"queue", "steer"}, body.Delivery)
			prompts.Add(1)
			_, _ = w.Write([]byte(`{"data":{"id":"msg_1","sessionID":"ses_owner","type":"user"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_, err := rpc.createSession(context.Background(), openCodeSessionCreate{
		Location:    &openCodeLocation{Directory: "/workspace with spaces"},
		Model:       &openCodeModelRef{ProviderID: "provider", ID: "model/name", Variant: "high"},
		Permissions: []openCodePermissionRule{{Action: "*", Resource: "*", Effect: "ask"}},
	})
	require.NoError(t, err)
	_, err = rpc.session(context.Background(), "ses_owner")
	require.NoError(t, err)
	for _, delivery := range []string{"queue", "steer"} {
		_, err = rpc.prompt(context.Background(), "ses_owner", openCodePrompt{
			Files: []openCodePromptFile{{URI: "file:///workspace/image.png", Name: "image.png"}}, Delivery: delivery,
		})
		require.NoError(t, err)
	}
	_, err = rpc.prompt(context.Background(), "ses_owner", openCodePrompt{Delivery: "unknown"})
	require.Error(t, err)
	assert.EqualValues(t, 2, prompts.Load(), "an invalid delivery must send no request")
}

func TestOpenCodeRPCRejectsForeignReceipts(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"data":{"id":"msg_1","sessionID":"ses_other","type":"user"}}`,
		`{"data":{"id":"","sessionID":"ses_owner","type":"user"}}`,
		`{"data":{"id":"msg_1","sessionID":"ses_owner","type":"synthetic"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			_, err := rpc.prompt(context.Background(), "ses_owner", openCodePrompt{Delivery: "queue"})
			require.ErrorContains(t, err, "matching inbox receipt")
		})
	}
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"data":{"id":"ses_other"}}`)) })
	_, err := rpc.session(context.Background(), "ses_owner")
	require.ErrorContains(t, err, "another session")
}

func TestOpenCodeRPCControlsPreserveZeroAndFalse(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/session/ses_owner/permission/per_1/reply":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, map[string]any{"decision": "reject", "message": "declined"}, body)
		case "POST /api/session/ses_owner/form/form_1/reply":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, map[string]any{"answer": map[string]any{"count": float64(0), "enabled": false, "text": ""}}, body)
		case "DELETE /api/session/ses_owner/form/form_1":
		default:
			t.Errorf("unexpected control request: %s %s", r.Method, r.URL)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	require.NoError(t, rpc.replyPermission(context.Background(), "ses_owner", "per_1", "reject", "declined"))
	require.NoError(t, rpc.replyForm(context.Background(), "ses_owner", "form_1", map[string]any{"count": 0, "enabled": false, "text": ""}))
	require.NoError(t, rpc.cancelForm(context.Background(), "ses_owner", "form_1"))
	require.Error(t, rpc.replyPermission(context.Background(), "ses_owner", "per_1", "allow", ""))
	require.Error(t, rpc.replyForm(context.Background(), "ses_owner", "../form_1", nil))
	require.Error(t, rpc.cancelForm(context.Background(), "../ses_owner", "form_1"))
	assert.EqualValues(t, 3, requests.Load())
}

func TestOpenCodeRPCReplayRequiresACompleteOwnedLog(t *testing.T) {
	t.Parallel()
	native := `{"id":"evt_2","type":"session.execution.succeeded","data":{"sessionID":"ses_owner"},"durable":{"aggregateID":"ses_owner","seq":2,"version":1}}`
	for _, tc := range []struct {
		name, ending string
		succeeds     bool
	}{
		{"complete", "data: {\"type\":\"log.synced\",\"aggregateID\":\"ses_owner\",\"seq\":2}\n\n", true},
		{"missing marker", "", false},
		{"foreign marker", "data: {\"type\":\"log.synced\",\"aggregateID\":\"ses_other\"}\n\n", false},
		{"truncated marker", "data: {\"type\":\"log.synced\",\"aggregateID\":\"ses_owner\"}", false},
		{"malformed marker", "data: {\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/experimental/session/ses_owner/log", r.URL.Path)
				assert.Equal(t, "1", r.URL.Query().Get("after"))
				assert.Equal(t, "false", r.URL.Query().Get("follow"))
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n%s", native, tc.ending)
			})
			var observed []json.RawMessage
			after := int64(1)
			err := rpc.replay(context.Background(), "ses_owner", &after, func(raw json.RawMessage) error { observed = append(observed, raw); return nil })
			if tc.succeeds {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Len(t, observed, 1)
			assert.Equal(t, native, string(observed[0]), "the transport preserves native bytes")
		})
	}
}

func TestOpenCodeRPCReplayPreservesDispatchFailure(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: {\"type\":\"session.text.ended\"}\n\n"))
	})
	failure := errors.New("the transcript write failed")
	after := int64(0)
	err := rpc.replay(context.Background(), "ses_owner", &after, func(json.RawMessage) error { return failure })
	assert.ErrorIs(t, err, failure)
}

func TestOpenCodeRPCCancellationEndsTheRequest(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	rpc := openCodeTestRPC(t, func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })
	deadline, endDeadline := context.WithTimeout(t.Context(), 30*time.Second)
	defer endDeadline()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := rpc.session(ctx, "ses_owner"); finished <- err }()
	<-entered
	cancel()
	assert.ErrorIs(t, <-finished, context.Canceled)
}

func TestOpenCodeRPCInterruptRequiresAnExplicitOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body          string
		active, valid bool
	}{
		{`{"interrupted":false}`, false, true},
		{`{"interrupted":true}`, true, true},
		{`{}`, false, false},
		{`{"interrupted":null}`, false, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "/api/session/ses_owner/interrupt", r.URL.Path)
				_, _ = w.Write([]byte(tc.body))
			})
			active, err := rpc.interrupt(context.Background(), "ses_owner")
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.active, active)
			} else {
				require.ErrorContains(t, err, "no interrupt outcome")
			}
		})
	}
}

func TestOpenCodeRPCPreservesHTTPRefusals(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"the session is busy"}`))
	})
	_, err := rpc.prompt(context.Background(), "ses_owner", openCodePrompt{Delivery: "queue"})
	var refusal *providerkit.HTTPStatusError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, http.StatusConflict, refusal.StatusCode)
	assert.Contains(t, refusal.Body, "the session is busy")
}

func TestOpenCodeRPCSelectionRoutes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var received []map[string]any
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, body)
		index := len(received)
		mu.Unlock()
		if index == 1 {
			assert.Equal(t, "/api/session/ses_1/agent", r.URL.Path)
		} else {
			assert.Equal(t, "/api/session/ses_1/model", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	require.NoError(t, rpc.switchAgent(t.Context(), "ses_1", "review/정확"))
	require.NoError(t, rpc.switchModel(t.Context(), "ses_1", openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "auto"}))
	require.NoError(t, rpc.switchModel(t.Context(), "ses_1", openCodeModelRef{ProviderID: "provider", ID: "model"}))
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []map[string]any{{"agent": "review/정확"}, {"model": map[string]any{"providerID": "provider", "id": "model", "variant": "auto"}}, {"model": map[string]any{"providerID": "provider", "id": "model"}}}, received)
}

func TestOpenCodeRPCSelectionRejectsMissingValuesBeforeHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	for _, value := range []string{"", " \n"} {
		assert.Error(t, rpc.switchAgent(t.Context(), "ses_1", value))
	}
	for _, model := range []openCodeModelRef{{}, {ProviderID: "provider"}, {ID: "model"}, {ProviderID: " ", ID: "model"}} {
		assert.Error(t, rpc.switchModel(t.Context(), "ses_1", model))
	}
	assert.Error(t, rpc.switchModel(t.Context(), "../other", openCodeModelRef{ProviderID: "p", ID: "m"}))
	assert.Zero(t, calls.Load())
}

func TestOpenCodeRPCCompactionAdmission(t *testing.T) {
	t.Parallel()
	for _, delivery := range []string{"", "queue", "steer"} {
		t.Run(delivery, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/api/session/ses_1/compact", r.URL.Path)
				var body map[string]any
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, "msg_compact", body["id"])
				if delivery == "" {
					assert.NotContains(t, body, "delivery")
				} else {
					assert.Equal(t, delivery, body["delivery"])
				}
				_, err := fmt.Fprint(w, `{"data":{"id":"msg_compact","sessionID":"ses_1","type":"compaction"}}`)
				assert.NoError(t, err)
			})
			receipt, err := rpc.compact(t.Context(), "ses_1", openCodeCompactionRequest{ID: "msg_compact", Delivery: delivery})
			require.NoError(t, err)
			assert.Equal(t, "msg_compact", receipt.ID)
		})
	}
}

func TestOpenCodeRPCCompactionRejectsUnownedOrMissingReceipts(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{"id":"msg_other","sessionID":"ses_1","type":"compaction"}}`, `{"data":{"id":"msg_compact","sessionID":"ses_other","type":"compaction"}}`, `{"data":{"id":"msg_compact","sessionID":"ses_1","type":"user"}}`, `{"data":{"id":"","sessionID":"ses_1","type":"compaction"}}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, err := fmt.Fprint(w, body); assert.NoError(t, err) })
			_, err := rpc.compact(t.Context(), "ses_1", openCodeCompactionRequest{ID: "msg_compact"})
			assert.Error(t, err)
		})
	}
}

func TestOpenCodeRPCPendingControlSnapshots(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ owner, resource string }{{"ses_1", "form"}, {"ses_1", "permission"}, {"global", "form"}} {
		t.Run(test.owner+"/"+test.resource, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/api/session/"+test.owner+"/"+test.resource, r.URL.Path)
				_, err := fmt.Fprintf(w, `{"data":[{"id":"request_1","sessionID":%q,"value":{"count":0,"enabled":false}}]}`, test.owner)
				assert.NoError(t, err)
			})
			var records []json.RawMessage
			var err error
			if test.resource == "form" {
				records, err = rpc.pendingForms(t.Context(), test.owner)
			} else {
				records, err = rpc.pendingPermissions(t.Context(), test.owner)
			}
			require.NoError(t, err)
			require.Len(t, records, 1)
			assert.JSONEq(t, fmt.Sprintf(`{"id":"request_1","sessionID":%q,"value":{"count":0,"enabled":false}}`, test.owner), string(records[0]))
		})
	}
}

func TestOpenCodeRPCPendingControlsRejectMalformedOrForeignRecords(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":[null]}`, `{"data":[{}]}`, `{"data":[{"id":"x","sessionID":"ses_other"}]}`, `{"data":[{"id":"x","sessionID":"ses_1"},{"id":"x","sessionID":"ses_1"}]}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, err := fmt.Fprint(w, body); assert.NoError(t, err) })
			_, err := rpc.pendingForms(t.Context(), "ses_1")
			assert.Error(t, err)
		})
	}
}

func TestOpenCodeRPCPromptPreservesExplicitNativeIdentity(t *testing.T) {
	t.Parallel()
	for _, receiptID := range []string{"msg_requested", "msg_other"} {
		t.Run(receiptID, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, "msg_requested", body["id"])
				_, err := fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, receiptID)
				assert.NoError(t, err)
			})
			var request openCodePrompt
			require.NoError(t, json.Unmarshal([]byte(`{"id":"msg_requested","text":"native identity","delivery":"queue"}`), &request))
			_, err := rpc.prompt(t.Context(), "ses_1", request)
			if receiptID == "msg_requested" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestOpenCodeRPCPromptRejectsUnsafeExplicitIdentityBeforeHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, err := fmt.Fprint(w, `{"data":{"id":"msg_other","sessionID":"ses_1","type":"user"}}`)
		assert.NoError(t, err)
	})
	var request openCodePrompt
	require.NoError(t, json.Unmarshal([]byte(`{"id":"../other","text":"native identity","delivery":"queue"}`), &request))
	_, err := rpc.prompt(t.Context(), "ses_1", request)
	require.Error(t, err)
	assert.Zero(t, calls.Load())
}

func TestOpenCodeRPCReplayDistinguishesInitialReadFromSequenceZero(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		after int64
		empty bool
		want  []int64
	}{
		{"initial", -1, false, []int64{0, 1}},
		{"after zero", 0, false, []int64{1}},
		{"after one", 1, false, nil},
		{"large cursor", 1<<53 - 1, false, nil},
		{"empty initial log", -1, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "false", r.URL.Query().Get("follow"))
				after := int64(-1)
				if r.URL.Query().Has("after") {
					parsed, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
					assert.NoError(t, err)
					after = parsed
				}
				assert.Equal(t, tc.after, after)
				if !tc.empty {
					for seq := int64(0); seq < 2; seq++ {
						if seq <= after {
							continue
						}
						_, _ = fmt.Fprintf(w, "data: {\"id\":\"evt_%d\",\"type\":\"session.created\",\"data\":{\"sessionID\":\"ses_1\"},\"durable\":{\"aggregateID\":\"ses_1\",\"seq\":%d}}\n\n", seq, seq)
					}
				}
				_, _ = fmt.Fprint(w, "data: {\"type\":\"log.synced\",\"aggregateID\":\"ses_1\",\"seq\":1}\n\n")
			})
			var observed []int64
			var cursor *int64
			if tc.after >= 0 {
				cursor = &tc.after
			}
			err := rpc.replay(t.Context(), "ses_1", cursor, func(raw json.RawMessage) error {
				var event struct {
					Durable struct {
						Seq int64 `json:"seq"`
					} `json:"durable"`
				}
				if err := json.Unmarshal(raw, &event); err != nil {
					return err
				}
				observed = append(observed, event.Durable.Seq)
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, observed)
		})
	}
}

func TestOpenCodeRPCReplayRejectsInvalidArgumentsBeforeHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	negative := int64(-1)
	require.Error(t, rpc.replay(t.Context(), "ses_1", &negative, func(json.RawMessage) error { return nil }))
	require.Error(t, rpc.replay(t.Context(), "ses_1", nil, nil))
	assert.Zero(t, calls.Load())
}

type trackedOpenCodeSSEBody struct {
	io.Reader
	closed atomic.Bool
}

func (body *trackedOpenCodeSSEBody) Close() error { body.closed.Store(true); return nil }

func TestReadOpenCodeSSEClosesBodiesAndPreservesTheFirstFailure(t *testing.T) {
	t.Parallel()
	readFailure, handlerFailure := errors.New("the stream read failed"), errors.New("the event handler failed")
	for _, tc := range []struct {
		name             string
		reader           io.Reader
		handlerErr, want error
		calls            int
	}{
		{"clean EOF", strings.NewReader(""), nil, nil, 0},
		{"read failure", iotest.ErrReader(readFailure), nil, readFailure, 0},
		{"buffered events after failure", io.MultiReader(strings.NewReader("data: first\n\ndata: second\n\n"), iotest.ErrReader(readFailure)), handlerFailure, handlerFailure, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := &trackedOpenCodeSSEBody{Reader: tc.reader}
			calls := 0
			err := readOpenCodeSSE(body, func(providerkit.SSEEvent) error { calls++; return tc.handlerErr })
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			assert.Equal(t, tc.calls, calls)
			assert.True(t, body.closed.Load())
		})
	}
}

type blockedOpenCodeSSEBody struct {
	first  []byte
	closed chan struct{}
	once   sync.Once
}

func (body *blockedOpenCodeSSEBody) Read(buffer []byte) (int, error) {
	if len(body.first) > 0 {
		n := copy(buffer, body.first)
		body.first = body.first[n:]
		return n, nil
	}
	<-body.closed
	return 0, io.ErrClosedPipe
}
func (body *blockedOpenCodeSSEBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

func TestReadOpenCodeSSEHandlerFailureReleasesABlockedReader(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	body := &blockedOpenCodeSSEBody{first: []byte("data: first\n\n"), closed: make(chan struct{})}
	t.Cleanup(func() { _ = body.Close() })
	failure := errors.New("the event handler failed")
	finished := make(chan error, 1)
	go func() { finished <- readOpenCodeSSE(body, func(providerkit.SSEEvent) error { return failure }) }()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, failure)
	case <-ctx.Done():
		t.Fatal("the handler failure did not close the blocked stream")
	}
}
