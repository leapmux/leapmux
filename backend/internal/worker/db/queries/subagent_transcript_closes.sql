-- name: ClaimSubagentTranscriptClose :execrows
-- Atomically record that this subagent transcript's closing divider is being
-- written, and report whether THIS caller won: 1 row affected means it did, 0
-- means another writer already claimed it.
--
-- Fail-open on a DB error is the caller's choice, and it is the safe one here:
-- the cost of a lost claim is a MISSING divider (a transcript that never
-- visibly ends, with a thinking indicator that never resolves), which is worse
-- than the duplicate a fail-open can produce.
INSERT INTO subagent_transcript_closes (child_agent_id) VALUES (?)
ON CONFLICT (child_agent_id) DO NOTHING;
