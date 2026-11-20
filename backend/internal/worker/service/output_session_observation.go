package service

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type sessionObservationIdentity struct{ marker byte }

type capturedSessionObservationWriter struct{ source nativeSessionSource }

func (sink *agentOutputSink) SessionObservationWriterFor(captured agent.CapturedTranscript) (agent.CapturedSessionObservationWriter, error) {
	if captured.FrozenContent().AgentSessionID == "" {
		return nil, errors.New("the captured session observation has no native session")
	}
	source, err := sink.capturedNativeSessionSource(captured)
	if err != nil {
		return nil, err
	}
	sink.catalogMu.Lock()
	defer sink.catalogMu.Unlock()
	release := source.lock()
	defer release()
	if !source.currentLocked() {
		return nil, errors.New("the captured session observation expired")
	}
	source.observation = &sessionObservationIdentity{marker: 1}
	sink.sessionObservation = source.observation
	return &capturedSessionObservationWriter{source: source}, nil
}

func (writer *capturedSessionObservationWriter) PersistSettingsRefresh(refresh optionmap.Map) {
	sink := writer.source.sink
	sink.persistSettingsRefresh(refresh, sink.beginCatalogOperationFrom(writer.source))
}

func (writer *capturedSessionObservationWriter) PublishSessionInfo(info map[string]interface{}) bool {
	prepared, err := prepareSessionInfo(info)
	if err != nil || len(prepared) == 0 {
		return false
	}
	sink := writer.source.sink
	sink.catalogMu.Lock()
	release := writer.source.lock()
	if !writer.source.currentLocked() {
		release()
		sink.catalogMu.Unlock()
		return false
	}
	enqueued := sink.enqueuePreparedSessionInfo(prepared)
	release()
	sink.catalogMu.Unlock()
	if enqueued {
		sink.h.watcher.DrainAgentEvents(sink.agentID)
	}
	return true
}

func (writer *capturedSessionObservationWriter) Retire() {
	sink := writer.source.sink
	sink.catalogMu.Lock()
	if sink.sessionObservation == writer.source.observation {
		sink.sessionObservation = nil
	}
	sink.catalogMu.Unlock()
}

// prepareSessionInfo freezes JSON outside admission; custom marshalers may call services.
func prepareSessionInfo(info map[string]interface{}) (map[string]json.RawMessage, error) {
	prepared := make(map[string]json.RawMessage, len(info))
	for key, value := range info {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		prepared[key] = encoded
	}
	return prepared, nil
}

// enqueuePreparedSessionInfo updates the cache and queues its event under the same lock.
func (sink *agentOutputSink) enqueuePreparedSessionInfo(prepared map[string]json.RawMessage) bool {
	sink.sessionInfoMu.Lock()
	defer sink.sessionInfoMu.Unlock()
	if sink.lastSessionInfo == nil {
		sink.lastSessionInfo = make(map[string][]byte, len(prepared))
	}
	changed := make(map[string]interface{}, len(prepared))
	for key, encoded := range prepared {
		if key == contracts.SessionInfoKeyModelAPIRetry && sink.modelAPIRetryProcessExited {
			continue
		}
		if _, exempt := dedupExemptSessionInfoKeys[key]; exempt {
			if key == contracts.SessionInfoKeyRunningTool {
				sink.cachePermissionCheckLocked(encoded)
			}
			changed[key] = encoded
			continue
		}
		if previous, found := sink.lastSessionInfo[key]; found && bytes.Equal(previous, encoded) {
			continue
		}
		sink.lastSessionInfo[key] = encoded
		changed[key] = encoded
	}
	if len(changed) == 0 {
		return false
	}
	event := sessionInfoEvent(sink.agentID, changed)
	if event == nil {
		return false
	}
	sink.h.watcher.EnqueueAgentEvent(sink.agentID, event)
	return true
}
