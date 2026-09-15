-- name: ClaimControlResponseAnswer :execrows
-- Reserve one request instance and retain the exact data needed for finalization.
--
-- The WHERE EXISTS term is NOT redundant with the Go guard in
-- worker/service/control_response.go, which refuses an answer whose
-- control_requests row is gone. That guard is the fast path, and it gives the
-- caller a clear error. This predicate closes the race between that guard's
-- read and this INSERT: an agent restart, a context clear, or a provider-side
-- cancel deletes the request row in between. Keep both.
INSERT INTO control_response_answers (agent_id, request_id, claim_token, request_payload, response_content, resolved_content, plan_approval_settings, source_seq, feedback, agent_session_id, agent_provider, input_id)
SELECT sqlc.arg(agent_id), sqlc.arg(request_id), sqlc.arg(claim_token), COALESCE(CAST(sqlc.arg(request_payload) AS BLOB), X''), COALESCE(CAST(sqlc.arg(response_content) AS BLOB), X''), COALESCE(CAST(sqlc.arg(resolved_content) AS BLOB), X''), COALESCE(CAST(sqlc.arg(plan_approval_settings) AS BLOB), X''), sqlc.arg(source_seq), sqlc.arg(feedback), sqlc.arg(agent_session_id), sqlc.arg(agent_provider), sqlc.arg(input_id)
WHERE EXISTS (SELECT 1 FROM control_requests
WHERE agent_id = sqlc.arg(agent_id) AND request_id = sqlc.arg(request_id) AND claim_token = sqlc.arg(claim_token))
ON CONFLICT (agent_id, request_id, claim_token) DO NOTHING;

-- name: GetControlResponseAnswer :one
SELECT * FROM control_response_answers
WHERE agent_id = ? AND request_id = ? AND claim_token = ?;

-- name: ListControlResponsesAwaitingRecording :many
-- A delivered response still needs recording after its original request disappears.
SELECT * FROM control_response_answers
WHERE agent_id = sqlc.arg(agent_id) AND state = sqlc.arg(state)
ORDER BY source_seq, request_id, claim_token;

-- name: SetControlResponseDeliveryState :execrows
UPDATE control_response_answers SET state = sqlc.arg(state)
WHERE agent_id = sqlc.arg(agent_id) AND request_id = sqlc.arg(request_id) AND claim_token = sqlc.arg(claim_token)
AND state = sqlc.arg(required_state);

-- name: CompleteControlResponseAnswer :execrows
UPDATE control_response_answers SET state = sqlc.arg(state)
WHERE agent_id = sqlc.arg(agent_id) AND request_id = sqlc.arg(request_id)
AND claim_token = sqlc.arg(claim_token) AND state = sqlc.arg(required_state);

-- name: ReleaseUnsentControlResponseAnswer :exec
DELETE FROM control_response_answers
WHERE agent_id = sqlc.arg(agent_id) AND request_id = sqlc.arg(request_id)
AND claim_token = sqlc.arg(claim_token) AND state = sqlc.arg(state);

-- name: GetControlResponseSourcesForInput :many
-- Two rows suffice to reject an ambiguous input association.
SELECT agent_session_id, agent_provider, state, execution_session_id FROM control_response_answers
WHERE agent_id = ? AND input_id = ? AND input_id <> ''
LIMIT 2;

-- name: RecordControlResponseExecutionSession :execrows
-- Record the session created by this approved context replacement exactly once.
-- `input_id <> ''` repeats the predicate of the PARTIAL
-- idx_control_response_input, which SQLite matches syntactically: without that
-- term this UPDATE falls back to the primary key and reads every answer row of
-- the agent. It also refuses an EMPTY input_id, which would otherwise match
-- every row that shares the session, the provider and the state.
UPDATE control_response_answers SET execution_session_id = sqlc.arg(execution_session_id)
WHERE agent_id = sqlc.arg(agent_id) AND input_id = sqlc.arg(input_id) AND input_id <> ''
AND agent_session_id = sqlc.arg(agent_session_id) AND agent_provider = sqlc.arg(agent_provider)
AND state = sqlc.arg(state) AND execution_session_id = '';
