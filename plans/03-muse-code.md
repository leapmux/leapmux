# Plan 03: Complete and accept Muse Code

## Scope and prerequisites

Execute this plan on top of the current `HEAD`.
Start after Wave 1 passes complete combined acceptance.
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


## Stage 3: Complete and accept Muse Code

### 3.1 Keep the transport and launch design

Use `/Users/trustin/.local/bin/muse` through the PATH locator in provider registration.
The inspected version was `1.4.3-R5018.1`.
Refresh version-specific evidence if the installed version changes.
Use Muse's native MSP protocol through `muse serve`.
Its native protocol exposes more required controls than its Agent Client Protocol adapter.
Keep all Muse wire parsing in `backend/internal/worker/agent/providers/muse/` and the Muse extraction plugin.

Complete every Muse source path in the Muse source path manifest below.
Keep each backend role in the project's standard file structure.
Preserve conformance, import-boundary, registration, and shipped-binary tests.
Keep the supplied icon at `icons/agents/muse-code.svg`.
The icon source is `https://upload.wikimedia.org/wikipedia/commons/8/88/Muse_icon.svg`.

Use a private HOME and private Muse settings in native tests.
Point the custom gateway at the local mock server.
Use a refusing proxy and remove inherited real provider credentials.
Keep the native repository inside the isolated test root.
Prove that a denied external request cannot reach a real endpoint.

### 3.2 Finish native output, controls, and lifecycle

Before: the merged provider implements the native transport and ordered controls, but known parser and lifecycle tests remain red.
After: output and controls preserve native evidence, exact identities, and retry stages across every lifecycle boundary.

Complete these provider roles and their co-located tests:

- Backend `agent.go`, `start.go`, `registration.go`, `connection.go`, and `rpc.go`.
- Backend `output.go`, `control.go`, `settings.go`, `session_lifecycle.go`, `stop.go`, and `subagent.go`.
- Frontend `plugin.ts`, `protocol.ts`, `toolKinds.ts`, `control.ts`, and the extraction files in the Muse source manifest.
- `contracts/muse-protocol.json` and `contracts/muse-protocol.schema.json`.
- `scripts/generate-contracts.mjs` and its tests.
- `frontend/tests/e2e/helpers/museEnvironment.ts` and its tests.
- `frontend/tests/e2e/muse-code/nativeHost.ts`, `scenarios.ts`, and their tests.

Preserve numeric RPC identities separately from ID-free requested notifications.
Keep FIFO control stages with canonical parameter fingerprints and cloned raw frames.
Run callbacks outside all provider locks.
Retry the failed head stage without repeating native approval, abort, feedback, or successful publication.
Preserve exact original settlement frames through retries and retirement.
Validate integral numeric identities without arbitrary caps. Accept valid zero values.
Keep a readable native label for a future choice.

Finish these known boundaries:

- Six known question settlements and readable future settlement words.
- No-op, failed, and cancelled compaction notices.
- Whitespace-only to-do status and compaction reason handling.
- Unsafe, fractional, infinite, zero, and maximum-safe compaction counts.
- A valid reason without an outcome.
- Exact preservation of a readable unknown outcome.
- Refusal to invent a notice for a malformed identity or outcome.
- Future and masked goal states.
- Invalid goal fields and unknown goal details.
- ClearContext and view retirement.
- Observation-time goal authority and callback reentry.
- Native inline effort-setting refresh.

Keep plain Model Context Protocol (MCP) text output in the current MCP reader.
Do not infer structured content from arbitrary JSON text.
Add actual native structured-content evidence before that feature's acceptance.
Fix array-valued Vitest cases through one-argument tuples.
A bare empty array in `it.each` supplies no argument and does not test an empty array input.
Preserve all 72 notification test cases and their assertions.

### 3.3 Complete question selection limits

Before: four merged test files specify selection limits, but the neutral model and runtime builders do not complete that behavior.
After: the question UI validates native limits and returns an exact accepted native answer.

Edit these exact files:

- `frontend/src/components/chat/controls/AskUserQuestionControl.tsx` and `AskUserQuestionControl.test.tsx`.
- `frontend/src/components/chat/ControlRequestBanner.tsx` and `ControlRequestBanner.test.tsx`.
- `frontend/src/components/chat/providers/capabilities.ts`.
- `frontend/src/components/chat/providers/muse/control.ts` and `control.test.ts`.
- `frontend/tests/e2e/helpers/providerToolCalls.ts` and `providerToolCalls.test.ts`.
- `frontend/tests/e2e/muse-code/agent-questions.spec.ts`.

Add optional numeric fields to the neutral question:

```ts
minimumSelections?: number
maximumSelections?: number
```

Map the native MSP selection record separately:

```json
{"mode":"multiple","minSelections":2,"maxSelections":2}
```

The native model tool uses its own snake-case fields:

```json
{"mode":"multiple","min_selections":2,"max_selections":3}
```

Respect each native domain. Native optional or zero counts do not imply that the model tool accepts them.
Preserve single-mode preview content and omitted numeric fields.
Use builder descriptors with `build` and `supportsSelectionLimits` for every question builder.
A builder capability does not prove that a native provider lacks the feature.

Test partial, complete, excessive, removed, typed, empty, and multi-page selections.
Test failed responses without loss of current answers.
Test reload and cancellation through the actual native host.

### 3.4 Close every Muse feature cell

Create or complete every exact Muse spec in the Muse feature list below.
Keep each file unique to one feature cell.
The first 26 merged files remain incomplete until their complete browser runs pass.
Add the remaining files with actual native behavior or a proved native limitation.

Investigate these unsettled native capabilities before a limitation claim:

- Native quota read and quota change events.
- PDF and other binary input bytes.
- Rich MCP structured content.
- Workspace trust at startup and during a session.
- Workflows and native code execution.
- Goals, blocked goals, and goal pause/resume.
- Background commands and native child operation controls.

LeapMux attachment validation is not proof that Muse lacks a native media type.
Muse's native input type allows media types beyond the currently accepted browser attachments.
Prove the installed native path or record a specific native refusal.
A missing current workflow builder does not prove that native Workflow JavaScript is unsupported.

For supported settings, prove the actual next model request uses the selected value.
For startup-only settings, prove the native lifetime rule and the refused live change.
For resume, prove the next native request contains the earlier conversation.
Stored Worker rows alone do not prove native context.

Update these files after the native decisions:

- `frontend/tests/e2e/feature-matrix/checklist.json`.
- `frontend/tests/e2e/feature-matrix/checklist.schema.json` only if the source schema needs a declared shape change.
- `site/content/docs/using/coding-agents.md`.
- `README.md` for the supported provider list.
- Provider registration, icon registration, and contract files already listed in the source manifest.

Run complete Muse backend race, frontend, script, and lint suites.
Run all 53 complete Muse browser files with the real Muse binary and local mock.
Require first-attempt success with zero skips and retries.
Accept all 53 cells only from that complete same-source report.

## Every Muse feature file

Before: some files exist as unfinished native tests, and the remaining files are absent.
After: all 53 unique files supply current native evidence and complete-file browser acceptance.

| Feature | Exact file | Required native behavior |
|---|---|---|
| text-attachments | `frontend/tests/e2e/muse-code/text-attachments.spec.ts` | The agent can use the contents of an attached text file. A filename alone does not count. |
| image-attachments | `frontend/tests/e2e/muse-code/image-attachments.spec.ts` | The agent can use the bytes of an attached image through its native input path. |
| pdf-attachments | `frontend/tests/e2e/muse-code/pdf-attachments.spec.ts` | The agent can use the bytes of an attached PDF. A filename or placeholder does not count. |
| other-binary-attachments | `frontend/tests/e2e/muse-code/other-binary-attachments.spec.ts` | The agent can use the bytes of an attached binary file other than an image or PDF. A filename or placeholder does not count. |
| thinking-in-the-transcript | `frontend/tests/e2e/muse-code/thinking-in-the-transcript.spec.ts` | LeapMux shows reasoning or thinking content from the provider's native output in the chat transcript. |
| steer-mid-turn | `frontend/tests/e2e/muse-code/steer-mid-turn.spec.ts` | New user input reaches the agent's current turn before it ends, instead of waiting in the next-turn queue. |
| agent-questions | `frontend/tests/e2e/muse-code/agent-questions.spec.ts` | LeapMux shows a question from the agent and returns the user's selected or typed answer through the native question path. |
| context-usage | `frontend/tests/e2e/muse-code/context-usage.spec.ts` | Agent info shows the provider's reported context use as token counts or an occupancy ratio. |
| compaction-notice | `frontend/tests/e2e/muse-code/compaction-notice.spec.ts` | The chat transcript shows a completed native compaction boundary or status. A start notice or a smaller context without a completed notice does not count. |
| manual-compaction | `frontend/tests/e2e/muse-code/manual-compaction.spec.ts` | A user command from LeapMux starts native context compaction, and the next model turn uses the compacted context. |
| model-context-on-resume | `frontend/tests/e2e/muse-code/model-context-on-resume.spec.ts` | After LeapMux reopens a native session, the next model turn can use prior conversation context. Saved LeapMux chat rows alone do not count. |
| rate-limit-state | `frontend/tests/e2e/muse-code/rate-limit-state.spec.ts` | Agent info shows a provider-reported quota window, use, or reset time. A retry notice alone does not count. |
| model | `frontend/tests/e2e/muse-code/model.spec.ts` | The user can select a model in LeapMux, and the provider uses that selection in a native model request. |
| reasoning-effort | `frontend/tests/e2e/muse-code/reasoning-effort.spec.ts` | The user can select a reasoning level in LeapMux, and the provider applies it to a native model request. A separate selector or a model variant can choose the level. |
| mode | `frontend/tests/e2e/muse-code/mode.spec.ts` | The user can select a provider mode in LeapMux, and the provider applies that mode to the session. |
| permission-prompts | `frontend/tests/e2e/muse-code/permission-prompts.spec.ts` | LeapMux shows a tool approval question and sends the user's allow or deny decision through the provider's control path. |
| smart-permissions-shortcut | `frontend/tests/e2e/muse-code/smart-permissions-shortcut.spec.ts` | A LeapMux shortcut selects the provider's safety-assisted permission preset. The preset can approve, block, or ask about a tool call. |
| bypass-permissions-shortcut | `frontend/tests/e2e/muse-code/bypass-permissions-shortcut.spec.ts` | A LeapMux shortcut selects the provider's own mode that runs tools without permission prompts. |
| plan-mode | `frontend/tests/e2e/muse-code/plan-mode.spec.ts` | The user can enter a provider mode that plans work before it makes changes. |
| plan-approval-banner | `frontend/tests/e2e/muse-code/plan-approval-banner.spec.ts` | LeapMux shows the provider's completed plan in a banner that accepts an approval or rejection. |
| session-goal-set-and-clear | `frontend/tests/e2e/muse-code/session-goal-set-and-clear.spec.ts` | The Goals sidebar lets the user set a standing objective and clear it through the provider's native goal path. |
| session-goal-pause-and-resume | `frontend/tests/e2e/muse-code/session-goal-pause-and-resume.spec.ts` | The Goals sidebar lets the user pause an active objective and resume it through the provider's native goal path. |
| to-do-sidebar | `frontend/tests/e2e/muse-code/to-do-sidebar.spec.ts` | A native to-do or plan update appears with item statuses in LeapMux's Goals and To-dos sidebar. |
| background-tasks-sidebar | `frontend/tests/e2e/muse-code/background-tasks-sidebar.spec.ts` | A provider-reported subagent or background process appears with its status in the Background tasks sidebar. |
| mcp-tool-execution | `frontend/tests/e2e/muse-code/mcp-tool-execution.spec.ts` | The agent calls a Model Context Protocol tool, receives its result, and can use that result in a subsequent model request. |
| mcp-input-request | `frontend/tests/e2e/muse-code/mcp-input-request.spec.ts` | LeapMux collects fields requested by a Model Context Protocol server through a form or native questions and returns the answers to the server. |
| code-execution | `frontend/tests/e2e/muse-code/code-execution.spec.ts` | The model supplies source to a dedicated code executor or a native script-and-language interface. The native agent runs the source and returns output or errors. Ordinary shell commands and browser-only scripts do not count. Provider notes state whether scripts can call agent tools when the documentation or the source of the agent shows it. |
| output-file-paths | `frontend/tests/e2e/muse-code/output-file-paths.spec.ts` | The agent reports a filesystem path for saved tool output. LeapMux stores that path with the native result. It shows the path and tool properties before the original output or inline preview. Copy keeps the original preview. LeapMux reads no output file. The provider controls file retention. An opaque ID or URI alone does not supply a filesystem path. |
| images-in-tool-results | `frontend/tests/e2e/muse-code/images-in-tool-results.spec.ts` | LeapMux shows an image in the tool result row from live result bytes or the provider's stored attachment record. |
| subagent-transcript-tab | `frontend/tests/e2e/muse-code/subagent-transcript-tab.spec.ts` | A subagent has a separate tab that shows its messages. The messages do not stay only inside the parent's tool card. |
| subagent-live-transcript | `frontend/tests/e2e/muse-code/subagent-live-transcript.spec.ts` | The subagent tab receives transcript rows, including tool activity, while the child runs and before its final report arrives. |
| send-to-a-subagent | `frontend/tests/e2e/muse-code/send-to-a-subagent.spec.ts` | A message typed in the subagent tab reaches that native child session instead of the parent session. |
| interrupt-a-subagent | `frontend/tests/e2e/muse-code/interrupt-a-subagent.spec.ts` | The subagent tab's Interrupt control stops the native child task without stopping its parent. |
| workflow-grouping | `frontend/tests/e2e/muse-code/workflow-grouping.spec.ts` | The Background tasks sidebar shows two or more native workflow work rows under one workflow heading. |
| basic-chat | `frontend/tests/e2e/muse-code/basic-chat.spec.ts` | The provider receives a user prompt and returns an assistant answer in the same tab. LeapMux ends the turn and preserves the answer after reload. |
| conversation-context | `frontend/tests/e2e/muse-code/conversation-context.spec.ts` | A later prompt in the same running session can use the earlier user prompt and assistant answer. A second displayed answer alone does not count. |
| shell-tool-execution | `frontend/tests/e2e/muse-code/shell-tool-execution.spec.ts` | The native shell tool runs a command. Its actual output and failed command result reach the transcript and the next model turn. |
| file-tool-execution | `frontend/tests/e2e/muse-code/file-tool-execution.spec.ts` | Native file tools read and change a scratch file. Filesystem and native result checks prove the contents and changes. LeapMux shows the native change result, which can be a diff or an edited-region snippet. |
| interrupt-a-turn | `frontend/tests/e2e/muse-code/interrupt-a-turn.spec.ts` | The agent tab Interrupt control stops an active native model call or tool and pauses its input queue. The session accepts another prompt after the queue resumes. |
| close-an-agent | `frontend/tests/e2e/muse-code/close-an-agent.spec.ts` | Closing an agent tab stops the native provider process and its owned child processes. The Worker confirms the completed close. |
| session-reset | `frontend/tests/e2e/muse-code/session-reset.spec.ts` | A user command starts a fresh native session or clears its context. The next model turn cannot use a unique marker from the previous conversation. |
| session-resume | `frontend/tests/e2e/muse-code/session-resume.spec.ts` | LeapMux reopens a native session from its picker or after a Worker restart and restores its saved Worker transcript. An external session can reopen without old Worker messages. |
| agent-startup | `frontend/tests/e2e/muse-code/agent-startup.spec.ts` | LeapMux starts the native provider process and delivers input submitted during startup. A startup failure shows an error. Before delivery, LeapMux retains the queued input. After delivery, LeapMux retains the submitted user message. |
| turn-end-sound | `frontend/tests/e2e/muse-code/turn-end-sound.spec.ts` | A completed native turn with tool activity plays the selected notification sound once. A text-only turn follows the configured quiet behavior. |
| generation-progress | `frontend/tests/e2e/muse-code/generation-progress.spec.ts` | The generation indicator reports an advancing token or byte count while native model or tool output arrives. Completed content and results remain in the transcript. |
| editor-requests | `frontend/tests/e2e/muse-code/editor-requests.spec.ts` | LeapMux shows a native multiline editor request and returns its exact text or cancellation. Empty text and whitespace remain distinct from cancellation. |
| workspace-trust | `frontend/tests/e2e/muse-code/workspace-trust.spec.ts` | A native workspace trust request prevents project configuration from loading until the user allows it. Denial keeps the configuration unloaded. |
| swarm-mode | `frontend/tests/e2e/muse-code/swarm-mode.spec.ts` | A LeapMux setting enables the provider's native swarm mode, and the next native turn uses that setting. The setting survives reload. |
| model-error | `frontend/tests/e2e/muse-code/model-error.spec.ts` | The transcript shows a native model-service failure and its useful cause. LeapMux ends the failed turn and can run the next valid turn. |
| credential-isolation | `frontend/tests/e2e/muse-code/credential-isolation.spec.ts` | The E2E fixture launches the provider with private configuration and mock model credentials. A native turn uses that configuration and the local mock service. |
| extended-thinking | `frontend/tests/e2e/muse-code/extended-thinking.spec.ts` | A separate LeapMux setting enables or disables native model thinking independently of model and effort selection. The next native request proves the selected state. |
| fast-mode | `frontend/tests/e2e/muse-code/fast-mode.spec.ts` | A separate LeapMux setting enables or disables the provider's native fast serving mode. Native applied settings or a model request prove the selected state. |
| output-style | `frontend/tests/e2e/muse-code/output-style.spec.ts` | A LeapMux setting selects the provider's native response style. The next native model request carries the selected style instruction. |

## Muse source path manifest

Complete these consolidated Muse files and their declared shared callers.

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
- `frontend/tests/e2e/helpers/museEnvironment.test.ts`.
- `frontend/tests/e2e/helpers/museEnvironment.ts`.
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
- `contracts/muse-protocol.json`.
- `contracts/muse-protocol.schema.json`.
- `icons/agents/muse-code.svg`.

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

### Question control

Before:

```text
Choose the tools
  [x] One
  [ ] Two
  [ ] Three
  [Submit]  [Cancel]
  [one selection can submit despite native minimum two]
```

After:

```text
Choose the tools
  Select exactly 2 options
  [x] One
  [ ] Two
  [ ] Three
  [Submit disabled]  [Cancel]
  Answers remain after a failed response.
```

Enable Submit when the exact native limits permit the current answer.
Preserve typed answers and multi-page question state.

### Provider selector

Before:

```text
New agent
  Provider [29 current providers v]
```

After:

```text
New agent
  Provider [Muse Code icon  Muse Code v]
  Model    [native catalog v]
  Mode     [native supported modes v]
```

### Native settings menus

Before:

```text
Agent settings
  Model             [current native model v]
  [native supported setting is absent]
```

After:

```text
Agent settings
  Model             [current native model v]
  Extended thinking [native choices v]  when the provider offers it
  Fast mode         [native choices v]  when the provider offers it
  Output style      [native choices v]  when the provider offers it
  Swarm mode        [native choices v]  when the provider offers it
  [startup-only setting becomes read-only at its native lifetime boundary]
```

Show only native supported axes and values.
Use the existing themed menu and radio components.

### Plan banner and session picker

Before:

```text
Chat                              New agent
  Plan approval                     Resume [session rows]
  [heading or file can be absent]    [UI alone does not prove native context]
```

After:

```text
Chat                              New agent
  Native plan heading               Resume [exact selected native session]
  Plan File [native path]            Next native request includes prior context
  [Approve] [Decline]                Reset excludes prior native context
  Manual tab title remains
```

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

### Published feature matrix

Before:

```text
Feature                 29 providers
  Native setting        Supported / LeapMux limit / Agent limit
  Muse Code             absent
```

After:

```text
Feature                 30 providers, including Muse Code
  Every feature         Native-supported behavior or proved native limit
  Every cell            Complete same-source browser proof
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
