-- name: ClaimControlResponseAnswer :execrows
-- Atomically record that (agent_id, request_id, claim_token) is being answered and report whether THIS
-- call won: 1 rows affected means the INSERT succeeded (the first/only answer for this request
-- INSTANCE), 0 means a row already existed (a duplicate -- an RPC retry, or a second window answering
-- the same instance, both echoing the SAME claim_token). Being a durable row, the claim survives a
-- worker-process restart, so a duplicate straddling one is still deduped (#258).
--
-- claim_token makes the dedup INSTANCE-scoped: a REUSED request_id gets a fresh token per instance, so
-- the new instance's genuine answer claims a distinct key and is never rejected as a duplicate of the
-- prior instance's -- while a stale duplicate of the prior instance (old token) still loses. No
-- release-on-reissue is needed; rows are cleaned up in bulk with the agent via ON DELETE CASCADE.
INSERT INTO control_response_answers (agent_id, request_id, claim_token) VALUES (?, ?, ?)
ON CONFLICT (agent_id, request_id, claim_token) DO NOTHING;
