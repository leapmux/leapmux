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

-- name: ReleaseSubagentTranscriptClose :exec
-- Give the claim back, so the NEXT completion of this subagent writes the next
-- closing divider.
--
-- A subagent transcript holds one divider for each completion, not one for its
-- whole life: Claude revives a finished subagent when the parent messages it,
-- and the revived run ends the same way the first one did. The claim is what
-- keeps the two writers of a single close (the registry and the child's own
-- turn end) from doubling it, so the revive releases it rather than working
-- around it.
DELETE FROM subagent_transcript_closes WHERE child_agent_id = ?;
