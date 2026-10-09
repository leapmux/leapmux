# Plan 01: Repair and complete the consolidated source

## Scope and prerequisites

Execute this plan on top of the current `HEAD`.
This file includes its implementation steps and required evidence.
Use this file and repository source without another plan file or an earlier session transcript.
Run one Task pipeline at a time across the repository.

## Current source and scope

Inspect the current `HEAD` before making changes.
Use its existing implementation and completed work as the starting point.
Verify this stage's prerequisites against the current source and acceptance evidence.
The branch contains unfinished changes from these areas:

- Captured transcript publication and replay.
- Compact notification fingerprints in message supplements.
- Goal publication and native session authority.
- Neutral completion without a known outcome.
- Background task status names and generated status tokens.
- Process exit ownership.
- MiMo native part identity and retained observations.
- Muse Code through its native `muse serve` protocol.
- Native test helpers and unfinished regression tests.

The consolidated MiMo source includes the earlier stash corrections.
Preserve its attribution and retry behavior while completing the remaining work.
Keep the later corrections where the original source layers overlap.
Do not restore obsolete provider interfaces or old numbered provider specs over their current replacements.

Determine current progress from `HEAD` and complete-file results for that source.
Wave 1 requires complete combined acceptance.
Muse requires acceptance of all 53 feature files.
Historical passing tests supply evidence about their source version only.
Accept a current result only when it matches the source under test.

The original matrix contains 29 providers and 53 features: 1,537 cells.
Muse adds 53 cells: the final matrix contains 30 providers and 1,590 cells.
Preserve the original 1,316 browser cases through 1,278 retained cases and 38 approved assertion merges.
The requirement inventory includes 150 ports, 1,530 target records, and 1,538 applicable pairs.
It also includes 47 native-setting groups with 343 targets and two source investigations.
Keep all 23 product gap records and all 18 native-setting gap cells.

## Verification context

Run the required checks against the current source.
Earlier Go syntax and duplicate-method checks passed.
Earlier full lint stopped in the script stage with exit code 201.
That run reported 24 style errors in these files:

- `scripts/contractsAreConsumed.test.mjs`.
- `scripts/startupOptionGroups.test.mjs`.

Inspect these files during the source repair stage and correct any remaining errors before full lint acceptance.
The later lint stages did not run in that attempt.
The consolidation run included no combined full test suite or browser acceptance run.
Verify the current test state and complete any missing red and green regression checks.

## Rules that apply to every stage

- Read each affected function and its callers before a change.
- Write a regression test before a bug fix.
- Run that test and confirm the expected failure before the fix.
- Run every new or changed test after the fix.
- Preserve an existing test's assertions unless the preservation records in this file specify an approved move or merge.
- Keep provider wire parsing inside its provider package and frontend extraction plugin.
- Keep the neutral chat model free of provider decisions and presentation code.
- Use generated contracts as the single source for shared values.
- Edit initial SQL schemas in place. Add no migration file.
- Exclude generated output from commits.
- Use the installed provider process with the local mock model server for browser proofs.
- Give every content turn a `modelScript.prompt(...)` marker.
- Use canonical builders from `frontend/tests/e2e/helpers/providerToolCalls.ts`.
- Create no message or control-request fixture through direct database writes.
- Use no external model endpoint. Use no real model.
- Use private provider configuration and credentials for every native probe.
- Require zero unexpected mock requests, zero retries, and zero skipped browser cases.
- Prove native behavior from actual request bytes, output frames, or durable native receipts.
- Do not infer a native limitation from a missing mock route or a LeapMux refusal.
- Scope visible chat and sidebar locators through the existing UI helpers.
- Wait for Worker startup and close verdicts. Optimistic tab removal does not prove process completion.
- Read complete failure logs before another run. Correct the cause before the next run.

For each requirement record, the listed browser host remains the cell's unique complete spec file.
A shared helper change also requires every caller's complete spec, including callers outside that feature directory.
A native absence can replace a proposed port only when the installed provider proves that absence.
Record the provider version and the deciding native evidence in the cell's `detailNote`.
A supported cell needs a positive native proof and a passing complete browser file.
A limitation cell needs the correct refusal proof and a passing complete browser file.

## Verification tools and evidence

Use these repository tools and data for stage acceptance:

- `scripts/audit-agent-case-preservation.mjs` and its co-located test.
- `scripts/verify-agent-feature-acceptance.mjs` and its co-located test.
- `scripts/verify-agent-source-freeze.mjs` and its co-located test.
- `testdata/agent-case-preservation.json` and its sibling schema.

The source repair stage creates these files.
Later stages require their accepted repository implementations.
Record the source tree and every source file's SHA-256.
Record full browser discovery for that source.
Accept a feature cell only when every discovered case in its complete file passes on the first attempt.
Reject stale discovery, changed source, retries, skipped cases, and expected failures.
Preserve the original case identity and exact assertion destination for each approved move or merge.


## Stage 1: Repair and complete the consolidated source

### 1.1 Establish the combined source and the preservation tools

Before: the branch combines source layers with different test histories.
After: one source inventory identifies every changed file and every required acceptance file.

Edit these files:

- `Taskfile.yaml`: add targets for the preservation and acceptance tools below.
- `scripts/audit-agent-case-preservation.mjs`: add the absent validator for original case mappings and final discovery.
- `scripts/audit-agent-case-preservation.test.mjs`: test missing, duplicate, moved, merged, and unchanged case mappings.
- `scripts/verify-agent-feature-acceptance.mjs`: add the absent complete-file acceptance validator.
- `scripts/verify-agent-feature-acceptance.test.mjs`: test incomplete reports, stale source identities, retries, skips, and duplicate cells.
- `scripts/verify-agent-source-freeze.mjs`: add the absent source identity recorder and validator.
- `scripts/verify-agent-source-freeze.test.mjs`: test modified bytes, modes, new files, deleted files, and changed discovery.
- `testdata/agent-case-preservation.json`: add the original 1,316 case identities and their exact final destinations.
- `testdata/agent-case-preservation.schema.json`: validate the preservation records.

Use this exact preservation record shape:

```json
{
  "originalFile": "frontend/tests/e2e/pi/close-an-agent.spec.ts",
  "originalCaseId": "568a568ce26d7af028b3-ba96ada01498214d231e",
  "originalTitle": "can close Pi agent tab",
  "disposition": "merged",
  "destinationFile": "frontend/tests/e2e/pi/close-an-agent.spec.ts",
  "destinationTitle": "closes the native agent and its actual owned process tree",
  "requiredAssertions": ["the tab closes", "the Worker confirms close", "the owned process tree exits"]
}
```

Require `disposition` to equal `retained`, `moved`, or `merged`.
Reject an absent destination, a lost required assertion, or an uncovered original case.
Store the final discovered case ID when a title move changes its ID.
Do not accept a title match alone as proof that the assertions survived.

The source identity includes these facts:

- HEAD and the effective source tree.
- Every source path and file mode.
- Every source file's SHA-256.
- New and deleted source files.
- Full browser discovery and selected complete-file manifests.

The acceptance validator accepts a cell only when every discovered case of its file passes on the first attempt.
Require the report and discovery to identify the same frozen source.
Reset historical `testStatus` values before final acceptance. Preserve support states and notes during that reset.

### 1.2 Finish the shared captured transcript mechanism

Before: the merged source stores captured messages and private notification identities, but complete combined acceptance remains absent.
After: every delayed write preserves its original transcript owner and native session without changing a replacement live turn.

Complete the files in the consolidated source path manifest below under these areas:

- `backend/internal/worker/agent/` for captured transcripts, service interfaces, notification parsing, and progress resets.
- `backend/internal/worker/agent/agenttest/` for recording sinks and real/fake parity.
- `backend/internal/worker/service/` for exact write admission, transactions, notification reduction, goals, and replay.
- `backend/internal/worker/db/` for initial schema, enum validation, and generated query inputs.
- `frontend/src/hooks/` for event replay and live effect admission.
- `frontend/src/stores/` for session authority and transcript state.
- `frontend/src/components/chat/` for notification projections and raw history.

Preserve these mechanisms:

1. Capture the exact outer publication owner before delayed work.
2. Preserve the native session fact from the original observation.
3. Admit a current write only for its exact sink and current native session fact.
4. Permit a stale captured write to persist in its original transcript.
5. Mark that write `transcript_only` so it changes no current live state.
6. Call provider code and callbacks outside shared lifecycle locks.
7. Keep a failed write's exact capture for a retry.
8. Release retained bytes only after the exact write succeeds.
9. Preserve current progress and registry activity when an old write retries.
10. Preserve the raw provider frame in history and raw JSON views.

Keep notifications in `messages.supplemental_content`, under Worker metadata.
Keep only compact keyed fingerprints for duplicate detection.
Store no second copy of the full raw notification for comparison.
The stored private record remains:

```json
{
  "metadata": {
    "notification_entries": [
      {
        "idempotency_key": "native-observation-1",
        "fingerprint_base64": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
      }
    ]
  }
}
```

The fingerprint contains exactly 32 SHA-256 bytes.
Compute it from provider, source, completion, and length-framed original bytes, supplement bytes, and metadata bytes.
Preserve distinctions in raw JSON bytes, including duplicate keys and numeric spellings.
Retain only the minimal existing `notification_reduction` facts that message projection requires.
Strip private journal and reduction fields from ordinary wire messages.
Preserve provider data and unrelated public metadata.
Reject corrupt private storage with the existing invalid-storage error.

Retain these schema and protocol changes already present in the merged source:

- Positive optional message enums use SQL `NULL` for absence.
- `messages.transcript_only` identifies a historical write without live effects.
- `AgentChatMessage.transcript_only = 22` carries that fact to the browser.
- `AgentEvent.replay_agent_id = 17` identifies the replay target.
- Goal `UNKNOWN = 5` and `DORMANT = 6` remain separate proto values.
- Stored active goal states accept ordinals 0 through 5. Dormant remains a derived state.
- Integer enum columns retain their `typeof(...)= 'integer'` checks and their contiguous ranges.

Test current and historical sessions with the same native IDs.
Test failed writes, exact retries, context replacement, callback reentry, and concurrent publication.
Test bigint message identities in frontend replay.
Test that stale history cannot clear current progress, goals, or background task state.
Keep the goal publication order: goal state, notification, then progress.
Retry the failed publication stage without repeating a completed stage.

### 1.3 Remove the measured notification lookup cost

Before: `findNotificationDuplicate` decompresses every message supplement in a session while the write lease stays held.
A measured 10,000-row case costs about 50–52 milliseconds and about 93 MB of allocations per lookup.
After: the duplicate query reads only messages whose generated count identifies a journal or corrupt private storage.

Edit these exact files:

- `backend/internal/worker/db/migrations/00001_initial.sql`.
- `backend/internal/worker/db/queries/messages.sql`.
- `backend/internal/worker/db/db.go`.
- `backend/internal/worker/db/notification_entry_count.go` (new).
- `backend/internal/worker/db/notification_entry_count_test.go` (new).
- `backend/internal/worker/agent/notification_journal.go`.
- `backend/internal/worker/agent/notification_journal_test.go`.
- `backend/internal/worker/service/output_notification.go`.
- `backend/internal/worker/service/output_notification_test.go`.
- `backend/internal/worker/service/output_notification_benchmark_test.go`.
- `backend/internal/worker/sqlc.yaml` if the generated column needs an explicit type override.

Add this exact column to the initial `messages` declaration:

```sql
notification_entry_count INTEGER GENERATED ALWAYS AS (
    leapmux_notification_entry_count(supplemental_content, supplemental_content_compression)
) STORED NOT NULL
    CHECK (typeof(notification_entry_count) = 'integer')
    CHECK (notification_entry_count >= -1),
```

Apply this exact initial-schema column diff:

```diff
--- a/backend/internal/worker/db/migrations/00001_initial.sql
+++ b/backend/internal/worker/db/migrations/00001_initial.sql
@@ -166,6 +166,11 @@
     -- supplement above, which is what a writer that sets neither column stores.
     supplemental_content BLOB NOT NULL DEFAULT X'',
     supplemental_content_compression INTEGER NOT NULL DEFAULT 1
+    notification_entry_count INTEGER GENERATED ALWAYS AS (
+        leapmux_notification_entry_count(supplemental_content, supplemental_content_compression)
+    ) STORED NOT NULL
+        CHECK (typeof(notification_entry_count) = 'integer')
+        CHECK (notification_entry_count >= -1),
         CHECK (typeof(supplemental_content_compression) = 'integer') CHECK (supplemental_content_compression BETWEEN 1 AND 2),
     supplemental_revision INTEGER NOT NULL DEFAULT 0
         CHECK (typeof(supplemental_revision) = 'integer' AND supplemental_revision >= 0),
```

Keep the generated count private to Worker storage.
Add no protobuf field for it.
Check generated message projections and explicit SQL column lists after generation.

Add this exact index:

```sql
CREATE INDEX idx_messages_notification_entries
    ON messages (agent_id, agent_session_id, seq)
    WHERE notification_entry_count <> 0;
```

Replace the unfiltered supplement lookup with this query:

```sql
SELECT source, agent_provider, supplemental_content, supplemental_content_compression
FROM messages
WHERE agent_id = ? AND agent_session_id = ? AND notification_entry_count <> 0
ORDER BY seq;
```

Use the authoritative stored-supplement parser in the deterministic SQLite function.
Return 0 for a valid supplement without a journal.
Return the journal length for valid entries.
Return -1 for decompression failure or invalid private storage.
The lookup must surface corruption. It must not treat -1 as an empty journal.
Register the function in the Worker database package before a Worker connection opens.
Keep the generic SQLite opener independent of Worker message formats.
Do not use `messages.idempotency_key` to find threaded aggregate journals.
Those aggregate rows can intentionally hold an empty row-level idempotency key.

Test direct SQL inserts, direct updates, cloned rows, every production writer, and invalid count overrides.
Test the query plan and its partial index use.
Measure reads and writes at 0, 100, 1,000, and 10,000 ordinary rows.
Use both empty and 1 KiB compressed provider supplements.
Measure present and absent duplicate keys.
Confirm that the new read cost does not depend on ordinary message count.
Measure the generated count's write cost before acceptance.
Verify external SQLite read, dump, backup, and integrity operations.
Record that external writes require the deterministic function.

### 1.4 Finish background task status names

Before: the consolidation includes the drafted status rename and its consumer edits.
After: all runtime, storage, frontend, fixture, and contract consumers use one clear vocabulary.

Complete every status-related file in the consolidated source path manifest below.
The exact proto enum changes are:

```diff
- BACKGROUND_TASK_STATUS_COMPLETED = 4;
+ BACKGROUND_TASK_STATUS_SUCCEEDED = 4;
- BACKGROUND_TASK_STATUS_FINISHED = 8;
+ BACKGROUND_TASK_STATUS_ENDED_WITH_UNKNOWN_OUTCOME = 8;
```

The Go domain constants become `bgtask.StatusSucceeded` and `bgtask.StatusEndedWithUnknownOutcome`.
The payload tokens become `succeeded` and `ended_with_unknown_outcome`.
Store integer ordinals 1 through 8. Do not change the ordinals for this rename.
Generate the browser's complete status union from `contracts/worker-vocab.json`.
Validate the token table against every `BackgroundTaskStatus` enum value.
Do not rename provider-native `completed` or `finished` values.
Do not rename message completion, goals, or to-do completion values for this change.

Use `Succeeded` for a known successful task.
Use `Ended with unknown outcome` when native evidence proves the end but supplies no outcome.
Use a muted status for the unknown outcome. Do not show the success color.

Preserve the Reasonix correction for partial failures and launch acknowledgements.
Preserve the Codewhale correction for an unknown workflow end or a shell lost through HTTP 404.
Verify those decisions against the installed native versions.
Keep malformed Copilot completion frames on their original child transcript.
A malformed completion cannot close the child or clear its state.

Test all tokens, every proto ordinal, and unknown status inputs.
Run generator determinism and enum coverage tests.
Prove the sidebar labels live and after reload.

### 1.5 Correct opaque background task identities

Before: an invalid native key normalizes to a public string namespace that a valid native key can imitate.
String trimming can also address a different child.
An empty upsert can create an empty-key registry row.
After: typed keys distinguish fresh native input from stored identity, and every child operation verifies exact root authority.

Edit the exact key, registry, provider, and fake-sink paths in the consolidated source path manifest below.
Add `backend/internal/worker/bgtask/row_key.go` and `row_key_test.go`.
Add one recursive child-identity query to `backend/internal/worker/db/queries/agents.sql`.
Update all direct callers discovered from these interfaces before the runtime change.
List each additional caller in the execution diff and preserve its existing tests.

Use these public Go types and service shapes:

```go
type RowKey struct { /* private canonical identity and compact diagnostic */ }
type RowIdentity struct { /* private canonical string */ }

type ChildIdentity struct {
    AgentID string
    ProviderChildKey string
}

LookupChildIdentity(RowIdentity) (ChildIdentity, bool, error)
```

Keep `RowIdentity` opaque through private construction and checked storage factories.
Expose its string only at SQL and protobuf serialization sites.
A public conversion from a raw string must not bypass validation.
The lookup's Boolean means that the exact durable child exists.
A span-only child can exist with an empty native provider key.
A provider control requires a nonempty provider key separately.
A report can use the verified child AgentID without a provider key.
Return refusal for a wrong root, wrong parent, or a cycle before checking native key readiness.
Return a zero identity and false for an absent task or child.

Use `leapmux-derived-key:` for invalid native input.
Use a distinct escaped-native prefix for fresh native input that starts with either reserved prefix.
Hash the complete raw bytes. Never trim an identity or truncate it.
Normalizing a typed key must preserve its identity.
Loading a stored identity must validate that identity without interpreting it as fresh native input.
Retain only the canonical identity and small diagnostic facts. Retain no raw key copy for logging.

Use typed identity in upserts and registry mutation methods.
Use typed identity in child stop, send, steer, and active-turn queries.
Return `(TurnState, error)` where active-turn lookup can refuse authority.
Keep native thread, session, tool, and provider child IDs as their original native data.
Do not send a normalized registry key as a Codex native thread ID.

Give recording sinks an explicit immutable root ID.
Share the physical registry across all descendants of that root.
Keep each child's transcript, options, and native identity separate.
Initialize embedded control sinks in place. Copy no mutex.
Use opaque child IDs scoped by root, immediate parent, and actual native spawn identity.
Remove assertions that predict `child-of-*` IDs. Assert the actual registry link instead.
Project a parent's owned rows for view tests while the root registry retains grandchildren physically.

Use a recursive SQL lookup with `UNION` to prevent cycles.
Join the task, child, and ancestor chain in that lookup.
Preserve the existing SQL schema and protobuf string representation for this type change.

Run the existing collision, whitespace, empty-key, and real/fake parity regression tests before the correction.
Add long native keys, invalid UTF-8, reserved-prefix native keys, reload, grandchildren, wrong-root, and cycle cases.
Add a browser proof in `frontend/tests/e2e/205-background-task-key-identity.spec.ts`.
Use real native Cursor IDs for its control character case.

### 1.6 Capture goal authority at native observation time

Before: a deferred Muse callback captures the Worker's current session when the callback executes.
A context replacement can therefore give old native goal data a new session authority.
Callbacks under Muse's dispatch mutex can also deadlock through native output reentry.
After: a goal writer carries the exact publication and native session fact from the observation.

Edit these files:

- `backend/internal/worker/agent/agent.go`.
- `backend/internal/worker/agent/transcript_capture.go`.
- `backend/internal/worker/agent/agenttest/sink.go`.
- `backend/internal/worker/agent/agenttest/control_sink.go`.
- `backend/internal/worker/service/output_goal.go`.
- `backend/internal/worker/service/output_goal_admission.go`.
- `backend/internal/worker/service/output_goal_admission_test.go`.
- `backend/internal/worker/service/output_goal_publication.go`.
- `backend/internal/worker/service/output_goal_publication_test.go`.
- `backend/internal/worker/agent/providers/muse/output.go`.
- `backend/internal/worker/agent/providers/muse/output_test.go`.
- `backend/internal/worker/agent/providers/muse/session_lifecycle.go`.
- `backend/internal/worker/agent/providers/muse/session_lifecycle_test.go`.

Add an error-returning captured writer factory to `GoalServices`:

```go
type GoalWriter interface {
    UpsertGoal(GoalUpdate) error
    ClearGoal() error
}

GoalWriterFor(CapturedTranscript) (GoalWriter, error)
```

Reuse the existing opaque publication capture. Introduce no second native session authority.
Require the captured expected session ID to equal the captured native session fact's ID.
Check the current sink, publisher, and native session fact atomically at publication.
Ordinary later turns in the same native session must not expire a goal writer.
Keep existing direct goal methods as capture-now operations through the same mechanism.
Use one error policy for refusal and failed persistence.

Run native callbacks outside dispatch, provider, service, and lifecycle locks.
Preserve ordered goal publication and exact failed-stage retries.
Test set and clear reentry through actual nested `HandleOutput` calls.
Test startup, context clear, source replacement, later same-session turns, stale callbacks, and failed native clear.

### 1.7 Complete delivery and close retries

Before: Claude can duplicate a child prompt after failed revival, or lose it after a rejected persistence write.
Codex can replace a retained failed close outcome with a later completed activity outcome.
After: retries preserve the original successful side effects and the first selected native final outcome.

Edit these files:

- `backend/internal/worker/agent/providers/claude/subagent.go`.
- `backend/internal/worker/agent/providers/claude/subagent_delivery_test.go`.
- `backend/internal/worker/agent/providers/codex/subagent.go`.
- `backend/internal/worker/agent/providers/codex/output_test.go`.
- `backend/internal/worker/agent/agenttest/sink.go` when the delivery seam needs an explicit failure state.

For Claude, retain one delivery intent with separate prompt-persisted and registry-revived facts.
A failed prompt write must retain the intent and prevent revival.
A successful prompt write followed by failed revival must retry only revival.
Preserve two distinct deliveries with identical text.
Use the native run identity when it proves delivery identity.
Do not use prompt text equality to suppress deliveries.
Test missing run IDs, repeated native starts, sender cleanup, concurrent retries, and each failure order.

For Codex, copy the selected `state.finalTransition` under the existing mutex before later flush and close consumers.
A later completed activity can retry a failed close. It cannot replace the original failed outcome.
Preserve one child divider and one active-to-idle transition.
Run `TestHandleCodexOutput_MultiAgentV2CompletionRetryKeepsTheFailedOutcome` red before the correction.

### 1.8 Complete MiMo retained observations

Before: the consolidation combines original MiMo source, review corrections, resolver changes, and the stashes.
The captured notification API integration and complete native acceptance remain incomplete.
After: every retained native observation persists once under its original session, actor, message, and part identity.

Complete these exact provider files and their co-located tests:

- `backend/internal/worker/agent/providers/mimo/agent.go` and `agent_test.go`.
- `backend/internal/worker/agent/providers/mimo/events.go` and `events_test.go`.
- `backend/internal/worker/agent/providers/mimo/control.go` and `control_test.go`.
- `backend/internal/worker/agent/providers/mimo/output.go` and `output_test.go`.
- `backend/internal/worker/agent/providers/mimo/rpc.go` and `rpc_test.go`.
- `backend/internal/worker/agent/providers/mimo/session_lifecycle.go` and `session_lifecycle_test.go`.
- `backend/internal/worker/agent/providers/mimo/stop.go` and `stop_test.go`.
- `backend/internal/worker/agent/providers/mimo/subagent.go` and `subagent_test.go`.
- `frontend/src/components/chat/providers/mimo/extractors/toolCall.ts` and `toolCall.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/toolCommon.ts` and `toolCommon.test.ts`.
- `frontend/tests/e2e/mimo-code/toolRowId.ts` and `toolRowId.test.ts`.
- `frontend/tests/e2e/mimo-code/scenarios.ts` and `scenarios.test.ts`.
- `frontend/tests/e2e/helpers/nativeLifecycle.ts` and `nativeLifecycle.test.ts`.
- `contracts/mimo-protocol.json` and `contracts/mimo-protocol.schema.json`.
- `scripts/generate-contracts.mjs` and `scripts/generate-contracts.test.mjs`.

Use native `part.id` as the tool span identity. Preserve native model `callID` separately.
Validate stored and native sessions together in the row resolver.
Keep child ownership in the selected child transcript. Read the parent only to prove the spawn link.
An optional outer session must agree with the queried native part.
Refuse reused model call IDs that identify distinct native parts.

Retain native observations on their existing captured message and actor records.
Keep independent root and child output independent.
Preserve opening user instruction order before later output on that child.
Keep registry presence and actual activity visible when the opening prompt write fails.
Do not infer a user or assistant role from a failed native GET.
Keep unresolved whole text and unresolved user instructions through Stop and ClearContext.

Keep the ordered pending failure queue.
A main failure requires its captured root epoch.
A live child can retain a failure across parent idle and the next busy turn.
Identical bare `session.error` bodies are distinct observations unless native identity proves a repeat.
Keep a child failure after attribution until its child row succeeds or closure persists its exact notification.
An interrupted divider cannot consume a failure that it does not contain.
Retain the original divider content and native tool count when its write fails.

Complete the retained compaction and retry notification queue through the shared captured API.
Capture session, publication, bytes, and destination before a lifecycle change.
Treat unidentifiable retry events as distinct, including equal bodies.
Flush at Stop, Wait, and ClearContext without repeating successful entries.

Run every late prompt, failure, actor replacement, text, divider, compaction, and retry regression.
Run complete MiMo race tests and all 53 complete MiMo feature files on the same source.

### 1.9 Repair helper proofs and audit the merged code

Complete the consolidated source path manifest's helper and frontend changes.
Require the exact status dot and exact secondary text in background task proofs.
A canonical status word in a title cannot satisfy the status assertion.
Use one scoped locator that handles secondary text suppression without a count/read race.
Keep long-title and suppressed-secondary positive controls.
Use a removed bidi override for the cleaning fixture. A legitimate joiner must remain.

Repair every compile, type, declaration, and import mismatch that the consolidation exposes.
Preserve later Copilot malformed-frame handling and process exit ownership.
Keep the existing nine neutral native resolver paths and both native scenario paths.
Finish the touched-file prose and identifier audit.
Correct stale comments without changing their scope or their stated invariants.

Run affected source tests before full suites.
Run `task generate`, `task lint`, and `task test` sequentially.
Run the affected backend packages with `task test-backend-race`.
Do not claim acceptance until all expected regression failures become passing tests.

## Consolidated source path manifest

These paths identify the unfinished source that Stage 1 repairs and verifies.
Before: this manifest identifies the consolidated source to inspect at `HEAD`.
After: each path implements its stated stage mechanism and passes its complete affected checks.
New files remain explicitly identified as new source by the final Git diff.
Retain their real tests and native fixture assertions.

### Captured transcript and service state

- `backend/internal/worker/db/enum_column_numbering_test.go`.
- `backend/internal/worker/db/migrations/00001_initial.sql`.
- `backend/internal/worker/db/notification_entries_test.go`.
- `backend/internal/worker/db/optional_enum.go`.
- `backend/internal/worker/db/optional_enum_storage_test.go`.
- `backend/internal/worker/db/optional_enum_test.go`.
- `backend/internal/worker/db/partial_index_test.go`.
- `backend/internal/worker/db/queries/agent_background_tasks.sql`.
- `backend/internal/worker/db/queries/message_enrichment.sql`.
- `backend/internal/worker/db/queries/messages.sql`.
- `backend/internal/worker/service/agent.go`.
- `backend/internal/worker/service/agent_input_queue_test.go`.
- `backend/internal/worker/service/agent_native_turn_restart.go`.
- `backend/internal/worker/service/agent_native_turn_restart_test.go`.
- `backend/internal/worker/service/agent_settings_test.go`.
- `backend/internal/worker/service/agent_transcript_resume_children_test.go`.
- `backend/internal/worker/service/agent_transcript_resume_test.go`.
- `backend/internal/worker/service/agent_transcript_resume_turn_ends_test.go`.
- `backend/internal/worker/service/background_task_key_identity_test.go`.
- `backend/internal/worker/service/canonical_storage_test.go`.
- `backend/internal/worker/service/close_during_startup_test.go`.
- `backend/internal/worker/service/closed_tab_test.go`.
- `backend/internal/worker/service/control_response_finalize.go`.
- `backend/internal/worker/service/control_response_state.go`.
- `backend/internal/worker/service/control_response_state_test.go`.
- `backend/internal/worker/service/control_response_test.go`.
- `backend/internal/worker/service/create_message_row_test.go`.
- `backend/internal/worker/service/empty_row_key_test.go`.
- `backend/internal/worker/service/generation_progress.go`.
- `backend/internal/worker/service/generation_progress_test.go`.
- `backend/internal/worker/service/get_agent_message_test.go`.
- `backend/internal/worker/service/get_agent_span_messages_test.go`.
- `backend/internal/worker/service/list_messages_anchor_test.go`.
- `backend/internal/worker/service/message_context.go`.
- `backend/internal/worker/service/message_context_test.go`.
- `backend/internal/worker/service/message_enrichment.go`.
- `backend/internal/worker/service/message_enrichment_log_test.go`.
- `backend/internal/worker/service/message_enrichment_test.go`.
- `backend/internal/worker/service/message_idempotency_test.go`.
- `backend/internal/worker/service/message_marks_test.go`.
- `backend/internal/worker/service/message_seq_monotonic_test.go`.
- `backend/internal/worker/service/notification_reduction.go`.
- `backend/internal/worker/service/notification_reduction_reference_test.go`.
- `backend/internal/worker/service/notification_reduction_test.go`.
- `backend/internal/worker/service/open_async_startup_test.go`.
- `backend/internal/worker/service/option_changes.go`.
- `backend/internal/worker/service/options_cas.go`.
- `backend/internal/worker/service/orphan_sweep_test.go`.
- `backend/internal/worker/service/output.go`.
- `backend/internal/worker/service/output_activity.go`.
- `backend/internal/worker/service/output_activity_removal_test.go`.
- `backend/internal/worker/service/output_activity_test.go`.
- `backend/internal/worker/service/output_bgtask.go`.
- `backend/internal/worker/service/output_bgtasks_test.go`.
- `backend/internal/worker/service/output_catalog.go`.
- `backend/internal/worker/service/output_catalog_test.go`.
- `backend/internal/worker/service/output_catalog_transaction_test.go`.
- `backend/internal/worker/service/output_child_agent_test.go`.
- `backend/internal/worker/service/output_child_identity_test.go`.
- `backend/internal/worker/service/output_consolidate_test.go`.
- `backend/internal/worker/service/output_control_publication_test.go`.
- `backend/internal/worker/service/output_goal.go`.
- `backend/internal/worker/service/output_goal_admission.go`.
- `backend/internal/worker/service/output_goal_admission_test.go`.
- `backend/internal/worker/service/output_goal_cache_test.go`.
- `backend/internal/worker/service/output_goal_identity_precision_test.go`.
- `backend/internal/worker/service/output_goal_publication.go`.
- `backend/internal/worker/service/output_goal_publication_test.go`.
- `backend/internal/worker/service/output_goal_test.go`.
- `backend/internal/worker/service/output_message_log_test.go`.
- `backend/internal/worker/service/output_notification.go`.
- `backend/internal/worker/service/output_notification_benchmark_test.go`.
- `backend/internal/worker/service/output_notification_reentry_test.go`.
- `backend/internal/worker/service/output_notification_span_lines_test.go`.
- `backend/internal/worker/service/output_notification_test.go`.
- `backend/internal/worker/service/output_notification_transport_test.go`.
- `backend/internal/worker/service/output_plan_test.go`.
- `backend/internal/worker/service/output_session_info_replay_test.go`.
- `backend/internal/worker/service/output_session_log_test.go`.
- `backend/internal/worker/service/output_settings_invalid_options_log_test.go`.
- `backend/internal/worker/service/output_settings_refreshed_test.go`.
- `backend/internal/worker/service/output_stop.go`.
- `backend/internal/worker/service/output_stop_test.go`.
- `backend/internal/worker/service/output_thread_test.go`.
- `backend/internal/worker/service/output_todos_test.go`.
- `backend/internal/worker/service/output_turn_admission.go`.
- `backend/internal/worker/service/output_turn_admission_test.go`.
- `backend/internal/worker/service/output_user_span_lines_test.go`.
- `backend/internal/worker/service/registry_root_scope_test.go`.
- `backend/internal/worker/service/restore_state_test.go`.
- `backend/internal/worker/service/service.go`.
- `backend/internal/worker/service/startup_error_persist_test.go`.
- `backend/internal/worker/service/subagent_native_completion_test.go`.
- `backend/internal/worker/service/subagent_report_identity_test.go`.
- `backend/internal/worker/service/terminal_signals_test.go`.
- `backend/internal/worker/service/title_cleaning_test.go`.
- `backend/internal/worker/service/transcript_capture.go`.
- `backend/internal/worker/service/transcript_capture_test.go`.
- `backend/internal/worker/service/watch_events.go`.
- `backend/internal/worker/service/watch_events_test.go`.
- `backend/internal/worker/service/watch_replay_identity_test.go`.
- `backend/internal/worker/service/watch_session.go`.
- `backend/internal/worker/service/watch_start_test.go`.
- `backend/internal/worker/service/watcher.go`.
- `backend/internal/worker/service/watcher_ownership_test.go`.
- `backend/internal/worker/service/watcher_test.go`.
- `backend/internal/worker/service/workspace_archive_test.go`.

### Shared agent APIs, registry state, and test sinks

- `backend/internal/worker/agent/agent.go`.
- `backend/internal/worker/agent/agenttest/empty_row_key_test.go`.
- `backend/internal/worker/agent/agenttest/registry_root_scope_test.go`.
- `backend/internal/worker/agent/agenttest/row_key_identity_test.go`.
- `backend/internal/worker/agent/agenttest/sink.go`.
- `backend/internal/worker/agent/agenttest/sink_fake_parity_test.go`.
- `backend/internal/worker/agent/agenttest/sink_test.go`.
- `backend/internal/worker/agent/agenttest/subagent_report_identity_test.go`.
- `backend/internal/worker/agent/agenttest/transcript_capture.go`.
- `backend/internal/worker/agent/generation_progress.go`.
- `backend/internal/worker/agent/generation_progress_test.go`.
- `backend/internal/worker/agent/goal.go`.
- `backend/internal/worker/agent/goal_test.go`.
- `backend/internal/worker/agent/message_completion.go`.
- `backend/internal/worker/agent/message_completion_test.go`.
- `backend/internal/worker/agent/message_enrichment_receipt.go`.
- `backend/internal/worker/agent/message_enrichment_receipt_test.go`.
- `backend/internal/worker/agent/message_metadata.go`.
- `backend/internal/worker/agent/notification_journal.go`.
- `backend/internal/worker/agent/notification_journal_test.go`.
- `backend/internal/worker/agent/notification_reduction.go`.
- `backend/internal/worker/agent/notification_reduction_test.go`.
- `backend/internal/worker/agent/provider.go`.
- `backend/internal/worker/agent/provider_services_test.go`.
- `backend/internal/worker/agent/subagent_report.go`.
- `backend/internal/worker/agent/transcript_capture.go`.
- `backend/internal/worker/agent/transcript_capture_test.go`.
- `backend/internal/worker/agent/transcript_write_receipt.go`.
- `backend/internal/worker/agent/transcript_write_receipt_test.go`.

### Provider runtime and provider tests

- `backend/internal/worker/agent/providers/acp/base.go`.
- `backend/internal/worker/agent/providers/acp/children.go`.
- `backend/internal/worker/agent/providers/acp/children_test.go`.
- `backend/internal/worker/agent/providers/acp/session_retirement_test.go`.
- `backend/internal/worker/agent/providers/acp/subagent_test.go`.
- `backend/internal/worker/agent/providers/acp/terminal.go`.
- `backend/internal/worker/agent/providers/acp/terminal_test.go`.
- `backend/internal/worker/agent/providers/acp/turn_active_test.go`.
- `backend/internal/worker/agent/providers/amp/subagent.go`.
- `backend/internal/worker/agent/providers/amp/subagent_test.go`.
- `backend/internal/worker/agent/providers/claude/interrupt_test.go`.
- `backend/internal/worker/agent/providers/claude/output.go`.
- `backend/internal/worker/agent/providers/claude/output_test.go`.
- `backend/internal/worker/agent/providers/claude/subagent.go`.
- `backend/internal/worker/agent/providers/claude/subagent_delivery_test.go`.
- `backend/internal/worker/agent/providers/claude/subagent_test.go`.
- `backend/internal/worker/agent/providers/cline/events.go`.
- `backend/internal/worker/agent/providers/cline/subagent.go`.
- `backend/internal/worker/agent/providers/cline/subagent_test.go`.
- `backend/internal/worker/agent/providers/cline/team.go`.
- `backend/internal/worker/agent/providers/cline/team_test.go`.
- `backend/internal/worker/agent/providers/codebuddy/output.go`.
- `backend/internal/worker/agent/providers/codebuddy/subagent.go`.
- `backend/internal/worker/agent/providers/codebuddy/subagent_store_test.go`.
- `backend/internal/worker/agent/providers/codebuddy/subagent_test.go`.
- `backend/internal/worker/agent/providers/codewhale/output.go`.
- `backend/internal/worker/agent/providers/codewhale/subagent.go`.
- `backend/internal/worker/agent/providers/codewhale/subagent_outcome_test.go`.
- `backend/internal/worker/agent/providers/codewhale/subagent_test.go`.
- `backend/internal/worker/agent/providers/codex/child_key_identity_test.go`.
- `backend/internal/worker/agent/providers/codex/output.go`.
- `backend/internal/worker/agent/providers/codex/output_test.go`.
- `backend/internal/worker/agent/providers/codex/subagent.go`.
- `backend/internal/worker/agent/providers/commandcode/output.go`.
- `backend/internal/worker/agent/providers/commandcode/subagent.go`.
- `backend/internal/worker/agent/providers/commandcode/subagent_test.go`.
- `backend/internal/worker/agent/providers/conformance_coverage_test.go`.
- `backend/internal/worker/agent/providers/copilot/subagent.go`.
- `backend/internal/worker/agent/providers/copilot/subagent_test.go`.
- `backend/internal/worker/agent/providers/copilot/subagent_validation_test.go`.
- `backend/internal/worker/agent/providers/cursor/extensions.go`.
- `backend/internal/worker/agent/providers/cursor/extensions_test.go`.
- `backend/internal/worker/agent/providers/cursor/subagent.go`.
- `backend/internal/worker/agent/providers/cursor/subagent_lifecycle_test.go`.
- `backend/internal/worker/agent/providers/cursor/subagent_test.go`.
- `backend/internal/worker/agent/providers/deepseekharness/subagent.go`.
- `backend/internal/worker/agent/providers/deepseekharness/subagent_test.go`.
- `backend/internal/worker/agent/providers/deepseekharness/workflow.go`.
- `backend/internal/worker/agent/providers/dirac/subagent_store.go`.
- `backend/internal/worker/agent/providers/dirac/subagent_store_test.go`.
- `backend/internal/worker/agent/providers/dirac/subagent_test.go`.
- `backend/internal/worker/agent/providers/droid/child_connection_test.go`.
- `backend/internal/worker/agent/providers/droid/child_steer_test.go`.
- `backend/internal/worker/agent/providers/droid/output.go`.
- `backend/internal/worker/agent/providers/droid/subagent_test.go`.
- `backend/internal/worker/agent/providers/droid/subagent_transcript_test.go`.
- `backend/internal/worker/agent/providers/factory_test.go`.
- `backend/internal/worker/agent/providers/fastagent/subagent_test.go`.
- `backend/internal/worker/agent/providers/gemini/output.go`.
- `backend/internal/worker/agent/providers/gemini/output_test.go`.
- `backend/internal/worker/agent/providers/gemini/subagent_transcript.go`.
- `backend/internal/worker/agent/providers/gemini/subagent_transcript_test.go`.
- `backend/internal/worker/agent/providers/goose/subagent_test.go`.
- `backend/internal/worker/agent/providers/grok/subagent.go`.
- `backend/internal/worker/agent/providers/grok/subagent_test.go`.
- `backend/internal/worker/agent/providers/imports_test.go`.
- `backend/internal/worker/agent/providers/internal/providerkit/generation_buffer.go`.
- `backend/internal/worker/agent/providers/internal/providerkit/generation_buffer_test.go`.
- `backend/internal/worker/agent/providers/internal/providerkit/process.go`.
- `backend/internal/worker/agent/providers/internal/providerkit/process_pipes.go`.
- `backend/internal/worker/agent/providers/internal/providerkit/process_start_test.go`.
- `backend/internal/worker/agent/providers/internal/providerkit/process_stop_unix_test.go`.
- `backend/internal/worker/agent/providers/internal/tooltranscript/tool_transcript.go`.
- `backend/internal/worker/agent/providers/internal/tooltranscript/tool_transcript_test.go`.
- `backend/internal/worker/agent/providers/junie/subagent.go`.
- `backend/internal/worker/agent/providers/junie/subagent_test.go`.
- `backend/internal/worker/agent/providers/kilo/agent_test.go`.
- `backend/internal/worker/agent/providers/kimi/output.go`.
- `backend/internal/worker/agent/providers/kimi/session_lifecycle_test.go`.
- `backend/internal/worker/agent/providers/kimi/subagent.go`.
- `backend/internal/worker/agent/providers/kimi/subagent_test.go`.
- `backend/internal/worker/agent/providers/kimi/tasks.go`.
- `backend/internal/worker/agent/providers/kimi/tasks_test.go`.
- `backend/internal/worker/agent/providers/kiro/subagent_test.go`.
- `backend/internal/worker/agent/providers/kiro/workflow.go`.
- `backend/internal/worker/agent/providers/kiro/workflow_test.go`.
- `backend/internal/worker/agent/providers/letta/output.go`.
- `backend/internal/worker/agent/providers/letta/rpc.go`.
- `backend/internal/worker/agent/providers/letta/subagent.go`.
- `backend/internal/worker/agent/providers/letta/subagent_test.go`.
- `backend/internal/worker/agent/providers/mimo/agent.go`.
- `backend/internal/worker/agent/providers/mimo/agent_test.go`.
- `backend/internal/worker/agent/providers/mimo/control.go`.
- `backend/internal/worker/agent/providers/mimo/control_test.go`.
- `backend/internal/worker/agent/providers/mimo/events.go`.
- `backend/internal/worker/agent/providers/mimo/events_test.go`.
- `backend/internal/worker/agent/providers/mimo/output.go`.
- `backend/internal/worker/agent/providers/mimo/output_paths_test.go`.
- `backend/internal/worker/agent/providers/mimo/output_test.go`.
- `backend/internal/worker/agent/providers/mimo/rpc.go`.
- `backend/internal/worker/agent/providers/mimo/rpc_test.go`.
- `backend/internal/worker/agent/providers/mimo/session_lifecycle.go`.
- `backend/internal/worker/agent/providers/mimo/session_lifecycle_test.go`.
- `backend/internal/worker/agent/providers/mimo/start.go`.
- `backend/internal/worker/agent/providers/mimo/stop.go`.
- `backend/internal/worker/agent/providers/mimo/stop_test.go`.
- `backend/internal/worker/agent/providers/mimo/subagent.go`.
- `backend/internal/worker/agent/providers/mimo/subagent_test.go`.
- `backend/internal/worker/agent/providers/mimo/testhelpers_test.go`.
- `backend/internal/worker/agent/providers/mimo/workflow.go`.
- `backend/internal/worker/agent/providers/mimo/workflow_test.go`.
- `backend/internal/worker/agent/providers/muse/agent.go`.
- `backend/internal/worker/agent/providers/muse/agent_test.go`.
- `backend/internal/worker/agent/providers/muse/catalog.go`.
- `backend/internal/worker/agent/providers/muse/catalog_test.go`.
- `backend/internal/worker/agent/providers/muse/connection.go`.
- `backend/internal/worker/agent/providers/muse/connection_test.go`.
- `backend/internal/worker/agent/providers/muse/contract_tags_test.go`.
- `backend/internal/worker/agent/providers/muse/control.go`.
- `backend/internal/worker/agent/providers/muse/control_publication_test.go`.
- `backend/internal/worker/agent/providers/muse/control_test.go`.
- `backend/internal/worker/agent/providers/muse/doc.go`.
- `backend/internal/worker/agent/providers/muse/events.go`.
- `backend/internal/worker/agent/providers/muse/events_test.go`.
- `backend/internal/worker/agent/providers/muse/goal.go`.
- `backend/internal/worker/agent/providers/muse/goal_test.go`.
- `backend/internal/worker/agent/providers/muse/log.go`.
- `backend/internal/worker/agent/providers/muse/log_index.go`.
- `backend/internal/worker/agent/providers/muse/log_index_test.go`.
- `backend/internal/worker/agent/providers/muse/log_test.go`.
- `backend/internal/worker/agent/providers/muse/output.go`.
- `backend/internal/worker/agent/providers/muse/output_finalization.go`.
- `backend/internal/worker/agent/providers/muse/output_test.go`.
- `backend/internal/worker/agent/providers/muse/protocol.go`.
- `backend/internal/worker/agent/providers/muse/provider.go`.
- `backend/internal/worker/agent/providers/muse/provider_test.go`.
- `backend/internal/worker/agent/providers/muse/registration.go`.
- `backend/internal/worker/agent/providers/muse/registration_test.go`.
- `backend/internal/worker/agent/providers/muse/rpc.go`.
- `backend/internal/worker/agent/providers/muse/rpc_test.go`.
- `backend/internal/worker/agent/providers/muse/session.go`.
- `backend/internal/worker/agent/providers/muse/session_lifecycle.go`.
- `backend/internal/worker/agent/providers/muse/session_lifecycle_test.go`.
- `backend/internal/worker/agent/providers/muse/session_test.go`.
- `backend/internal/worker/agent/providers/muse/session_wire.go`.
- `backend/internal/worker/agent/providers/muse/settings.go`.
- `backend/internal/worker/agent/providers/muse/settings_test.go`.
- `backend/internal/worker/agent/providers/muse/start.go`.
- `backend/internal/worker/agent/providers/muse/start_test.go`.
- `backend/internal/worker/agent/providers/muse/start_unix_test.go`.
- `backend/internal/worker/agent/providers/muse/stop.go`.
- `backend/internal/worker/agent/providers/muse/stop_test.go`.
- `backend/internal/worker/agent/providers/muse/subagent.go`.
- `backend/internal/worker/agent/providers/muse/subagent_test.go`.
- `backend/internal/worker/agent/providers/muse/tool_content.go`.
- `backend/internal/worker/agent/providers/muse/tool_content_test.go`.
- `backend/internal/worker/agent/providers/muse/usage.go`.
- `backend/internal/worker/agent/providers/muse/usage_test.go`.
- `backend/internal/worker/agent/providers/muse/view.go`.
- `backend/internal/worker/agent/providers/muse/view_test.go`.
- `backend/internal/worker/agent/providers/ohmypi/agent.go`.
- `backend/internal/worker/agent/providers/ohmypi/control.go`.
- `backend/internal/worker/agent/providers/ohmypi/events.go`.
- `backend/internal/worker/agent/providers/ohmypi/subagent.go`.
- `backend/internal/worker/agent/providers/ohmypi/subagent_test.go`.
- `backend/internal/worker/agent/providers/opencode/subagent_test.go`.
- `backend/internal/worker/agent/providers/pi/output.go`.
- `backend/internal/worker/agent/providers/pi/output_test.go`.
- `backend/internal/worker/agent/providers/pi/subagent.go`.
- `backend/internal/worker/agent/providers/pi/subagent_test.go`.
- `backend/internal/worker/agent/providers/providers.go`.
- `backend/internal/worker/agent/providers/qoder/subagent.go`.
- `backend/internal/worker/agent/providers/qoder/subagent_test.go`.
- `backend/internal/worker/agent/providers/qoder/workflow_store.go`.
- `backend/internal/worker/agent/providers/qoder/workflow_store_test.go`.
- `backend/internal/worker/agent/providers/qwen/output_test.go`.
- `backend/internal/worker/agent/providers/qwen/subagent.go`.
- `backend/internal/worker/agent/providers/qwen/subagent_test.go`.
- `backend/internal/worker/agent/providers/reasonix/agent_test.go`.
- `backend/internal/worker/agent/providers/reasonix/subagent.go`.
- `backend/internal/worker/agent/providers/reasonix/subagent_test.go`.
- `backend/internal/worker/agent/providers/zcode/output.go`.
- `backend/internal/worker/agent/providers/zcode/output_test.go`.
- `backend/internal/worker/agent/providers/zcode/stop.go`.
- `backend/internal/worker/agent/providers/zcode/stopped_turn_test.go`.
- `backend/internal/worker/agent/providers/zcode/subagent.go`.
- `backend/internal/worker/agent/providers/zcode/subagent_test.go`.

### Process ownership

- `backend/util/procutil/process_exit.go`.
- `backend/util/procutil/process_exit_test.go`.
- `backend/util/procutil/process_owner.go`.
- `backend/util/procutil/process_owner_boundaries_test.go`.

### Frontend model, extraction, and views

- `frontend/src/api/workerRpc.watchEvents.test.ts`.
- `frontend/src/components/backgroundtasks/BackgroundTaskList.test.tsx`.
- `frontend/src/components/backgroundtasks/BackgroundTaskList.tsx`.
- `frontend/src/components/chat/ControlRequestBanner.test.tsx`.
- `frontend/src/components/chat/MessageBubble.test.tsx`.
- `frontend/src/components/chat/assembledMessage.test.ts`.
- `frontend/src/components/chat/assembledMessage.ts`.
- `frontend/src/components/chat/chatMarkPreview.test.ts`.
- `frontend/src/components/chat/chatRawJson.test.ts`.
- `frontend/src/components/chat/chatRawJson.ts`.
- `frontend/src/components/chat/controls/AskUserQuestionControl.test.tsx`.
- `frontend/src/components/chat/notificationEntries.test.ts`.
- `frontend/src/components/chat/notificationEntries.ts`.
- `frontend/src/components/chat/notificationRenderers.test.tsx`.
- `frontend/src/components/chat/providers/capabilities.ts`.
- `frontend/src/components/chat/providers/claude/extractors/toolCall.test.ts`.
- `frontend/src/components/chat/providers/claude/extractors/toolCall.ts`.
- `frontend/src/components/chat/providers/codebuddy/extractors/row.test.ts`.
- `frontend/src/components/chat/providers/codebuddy/extractors/row.ts`.
- `frontend/src/components/chat/providers/copilot/extractors/row.test.ts`.
- `frontend/src/components/chat/providers/copilot/extractors/toolCall.test.ts`.
- `frontend/src/components/chat/providers/copilot/extractors/toolCall.ts`.
- `frontend/src/components/chat/providers/copilot/toolRendering.test.tsx`.
- `frontend/src/components/chat/providers/droid/extractors/row.test.ts`.
- `frontend/src/components/chat/providers/droid/extractors/row.ts`.
- `frontend/src/components/chat/providers/index.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/agent.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/execute.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/fileEdit.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/outputFilePaths.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/outputFilePaths.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/resultDivider.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/row.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/row.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/todo.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/toolCall.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/toolCall.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/toolCommon.test.ts`.
- `frontend/src/components/chat/providers/mimo/extractors/toolCommon.ts`.
- `frontend/src/components/chat/providers/mimo/protocol.ts`.
- `frontend/src/components/chat/providers/muse/classification.test.ts`.
- `frontend/src/components/chat/providers/muse/classification.ts`.
- `frontend/src/components/chat/providers/muse/control.test.ts`.
- `frontend/src/components/chat/providers/muse/control.ts`.
- `frontend/src/components/chat/providers/muse/extractors/mcp.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/mcp.ts`.
- `frontend/src/components/chat/providers/muse/extractors/notification.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/notification.ts`.
- `frontend/src/components/chat/providers/muse/extractors/outputFilePaths.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/outputFilePaths.ts`.
- `frontend/src/components/chat/providers/muse/extractors/resultDivider.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/resultDivider.ts`.
- `frontend/src/components/chat/providers/muse/extractors/row.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/row.ts`.
- `frontend/src/components/chat/providers/muse/extractors/todo.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/todo.ts`.
- `frontend/src/components/chat/providers/muse/extractors/toolCall.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/toolCall.ts`.
- `frontend/src/components/chat/providers/muse/extractors/toolCommon.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/toolCommon.ts`.
- `frontend/src/components/chat/providers/muse/extractors/workflow.test.ts`.
- `frontend/src/components/chat/providers/muse/extractors/workflow.ts`.
- `frontend/src/components/chat/providers/muse/permissionPresets.test.ts`.
- `frontend/src/components/chat/providers/muse/permissionPresets.ts`.
- `frontend/src/components/chat/providers/muse/plugin.test.ts`.
- `frontend/src/components/chat/providers/muse/plugin.ts`.
- `frontend/src/components/chat/providers/muse/pluginConfiguration.test.ts`.
- `frontend/src/components/chat/providers/muse/pluginConfiguration.ts`.
- `frontend/src/components/chat/providers/muse/protocol.test.ts`.
- `frontend/src/components/chat/providers/muse/protocol.ts`.
- `frontend/src/components/chat/providers/muse/sourceData.test.ts`.
- `frontend/src/components/chat/providers/muse/sourceData.ts`.
- `frontend/src/components/chat/providers/muse/toolKinds.ts`.
- `frontend/src/components/chat/providers/muse/toolNames.ts`.
- `frontend/src/components/chat/providers/muse/toolResults.fixtures.ts`.
- `frontend/src/components/chat/providers/permissionPresets.ts`.
- `frontend/src/components/chat/providers/registry.test.ts`.
- `frontend/src/components/chat/providers/registry.ts`.
- `frontend/src/components/chat/providers/startupOptions.test.ts`.
- `frontend/src/components/chat/providers/startupOptions.ts`.
- `frontend/src/components/chat/rowExtraction.test.ts`.
- `frontend/src/components/chat/rowPreparation.ts`.
- `frontend/src/components/chat/rowRenderers.test.tsx`.
- `frontend/src/components/chat/widgets/ThinkingIndicator.test.tsx`.
- `frontend/src/components/common/AgentProviderIcon.test.tsx`.
- `frontend/src/components/common/AgentProviderIcon.tsx`.
- `frontend/src/components/common/nativeProviderMarks.test.ts`.
- `frontend/src/components/common/nativeProviderMarks.ts`.
- `frontend/src/components/goal/GoalActionsMenu.test.tsx`.
- `frontend/src/components/goal/GoalCard.test.tsx`.
- `frontend/src/components/goal/GoalCard.tsx`.
- `frontend/src/components/shell/AgentStartupOptions.test.tsx`.
- `frontend/src/components/shell/AgentStartupOptions.tsx`.
- `frontend/src/components/shell/NewAgentDialog.test.tsx`.
- `frontend/src/components/shell/NewAgentDialog.tsx`.
- `frontend/src/components/shell/tabBusyProbe.test.ts`.
- `frontend/src/components/todo/GoalsAndTodos.test.tsx`.
- `frontend/src/components/todo/GoalsAndTodos.tsx`.
- `frontend/src/hooks/agentEvents.toolProgress.test.ts`.
- `frontend/src/hooks/agentEvents.transcriptCapture.test.ts`.
- `frontend/src/hooks/agentEvents.ts`.
- `frontend/src/hooks/useWatchEventsStreams.test.ts`.
- `frontend/src/hooks/useWatchEventsStreams.ts`.
- `frontend/src/hooks/useWorkspaceConnection.activity.test.ts`.
- `frontend/src/hooks/useWorkspaceConnection.liveStatus.test.ts`.
- `frontend/src/hooks/useWorkspaceConnection.test.ts`.
- `frontend/src/hooks/useWorkspaceConnection.ts`.
- `frontend/src/hooks/watchPlan.test.ts`.
- `frontend/src/hooks/watchPlan.ts`.
- `frontend/src/lib/messageParser.test.ts`.
- `frontend/src/stores/agentActivity.store.test.ts`.
- `frontend/src/stores/agentActivity.store.ts`.
- `frontend/src/stores/agentSession.store.test.ts`.
- `frontend/src/stores/agentSession.store.ts`.
- `frontend/src/stores/chat.store.ts`.
- `frontend/src/stores/chatBackgroundTaskStore.test.ts`.
- `frontend/src/stores/chatBackgroundTasks.test.ts`.
- `frontend/src/stores/chatBackgroundTasks.ts`.
- `frontend/src/stores/chatGoal.test.ts`.
- `frontend/src/stores/chatGoal.ts`.
- `frontend/src/stores/chatGoalStore.test.ts`.
- `frontend/src/stores/chatGoalStore.ts`.
- `frontend/src/test-support/providerTranscriptCorpus.test.tsx`.
- `frontend/src/test-support/toolCallFixture.test.ts`.

### Native browser helpers and provider specs

- `frontend/tests/e2e/204-notification-identity-delivery.spec.ts`.
- `frontend/tests/e2e/agentSettings.ts`.
- `frontend/tests/e2e/amp/background-tasks-sidebar.spec.ts`.
- `frontend/tests/e2e/amp/nativeCatalog.ts`.
- `frontend/tests/e2e/amp/opaqueTask.test.ts`.
- `frontend/tests/e2e/amp/opaqueTask.ts`.
- `frontend/tests/e2e/amp/output-file-paths.spec.ts`.
- `frontend/tests/e2e/claude-code/code-execution.spec.ts`.
- `frontend/tests/e2e/claude-code/subagent-live-transcript.spec.ts`.
- `frontend/tests/e2e/claude-code/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/cline/subagent-transcript-tab.spec.ts`.
- `frontend/tests/e2e/cline/toolCatalog.ts`.
- `frontend/tests/e2e/cline/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts`.
- `frontend/tests/e2e/codewhale/background-tasks-sidebar.spec.ts`.
- `frontend/tests/e2e/codewhale/subagent-transcript-tab.spec.ts`.
- `frontend/tests/e2e/codewhale/workspace-trust.spec.ts`.
- `frontend/tests/e2e/codex/background-tasks-sidebar.spec.ts`.
- `frontend/tests/e2e/codex/mcp-tool-execution.spec.ts`.
- `frontend/tests/e2e/codex/permission-prompts.spec.ts`.
- `frontend/tests/e2e/codex/subagent-transcript-tab.spec.ts`.
- `frontend/tests/e2e/command-code/mcpScenarios.ts`.
- `frontend/tests/e2e/deepseek-harness/attachmentScenarios.ts`.
- `frontend/tests/e2e/deepseek-harness/output-file-paths.spec.ts`.
- `frontend/tests/e2e/deepseek-harness/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/dirac/output-file-paths.spec.ts`.
- `frontend/tests/e2e/dirac/planReadiness.ts`.
- `frontend/tests/e2e/dirac/subagent-transcript-tab.spec.ts`.
- `frontend/tests/e2e/fast-agent/subagent-transcript-tab.spec.ts`.
- `frontend/tests/e2e/gemini-cli/background-tasks-sidebar.spec.ts`.
- `frontend/tests/e2e/gemini-cli/childScenarios.ts`.
- `frontend/tests/e2e/gemini-cli/nativeStore.ts`.
- `frontend/tests/e2e/github-copilot/output-file-paths.spec.ts`.
- `frontend/tests/e2e/grok-build/code-execution.spec.ts`.
- `frontend/tests/e2e/grok-build/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/helpers/agentEventWatch.test.ts`.
- `frontend/tests/e2e/helpers/agentEventWatch.ts`.
- `frontend/tests/e2e/helpers/liveChildTranscript.test.ts`.
- `frontend/tests/e2e/helpers/mockAgentEnvironment.ts`.
- `frontend/tests/e2e/helpers/museEnvironment.test.ts`.
- `frontend/tests/e2e/helpers/museEnvironment.ts`.
- `frontend/tests/e2e/helpers/nativeBypassPermissions.ts`.
- `frontend/tests/e2e/helpers/nativeConfigurationFile.ts`.
- `frontend/tests/e2e/helpers/nativeCredentialIsolation.test.ts`.
- `frontend/tests/e2e/helpers/nativeCredentialIsolation.ts`.
- `frontend/tests/e2e/helpers/nativeInputQueueIdle.test.ts`.
- `frontend/tests/e2e/helpers/nativeLifecycle.test.ts`.
- `frontend/tests/e2e/helpers/nativeLifecycle.ts`.
- `frontend/tests/e2e/helpers/nativePrivatePath.test.ts`.
- `frontend/tests/e2e/helpers/nativePrivatePath.ts`.
- `frontend/tests/e2e/helpers/nativeToolOutputFilePaths.test.ts`.
- `frontend/tests/e2e/helpers/nativeToolOutputFilePaths.ts`.
- `frontend/tests/e2e/helpers/providerToolCalls.test.ts`.
- `frontend/tests/e2e/helpers/providerToolCalls.ts`.
- `frontend/tests/e2e/helpers/runningChildProof.test.ts`.
- `frontend/tests/e2e/helpers/runningChildProof.ts`.
- `frontend/tests/e2e/helpers/subagentRegistry.spec.ts`.
- `frontend/tests/e2e/helpers/subagentRegistry.test.ts`.
- `frontend/tests/e2e/helpers/subagentRegistry.ts`.
- `frontend/tests/e2e/helpers/workflowGrouping.test.ts`.
- `frontend/tests/e2e/helpers/workflowGrouping.ts`.
- `frontend/tests/e2e/junie/output-file-paths.spec.ts`.
- `frontend/tests/e2e/kilo/goalScenario.test.ts`.
- `frontend/tests/e2e/kimi-code/background-tasks-sidebar.spec.ts`.
- `frontend/tests/e2e/kimi-code/toolCatalog.ts`.
- `frontend/tests/e2e/kimi-code/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/kiro/goalReceipt.ts`.
- `frontend/tests/e2e/kiro/output-file-paths.spec.ts`.
- `frontend/tests/e2e/kiro/session-goal-pause-and-resume.spec.ts`.
- `frontend/tests/e2e/letta-code/mcpConfiguration.ts`.
- `frontend/tests/e2e/letta-code/output-file-paths.spec.ts`.
- `frontend/tests/e2e/mimo-code/interrupt-a-subagent.spec.ts`.
- `frontend/tests/e2e/mimo-code/interrupt-a-turn.spec.ts`.
- `frontend/tests/e2e/mimo-code/outputFilePaths.test.ts`.
- `frontend/tests/e2e/mimo-code/outputFilePaths.ts`.
- `frontend/tests/e2e/mimo-code/scenarios.test.ts`.
- `frontend/tests/e2e/mimo-code/scenarios.ts`.
- `frontend/tests/e2e/mimo-code/shellToolExecution.test.ts`.
- `frontend/tests/e2e/mimo-code/shellToolExecution.ts`.
- `frontend/tests/e2e/mimo-code/subagent-live-transcript.spec.ts`.
- `frontend/tests/e2e/mimo-code/toolRowId.test.ts`.
- `frontend/tests/e2e/mimo-code/toolRowId.ts`.
- `frontend/tests/e2e/mimo-code/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/muse-code/agent-questions.spec.ts`.
- `frontend/tests/e2e/muse-code/agent-startup.spec.ts`.
- `frontend/tests/e2e/muse-code/basic-chat.spec.ts`.
- `frontend/tests/e2e/muse-code/bypass-permissions-shortcut.spec.ts`.
- `frontend/tests/e2e/muse-code/close-an-agent.spec.ts`.
- `frontend/tests/e2e/muse-code/context-usage.spec.ts`.
- `frontend/tests/e2e/muse-code/conversation-context.spec.ts`.
- `frontend/tests/e2e/muse-code/credential-isolation.spec.ts`.
- `frontend/tests/e2e/muse-code/file-tool-execution.spec.ts`.
- `frontend/tests/e2e/muse-code/generation-progress.spec.ts`.
- `frontend/tests/e2e/muse-code/image-attachments.spec.ts`.
- `frontend/tests/e2e/muse-code/interrupt-a-turn.spec.ts`.
- `frontend/tests/e2e/muse-code/mode.spec.ts`.
- `frontend/tests/e2e/muse-code/model-context-on-resume.spec.ts`.
- `frontend/tests/e2e/muse-code/model-error.spec.ts`.
- `frontend/tests/e2e/muse-code/model.spec.ts`.
- `frontend/tests/e2e/muse-code/nativeHost.test.ts`.
- `frontend/tests/e2e/muse-code/nativeHost.ts`.
- `frontend/tests/e2e/muse-code/permission-prompts.spec.ts`.
- `frontend/tests/e2e/muse-code/reasoning-effort.spec.ts`.
- `frontend/tests/e2e/muse-code/scenarios.test.ts`.
- `frontend/tests/e2e/muse-code/scenarios.ts`.
- `frontend/tests/e2e/muse-code/session-reset.spec.ts`.
- `frontend/tests/e2e/muse-code/session-resume.spec.ts`.
- `frontend/tests/e2e/muse-code/shell-tool-execution.spec.ts`.
- `frontend/tests/e2e/muse-code/steer-mid-turn.spec.ts`.
- `frontend/tests/e2e/muse-code/text-attachments.spec.ts`.
- `frontend/tests/e2e/muse-code/thinking-in-the-transcript.spec.ts`.
- `frontend/tests/e2e/muse-code/to-do-sidebar.spec.ts`.
- `frontend/tests/e2e/muse-code/turn-end-sound.spec.ts`.
- `frontend/tests/e2e/muse-fixtures.test.ts`.
- `frontend/tests/e2e/muse-fixtures.ts`.
- `frontend/tests/e2e/pi/scriptedModel.ts`.
- `frontend/tests/e2e/pi/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/provider-fixture-factory.test.ts`.
- `frontend/tests/e2e/qoder-cli/code-execution.spec.ts`.
- `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts`.
- `frontend/tests/e2e/qoder-cli/workflow-grouping.spec.ts`.
- `frontend/tests/e2e/qwen-code/output-file-paths.spec.ts`.
- `frontend/tests/e2e/zcode/code-execution.spec.ts`.
- `frontend/tests/e2e/zcode/output-file-paths.spec.ts`.

### Contracts, schemas, generators, and documentation

- `plans/README.md`.
- `backend/internal/cli/control/cmd/agent_messages_follow_test.go`.
- `backend/internal/cli/control/streamevents/cursor.go`.
- `backend/internal/cli/control/streamevents/subscription.go`.
- `backend/internal/cli/control/streamevents/subscription_test.go`.
- `backend/internal/cli/control/streamevents/transport.go`.
- `backend/internal/cli/control/streamevents/transport_test.go`.
- `backend/internal/util/optionmap/optionmap.go`.
- `backend/internal/util/optionmap/optionmap_test.go`.
- `backend/internal/worker/bgtask/bgtask.go`.
- `backend/internal/worker/bgtask/bgtask_test.go`.
- `backend/internal/worker/bgtask/row_key_identity_test.go`.
- `backend/internal/worker/inputqueue/manager.go`.
- `backend/internal/worker/inputqueue/manager_log_test.go`.
- `backend/internal/worker/inputqueue/manager_test.go`.
- `backend/internal/worker/inputqueue/model.go`.
- `backend/internal/worker/inputqueue/store.go`.
- `backend/internal/worker/inputqueue/store_test.go`.
- `backend/internal/worker/sqlc.yaml`.
- `contracts/mimo-protocol.json`.
- `contracts/mimo-protocol.schema.json`.
- `contracts/muse-protocol.json`.
- `contracts/muse-protocol.schema.json`.
- `contracts/providers.json`.
- `contracts/worker-vocab.json`.
- `contracts/worker-vocab.schema.json`.
- `frontend/scripts/e2eCommandProcess.test.ts`.
- `frontend/scripts/e2eCommandProcess.ts`.
- `icons/agents/muse-code.svg`.
- `proto/leapmux/v1/agent.proto`.
- `proto/leapmux/v1/workspace.proto`.
- `scripts/contractsAreConsumed.test.mjs`.
- `scripts/generate-contracts.mjs`.
- `scripts/generate-contracts.test.mjs`.
- `scripts/startupOptionGroups.test.mjs`.

## UI before and after

### Shared transcript and background task list

Before:

```text
Chat                              Background tasks
  Current reply                     [success] Finished
  [old retry can change live state]  [title can satisfy a wrong status check]
  [child opening prompt can vanish]
```

After:

```text
Chat                              Background tasks
  Original child instruction        [success] Succeeded
  Original child output             [muted] Ended with unknown outcome
  Original retained notification    Exact native title
  Current reply and current progress
```

The final text and status derive from native evidence.
Historical retries preserve transcript order and change no replacement live state.

### Goal card

Before:

```text
Goal
  [old callback can replace the current objective]
  [set or clear callback can block native output]
```

After:

```text
Goal
  Exact current native objective
  Running | Paused | Unknown
  [Pause] [Resume] [Clear]
  Current native progress, including zero
```

A stale captured goal cannot change this card.
A known unknown native status stays visible without an invented successful state.

### Child transcript tab and image viewer

Before:

```text
Parent chat                       Tool result
  Child task card                   Image preview
  [card alone can stand in for       [shared viewer proof can be mixed with
   a missing live child tab]          provider-native output proof]
```

After:

```text
Parent chat    Child tab           Tool result
  Child link     Exact instruction  Actual native image preview
                 Live native output [Open image viewer]
                 Exact final result Full shared viewer UI proof
```

## Completion and self-audit

Read this file from beginning to end before declaring this stage complete.
Compare each required file change with its stated final behavior.
Check every changed hunk and its enclosing function.
Check every caller and every removed guard against its replacement.
Check every approved test move against its exact assertion destination.
Confirm that each new and changed test ran and passed.
Confirm that the required lint, typecheck, and complete browser files passed on the same source.
Check the UI against the diagrams included in this file.
Check each SQL, protocol, contract, and generated-file change against the declared shape.
Keep generated output out of commits.
Record the source identity and complete acceptance evidence for this stage.
Keep every unverified result explicit until its required check passes.
