-- name: StoreControlResponseAnswer :execrows
-- Retain the exact data needed to finalize a reserved request instance.
--
-- The WHERE EXISTS term is NOT redundant with the Go guard in
-- worker/service/control_response.go, which refuses an answer whose
-- control_requests row is gone. That guard is the fast path, and it gives the
-- caller a clear error. This predicate closes the race between that guard's
-- read and this INSERT: an agent restart, a context clear, or a provider-side
-- cancel deletes the request row in between. Keep both.
INSERT INTO control_response_payloads (delivery_id, request_payload, response_content, resolved_content, plan_approval_settings, source_seq, follow_up, follow_up_kind, non_blocking, agent_session_id, agent_provider, input_id)
SELECT sqlc.arg(delivery_id), COALESCE(CAST(sqlc.arg(request_payload) AS BLOB), X''), COALESCE(CAST(sqlc.arg(response_content) AS BLOB), X''), COALESCE(CAST(sqlc.arg(resolved_content) AS BLOB), X''), COALESCE(CAST(sqlc.arg(plan_approval_settings) AS BLOB), X''), sqlc.arg(source_seq), sqlc.arg(follow_up), sqlc.arg(follow_up_kind), sqlc.arg(non_blocking), sqlc.arg(agent_session_id), sqlc.arg(agent_provider), sqlc.arg(input_id)
WHERE EXISTS (SELECT 1 FROM control_requests
WHERE agent_id = sqlc.arg(agent_id) AND request_id = sqlc.arg(request_id) AND claim_token = sqlc.arg(claim_token));

-- name: GetControlResponseAnswer :one
SELECT * FROM control_response_answers
WHERE agent_id = ? AND request_id = ? AND claim_token = ?;

-- name: ListControlResponsesAwaitingRecording :many
-- A delivered response still needs recording after its original request disappears.
SELECT * FROM control_response_answers
WHERE agent_id = sqlc.arg(agent_id) AND state = sqlc.arg(state)
ORDER BY source_seq, request_id, claim_token;

-- name: GetControlResponseRequestPayloadForInput :one
-- The request payload of the answer that queued one input. Resolution needs the
-- original request to build the provider reply; the dispatch gate above runs on
-- every prepare and snapshot, so it must not read this blob.
SELECT p.request_payload FROM control_response_payloads p CROSS JOIN control_deliveries d
WHERE p.delivery_id = d.id AND d.agent_id = ? AND p.input_id = ? AND p.input_id <> ''
LIMIT 1;

-- name: GetControlResponseSourcesForInput :many
-- Two rows suffice to reject an ambiguous input association.
-- CROSS JOIN starts from matching input payloads. An agent-first join scans its delivery history.
SELECT p.agent_session_id, p.agent_provider, d.state, p.execution_session_id
FROM control_response_payloads p CROSS JOIN control_deliveries d
WHERE p.delivery_id = d.id AND d.agent_id = ? AND p.input_id = ? AND p.input_id <> ''
LIMIT 2;

-- name: RecordControlResponseExecutionSession :execrows
-- Record the session created by an approved context replacement once.
-- The nonempty input predicate selects the partial index and excludes unrelated empty input IDs.
UPDATE control_response_payloads SET execution_session_id = sqlc.arg(execution_session_id)
WHERE input_id = sqlc.arg(input_id) AND input_id <> ''
AND agent_session_id = sqlc.arg(agent_session_id) AND agent_provider = sqlc.arg(agent_provider)
AND execution_session_id = '' AND EXISTS (
    SELECT 1 FROM control_deliveries d WHERE d.id = control_response_payloads.delivery_id
    AND d.agent_id = sqlc.arg(agent_id) AND d.state = sqlc.arg(state)
);
