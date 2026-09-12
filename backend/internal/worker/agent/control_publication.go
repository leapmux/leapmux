package agent

import "log/slog"

const controlPublicationFailure = "LeapMux could not store this control request."

// publishControlRequest returns a JSON-RPC error when storage prevents publication.
func (b *jsonrpcBase) publishControlRequest(sink ControlServices, requestID string, content []byte) {
	if err := sink.PublishControlRequest(ControlRequest{RequestID: requestID, Payload: content}); err != nil {
		slog.Error("publish control request", "agent_id", b.agentID, "request_id", requestID, "error", err)
		wireID, _, ok := ExtractJSONRPCID(content)
		if !ok {
			slog.Warn("cannot reply to control request without a wire ID", "agent_id", b.agentID, "request_id", requestID)
			return
		}
		if err := b.sendErrorResponse(wireID, -32603, controlPublicationFailure); err != nil {
			slog.Warn("send control request failure", "agent_id", b.agentID, "request_id", requestID, "error", err)
		}
	}
}
