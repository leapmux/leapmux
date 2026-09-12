package agent

import "log/slog"

const controlPublicationFailure = "LeapMux could not store this control request."

// publishControlRequest returns a JSON-RPC error when storage prevents publication.
func (b *jsonrpcBase) publishControlRequest(sink ControlServices, content []byte) {
	wireID, _, found := ExtractJSONRPCID(content)
	requestID, valid := JSONRPCControlRequestID(wireID)
	if !found || !valid {
		slog.Warn("cannot publish control request without a valid JSON-RPC ID", "agent_id", b.agentID)
		return
	}
	if err := sink.PublishControlRequest(ControlRequest{RequestID: requestID, Payload: content}); err != nil {
		slog.Error("publish control request", "agent_id", b.agentID, "request_id", requestID, "error", err)
		if err := b.sendErrorResponse(wireID, -32603, controlPublicationFailure); err != nil {
			slog.Warn("send control request failure", "agent_id", b.agentID, "request_id", requestID, "error", err)
		}
	}
}
