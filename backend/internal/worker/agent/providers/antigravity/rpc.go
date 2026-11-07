package antigravity

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	languageServerService    = "/exa.language_server_pb.LanguageServerService/"
	antigravityMaxFrameBytes = 32 << 20
	connectJSONContentType   = "application/connect+json"
)

var connectHeaders = http.Header{"Connect-Protocol-Version": {"1"}}

var errLanguageServerNotConnected = errors.New("antigravity language server is not connected")

type antigravityRPC struct {
	endpoint *providerkit.HTTPEndpoint
	timeout  time.Duration
}

func (r antigravityRPC) do(ctx context.Context, method string, body, out any) error {
	if r.endpoint == nil {
		return errLanguageServerNotConnected
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.endpoint.Do(ctx, http.MethodPost, languageServerService+method, body, out)
}

func (r antigravityRPC) sendUserMessage(ctx context.Context, request map[string]any) error {
	return r.deliver(ctx, "SendUserCascadeMessage", request)
}

func (r antigravityRPC) handleInteraction(ctx context.Context, cascadeID string, interaction map[string]any) error {
	return r.deliver(ctx, "HandleCascadeUserInteraction", map[string]any{"cascadeId": cascadeID, "interaction": interaction})
}

func (r antigravityRPC) deliver(ctx context.Context, method string, body any) error {
	err := r.do(ctx, method, body, nil)
	if errors.Is(err, errLanguageServerNotConnected) {
		return err
	}
	return providerkit.ClassifyHTTPDeliveryError("the Antigravity request", err)
}

func (r antigravityRPC) cancel(ctx context.Context, cascadeID string) error {
	return r.do(ctx, "CancelCascadeInvocation", map[string]any{"cascadeId": cascadeID, "killBackgroundTasks": true}, nil)
}

func (r antigravityRPC) modelConfig(ctx context.Context) (antigravityCatalogReply, error) {
	var reply antigravityCatalogReply
	err := r.do(ctx, "GetCascadeModelConfigData", map[string]any{}, &reply)
	return reply, err
}

func (r antigravityRPC) steps(ctx context.Context, cascadeID string, offset int) ([]json.RawMessage, error) {
	var reply struct {
		Steps []json.RawMessage `json:"steps"`
	}
	err := r.do(ctx, "GetCascadeTrajectorySteps", map[string]any{"cascadeId": cascadeID, "stepOffset": offset}, &reply)
	return reply.Steps, err
}

func (r antigravityRPC) slashCommands(ctx context.Context, cascadeID string, config map[string]any) ([]string, error) {
	var reply struct {
		Commands []struct {
			Info struct {
				Name string `json:"name"`
			} `json:"info"`
		} `json:"commands"`
	}
	if err := r.do(ctx, "GetSlashCommands", map[string]any{"cascadeId": cascadeID, "cascadeConfig": config}, &reply); err != nil {
		return nil, err
	}
	commands := make([]string, 0, len(reply.Commands))
	for _, command := range reply.Commands {
		if command.Info.Name != "" {
			commands = append(commands, command.Info.Name)
		}
	}
	return commands, nil
}

func (r antigravityRPC) status(ctx context.Context) error {
	return r.do(ctx, "GetStatus", map[string]any{}, nil)
}

var errStepCountRead = errors.New("antigravity step count read")

func (r antigravityRPC) readStepCount(ctx context.Context, conversationID string) (int, error) {
	var count int
	delivered, err := r.consumeStream(ctx, conversationID, pastEveryStep, func(update agentStateUpdate) error {
		count = update.MainTrajectory.StepsUpdate.TotalLength
		return errStepCountRead
	})
	if errors.Is(err, errStepCountRead) {
		return count, nil
	}
	if err != nil {
		return 0, err
	}
	if !delivered {
		return 0, io.EOF
	}
	return count, nil
}

func encodeStreamRequest(conversationID, subscriberID string, startIndex int) ([]byte, error) {
	if subscriberID == "" {
		subscriberID = id.Generate()
	}
	payload, err := json.Marshal(map[string]any{
		"conversationId":         conversationID,
		"subscriberId":           subscriberID,
		"initialStepsPageBounds": map[string]any{"startIndex": startIndex},
	})
	if err != nil {
		return nil, err
	}
	return encodeSingleFrame(payload), nil
}

type streamEndError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *streamEndError) Error() string {
	if e.Message != "" {
		return "Antigravity stream ended: " + e.Code + ": " + e.Message
	}
	return "Antigravity stream ended: " + e.Code
}

type frameSizeError struct{ size uint32 }

func (e frameSizeError) Error() string {
	return fmt.Sprintf("Antigravity stream frame of %d bytes exceeds the %d-byte limit", e.size, antigravityMaxFrameBytes)
}

func decodeStreamFrames(reader io.Reader, handler func(json.RawMessage) error) (bool, error) {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	delivered := false
	for {
		header := make([]byte, 5)
		if _, err := io.ReadFull(buffered, header); err != nil {
			return delivered, err
		}
		flags := header[0]
		length := binary.BigEndian.Uint32(header[1:5])
		if length > antigravityMaxFrameBytes {
			return delivered, frameSizeError{size: length}
		}
		payload := make([]byte, int(length))
		if _, err := io.ReadFull(buffered, payload); err != nil {
			return delivered, err
		}
		if flags&0x02 != 0 {
			var end struct {
				Error *streamEndError `json:"error"`
			}
			if err := json.Unmarshal(payload, &end); err != nil {
				return delivered, fmt.Errorf("decode the Antigravity end frame: %w", err)
			}
			if end.Error != nil {
				return delivered, end.Error
			}
			return delivered, io.EOF
		}
		if err := handler(payload); err != nil {
			return delivered, err
		}
		delivered = true
	}
}

func (r antigravityRPC) consumeStream(ctx context.Context, conversationID string, startIndex int, handler func(agentStateUpdate) error) (bool, error) {
	body, err := encodeStreamRequest(conversationID, id.Generate(), startIndex)
	if err != nil {
		return false, err
	}
	response, err := r.endpoint.OpenStreamBody(ctx, http.MethodPost, languageServerService+"StreamAgentStateUpdates", connectHeaders, connectJSONContentType, body)
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	return decodeStreamFrames(response.Body, func(raw json.RawMessage) error {
		var envelope agentStateEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Update) == 0 {
			if err == nil {
				err = fmt.Errorf("frame has no update")
			}
			return &undecodableFrameError{err: err}
		}
		var update agentStateUpdate
		if err := json.Unmarshal(envelope.Update, &update); err != nil {
			return &undecodableFrameError{err: err}
		}
		return handler(update)
	})
}

type undecodableFrameError struct{ err error }

func (e *undecodableFrameError) Error() string {
	return "undecodable Antigravity frame: " + e.err.Error()
}
func (e *undecodableFrameError) Unwrap() error { return e.err }

func isStreamFatalOpen(err error) bool {
	var status *providerkit.HTTPStatusError
	if !errors.As(err, &status) {
		return false
	}
	return status.StatusCode == http.StatusNotImplemented || status.StatusCode >= 400 && status.StatusCode < 500 && status.StatusCode != http.StatusTooManyRequests
}

func isStreamNotFound(err error) bool {
	var end *streamEndError
	return errors.As(err, &end) && (end.Code == "not_found" || end.Code == "unimplemented") || providerkit.IsHTTPStatus(err, http.StatusNotFound)
}

func isFrameOversized(err error) bool {
	var size frameSizeError
	return errors.As(err, &size)
}

func isUndecodableFrame(err error) bool {
	var decode *undecodableFrameError
	return errors.As(err, &decode)
}

func streamErrorText(err error) string {
	var status *providerkit.HTTPStatusError
	if errors.As(err, &status) && status.Body != "" {
		return status.Body
	}
	return err.Error()
}

func encodeSingleFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}
