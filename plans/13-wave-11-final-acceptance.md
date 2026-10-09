# Plan 13: Complete Wave 11 and final acceptance

## Scope and prerequisites

Execute this plan on top of the current `HEAD`.
Start after Wave 10 passes complete acceptance.
This file includes its implementation steps and required evidence.
Use this file and repository source without another plan file or an earlier session transcript.
Run one Task pipeline at a time across the repository.

Execute the target rows whose requirement group starts with `W11-`.
Retain the other rows in a shared record as dependency context.

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


## Stage 13: Complete Wave 11 and final acceptance

### 13.1 Close the requirement inventory

Resolve every requirement and every gap record in this file.
Record exact native evidence for every deciding probe.
Keep accepted same-provider redundant pairs covered by their cited current tests.
Keep the 15 code-execution truncation pairs redundant as approved.
Do not add separate ports for native-only MCP argument fidelity or private metadata removal.
MCP resources keep their existing scope because the feature matrix defines no separate resource row.
Preserve Pi's resource proof and Claude's existing readers.

Apply every matrix note from native evidence.
Keep CodeBuddy stderr as the approved detail note, without changing its source behavior.
Retain each provider-feature file even when another feature uses a similar flow.
Remove no test without its exact approved assertion destination.

### 13.2 Run final verification on one frozen source

Run these commands sequentially:

```sh
task generate
task lint
task test
task test-backend-race -- ./internal/worker/agent/... ./internal/worker/service/... ./cmd/leapmux/...
task test-e2e -- --list --reporter=json
task test-e2e -- --workers=4
task build
task site
```

Save full logs with explicit exit codes and source identities.
Use the repository tools listed under Verification tools and evidence.
Run all configured database integration suites through `task test`, including their Docker prerequisites.
Do not accept a package that reports no test files because an integration tag was absent.
Run desktop lint and tests through their Task targets.

Validate all 1,316 original case mappings against final discovery.
Validate all 1,590 cells against complete final-source browser reports.
A focused subset, attachment pill, source inspection, or older checkpoint cannot accept a cell.
Require first-attempt browser success and zero skips or expected failures.
A source change invalidates every affected final receipt. Refresh the affected complete files and source identity.

### 13.3 Review and self-audit

Read every changed hunk and its enclosing function.
Check correctness, removed behavior, callers, language traps, wrappers, cost, project rules, design, duplication, and layer placement.
Check every removed guard and every removed assertion against its replacement.
Review the plan from beginning to end against the final diff.

Confirm these results explicitly:

- Every listed file reaches its stated final behavior.
- Every new file exists and every approved deletion has its replacement.
- Every UI matches the diagrams below.
- SQL and protobuf match the exact shapes in this plan.
- Generated outputs match their sources and stay untracked.
- Every new and changed test runs and passes.
- Every final lint, typecheck, test, browser, build, and site command passes.
- Every provider limit has actual native evidence.
- Every original case retains its required assertions.
- Every matrix cell has complete-file evidence from the final source.

Correct every legitimate review finding and repeat the affected complete checks.
Stage only the explicit program source and this plan.
Use Conventional Commits with an imperative capitalized subject under 72 characters.
Use Markdown body sections with wrapped bullets and no attribution trailer.
Push the final accepted source to the working branch's remote.
Verify the remote commit identity before reporting completion.

## Exact preservation moves and assertion destinations

Preserve each listed assertion before removing or replacing its original case.
Keep the original and destination case identities in the preservation validator.

### Move-up records

| Report ID | Execution group | Old path and title | Destination or covering case | Receipt |
|---|---|---|---|---|
| P1 agent-questions M1 | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, "multi-question - option click auto-advances to next page" | `frontend/tests/e2e/042-control-request.spec.ts`, "multi-question - option click auto-advances to next page" | pending |
| P1 agent-questions M2 | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, "YOLO button fills unanswered questions" | `frontend/tests/e2e/042-control-request.spec.ts`, "YOLO button fills unanswered questions" | pending |
| P1 agent-questions M3 | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, "multi-question control request stays on the correct agent tab" | `frontend/tests/e2e/042-control-request.spec.ts`, "multi-question control request stays on the correct agent tab" | pending |
| P1 agent-questions M4 | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, "control request on a background agent tab badges it" | `frontend/tests/e2e/042-control-request.spec.ts`, "control request on a background agent tab badges it" | pending |
| P1 agent-questions M5 | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, "control request on a background workspace badges its tab when returned to" | `frontend/tests/e2e/042-control-request.spec.ts`, "control request on a background workspace badges its tab when returned to" | pending |
| P1 agent-questions M6 | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, "AskUserQuestion custom text draft survives page reload" | `frontend/tests/e2e/043-control-request-draft.spec.ts`, "AskUserQuestion custom text draft survives page reload" | pending |
| P1 agent-questions M7 | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, "control request draft is isolated from conversation draft" | `frontend/tests/e2e/043-control-request-draft.spec.ts`, "control request draft is isolated from conversation draft" | pending |
| P1 MU-AS-1 | W2-SMALL | `frontend/tests/e2e/claude-code/agent-startup.spec.ts`, describe "Claude Code agent startup error", "shows in-tab error and rejects subsequent sends" | `frontend/tests/e2e/092-agent-startup-error.spec.ts`, describe "agent startup error", "shows in-tab error and rejects subsequent sends" | pending |
| P1 AS-2 move | W2-SMALL | `frontend/tests/e2e/codex/agent-startup.spec.ts`, "can create multiple Codex agents" | `frontend/tests/e2e/035-tabbar-improvements.spec.ts`, "the new-agent button of each available provider opens an agent of that provider" | pending |
| P1 MU-MODE-1 | W2-MODE | `frontend/tests/e2e/claude-code/mode.spec.ts`, "focus returns to editor after mode change" | `frontend/tests/e2e/044-agent-settings.spec.ts`, describe "Agent Settings", "focus returns to editor after mode change" | pending |
| P1 MU-MODE-2 | W2-MODE | `frontend/tests/e2e/claude-code/mode.spec.ts`, "permission mode change in new agent tab targets correct agent" | `frontend/tests/e2e/044-agent-settings.spec.ts`, "permission mode change in new agent tab targets correct agent" | pending |
| P1 MU-MODE-3 | W2-MODE | `frontend/tests/e2e/claude-code/mode.spec.ts`, "settings loading indicator in the status bar" | `frontend/tests/e2e/044-agent-settings.spec.ts`, "settings loading indicator in the status bar" | pending |
| P1 MU-PAB-1 | W3-PLANBANNER | `frontend/tests/e2e/claude-code/plan-approval-banner.spec.ts`, "approve and switches toggle with feedback on editor content", merged with `frontend/tests/e2e/033-markdown-editor-code.spec.ts`, "ExitPlanMode banner shows Reject when editor is empty and Send feedback when typing" | `frontend/tests/e2e/033-markdown-editor-code.spec.ts`, one test: "ExitPlanMode banner swaps Reject for Send feedback and hides the approval switches while the editor holds text" (with the two 780 px checks) | pending |
| P1 MU-PAB-2 | W3-PLANBANNER | `frontend/tests/e2e/claude-code/plan-approval-banner.spec.ts`, "ExitPlanMode draft survives page reload" | `frontend/tests/e2e/031-markdown-editor-draft.spec.ts`, describe "Draft Persistence", "ExitPlanMode draft survives page reload" | pending |
| P1 MU-PAB-3 | W3-PLANBANNER | `frontend/tests/e2e/claude-code/plan-approval-banner.spec.ts`, "lays the pill radios and their moving copies out identically" | `frontend/tests/e2e/196-pill-group.spec.ts`, describe "pill group segmented control", "lays the pill radios and their moving copies out identically" | pending |
| P1 FT7 move | W3-FILE | `frontend/tests/e2e/codex/file-tool-execution.spec.ts`, "file-change statistics keep one presentation for one file and multiple files" | `frontend/tests/e2e/208-file-change-row-stats.spec.ts`, "file-change statistics keep one presentation for one file and multiple files" | pending |
| P2 session-resume-M1 | W3-RESTART | `frontend/tests/e2e/claude-code/session-resume.spec.ts`, "should hide thinking indicator when worker goes offline during agent turn" | `frontend/tests/e2e/209-worker-outage-agent-state.spec.ts`, "should hide thinking indicator when worker goes offline during agent turn" | pending |
| P2 session-resume-M2 | W3-RESTART | `frontend/tests/e2e/claude-code/session-resume.spec.ts`, the menu-shape block of "offers a closed session, hides the open one, and resumes what was picked" | `frontend/tests/e2e/210-new-agent-session-picker.spec.ts`, "the session menu fits its field, shows each row's age, and routes to the session-ID box and back"; the Claude test stays without the block | pending |
| P2 session-resume-M3 | W3-RESTART | `frontend/tests/e2e/claude-code/session-resume.spec.ts`, "should preserve chat history after hub and worker restart" | `frontend/tests/e2e/023-full-restart.spec.ts`, "should preserve chat history after hub and worker restart" | pending |
| P2 session-resume-M4 | W3-RESTART | `frontend/tests/e2e/claude-code/session-resume.spec.ts`, "should preserve agent tab after clicking it post-restart" | `frontend/tests/e2e/023-full-restart.spec.ts`, "should preserve agent tab after clicking it post-restart" | pending |
| P2 session-resume-M5 | W3-RESTART | `frontend/tests/e2e/claude-code/session-resume.spec.ts`, "should not show thinking indicator after full restart during active turn" | `frontend/tests/e2e/023-full-restart.spec.ts`, "should not show thinking indicator after full restart during active turn" | pending |
| P2 session-reset-M1 | W3-PICKER | `frontend/tests/e2e/claude-code/session-reset.spec.ts`, "slash reset clears context (alias for /clear)" | `frontend/tests/e2e/211-clear-command.spec.ts`, "slash reset clears context (alias for /clear)", plus a `/new` case | pending |
| P2 session-reset-M2 | W3-PICKER | `frontend/tests/e2e/claude-code/session-reset.spec.ts`, "slash clear clears context and shows notification" | `frontend/tests/e2e/211-clear-command.spec.ts`, "slash clear clears context and shows notification" | pending |
| P2 subagent-transcript-tab-M1 | W5-SUBTAB-H | `frontend/tests/e2e/claude-code/subagent-transcript-tab.spec.ts`, steps 3, 7 and 8 of "subagent spawn creates a registry row, a child tab, and isolates the transcript" | `frontend/tests/e2e/212-subagent-registry.spec.ts`, "filters a subagent row by kind and draws it at normal weight" and "closing the parent tab removes its child tab"; the Claude test stays without the blocks | pending |
| P2 background-tasks-sidebar-M1 | W5-BGSHELL | `frontend/tests/e2e/claude-code/background-tasks-sidebar.spec.ts`, "refuses an early empty DOM while an actual native task remains in the Worker registry" | `frontend/tests/e2e/213-background-task-registry-hydration.spec.ts`, "refuses an early empty DOM while an actual native task remains in the Worker registry", with its `ISOLATED_CONTEXT_SPECS` entry | pending |
| P2 session-goal-set-and-clear-M1 | W5-GOALS | `frontend/tests/e2e/codex/session-goal-set-and-clear.spec.ts`, step 6 of "set a goal from the panel, pause it, resume it, and clear it" | `frontend/tests/e2e/214-goal-card.spec.ts`, "clamps a long objective and discloses it with Show more and Show less"; the Codex test stays without step 6 | pending |
| P2 session-goal-set-and-clear-M2 | W5-GOALS | `frontend/tests/e2e/codex/session-goal-set-and-clear.spec.ts`, "opens the goal actions from the to-dos popover" | `frontend/tests/e2e/214-goal-card.spec.ts`, "opens the goal actions from the to-dos popover" | pending |
| P2 images-in-tool-results-M1 | W7-IMAGES | `frontend/tests/e2e/codewhale/images-in-tool-results.spec.ts`, the `expectStoredImageViewer` step of "draws the actual native MCP image before and after reload" | `frontend/tests/e2e/040-chat-message-rendering.spec.ts`, "opens a tool image in the image viewer"; the Codewhale test stays without the step | pending |
| P3 IA-B1 move | W8-SMALL | `frontend/tests/e2e/claude-code/image-attachments.spec.ts`, "attachment-only message (no text) can be sent" | `frontend/tests/e2e/038-attachment-support.spec.ts`, describe "Attachment Support", "attachment-only message (no text) can be sent" | pending |

### Move, merge, or rename records

| Report ID | Execution group | Old path and title | Destination or covering case | Receipt |
|---|---|---|---|---|
| PORT-PROGRAM 7.2 row 1 | W1-INTERRUPT | `frontend/tests/e2e/claude-code/agent-questions.spec.ts`, describe "Agent Settings", "interrupt via control request" | `frontend/tests/e2e/claude-code/interrupt-a-turn.spec.ts`, "interrupt via control request", in the interrupt describe | pending |
| PORT-PROGRAM 7.2 row 2 | W1-INTERRUPT | `frontend/tests/e2e/codex/interrupt-a-turn.spec.ts`, describe "generation progress" | `frontend/tests/e2e/codex/interrupt-a-turn.spec.ts`, describe "codex interrupted partial answer" | pending |
| PORT-PROGRAM 7.2 row 3 | W1-INTERRUPT | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts`, describe "Kiro interrupt, steering and process lifetime" | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts`, describe "Kiro interrupt" | pending |
| PORT-PROGRAM 7.2 row 4 | W1-PERM | `frontend/tests/e2e/goose/permission-prompts.spec.ts`, "permission-prompts: smart mode asks before a removal and auto mode runs it" | `frontend/tests/e2e/goose/permission-prompts.spec.ts`, "smart mode asks before a removal and auto mode runs it" | pending |
| PORT-PROGRAM 7.2 row 5 | W1-PERM | `frontend/tests/e2e/reasonix/permission-prompts.spec.ts`, "permission-prompts: refuses a native write in Plan mode and asks in Normal mode" | `frontend/tests/e2e/reasonix/permission-prompts.spec.ts`, a plain deny in Ask mode (new title); the scenario stays in `frontend/tests/e2e/reasonix/plan-mode.spec.ts` and `frontend/tests/e2e/reasonix/mode.spec.ts` | pending |
| PORT-PROGRAM 7.2 row 6 | W1-PERM | `frontend/tests/e2e/zcode/permission-prompts.spec.ts`, "the permission banner applies the selected bypass pill on allow" | `frontend/tests/e2e/zcode/permission-prompts.spec.ts`, a plain allow under the Unchanged pill (new title); the bypass case stays in `frontend/tests/e2e/zcode/bypass-permissions-shortcut.spec.ts` | pending |
| PORT-PROGRAM 7.2 row 7 | W1-SHELL | the removed pi test "bash command execution renders output in chat" (output in a tool row) | `frontend/tests/e2e/pi/shell-tool-execution.spec.ts`, "keeps actual native shell output and a failed command result" (the tool-row requirement, as a default or Pi's `rowProof`) | pending |
| PORT-PROGRAM 7.2 row 8 | W1-SHELL | the exit-code block of `frontend/tests/e2e/grok-build/file-tool-execution.spec.ts` and `frontend/tests/e2e/kiro/file-tool-execution.spec.ts` | the `shell-tool-execution` spec of each | pending |
| PORT-PROGRAM 7.2 row 9 | W1-EFFORT | the permission and Bypass steps of `frontend/tests/e2e/oh-my-pi/reasoning-effort.spec.ts` "applies Oh My Pi settings, keeps them over a restart and a reload, and sends the thinking level" | removed; covered by `frontend/tests/e2e/oh-my-pi/mode.spec.ts` "applies the native approval mode through a restart and a reload" and `frontend/tests/e2e/oh-my-pi/bypass-permissions-shortcut.spec.ts` "applies native Bypass before and after reload without a permission prompt" | pending |
| PORT-PROGRAM 7.2 row 10 | W2-DECLINE-NATIVE | the Build-mode block of `frontend/tests/e2e/zcode/shell-tool-execution.spec.ts` "a bash command renders as a tool card with its output" | `frontend/tests/e2e/zcode/permission-prompts.spec.ts`, "Build mode runs a read-only command with no approval" | pending |
| PORT-PROGRAM 7.2 row 11 | W3-FILE | the to-do blocks of the `file-tool-execution` spec of grok-build, kiro, qwen-code | removed; covered by each provider's `to-do-sidebar` cell proof | pending |
| PORT-PROGRAM 7.2 row 12 | W3-MODEL | `frontend/tests/e2e/claude-code/agent-startup.spec.ts`, describe "Agent Settings", "default settings on startup" | `frontend/tests/e2e/claude-code/model.spec.ts`, "default settings on startup" | pending |
| PORT-PROGRAM 7.2 row 13 | W3-PICKER | `frontend/tests/e2e/claude-code/session-reset.spec.ts`, "excludes unique prior native context after ${command} and keeps saved Worker rows" (`/clear` and `/reset`) | `frontend/tests/e2e/claude-code/session-reset.spec.ts`, "excludes unique prior native context after /clear and keeps saved Worker rows" (the `/reset` value is session-reset-R1) | pending |
| PORT-PROGRAM 7.2 row 14 | W4-GUARD | the approval-banner block of `frontend/tests/e2e/qoder-cli/file-tool-execution.spec.ts` "seeds, reads and edits a file, and draws the edit diff" | `frontend/tests/e2e/qoder-cli/permission-prompts.spec.ts`, "the approval banner names the file" | pending |
| PORT-PROGRAM 7.2 row 15 | W5-BGSHELL | "keeps the actual native background task row through completion and reload" in `cline`, `grok-build`, `kiro`, `mimo-code`, `oh-my-pi`, `qwen-code` `frontend/tests/e2e/background-tasks-sidebar.spec.ts` | `frontend/tests/e2e/background-tasks-sidebar.spec.ts`s, "keeps the actual native background task row through completion" | pending |
| PORT-PROGRAM 7.2 row 16 | W5-GOALS | `frontend/tests/e2e/pi/session-goal-set-and-clear.spec.ts`, "pauses and resumes a native goal after reload", merged into "controls a real Pi goal through the shared goal panel and confirmation" | `frontend/tests/e2e/pi/session-goal-set-and-clear.spec.ts`, the panel test with the card objective check after the reload (decision 18) | pending |
| PORT-PROGRAM 7.2 row 17 | W6-GOALPAUSE | `frontend/tests/e2e/pi/session-goal-pause-and-resume.spec.ts`, "session-goal-pause-and-resume: pauses and resumes a native goal after reload", merged into "session-goal-pause-and-resume: controls a real Pi goal through the shared goal panel and confirmation" | `frontend/tests/e2e/pi/session-goal-pause-and-resume.spec.ts`, the panel test with the card objective check (P3 D11) | pending |
| PORT-PROGRAM 7.2 row 18 | W3-RESTART | `frontend/tests/e2e/claude-code/session-resume.spec.ts`, "should handle interrupt after worker restart" (no interrupt in the body) | "should handle interrupt after worker restart"; the body now interrupts after the restart | pending |

### Deletion records

| Report ID | Execution group | Old path and title | Destination or covering case | Receipt |
|---|---|---|---|---|
| PORT-PROGRAM 7.3 row 1 | W1-PERM | `frontend/tests/e2e/goose/permission-prompts.spec.ts`, "permission-prompts: the sidebar follows each checklist the agent writes, and keeps it after a reload" | `frontend/tests/e2e/goose/to-do-sidebar.spec.ts` (same call `exerciseGooseTodoListReplacement`) | pending |
| PORT-PROGRAM 7.3 row 2 | W1-PERM | `frontend/tests/e2e/goose/permission-prompts.spec.ts`, "permission-prompts: roundtrips zero, false, and blue through native form elicitation" | `frontend/tests/e2e/goose/mcp-input-request.spec.ts` and `frontend/tests/e2e/goose/mcp-tool-execution.spec.ts` (`exerciseGooseMcpForm`) | pending |
| PORT-PROGRAM 7.3 row 3 | W1-PERM | `frontend/tests/e2e/github-copilot/permission-prompts.spec.ts`, "permission-prompts: places queued guidance in the next native model request" | `frontend/tests/e2e/github-copilot/steer-mid-turn.spec.ts` (same call) | pending |
| PORT-PROGRAM 7.3 row 4 | W1-PERM | `frontend/tests/e2e/kilo/permission-prompts.spec.ts`, same title | `frontend/tests/e2e/kilo/steer-mid-turn.spec.ts` (same call) | pending |
| PORT-PROGRAM 7.3 row 5 | W1-BASICCHAT | `frontend/tests/e2e/opencode/basic-chat.spec.ts`, "agent reconnects after page reload" | `frontend/tests/e2e/080-turn-end-sound-preferences.spec.ts`, "should play ding-dong sound when turn ends" | pending |
| PORT-PROGRAM 7.3 row 6 | W2-SMALL | `frontend/tests/e2e/codex/agent-startup.spec.ts`, "codex agent tab is visible after creation" | `frontend/tests/e2e/035-tabbar-improvements.spec.ts`, "new agent tab focuses editor and surfaces session ID after first turn", and the AS-2 test | pending |
| PORT-PROGRAM 7.3 row 7 | W2-WORKFLOW | `frontend/tests/e2e/codewhale/workflow-grouping.spec.ts`, "shows one workflow row after its native child answers" | "keeps two actual native workflow units inside one opaque workflow row" (after the subset check) | pending |
| PORT-PROGRAM 7.3 row 8 | W2-WORKFLOW | `frontend/tests/e2e/qwen-code/workflow-grouping.spec.ts`, same title | "keeps two actual native workflow units inside one opaque workflow row", in the Qwen file | pending |
| PORT-PROGRAM 7.3 row 9 | W3-MODEL | `frontend/tests/e2e/claude-code/model.spec.ts`, "model persistence across refresh" | claude-code "switch model" | pending |
| PORT-PROGRAM 7.3 row 10 | W3-MODEL | `frontend/tests/e2e/claude-code/model.spec.ts`, "model/effort items not disabled when idle" | claude-code "switch model"; its footnote check targets a test ID that no element has | pending |
| PORT-PROGRAM 7.3 row 11 | W3-MODEL | `frontend/tests/e2e/codex/model.spec.ts`, "the plus menu offers the settings groups the matrix lists" | codex "a model switch reaches the next request" and the codex `reasoning-effort` test | pending |
| PORT-PROGRAM 7.3 row 12 | W3-PLANBANNER | `frontend/tests/e2e/junie/plan-approval-banner.spec.ts`, "a plan raises a review request and the plan entries in the to-do sidebar" | `frontend/tests/e2e/junie/to-do-sidebar.spec.ts`, `frontend/tests/e2e/junie/plan-mode.spec.ts` (same function) | pending |
| PORT-PROGRAM 7.3 row 13 | W3-PLANBANNER | `frontend/tests/e2e/github-copilot/plan-approval-banner.spec.ts`, "plan-approval-banner: uses the native exit tool and resumes after plan approval" | `frontend/tests/e2e/github-copilot/plan-mode.spec.ts`, "uses the native exit tool and resumes after plan approval" | pending |
| PORT-PROGRAM 7.3 row 14 | W3-PLANBANNER | `frontend/tests/e2e/pi/plan-approval-banner.spec.ts`, "plan-approval-banner: tracks a fresh Pi implementation session after plan approval" | `frontend/tests/e2e/pi/plan-mode.spec.ts`, "tracks a fresh Pi implementation session after plan approval" | pending |
| PORT-PROGRAM 7.3 row 15 | W3-PICKER | `frontend/tests/e2e/claude-code/session-reset.spec.ts`, the `/reset` value of the loop (session-reset-R1) | the `/clear` value, and `frontend/tests/e2e/211-clear-command.spec.ts` "slash reset clears context (alias for /clear)" | pending |
| PORT-PROGRAM 7.3 row 16 | W6-STEER | `frontend/tests/e2e/claude-code/steer-mid-turn.spec.ts`, "offers Steer for input queued during a Claude turn" | claude-code "delivers steering to the actual native tool turn before its single turn end" | pending |
| PORT-PROGRAM 7.3 row 17 | W6-MCP | `frontend/tests/e2e/pi/mcp-tool-execution.spec.ts`, "keeps the native real MCP output path and preview after reload" | `frontend/tests/e2e/pi/output-file-paths.spec.ts`, "keeps the native real MCP output path and exact preview after reload" | pending |
| PORT-PROGRAM 7.3 row 18 | W6-GOALPAUSE | `frontend/tests/e2e/reasonix/session-goal-pause-and-resume.spec.ts`, "session-goal-pause-and-resume: sets and clears a native Reasonix goal before and after cancellation" | `frontend/tests/e2e/reasonix/session-goal-set-and-clear.spec.ts`, "sets and clears a native Reasonix goal before and after cancellation" | pending |
| PORT-PROGRAM 7.3 row 19 | W8-CODEEXEC | `frontend/tests/e2e/pi/code-execution.spec.ts`, "keeps the native codemode output path and preview without an MCP call after reload" | `frontend/tests/e2e/pi/output-file-paths.spec.ts`, "keeps the native codemode output path and exact preview after reload" | pending |
| PORT-PROGRAM 7.3 row 20 | W8-CHILDOPS | `frontend/tests/e2e/goose/send-to-a-subagent.spec.ts`, "send-to-a-subagent: delegate spawn creates a clickable row with a tool-request transcript" | `frontend/tests/e2e/goose/subagent-transcript-tab.spec.ts`, "delegate spawn creates a clickable row with a tool-request transcript" | pending |
| PORT-PROGRAM 7.3 row 21 | W8-SMALL | `frontend/tests/e2e/codex/close-an-agent.spec.ts`, "can close Codex agent tab" | `frontend/tests/e2e/080-turn-end-sound-preferences.spec.ts`, "should NOT play sound when closing an agent tab" | pending |

### Additional exact helper and coverage moves

| Report ID | Execution group | Old path or block | Destination | Final evidence |
|---|---|---|---|---|
| P2 D3 | W3-RESTART | `frontend/tests/e2e/claude-code/workerRestart.ts`; `frontend/tests/e2e/claude-code/workerRestart.test.ts` | `frontend/tests/e2e/helpers/workerRestart.ts`; `frontend/tests/e2e/helpers/workerRestart.test.ts` | Update direct imports in Claude session-resume and model-context-on-resume in the same change. Preserve all helper tests. |
| P1 question helpers | W3-QUESTIONS-H | Remaining question helpers in `frontend/tests/e2e/claude-code/agent-questions.spec.ts` | `frontend/tests/e2e/helpers/nativeQuestion.ts`; `frontend/tests/e2e/helpers/nativeQuestion.test.ts` | Transfer expectQuestionAnswers and submitAnswers. Reuse the accepted askQuestions source. Preserve native replies. |
| P3 PM-B1 | W8-PLANMODE | nativeCurrentMode in `frontend/tests/e2e/dirac/mode.spec.ts` | `frontend/tests/e2e/dirac/modeScenario.ts`; `frontend/tests/e2e/dirac/modeScenario.test.ts` | Update both Dirac callers. Preserve native environment-details parsing. |
| P1 decision 5 to-do blocks | W3-FILE | To-do blocks in Grok, Kiro, and Qwen file-tool specs | `frontend/tests/e2e/grok-build/to-do-sidebar.spec.ts`; `frontend/tests/e2e/kiro/to-do-sidebar.spec.ts`; `frontend/tests/e2e/qwen-code/to-do-sidebar.spec.ts` | Transfer the same assertions before removal. Keep all file-tool assertions. |
| P3 D15 | W8-SMALL | No generic attachment-row reload case | `frontend/tests/e2e/038-attachment-support.spec.ts` | Add one real stored attachment user-row proof after reload. Preserve native attachment validators and Letta limit proofs. |
| Section 11 item 11 | W11-CLOSE | Pi original close case `568a568ce26d7af028b3-ba96ada01498214d231e` | Pi "closes the native agent and its actual owned process tree" | Record the final destination case ID and complete file receipt. |

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

## Final requirement audit

Verify every behavior below against final source and final complete-file evidence.
The target assignments retain the original execution sequence.

### L001: P1 Q-P5 / AQ12

Kind: port. Execution wave: 1.

AQ12. The Interrupt control of the banner stops a turn that waits on a question. The Worker withdraws the question, the agent becomes idle, the input queue pauses, and the next turn runs.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W1-INTERRUPT | `frontend/tests/e2e/cline/interrupt-a-turn.spec.ts` |
| codewhale | W1-INTERRUPT | `frontend/tests/e2e/codewhale/interrupt-a-turn.spec.ts` |
| codex | W1-INTERRUPT | `frontend/tests/e2e/codex/interrupt-a-turn.spec.ts` |
| cursor | W1-INTERRUPT | `frontend/tests/e2e/cursor/interrupt-a-turn.spec.ts` |
| deepseek-harness | W1-INTERRUPT | `frontend/tests/e2e/deepseek-harness/interrupt-a-turn.spec.ts` |
| dirac | W1-INTERRUPT | `frontend/tests/e2e/dirac/interrupt-a-turn.spec.ts` |
| factory-droid | W1-INTERRUPT | `frontend/tests/e2e/factory-droid/interrupt-a-turn.spec.ts` |
| github-copilot | W1-INTERRUPT | `frontend/tests/e2e/github-copilot/interrupt-a-turn.spec.ts` |
| grok-build | W1-INTERRUPT | `frontend/tests/e2e/grok-build/interrupt-a-turn.spec.ts` |
| junie | W1-INTERRUPT | `frontend/tests/e2e/junie/interrupt-a-turn.spec.ts` |
| kilo | W1-INTERRUPT | `frontend/tests/e2e/kilo/interrupt-a-turn.spec.ts` |
| kimi-code | W1-INTERRUPT | `frontend/tests/e2e/kimi-code/interrupt-a-turn.spec.ts` |
| kiro | W1-INTERRUPT | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts` |
| mimo-code | W1-INTERRUPT | `frontend/tests/e2e/mimo-code/interrupt-a-turn.spec.ts` |
| oh-my-pi | W1-INTERRUPT | `frontend/tests/e2e/oh-my-pi/interrupt-a-turn.spec.ts` |
| opencode | W1-INTERRUPT | `frontend/tests/e2e/opencode/interrupt-a-turn.spec.ts` |
| pi | W1-INTERRUPT | `frontend/tests/e2e/pi/interrupt-a-turn.spec.ts` |
| qoder-cli | W1-INTERRUPT | `frontend/tests/e2e/qoder-cli/interrupt-a-turn.spec.ts` |
| qwen-code | W1-INTERRUPT | `frontend/tests/e2e/qwen-code/interrupt-a-turn.spec.ts` |
| zcode | W1-INTERRUPT | `frontend/tests/e2e/zcode/interrupt-a-turn.spec.ts` |

### L002: P1 IT-1 / I1

Kind: port. Execution wave: 1.

I1. Interrupt stops a running native tool, and the session takes the next prompt.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W1-INTERRUPT | `frontend/tests/e2e/codex/interrupt-a-turn.spec.ts` |
| cursor | W1-INTERRUPT | `frontend/tests/e2e/cursor/interrupt-a-turn.spec.ts` |
| github-copilot | W1-INTERRUPT | `frontend/tests/e2e/github-copilot/interrupt-a-turn.spec.ts` |
| goose | W1-INTERRUPT | `frontend/tests/e2e/goose/interrupt-a-turn.spec.ts` |
| kilo | W1-INTERRUPT | `frontend/tests/e2e/kilo/interrupt-a-turn.spec.ts` |
| kiro | W1-INTERRUPT | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts` |
| opencode | W1-INTERRUPT | `frontend/tests/e2e/opencode/interrupt-a-turn.spec.ts` |
| pi | W1-INTERRUPT | `frontend/tests/e2e/pi/interrupt-a-turn.spec.ts` |
| reasonix | W1-INTERRUPT | `frontend/tests/e2e/reasonix/interrupt-a-turn.spec.ts` |
| zcode | W1-INTERRUPT | `frontend/tests/e2e/zcode/interrupt-a-turn.spec.ts` |

### L003: P1 IT-2 / I3

Kind: port. Execution wave: 1.

I3. A partial streamed answer keeps the marker `Text truncated by interruption.` after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W1-INTERRUPT | `frontend/tests/e2e/cline/interrupt-a-turn.spec.ts` |
| codewhale | W1-INTERRUPT | `frontend/tests/e2e/codewhale/interrupt-a-turn.spec.ts` |
| command-code | W1-INTERRUPT | `frontend/tests/e2e/command-code/interrupt-a-turn.spec.ts` |
| deepseek-harness | W1-INTERRUPT | `frontend/tests/e2e/deepseek-harness/interrupt-a-turn.spec.ts` |
| github-copilot | W1-INTERRUPT | `frontend/tests/e2e/github-copilot/interrupt-a-turn.spec.ts` |
| kimi-code | W1-INTERRUPT | `frontend/tests/e2e/kimi-code/interrupt-a-turn.spec.ts` |
| mimo-code | W1-INTERRUPT | `frontend/tests/e2e/mimo-code/interrupt-a-turn.spec.ts` |
| oh-my-pi | W1-INTERRUPT | `frontend/tests/e2e/oh-my-pi/interrupt-a-turn.spec.ts` |
| pi | W1-INTERRUPT | `frontend/tests/e2e/pi/interrupt-a-turn.spec.ts` |
| zcode | W1-INTERRUPT | `frontend/tests/e2e/zcode/interrupt-a-turn.spec.ts` |

### L004: P1 IT-3 / I5

Kind: port. Execution wave: 1.

I5. The divider of an interrupted tool turn counts the stopped tool (`1 tool`).

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-INTERRUPT | `frontend/tests/e2e/amp/interrupt-a-turn.spec.ts` |
| claude-code | W1-INTERRUPT | `frontend/tests/e2e/claude-code/interrupt-a-turn.spec.ts` |
| cline | W1-INTERRUPT | `frontend/tests/e2e/cline/interrupt-a-turn.spec.ts` |
| codebuddy-code | W1-INTERRUPT | `frontend/tests/e2e/codebuddy-code/interrupt-a-turn.spec.ts` |
| codewhale | W1-INTERRUPT | `frontend/tests/e2e/codewhale/interrupt-a-turn.spec.ts` |
| codex | W1-INTERRUPT | `frontend/tests/e2e/codex/interrupt-a-turn.spec.ts` |
| command-code | W1-INTERRUPT | `frontend/tests/e2e/command-code/interrupt-a-turn.spec.ts` |
| cursor | W1-INTERRUPT | `frontend/tests/e2e/cursor/interrupt-a-turn.spec.ts` |
| deepseek-harness | W1-INTERRUPT | `frontend/tests/e2e/deepseek-harness/interrupt-a-turn.spec.ts` |
| dirac | W1-INTERRUPT | `frontend/tests/e2e/dirac/interrupt-a-turn.spec.ts` |
| factory-droid | W1-INTERRUPT | `frontend/tests/e2e/factory-droid/interrupt-a-turn.spec.ts` |
| fast-agent | W1-INTERRUPT | `frontend/tests/e2e/fast-agent/interrupt-a-turn.spec.ts` |
| gemini-cli | W1-INTERRUPT | `frontend/tests/e2e/gemini-cli/interrupt-a-turn.spec.ts` |
| github-copilot | W1-INTERRUPT | `frontend/tests/e2e/github-copilot/interrupt-a-turn.spec.ts` |
| goose | W1-INTERRUPT | `frontend/tests/e2e/goose/interrupt-a-turn.spec.ts` |
| grok-build | W1-INTERRUPT | `frontend/tests/e2e/grok-build/interrupt-a-turn.spec.ts` |
| junie | W1-INTERRUPT | `frontend/tests/e2e/junie/interrupt-a-turn.spec.ts` |
| kilo | W1-INTERRUPT | `frontend/tests/e2e/kilo/interrupt-a-turn.spec.ts` |
| kimi-code | W1-INTERRUPT | `frontend/tests/e2e/kimi-code/interrupt-a-turn.spec.ts` |
| kiro | W1-INTERRUPT | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts` |
| letta-code | W1-INTERRUPT | `frontend/tests/e2e/letta-code/interrupt-a-turn.spec.ts` |
| mimo-code | W1-INTERRUPT | `frontend/tests/e2e/mimo-code/interrupt-a-turn.spec.ts` |
| opencode | W1-INTERRUPT | `frontend/tests/e2e/opencode/interrupt-a-turn.spec.ts` |
| pi | W1-INTERRUPT | `frontend/tests/e2e/pi/interrupt-a-turn.spec.ts` |
| qoder-cli | W1-INTERRUPT | `frontend/tests/e2e/qoder-cli/interrupt-a-turn.spec.ts` |
| qwen-code | W1-INTERRUPT | `frontend/tests/e2e/qwen-code/interrupt-a-turn.spec.ts` |
| reasonix | W1-INTERRUPT | `frontend/tests/e2e/reasonix/interrupt-a-turn.spec.ts` |
| zcode | W1-INTERRUPT | `frontend/tests/e2e/zcode/interrupt-a-turn.spec.ts` |

### L005: P1 OP5 / OP5

Kind: port. Execution wave: 1.

OP5. A small output shows no output-path chip.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W1-SHELL | `frontend/tests/e2e/claude-code/output-file-paths.spec.ts` |
| codebuddy-code | W1-SHELL | `frontend/tests/e2e/codebuddy-code/output-file-paths.spec.ts` |
| codewhale | W1-SHELL | `frontend/tests/e2e/codewhale/output-file-paths.spec.ts` |
| command-code | W1-SHELL | `frontend/tests/e2e/command-code/output-file-paths.spec.ts` |
| deepseek-harness | W1-SHELL | `frontend/tests/e2e/deepseek-harness/output-file-paths.spec.ts` |
| dirac | W1-SHELL | `frontend/tests/e2e/dirac/output-file-paths.spec.ts` |
| factory-droid | W1-SHELL | `frontend/tests/e2e/factory-droid/output-file-paths.spec.ts` |
| github-copilot | W1-SHELL | `frontend/tests/e2e/github-copilot/output-file-paths.spec.ts` |
| grok-build | W1-SHELL | `frontend/tests/e2e/grok-build/output-file-paths.spec.ts` |
| junie | W1-SHELL | `frontend/tests/e2e/junie/output-file-paths.spec.ts` |
| kilo | W1-SHELL | `frontend/tests/e2e/kilo/output-file-paths.spec.ts` |
| kimi-code | W1-SHELL | `frontend/tests/e2e/kimi-code/output-file-paths.spec.ts` |
| letta-code | W1-SHELL | `frontend/tests/e2e/letta-code/output-file-paths.spec.ts` |
| mimo-code | W1-SHELL | `frontend/tests/e2e/mimo-code/output-file-paths.spec.ts` |
| opencode | W1-SHELL | `frontend/tests/e2e/opencode/output-file-paths.spec.ts` |
| pi | W1-SHELL | `frontend/tests/e2e/pi/output-file-paths.spec.ts` |
| qoder-cli | W1-SHELL | `frontend/tests/e2e/qoder-cli/output-file-paths.spec.ts` |
| qwen-code | W1-SHELL | `frontend/tests/e2e/qwen-code/output-file-paths.spec.ts` |
| zcode | W1-SHELL | `frontend/tests/e2e/zcode/output-file-paths.spec.ts` |

### L006: P1 PP-4 unclear / PP-4

Kind: probe. Execution wave: 1.

PP-4. A remembered allow (session, always, or workspace scope) covers the same later call, which then raises no banner.

| Target | Requirement group | Complete browser file |
|---|---|---|
| dirac | W1-PERM | `frontend/tests/e2e/dirac/permission-prompts.spec.ts` |
| gemini-cli | W1-PERM | `frontend/tests/e2e/gemini-cli/permission-prompts.spec.ts` |
| goose | W1-PERM | `frontend/tests/e2e/goose/permission-prompts.spec.ts` |
| grok-build | W1-PERM | `frontend/tests/e2e/grok-build/permission-prompts.spec.ts` |
| junie | W1-PERM | `frontend/tests/e2e/junie/permission-prompts.spec.ts` |
| reasonix | W1-PERM | `frontend/tests/e2e/reasonix/permission-prompts.spec.ts` |

### L007: P1 PP-E1 / PP-1

Kind: port. Execution wave: 1.

PP-1. The reader allows a call through the banner, and the call runs.

| Target | Requirement group | Complete browser file |
|---|---|---|
| goose | W1-PERM | `frontend/tests/e2e/goose/permission-prompts.spec.ts` |

### L008: P1 PP-E2 / PP-3

Kind: port. Execution wave: 1.

PP-3. Text in the composer turns Deny into "Send feedback", and the typed reason reaches the model, in the native reply or as the next message.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W1-PERM | `frontend/tests/e2e/claude-code/permission-prompts.spec.ts` |
| codewhale | W1-PERM | `frontend/tests/e2e/codewhale/permission-prompts.spec.ts` |
| codex | W1-PERM | `frontend/tests/e2e/codex/permission-prompts.spec.ts` |
| cursor | W1-PERM | `frontend/tests/e2e/cursor/permission-prompts.spec.ts` |
| deepseek-harness | W1-PERM | `frontend/tests/e2e/deepseek-harness/permission-prompts.spec.ts` |
| dirac | W1-PERM | `frontend/tests/e2e/dirac/permission-prompts.spec.ts` |
| fast-agent | W1-PERM | `frontend/tests/e2e/fast-agent/permission-prompts.spec.ts` |
| gemini-cli | W1-PERM | `frontend/tests/e2e/gemini-cli/permission-prompts.spec.ts` |
| github-copilot | W1-PERM | `frontend/tests/e2e/github-copilot/permission-prompts.spec.ts` |
| goose | W1-PERM | `frontend/tests/e2e/goose/permission-prompts.spec.ts` |
| junie | W1-PERM | `frontend/tests/e2e/junie/permission-prompts.spec.ts` |
| kilo | W1-PERM | `frontend/tests/e2e/kilo/permission-prompts.spec.ts` |
| kimi-code | W1-PERM | `frontend/tests/e2e/kimi-code/permission-prompts.spec.ts` |
| letta-code | W1-PERM | `frontend/tests/e2e/letta-code/permission-prompts.spec.ts` |
| oh-my-pi | W1-PERM | `frontend/tests/e2e/oh-my-pi/permission-prompts.spec.ts` |
| opencode | W1-PERM | `frontend/tests/e2e/opencode/permission-prompts.spec.ts` |
| reasonix | W1-PERM | `frontend/tests/e2e/reasonix/permission-prompts.spec.ts` |
| zcode | W1-PERM | `frontend/tests/e2e/zcode/permission-prompts.spec.ts` |

### L009: P1 PP-E3 / PP-4

Kind: port. Execution wave: 1.

PP-4. A remembered allow (session, always, or workspace scope) covers the same later call, which then raises no banner.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W1-PERM | `frontend/tests/e2e/codex/permission-prompts.spec.ts` |
| cursor | W1-PERM | `frontend/tests/e2e/cursor/permission-prompts.spec.ts` |
| fast-agent | W1-PERM | `frontend/tests/e2e/fast-agent/permission-prompts.spec.ts` |
| github-copilot | W1-PERM | `frontend/tests/e2e/github-copilot/permission-prompts.spec.ts` |
| kilo | W1-PERM | `frontend/tests/e2e/kilo/permission-prompts.spec.ts` |
| opencode | W1-PERM | `frontend/tests/e2e/opencode/permission-prompts.spec.ts` |
| qwen-code | W1-PERM | `frontend/tests/e2e/qwen-code/permission-prompts.spec.ts` |

### L010: P1 PP-E4 / PP-6

Kind: port. Execution wave: 1.

PP-6. The transcript keeps the answer as the provider's own decision words (the saved control answer row).

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W1-PERM | `frontend/tests/e2e/claude-code/permission-prompts.spec.ts` |
| cline | W1-PERM | `frontend/tests/e2e/cline/permission-prompts.spec.ts` |
| codebuddy-code | W1-PERM | `frontend/tests/e2e/codebuddy-code/permission-prompts.spec.ts` |
| codewhale | W1-PERM | `frontend/tests/e2e/codewhale/permission-prompts.spec.ts` |
| codex | W1-PERM | `frontend/tests/e2e/codex/permission-prompts.spec.ts` |
| cursor | W1-PERM | `frontend/tests/e2e/cursor/permission-prompts.spec.ts` |
| deepseek-harness | W1-PERM | `frontend/tests/e2e/deepseek-harness/permission-prompts.spec.ts` |
| dirac | W1-PERM | `frontend/tests/e2e/dirac/permission-prompts.spec.ts` |
| fast-agent | W1-PERM | `frontend/tests/e2e/fast-agent/permission-prompts.spec.ts` |
| gemini-cli | W1-PERM | `frontend/tests/e2e/gemini-cli/permission-prompts.spec.ts` |
| github-copilot | W1-PERM | `frontend/tests/e2e/github-copilot/permission-prompts.spec.ts` |
| goose | W1-PERM | `frontend/tests/e2e/goose/permission-prompts.spec.ts` |
| junie | W1-PERM | `frontend/tests/e2e/junie/permission-prompts.spec.ts` |
| kimi-code | W1-PERM | `frontend/tests/e2e/kimi-code/permission-prompts.spec.ts` |
| oh-my-pi | W1-PERM | `frontend/tests/e2e/oh-my-pi/permission-prompts.spec.ts` |
| qoder-cli | W1-PERM | `frontend/tests/e2e/qoder-cli/permission-prompts.spec.ts` |
| qwen-code | W1-PERM | `frontend/tests/e2e/qwen-code/permission-prompts.spec.ts` |
| reasonix | W1-PERM | `frontend/tests/e2e/reasonix/permission-prompts.spec.ts` |
| zcode | W1-PERM | `frontend/tests/e2e/zcode/permission-prompts.spec.ts` |

### L011: P1 PP-E5 / PP-7

Kind: port. Execution wave: 1.

PP-7. A refused call reads "Declined" with its refusal, before and after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-PERM | `frontend/tests/e2e/amp/permission-prompts.spec.ts` |
| cline | W1-PERM | `frontend/tests/e2e/cline/permission-prompts.spec.ts` |
| codex | W1-PERM | `frontend/tests/e2e/codex/permission-prompts.spec.ts` |
| command-code | W1-PERM | `frontend/tests/e2e/command-code/permission-prompts.spec.ts` |
| cursor | W1-PERM | `frontend/tests/e2e/cursor/permission-prompts.spec.ts` |

### L012: P1 PP-E6 / PP-8

Kind: port. Execution wave: 1.

PP-8. A denied MCP tool call never reaches the MCP server.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W1-PERM | `frontend/tests/e2e/codex/permission-prompts.spec.ts` |

### L013: P1 RE-3 unclear / RE-3

Kind: probe. Execution wave: 1.

RE-3. A model that offers no effort levels hides the effort control, in the menu and as a composer chip.

| Target | Requirement group | Complete browser file |
|---|---|---|
| kilo | W1-EFFORT | `frontend/tests/e2e/kilo/reasoning-effort.spec.ts` |
| opencode | W1-EFFORT | `frontend/tests/e2e/opencode/reasoning-effort.spec.ts` |
| qwen-code | W1-EFFORT | `frontend/tests/e2e/qwen-code/reasoning-effort.spec.ts` |
| reasonix | W1-EFFORT | `frontend/tests/e2e/reasonix/reasoning-effort.spec.ts` |

### L014: P1 RE-4 unclear / RE-4

Kind: probe. Execution wave: 1.

RE-4. The effort menu of a model is the same before and after a model round trip, so the static fallback and the live catalog agree.

| Target | Requirement group | Complete browser file |
|---|---|---|
| reasonix | W1-EFFORT | `frontend/tests/e2e/reasonix/reasoning-effort.spec.ts` |

### L015: P1 RE-6 unclear / RE-6

Kind: probe. Execution wave: 1.

RE-6. A round trip through a model that lacks the chosen level leaves a defined effort, and the next native request carries it.

| Target | Requirement group | Complete browser file |
|---|---|---|
| kilo | W1-EFFORT | `frontend/tests/e2e/kilo/reasoning-effort.spec.ts` |
| opencode | W1-EFFORT | `frontend/tests/e2e/opencode/reasoning-effort.spec.ts` |
| qwen-code | W1-EFFORT | `frontend/tests/e2e/qwen-code/reasoning-effort.spec.ts` |
| reasonix | W1-EFFORT | `frontend/tests/e2e/reasonix/reasoning-effort.spec.ts` |

### L016: P1 RE-7 unclear / RE-7

Kind: probe. Execution wave: 1.

RE-7. In an automatic-effort session, the menu shows the level that the agent chose, and a model switch keeps that level.

| Target | Requirement group | Complete browser file |
|---|---|---|
| command-code | W1-EFFORT | `frontend/tests/e2e/command-code/reasoning-effort.spec.ts` |
| mimo-code | W1-EFFORT | `frontend/tests/e2e/mimo-code/reasoning-effort.spec.ts` |
| pi | W1-EFFORT | `frontend/tests/e2e/pi/reasoning-effort.spec.ts` |
| zcode | W1-EFFORT | `frontend/tests/e2e/zcode/reasoning-effort.spec.ts` |

### L017: P1 RE-E1 / RE-2

Kind: port. Execution wave: 1.

RE-2. A model switch keeps an effort that the new model offers, on screen, in the Worker row, and in the next native request, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| command-code | W1-EFFORT | `frontend/tests/e2e/command-code/reasoning-effort.spec.ts` |
| dirac | W1-EFFORT | `frontend/tests/e2e/dirac/reasoning-effort.spec.ts` |
| goose | W1-EFFORT | `frontend/tests/e2e/goose/reasoning-effort.spec.ts` |
| grok-build | W1-EFFORT | `frontend/tests/e2e/grok-build/reasoning-effort.spec.ts` |
| junie | W1-EFFORT | `frontend/tests/e2e/junie/reasoning-effort.spec.ts` |
| kimi-code | W1-EFFORT | `frontend/tests/e2e/kimi-code/reasoning-effort.spec.ts` |

### L018: P1 RE-E2 / RE-3

Kind: port. Execution wave: 1.

RE-3. A model that offers no effort levels hides the effort control, in the menu and as a composer chip.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W1-EFFORT | `frontend/tests/e2e/cline/reasoning-effort.spec.ts` |
| codewhale | W1-EFFORT | `frontend/tests/e2e/codewhale/reasoning-effort.spec.ts` |
| command-code | W1-EFFORT | `frontend/tests/e2e/command-code/reasoning-effort.spec.ts` |
| deepseek-harness | W1-EFFORT | `frontend/tests/e2e/deepseek-harness/reasoning-effort.spec.ts` |
| github-copilot | W1-EFFORT | `frontend/tests/e2e/github-copilot/reasoning-effort.spec.ts` |
| grok-build | W1-EFFORT | `frontend/tests/e2e/grok-build/reasoning-effort.spec.ts` |
| junie | W1-EFFORT | `frontend/tests/e2e/junie/reasoning-effort.spec.ts` |
| kiro | W1-EFFORT | `frontend/tests/e2e/kiro/reasoning-effort.spec.ts` |
| mimo-code | W1-EFFORT | `frontend/tests/e2e/mimo-code/reasoning-effort.spec.ts` |
| zcode | W1-EFFORT | `frontend/tests/e2e/zcode/reasoning-effort.spec.ts` |

### L019: P1 RE-E3 / RE-4

Kind: port. Execution wave: 1.

RE-4. The effort menu of a model is the same before and after a model round trip, so the static fallback and the live catalog agree.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W1-EFFORT | `frontend/tests/e2e/codex/reasoning-effort.spec.ts` |
| command-code | W1-EFFORT | `frontend/tests/e2e/command-code/reasoning-effort.spec.ts` |
| factory-droid | W1-EFFORT | `frontend/tests/e2e/factory-droid/reasoning-effort.spec.ts` |
| pi | W1-EFFORT | `frontend/tests/e2e/pi/reasoning-effort.spec.ts` |

### L020: P1 RE-E4 / RE-6

Kind: port. Execution wave: 1.

RE-6. A round trip through a model that lacks the chosen level leaves a defined effort, and the next native request carries it.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W1-EFFORT | `frontend/tests/e2e/cline/reasoning-effort.spec.ts` |
| codewhale | W1-EFFORT | `frontend/tests/e2e/codewhale/reasoning-effort.spec.ts` |
| codex | W1-EFFORT | `frontend/tests/e2e/codex/reasoning-effort.spec.ts` |
| command-code | W1-EFFORT | `frontend/tests/e2e/command-code/reasoning-effort.spec.ts` |
| deepseek-harness | W1-EFFORT | `frontend/tests/e2e/deepseek-harness/reasoning-effort.spec.ts` |
| factory-droid | W1-EFFORT | `frontend/tests/e2e/factory-droid/reasoning-effort.spec.ts` |
| github-copilot | W1-EFFORT | `frontend/tests/e2e/github-copilot/reasoning-effort.spec.ts` |
| grok-build | W1-EFFORT | `frontend/tests/e2e/grok-build/reasoning-effort.spec.ts` |
| junie | W1-EFFORT | `frontend/tests/e2e/junie/reasoning-effort.spec.ts` |
| kimi-code | W1-EFFORT | `frontend/tests/e2e/kimi-code/reasoning-effort.spec.ts` |
| kiro | W1-EFFORT | `frontend/tests/e2e/kiro/reasoning-effort.spec.ts` |
| mimo-code | W1-EFFORT | `frontend/tests/e2e/mimo-code/reasoning-effort.spec.ts` |
| oh-my-pi | W1-EFFORT | `frontend/tests/e2e/oh-my-pi/reasoning-effort.spec.ts` |
| pi | W1-EFFORT | `frontend/tests/e2e/pi/reasoning-effort.spec.ts` |
| zcode | W1-EFFORT | `frontend/tests/e2e/zcode/reasoning-effort.spec.ts` |

### L021: P1 RE-E5 / RE-7

Kind: port. Execution wave: 1.

RE-7. In an automatic-effort session, the menu shows the level that the agent chose, and a model switch keeps that level.

| Target | Requirement group | Complete browser file |
|---|---|---|
| github-copilot | W1-EFFORT | `frontend/tests/e2e/github-copilot/reasoning-effort.spec.ts` |

### L022: P1 RE-E6 / RE-8

Kind: port. Execution wave: 1.

RE-8. An effort and a mode chosen together both hold after a reload; where the mode change restarts the agent, the next native request still carries the effort.

| Target | Requirement group | Complete browser file |
|---|---|---|
| command-code | W1-EFFORT | `frontend/tests/e2e/command-code/reasoning-effort.spec.ts` |
| kiro | W1-EFFORT | `frontend/tests/e2e/kiro/reasoning-effort.spec.ts` |

### L023: P1 SH1 / SH1

Kind: port. Execution wave: 1.

SH1. A failed command: its stderr and its nonzero exit reach the tool row and the next model request.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-SHELL | `frontend/tests/e2e/amp/shell-tool-execution.spec.ts` |
| mimo-code | W1-SHELL | `frontend/tests/e2e/mimo-code/shell-tool-execution.spec.ts` |
| oh-my-pi | W1-SHELL | `frontend/tests/e2e/oh-my-pi/shell-tool-execution.spec.ts` |

### L024: P1 SH2 / SH2

Kind: port. Execution wave: 1.

SH2. The header of a failed command row states the native exit code (`Error (exit N)`).

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-SHELL | `frontend/tests/e2e/amp/shell-tool-execution.spec.ts` |
| claude-code | W1-SHELL | `frontend/tests/e2e/claude-code/shell-tool-execution.spec.ts` |
| codewhale | W1-SHELL | `frontend/tests/e2e/codewhale/shell-tool-execution.spec.ts` |
| deepseek-harness | W1-SHELL | `frontend/tests/e2e/deepseek-harness/shell-tool-execution.spec.ts` |
| dirac | W1-SHELL | `frontend/tests/e2e/dirac/shell-tool-execution.spec.ts` |
| fast-agent | W1-SHELL | `frontend/tests/e2e/fast-agent/shell-tool-execution.spec.ts` |
| gemini-cli | W1-SHELL | `frontend/tests/e2e/gemini-cli/shell-tool-execution.spec.ts` |
| github-copilot | W1-SHELL | `frontend/tests/e2e/github-copilot/shell-tool-execution.spec.ts` |
| goose | W1-SHELL | `frontend/tests/e2e/goose/shell-tool-execution.spec.ts` |
| mimo-code | W1-SHELL | `frontend/tests/e2e/mimo-code/shell-tool-execution.spec.ts` |
| oh-my-pi | W1-SHELL | `frontend/tests/e2e/oh-my-pi/shell-tool-execution.spec.ts` |
| pi | W1-SHELL | `frontend/tests/e2e/pi/shell-tool-execution.spec.ts` |
| qoder-cli | W1-SHELL | `frontend/tests/e2e/qoder-cli/shell-tool-execution.spec.ts` |
| qwen-code | W1-SHELL | `frontend/tests/e2e/qwen-code/shell-tool-execution.spec.ts` |
| zcode | W1-SHELL | `frontend/tests/e2e/zcode/shell-tool-execution.spec.ts` |

### L025: P1 SH2 unclear / SH2

Kind: probe. Execution wave: 1.

SH2. The header of a failed command row states the native exit code (`Error (exit N)`).

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W1-SHELL | `frontend/tests/e2e/codebuddy-code/shell-tool-execution.spec.ts` |
| command-code | W1-SHELL | `frontend/tests/e2e/command-code/shell-tool-execution.spec.ts` |
| junie | W1-SHELL | `frontend/tests/e2e/junie/shell-tool-execution.spec.ts` |
| kilo | W1-SHELL | `frontend/tests/e2e/kilo/shell-tool-execution.spec.ts` |
| letta-code | W1-SHELL | `frontend/tests/e2e/letta-code/shell-tool-execution.spec.ts` |
| opencode | W1-SHELL | `frontend/tests/e2e/opencode/shell-tool-execution.spec.ts` |
| reasonix | W1-SHELL | `frontend/tests/e2e/reasonix/shell-tool-execution.spec.ts` |

### L026: P1 SH3 / SH3

Kind: port. Execution wave: 1.

SH3. The row body draws the command output without the native notice, trailer or result record that the provider adds for its model.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W1-SHELL | `frontend/tests/e2e/claude-code/shell-tool-execution.spec.ts` |
| codex | W1-SHELL | `frontend/tests/e2e/codex/shell-tool-execution.spec.ts` |
| deepseek-harness | W1-SHELL | `frontend/tests/e2e/deepseek-harness/shell-tool-execution.spec.ts` |
| gemini-cli | W1-SHELL | `frontend/tests/e2e/gemini-cli/shell-tool-execution.spec.ts` |
| goose | W1-SHELL | `frontend/tests/e2e/goose/shell-tool-execution.spec.ts` |
| kiro | W1-SHELL | `frontend/tests/e2e/kiro/shell-tool-execution.spec.ts` |
| mimo-code | W1-SHELL | `frontend/tests/e2e/mimo-code/shell-tool-execution.spec.ts` |
| pi | W1-SHELL | `frontend/tests/e2e/pi/shell-tool-execution.spec.ts` |
| zcode | W1-SHELL | `frontend/tests/e2e/zcode/shell-tool-execution.spec.ts` |

### L027: P1 SH3 unclear / SH3

Kind: probe. Execution wave: 1.

SH3. The row body draws the command output without the native notice, trailer or result record that the provider adds for its model.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W1-SHELL | `frontend/tests/e2e/codebuddy-code/shell-tool-execution.spec.ts` |
| codewhale | W1-SHELL | `frontend/tests/e2e/codewhale/shell-tool-execution.spec.ts` |
| command-code | W1-SHELL | `frontend/tests/e2e/command-code/shell-tool-execution.spec.ts` |
| cursor | W1-SHELL | `frontend/tests/e2e/cursor/shell-tool-execution.spec.ts` |
| fast-agent | W1-SHELL | `frontend/tests/e2e/fast-agent/shell-tool-execution.spec.ts` |
| github-copilot | W1-SHELL | `frontend/tests/e2e/github-copilot/shell-tool-execution.spec.ts` |
| junie | W1-SHELL | `frontend/tests/e2e/junie/shell-tool-execution.spec.ts` |
| kilo | W1-SHELL | `frontend/tests/e2e/kilo/shell-tool-execution.spec.ts` |
| letta-code | W1-SHELL | `frontend/tests/e2e/letta-code/shell-tool-execution.spec.ts` |
| opencode | W1-SHELL | `frontend/tests/e2e/opencode/shell-tool-execution.spec.ts` |
| qoder-cli | W1-SHELL | `frontend/tests/e2e/qoder-cli/shell-tool-execution.spec.ts` |
| reasonix | W1-SHELL | `frontend/tests/e2e/reasonix/shell-tool-execution.spec.ts` |

### L028: P1 SH4 / SH4

Kind: port. Execution wave: 1.

SH4. The tool row shows the command that ran.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-SHELL | `frontend/tests/e2e/amp/shell-tool-execution.spec.ts` |
| claude-code | W1-SHELL | `frontend/tests/e2e/claude-code/shell-tool-execution.spec.ts` |
| cline | W1-SHELL | `frontend/tests/e2e/cline/shell-tool-execution.spec.ts` |
| codebuddy-code | W1-SHELL | `frontend/tests/e2e/codebuddy-code/shell-tool-execution.spec.ts` |
| codewhale | W1-SHELL | `frontend/tests/e2e/codewhale/shell-tool-execution.spec.ts` |
| command-code | W1-SHELL | `frontend/tests/e2e/command-code/shell-tool-execution.spec.ts` |
| cursor | W1-SHELL | `frontend/tests/e2e/cursor/shell-tool-execution.spec.ts` |
| deepseek-harness | W1-SHELL | `frontend/tests/e2e/deepseek-harness/shell-tool-execution.spec.ts` |
| dirac | W1-SHELL | `frontend/tests/e2e/dirac/shell-tool-execution.spec.ts` |
| factory-droid | W1-SHELL | `frontend/tests/e2e/factory-droid/shell-tool-execution.spec.ts` |
| fast-agent | W1-SHELL | `frontend/tests/e2e/fast-agent/shell-tool-execution.spec.ts` |
| gemini-cli | W1-SHELL | `frontend/tests/e2e/gemini-cli/shell-tool-execution.spec.ts` |
| github-copilot | W1-SHELL | `frontend/tests/e2e/github-copilot/shell-tool-execution.spec.ts` |
| goose | W1-SHELL | `frontend/tests/e2e/goose/shell-tool-execution.spec.ts` |
| grok-build | W1-SHELL | `frontend/tests/e2e/grok-build/shell-tool-execution.spec.ts` |
| junie | W1-SHELL | `frontend/tests/e2e/junie/shell-tool-execution.spec.ts` |
| kilo | W1-SHELL | `frontend/tests/e2e/kilo/shell-tool-execution.spec.ts` |
| kimi-code | W1-SHELL | `frontend/tests/e2e/kimi-code/shell-tool-execution.spec.ts` |
| kiro | W1-SHELL | `frontend/tests/e2e/kiro/shell-tool-execution.spec.ts` |
| letta-code | W1-SHELL | `frontend/tests/e2e/letta-code/shell-tool-execution.spec.ts` |
| mimo-code | W1-SHELL | `frontend/tests/e2e/mimo-code/shell-tool-execution.spec.ts` |
| oh-my-pi | W1-SHELL | `frontend/tests/e2e/oh-my-pi/shell-tool-execution.spec.ts` |
| opencode | W1-SHELL | `frontend/tests/e2e/opencode/shell-tool-execution.spec.ts` |
| pi | W1-SHELL | `frontend/tests/e2e/pi/shell-tool-execution.spec.ts` |
| qwen-code | W1-SHELL | `frontend/tests/e2e/qwen-code/shell-tool-execution.spec.ts` |
| reasonix | W1-SHELL | `frontend/tests/e2e/reasonix/shell-tool-execution.spec.ts` |
| zcode | W1-SHELL | `frontend/tests/e2e/zcode/shell-tool-execution.spec.ts` |

### L029: P1 SH6 / SH6

Kind: port. Execution wave: 1.

SH6. A shell call opens a span, so its rows draw a rail.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-SHELL | `frontend/tests/e2e/amp/shell-tool-execution.spec.ts` |
| claude-code | W1-SHELL | `frontend/tests/e2e/claude-code/shell-tool-execution.spec.ts` |
| cline | W1-SHELL | `frontend/tests/e2e/cline/shell-tool-execution.spec.ts` |
| codewhale | W1-SHELL | `frontend/tests/e2e/codewhale/shell-tool-execution.spec.ts` |
| codex | W1-SHELL | `frontend/tests/e2e/codex/shell-tool-execution.spec.ts` |
| command-code | W1-SHELL | `frontend/tests/e2e/command-code/shell-tool-execution.spec.ts` |
| deepseek-harness | W1-SHELL | `frontend/tests/e2e/deepseek-harness/shell-tool-execution.spec.ts` |
| factory-droid | W1-SHELL | `frontend/tests/e2e/factory-droid/shell-tool-execution.spec.ts` |
| github-copilot | W1-SHELL | `frontend/tests/e2e/github-copilot/shell-tool-execution.spec.ts` |
| kimi-code | W1-SHELL | `frontend/tests/e2e/kimi-code/shell-tool-execution.spec.ts` |
| letta-code | W1-SHELL | `frontend/tests/e2e/letta-code/shell-tool-execution.spec.ts` |
| oh-my-pi | W1-SHELL | `frontend/tests/e2e/oh-my-pi/shell-tool-execution.spec.ts` |
| pi | W1-SHELL | `frontend/tests/e2e/pi/shell-tool-execution.spec.ts` |
| zcode | W1-SHELL | `frontend/tests/e2e/zcode/shell-tool-execution.spec.ts` |

### L030: P1 SH7 / SH7

Kind: port. Execution wave: 1.

SH7. The shell row keeps its output after a page reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| pi | W1-SHELL | `frontend/tests/e2e/pi/shell-tool-execution.spec.ts` |
| zcode | W1-SHELL | `frontend/tests/e2e/zcode/shell-tool-execution.spec.ts` |

### L031: P1 SH9 / SH9

Kind: port. Execution wave: 1.

SH9. Two shell calls in one model response keep their own request rows, outputs and completed status.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-SHELL | `frontend/tests/e2e/amp/shell-tool-execution.spec.ts` |
| claude-code | W1-SHELL | `frontend/tests/e2e/claude-code/shell-tool-execution.spec.ts` |
| cline | W1-SHELL | `frontend/tests/e2e/cline/shell-tool-execution.spec.ts` |
| codebuddy-code | W1-SHELL | `frontend/tests/e2e/codebuddy-code/shell-tool-execution.spec.ts` |
| codewhale | W1-SHELL | `frontend/tests/e2e/codewhale/shell-tool-execution.spec.ts` |
| codex | W1-SHELL | `frontend/tests/e2e/codex/shell-tool-execution.spec.ts` |
| command-code | W1-SHELL | `frontend/tests/e2e/command-code/shell-tool-execution.spec.ts` |
| deepseek-harness | W1-SHELL | `frontend/tests/e2e/deepseek-harness/shell-tool-execution.spec.ts` |
| factory-droid | W1-SHELL | `frontend/tests/e2e/factory-droid/shell-tool-execution.spec.ts` |
| github-copilot | W1-SHELL | `frontend/tests/e2e/github-copilot/shell-tool-execution.spec.ts` |
| kimi-code | W1-SHELL | `frontend/tests/e2e/kimi-code/shell-tool-execution.spec.ts` |
| letta-code | W1-SHELL | `frontend/tests/e2e/letta-code/shell-tool-execution.spec.ts` |
| mimo-code | W1-SHELL | `frontend/tests/e2e/mimo-code/shell-tool-execution.spec.ts` |
| oh-my-pi | W1-SHELL | `frontend/tests/e2e/oh-my-pi/shell-tool-execution.spec.ts` |
| pi | W1-SHELL | `frontend/tests/e2e/pi/shell-tool-execution.spec.ts` |
| zcode | W1-SHELL | `frontend/tests/e2e/zcode/shell-tool-execution.spec.ts` |

### L032: P3 BC-B1 / BC-B1

Kind: port. Execution wave: 1.

The turn-end divider states the turn duration: "Turn ended (<duration>)".

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W1-BASICCHAT | `frontend/tests/e2e/claude-code/basic-chat.spec.ts` |
| codebuddy-code | W1-BASICCHAT | `frontend/tests/e2e/codebuddy-code/basic-chat.spec.ts` |
| codewhale | W1-BASICCHAT | `frontend/tests/e2e/codewhale/basic-chat.spec.ts` |
| kimi-code | W1-BASICCHAT | `frontend/tests/e2e/kimi-code/basic-chat.spec.ts` |
| qoder-cli | W1-BASICCHAT | `frontend/tests/e2e/qoder-cli/basic-chat.spec.ts` |
| zcode | W1-BASICCHAT | `frontend/tests/e2e/zcode/basic-chat.spec.ts` |

### L033: P3 BC-B4 / BC-B4

Kind: port. Execution wave: 1.

The native request carries the prompt as its LAST user text, not only somewhere in the request.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W1-BASICCHAT | `frontend/tests/e2e/amp/basic-chat.spec.ts` |
| claude-code | W1-BASICCHAT | `frontend/tests/e2e/claude-code/basic-chat.spec.ts` |
| cline | W1-BASICCHAT | `frontend/tests/e2e/cline/basic-chat.spec.ts` |
| codebuddy-code | W1-BASICCHAT | `frontend/tests/e2e/codebuddy-code/basic-chat.spec.ts` |
| codewhale | W1-BASICCHAT | `frontend/tests/e2e/codewhale/basic-chat.spec.ts` |
| codex | W1-BASICCHAT | `frontend/tests/e2e/codex/basic-chat.spec.ts` |
| command-code | W1-BASICCHAT | `frontend/tests/e2e/command-code/basic-chat.spec.ts` |
| cursor | W1-BASICCHAT | `frontend/tests/e2e/cursor/basic-chat.spec.ts` |
| deepseek-harness | W1-BASICCHAT | `frontend/tests/e2e/deepseek-harness/basic-chat.spec.ts` |
| dirac | W1-BASICCHAT | `frontend/tests/e2e/dirac/basic-chat.spec.ts` |
| factory-droid | W1-BASICCHAT | `frontend/tests/e2e/factory-droid/basic-chat.spec.ts` |
| fast-agent | W1-BASICCHAT | `frontend/tests/e2e/fast-agent/basic-chat.spec.ts` |
| gemini-cli | W1-BASICCHAT | `frontend/tests/e2e/gemini-cli/basic-chat.spec.ts` |
| github-copilot | W1-BASICCHAT | `frontend/tests/e2e/github-copilot/basic-chat.spec.ts` |
| goose | W1-BASICCHAT | `frontend/tests/e2e/goose/basic-chat.spec.ts` |
| junie | W1-BASICCHAT | `frontend/tests/e2e/junie/basic-chat.spec.ts` |
| kilo | W1-BASICCHAT | `frontend/tests/e2e/kilo/basic-chat.spec.ts` |
| kimi-code | W1-BASICCHAT | `frontend/tests/e2e/kimi-code/basic-chat.spec.ts` |
| kiro | W1-BASICCHAT | `frontend/tests/e2e/kiro/basic-chat.spec.ts` |
| letta-code | W1-BASICCHAT | `frontend/tests/e2e/letta-code/basic-chat.spec.ts` |
| mimo-code | W1-BASICCHAT | `frontend/tests/e2e/mimo-code/basic-chat.spec.ts` |
| oh-my-pi | W1-BASICCHAT | `frontend/tests/e2e/oh-my-pi/basic-chat.spec.ts` |
| opencode | W1-BASICCHAT | `frontend/tests/e2e/opencode/basic-chat.spec.ts` |
| pi | W1-BASICCHAT | `frontend/tests/e2e/pi/basic-chat.spec.ts` |
| qoder-cli | W1-BASICCHAT | `frontend/tests/e2e/qoder-cli/basic-chat.spec.ts` |
| reasonix | W1-BASICCHAT | `frontend/tests/e2e/reasonix/basic-chat.spec.ts` |
| zcode | W1-BASICCHAT | `frontend/tests/e2e/zcode/basic-chat.spec.ts` |

### L034: P1 AS-1 / S1

Kind: port. Execution wave: 2.

S1. A native launch that fails keeps the queued input as a failed item and shows the startup error panel.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W2-SMALL | `frontend/tests/e2e/claude-code/agent-startup.spec.ts` |

### L035: P1 AS-2 / S6

Kind: port. Execution wave: 2.

S6. The provider's new-agent button in the tab bar opens a second agent of that provider.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W2-SMALL | `frontend/tests/e2e/amp/agent-startup.spec.ts` |
| cline | W2-SMALL | `frontend/tests/e2e/cline/agent-startup.spec.ts` |
| codebuddy-code | W2-SMALL | `frontend/tests/e2e/codebuddy-code/agent-startup.spec.ts` |
| codewhale | W2-SMALL | `frontend/tests/e2e/codewhale/agent-startup.spec.ts` |
| command-code | W2-SMALL | `frontend/tests/e2e/command-code/agent-startup.spec.ts` |
| cursor | W2-SMALL | `frontend/tests/e2e/cursor/agent-startup.spec.ts` |
| deepseek-harness | W2-SMALL | `frontend/tests/e2e/deepseek-harness/agent-startup.spec.ts` |
| dirac | W2-SMALL | `frontend/tests/e2e/dirac/agent-startup.spec.ts` |
| factory-droid | W2-SMALL | `frontend/tests/e2e/factory-droid/agent-startup.spec.ts` |
| fast-agent | W2-SMALL | `frontend/tests/e2e/fast-agent/agent-startup.spec.ts` |
| gemini-cli | W2-SMALL | `frontend/tests/e2e/gemini-cli/agent-startup.spec.ts` |
| github-copilot | W2-SMALL | `frontend/tests/e2e/github-copilot/agent-startup.spec.ts` |
| goose | W2-SMALL | `frontend/tests/e2e/goose/agent-startup.spec.ts` |
| grok-build | W2-SMALL | `frontend/tests/e2e/grok-build/agent-startup.spec.ts` |
| junie | W2-SMALL | `frontend/tests/e2e/junie/agent-startup.spec.ts` |
| kilo | W2-SMALL | `frontend/tests/e2e/kilo/agent-startup.spec.ts` |
| kimi-code | W2-SMALL | `frontend/tests/e2e/kimi-code/agent-startup.spec.ts` |
| kiro | W2-SMALL | `frontend/tests/e2e/kiro/agent-startup.spec.ts` |
| letta-code | W2-SMALL | `frontend/tests/e2e/letta-code/agent-startup.spec.ts` |
| mimo-code | W2-SMALL | `frontend/tests/e2e/mimo-code/agent-startup.spec.ts` |
| oh-my-pi | W2-SMALL | `frontend/tests/e2e/oh-my-pi/agent-startup.spec.ts` |
| opencode | W2-SMALL | `frontend/tests/e2e/opencode/agent-startup.spec.ts` |
| pi | W2-SMALL | `frontend/tests/e2e/pi/agent-startup.spec.ts` |
| qoder-cli | W2-SMALL | `frontend/tests/e2e/qoder-cli/agent-startup.spec.ts` |
| qwen-code | W2-SMALL | `frontend/tests/e2e/qwen-code/agent-startup.spec.ts` |
| reasonix | W2-SMALL | `frontend/tests/e2e/reasonix/agent-startup.spec.ts` |
| zcode | W2-SMALL | `frontend/tests/e2e/zcode/agent-startup.spec.ts` |

### L036: P1 M2 unclear / M2

Kind: probe. Execution wave: 2.

M2. The mode menu offers exactly the modes that LeapMux supports for the provider.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W2-MODE | `frontend/tests/e2e/cursor/mode.spec.ts` |
| kilo | W2-MODE | `frontend/tests/e2e/kilo/mode.spec.ts` |
| opencode | W2-MODE | `frontend/tests/e2e/opencode/mode.spec.ts` |

### L037: P1 M4 unclear / M4

Kind: source investigation. Execution wave: 2.

M4. A mode change writes exactly one "Mode (old → new)" notice row in the chat.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W2-MODE | `frontend/tests/e2e/amp/mode.spec.ts` |

### L038: P1 M6 unclear / M6

Kind: source investigation. Execution wave: 2.

M6. A settings change shows no thinking indicator and sends no model request.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W2-MODE | `frontend/tests/e2e/cline/mode.spec.ts` |

### L039: P1 MODE-1 / M1

Kind: port. Execution wave: 2.

M1. A switch from a non-default mode back to the default (or to another mode) through the menu takes effect in the native session.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W2-MODE | `frontend/tests/e2e/codebuddy-code/mode.spec.ts` |
| codex | W2-MODE | `frontend/tests/e2e/codex/mode.spec.ts` |
| command-code | W2-MODE | `frontend/tests/e2e/command-code/mode.spec.ts` |
| cursor | W2-MODE | `frontend/tests/e2e/cursor/mode.spec.ts` |
| deepseek-harness | W2-MODE | `frontend/tests/e2e/deepseek-harness/mode.spec.ts` |
| dirac | W2-MODE | `frontend/tests/e2e/dirac/mode.spec.ts` |
| factory-droid | W2-MODE | `frontend/tests/e2e/factory-droid/mode.spec.ts` |
| fast-agent | W2-MODE | `frontend/tests/e2e/fast-agent/mode.spec.ts` |
| gemini-cli | W2-MODE | `frontend/tests/e2e/gemini-cli/mode.spec.ts` |
| github-copilot | W2-MODE | `frontend/tests/e2e/github-copilot/mode.spec.ts` |
| junie | W2-MODE | `frontend/tests/e2e/junie/mode.spec.ts` |
| kilo | W2-MODE | `frontend/tests/e2e/kilo/mode.spec.ts` |
| kimi-code | W2-MODE | `frontend/tests/e2e/kimi-code/mode.spec.ts` |
| kiro | W2-MODE | `frontend/tests/e2e/kiro/mode.spec.ts` |
| letta-code | W2-MODE | `frontend/tests/e2e/letta-code/mode.spec.ts` |
| mimo-code | W2-MODE | `frontend/tests/e2e/mimo-code/mode.spec.ts` |
| oh-my-pi | W2-MODE | `frontend/tests/e2e/oh-my-pi/mode.spec.ts` |
| opencode | W2-MODE | `frontend/tests/e2e/opencode/mode.spec.ts` |
| qoder-cli | W2-MODE | `frontend/tests/e2e/qoder-cli/mode.spec.ts` |

### L040: P1 MODE-2 / M2

Kind: port. Execution wave: 2.

M2. The mode menu offers exactly the modes that LeapMux supports for the provider.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W2-MODE | `frontend/tests/e2e/claude-code/mode.spec.ts` |
| cline | W2-MODE | `frontend/tests/e2e/cline/mode.spec.ts` |
| codewhale | W2-MODE | `frontend/tests/e2e/codewhale/mode.spec.ts` |
| codex | W2-MODE | `frontend/tests/e2e/codex/mode.spec.ts` |
| command-code | W2-MODE | `frontend/tests/e2e/command-code/mode.spec.ts` |
| deepseek-harness | W2-MODE | `frontend/tests/e2e/deepseek-harness/mode.spec.ts` |
| factory-droid | W2-MODE | `frontend/tests/e2e/factory-droid/mode.spec.ts` |
| fast-agent | W2-MODE | `frontend/tests/e2e/fast-agent/mode.spec.ts` |
| gemini-cli | W2-MODE | `frontend/tests/e2e/gemini-cli/mode.spec.ts` |
| github-copilot | W2-MODE | `frontend/tests/e2e/github-copilot/mode.spec.ts` |
| goose | W2-MODE | `frontend/tests/e2e/goose/mode.spec.ts` |
| grok-build | W2-MODE | `frontend/tests/e2e/grok-build/mode.spec.ts` |
| kimi-code | W2-MODE | `frontend/tests/e2e/kimi-code/mode.spec.ts` |
| kiro | W2-MODE | `frontend/tests/e2e/kiro/mode.spec.ts` |
| mimo-code | W2-MODE | `frontend/tests/e2e/mimo-code/mode.spec.ts` |
| oh-my-pi | W2-MODE | `frontend/tests/e2e/oh-my-pi/mode.spec.ts` |
| qwen-code | W2-MODE | `frontend/tests/e2e/qwen-code/mode.spec.ts` |
| reasonix | W2-MODE | `frontend/tests/e2e/reasonix/mode.spec.ts` |

### L041: P1 MODE-3 / M3

Kind: port. Execution wave: 2.

M3. The chosen mode survives a page reload, in the chip and in the next native request.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W2-MODE | `frontend/tests/e2e/codebuddy-code/mode.spec.ts` |
| dirac | W2-MODE | `frontend/tests/e2e/dirac/mode.spec.ts` |

### L042: P1 MODE-4 / M4

Kind: port. Execution wave: 2.

M4. A mode change writes exactly one "Mode (old → new)" notice row in the chat.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W2-MODE | `frontend/tests/e2e/cline/mode.spec.ts` |
| codebuddy-code | W2-MODE | `frontend/tests/e2e/codebuddy-code/mode.spec.ts` |
| codewhale | W2-MODE | `frontend/tests/e2e/codewhale/mode.spec.ts` |
| codex | W2-MODE | `frontend/tests/e2e/codex/mode.spec.ts` |
| command-code | W2-MODE | `frontend/tests/e2e/command-code/mode.spec.ts` |
| cursor | W2-MODE | `frontend/tests/e2e/cursor/mode.spec.ts` |
| deepseek-harness | W2-MODE | `frontend/tests/e2e/deepseek-harness/mode.spec.ts` |
| dirac | W2-MODE | `frontend/tests/e2e/dirac/mode.spec.ts` |
| factory-droid | W2-MODE | `frontend/tests/e2e/factory-droid/mode.spec.ts` |
| fast-agent | W2-MODE | `frontend/tests/e2e/fast-agent/mode.spec.ts` |
| gemini-cli | W2-MODE | `frontend/tests/e2e/gemini-cli/mode.spec.ts` |
| github-copilot | W2-MODE | `frontend/tests/e2e/github-copilot/mode.spec.ts` |
| goose | W2-MODE | `frontend/tests/e2e/goose/mode.spec.ts` |
| grok-build | W2-MODE | `frontend/tests/e2e/grok-build/mode.spec.ts` |
| junie | W2-MODE | `frontend/tests/e2e/junie/mode.spec.ts` |
| kilo | W2-MODE | `frontend/tests/e2e/kilo/mode.spec.ts` |
| kimi-code | W2-MODE | `frontend/tests/e2e/kimi-code/mode.spec.ts` |
| kiro | W2-MODE | `frontend/tests/e2e/kiro/mode.spec.ts` |
| letta-code | W2-MODE | `frontend/tests/e2e/letta-code/mode.spec.ts` |
| mimo-code | W2-MODE | `frontend/tests/e2e/mimo-code/mode.spec.ts` |
| oh-my-pi | W2-MODE | `frontend/tests/e2e/oh-my-pi/mode.spec.ts` |
| opencode | W2-MODE | `frontend/tests/e2e/opencode/mode.spec.ts` |
| qoder-cli | W2-MODE | `frontend/tests/e2e/qoder-cli/mode.spec.ts` |
| qwen-code | W2-MODE | `frontend/tests/e2e/qwen-code/mode.spec.ts` |
| reasonix | W2-MODE | `frontend/tests/e2e/reasonix/mode.spec.ts` |
| zcode | W2-MODE | `frontend/tests/e2e/zcode/mode.spec.ts` |

### L043: P1 MODE-5 / M6

Kind: port. Execution wave: 2.

M6. A settings change shows no thinking indicator and sends no model request.

| Target | Requirement group | Complete browser file |
|---|---|---|
| command-code | W2-MODE | `frontend/tests/e2e/command-code/mode.spec.ts` |
| letta-code | W2-MODE | `frontend/tests/e2e/letta-code/mode.spec.ts` |
| oh-my-pi | W2-MODE | `frontend/tests/e2e/oh-my-pi/mode.spec.ts` |

### L044: P2 background-tasks-sidebar-P2 / background-tasks-sidebar-B1

Kind: port. Execution wave: 2.

A background shell command opens a non-clickable shell row that shows its command while it runs.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W2-SMALL | `frontend/tests/e2e/cursor/background-tasks-sidebar.spec.ts` |

### L045: P2 editor-requests-P1 / editor-requests-B3

Kind: port. Execution wave: 2.

An interrupt while the editor waits withdraws the editor and returns a cancellation to the native extension.

| Target | Requirement group | Complete browser file |
|---|---|---|
| pi | W2-SMALL | `frontend/tests/e2e/pi/editor-requests.spec.ts` |

### L046: P2 editor-requests-P2 / editor-requests-B5

Kind: port. Execution wave: 2.

The saved answer row states the exact whitespace text, and "Empty answer" for empty text.

| Target | Requirement group | Complete browser file |
|---|---|---|
| oh-my-pi | W2-SMALL | `frontend/tests/e2e/oh-my-pi/editor-requests.spec.ts` |

### L047: P2 workflow-grouping-B4 unclear / workflow-grouping-B4

Kind: probe. Execution wave: 2.

A refused or failed workflow request leaves no running or phantom workflow row, and the transcript shows the native error.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W2-WORKFLOW | `frontend/tests/e2e/claude-code/workflow-grouping.spec.ts` |
| codebuddy-code | W2-WORKFLOW | `frontend/tests/e2e/codebuddy-code/workflow-grouping.spec.ts` |
| codewhale | W2-WORKFLOW | `frontend/tests/e2e/codewhale/workflow-grouping.spec.ts` |
| deepseek-harness | W2-WORKFLOW | `frontend/tests/e2e/deepseek-harness/workflow-grouping.spec.ts` |
| grok-build | W2-WORKFLOW | `frontend/tests/e2e/grok-build/workflow-grouping.spec.ts` |
| mimo-code | W2-WORKFLOW | `frontend/tests/e2e/mimo-code/workflow-grouping.spec.ts` |
| qoder-cli | W2-WORKFLOW | `frontend/tests/e2e/qoder-cli/workflow-grouping.spec.ts` |
| zcode | W2-WORKFLOW | `frontend/tests/e2e/zcode/workflow-grouping.spec.ts` |

### L048: P2 workflow-grouping-P1 / workflow-grouping-B2

Kind: port. Execution wave: 2.

A grouped member row opens its own child transcript, and the parent shows none of the member's rows.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W2-WORKFLOW | `frontend/tests/e2e/cline/workflow-grouping.spec.ts` |
| deepseek-harness | W2-WORKFLOW | `frontend/tests/e2e/deepseek-harness/workflow-grouping.spec.ts` |
| grok-build | W2-WORKFLOW | `frontend/tests/e2e/grok-build/workflow-grouping.spec.ts` |
| kimi-code | W2-WORKFLOW | `frontend/tests/e2e/kimi-code/workflow-grouping.spec.ts` |
| kiro | W2-WORKFLOW | `frontend/tests/e2e/kiro/workflow-grouping.spec.ts` |

### L049: P2 workflow-grouping-P2 / workflow-grouping-B3

Kind: port. Execution wave: 2.

The grouped rows show running while a member still works.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W2-WORKFLOW | `frontend/tests/e2e/cline/workflow-grouping.spec.ts` |
| codebuddy-code | W2-WORKFLOW | `frontend/tests/e2e/codebuddy-code/workflow-grouping.spec.ts` |
| grok-build | W2-WORKFLOW | `frontend/tests/e2e/grok-build/workflow-grouping.spec.ts` |
| kimi-code | W2-WORKFLOW | `frontend/tests/e2e/kimi-code/workflow-grouping.spec.ts` |
| kiro | W2-WORKFLOW | `frontend/tests/e2e/kiro/workflow-grouping.spec.ts` |
| mimo-code | W2-WORKFLOW | `frontend/tests/e2e/mimo-code/workflow-grouping.spec.ts` |

### L050: P2 workflow-grouping-P3 / workflow-grouping-B4

Kind: port. Execution wave: 2.

A refused or failed workflow request leaves no running or phantom workflow row, and the transcript shows the native error.

| Target | Requirement group | Complete browser file |
|---|---|---|
| qwen-code | W2-WORKFLOW | `frontend/tests/e2e/qwen-code/workflow-grouping.spec.ts` |

### L051: P3 TT-B1 / TT-B1

Kind: port. Execution wave: 2.

In a text-only turn, the reasoning shows in its own thought row and the answer in a separate text row. No text row holds the reasoning, and the two rows keep their order, live and after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W2-THINK | `frontend/tests/e2e/claude-code/thinking-in-the-transcript.spec.ts` |
| codex | W2-THINK | `frontend/tests/e2e/codex/thinking-in-the-transcript.spec.ts` |

### L052: P3 TT-B2 / TT-B2

Kind: port. Execution wave: 2.

In a turn that calls tools, the reasoning of each tool-call step shows as a thought row, live and after a reload, beside the tool rows and the answer.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W2-THINK | `frontend/tests/e2e/amp/thinking-in-the-transcript.spec.ts` |
| claude-code | W2-THINK | `frontend/tests/e2e/claude-code/thinking-in-the-transcript.spec.ts` |
| cline | W2-THINK | `frontend/tests/e2e/cline/thinking-in-the-transcript.spec.ts` |
| codebuddy-code | W2-THINK | `frontend/tests/e2e/codebuddy-code/thinking-in-the-transcript.spec.ts` |
| codewhale | W2-THINK | `frontend/tests/e2e/codewhale/thinking-in-the-transcript.spec.ts` |
| command-code | W2-THINK | `frontend/tests/e2e/command-code/thinking-in-the-transcript.spec.ts` |
| cursor | W2-THINK | `frontend/tests/e2e/cursor/thinking-in-the-transcript.spec.ts` |
| deepseek-harness | W2-THINK | `frontend/tests/e2e/deepseek-harness/thinking-in-the-transcript.spec.ts` |
| dirac | W2-THINK | `frontend/tests/e2e/dirac/thinking-in-the-transcript.spec.ts` |
| factory-droid | W2-THINK | `frontend/tests/e2e/factory-droid/thinking-in-the-transcript.spec.ts` |
| fast-agent | W2-THINK | `frontend/tests/e2e/fast-agent/thinking-in-the-transcript.spec.ts` |
| gemini-cli | W2-THINK | `frontend/tests/e2e/gemini-cli/thinking-in-the-transcript.spec.ts` |
| github-copilot | W2-THINK | `frontend/tests/e2e/github-copilot/thinking-in-the-transcript.spec.ts` |
| goose | W2-THINK | `frontend/tests/e2e/goose/thinking-in-the-transcript.spec.ts` |
| grok-build | W2-THINK | `frontend/tests/e2e/grok-build/thinking-in-the-transcript.spec.ts` |
| kilo | W2-THINK | `frontend/tests/e2e/kilo/thinking-in-the-transcript.spec.ts` |
| kimi-code | W2-THINK | `frontend/tests/e2e/kimi-code/thinking-in-the-transcript.spec.ts` |
| kiro | W2-THINK | `frontend/tests/e2e/kiro/thinking-in-the-transcript.spec.ts` |
| letta-code | W2-THINK | `frontend/tests/e2e/letta-code/thinking-in-the-transcript.spec.ts` |
| mimo-code | W2-THINK | `frontend/tests/e2e/mimo-code/thinking-in-the-transcript.spec.ts` |
| oh-my-pi | W2-THINK | `frontend/tests/e2e/oh-my-pi/thinking-in-the-transcript.spec.ts` |
| opencode | W2-THINK | `frontend/tests/e2e/opencode/thinking-in-the-transcript.spec.ts` |
| pi | W2-THINK | `frontend/tests/e2e/pi/thinking-in-the-transcript.spec.ts` |
| qoder-cli | W2-THINK | `frontend/tests/e2e/qoder-cli/thinking-in-the-transcript.spec.ts` |
| qwen-code | W2-THINK | `frontend/tests/e2e/qwen-code/thinking-in-the-transcript.spec.ts` |
| reasonix | W2-THINK | `frontend/tests/e2e/reasonix/thinking-in-the-transcript.spec.ts` |
| zcode | W2-THINK | `frontend/tests/e2e/zcode/thinking-in-the-transcript.spec.ts` |

### L053: P1 Q-P2 / AQ3

Kind: port. Execution wave: 3, 5, 6.

AQ3. A multi-select question sends every pick of that question in one native answer.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts` |
| codewhale | W6-QUESTIONS-B | `frontend/tests/e2e/codewhale/agent-questions.spec.ts` |
| cursor | W5-QUESTIONS-A | `frontend/tests/e2e/cursor/agent-questions.spec.ts` |
| deepseek-harness | W5-QUESTIONS-A | `frontend/tests/e2e/deepseek-harness/agent-questions.spec.ts` |
| grok-build | W6-QUESTIONS-B | `frontend/tests/e2e/grok-build/agent-questions.spec.ts` |
| kilo | W5-QUESTIONS-A | `frontend/tests/e2e/kilo/agent-questions.spec.ts` |
| kimi-code | W6-QUESTIONS-B | `frontend/tests/e2e/kimi-code/agent-questions.spec.ts` |
| letta-code | W6-QUESTIONS-B | `frontend/tests/e2e/letta-code/agent-questions.spec.ts` |
| oh-my-pi | W6-QUESTIONS-B | `frontend/tests/e2e/oh-my-pi/agent-questions.spec.ts` |
| opencode | W5-QUESTIONS-A | `frontend/tests/e2e/opencode/agent-questions.spec.ts` |
| pi | W5-QUESTIONS-A | `frontend/tests/e2e/pi/agent-questions.spec.ts` |
| qoder-cli | W6-QUESTIONS-B | `frontend/tests/e2e/qoder-cli/agent-questions.spec.ts` |
| qwen-code | W6-QUESTIONS-B | `frontend/tests/e2e/qwen-code/agent-questions.spec.ts` |
| zcode | W5-QUESTIONS-A | `frontend/tests/e2e/zcode/agent-questions.spec.ts` |

### L054: P1 Q-P6 / AQ13

Kind: port. Execution wave: 3, 5, 6.

AQ13. The saved-answer row (`control-response-text`) states the chosen answer, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts` |
| cline | W5-QUESTIONS-A | `frontend/tests/e2e/cline/agent-questions.spec.ts` |
| codewhale | W6-QUESTIONS-B | `frontend/tests/e2e/codewhale/agent-questions.spec.ts` |
| codex | W5-QUESTIONS-A | `frontend/tests/e2e/codex/agent-questions.spec.ts` |
| cursor | W5-QUESTIONS-A | `frontend/tests/e2e/cursor/agent-questions.spec.ts` |
| dirac | W5-QUESTIONS-A | `frontend/tests/e2e/dirac/agent-questions.spec.ts` |
| github-copilot | W5-QUESTIONS-A | `frontend/tests/e2e/github-copilot/agent-questions.spec.ts` |
| junie | W6-QUESTIONS-B | `frontend/tests/e2e/junie/agent-questions.spec.ts` |
| kilo | W5-QUESTIONS-A | `frontend/tests/e2e/kilo/agent-questions.spec.ts` |
| kimi-code | W6-QUESTIONS-B | `frontend/tests/e2e/kimi-code/agent-questions.spec.ts` |
| kiro | W6-QUESTIONS-B | `frontend/tests/e2e/kiro/agent-questions.spec.ts` |
| opencode | W5-QUESTIONS-A | `frontend/tests/e2e/opencode/agent-questions.spec.ts` |
| pi | W5-QUESTIONS-A | `frontend/tests/e2e/pi/agent-questions.spec.ts` |
| qoder-cli | W6-QUESTIONS-B | `frontend/tests/e2e/qoder-cli/agent-questions.spec.ts` |
| zcode | W5-QUESTIONS-A | `frontend/tests/e2e/zcode/agent-questions.spec.ts` |

### L055: P1 Q-P7 / AQ14

Kind: port. Execution wave: 3, 5, 6.

AQ14. The banner shows the description of each option.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts` |
| codewhale | W6-QUESTIONS-B | `frontend/tests/e2e/codewhale/agent-questions.spec.ts` |
| codex | W5-QUESTIONS-A | `frontend/tests/e2e/codex/agent-questions.spec.ts` |
| deepseek-harness | W5-QUESTIONS-A | `frontend/tests/e2e/deepseek-harness/agent-questions.spec.ts` |
| grok-build | W6-QUESTIONS-B | `frontend/tests/e2e/grok-build/agent-questions.spec.ts` |
| kilo | W5-QUESTIONS-A | `frontend/tests/e2e/kilo/agent-questions.spec.ts` |
| kiro | W6-QUESTIONS-B | `frontend/tests/e2e/kiro/agent-questions.spec.ts` |
| letta-code | W6-QUESTIONS-B | `frontend/tests/e2e/letta-code/agent-questions.spec.ts` |
| opencode | W5-QUESTIONS-A | `frontend/tests/e2e/opencode/agent-questions.spec.ts` |
| pi | W5-QUESTIONS-A | `frontend/tests/e2e/pi/agent-questions.spec.ts` |
| qoder-cli | W6-QUESTIONS-B | `frontend/tests/e2e/qoder-cli/agent-questions.spec.ts` |
| qwen-code | W6-QUESTIONS-B | `frontend/tests/e2e/qwen-code/agent-questions.spec.ts` |

### L056: P1 Q-P8 / AQ15

Kind: port. Execution wave: 3, 5, 6.

AQ15. An option preview shows in a region of its own, a code preview renders as code, and the banner does not overflow.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-QUESTIONS-H | `frontend/tests/e2e/claude-code/agent-questions.spec.ts` |
| grok-build | W6-QUESTIONS-B | `frontend/tests/e2e/grok-build/agent-questions.spec.ts` |
| oh-my-pi | W6-QUESTIONS-B | `frontend/tests/e2e/oh-my-pi/agent-questions.spec.ts` |
| pi | W5-QUESTIONS-A | `frontend/tests/e2e/pi/agent-questions.spec.ts` |

### L057: P1 S4 unclear / S4

Kind: probe. Execution wave: 3.

S4. The settings bar shows the default model and mode chips after startup.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W3-MODEL | `frontend/tests/e2e/amp/mode.spec.ts` |
| cline | W3-MODEL | `frontend/tests/e2e/cline/model.spec.ts` |
| codebuddy-code | W3-MODEL | `frontend/tests/e2e/codebuddy-code/model.spec.ts` |
| codewhale | W3-MODEL | `frontend/tests/e2e/codewhale/model.spec.ts` |
| codex | W3-MODEL | `frontend/tests/e2e/codex/model.spec.ts` |
| command-code | W3-MODEL | `frontend/tests/e2e/command-code/model.spec.ts` |
| cursor | W3-MODEL | `frontend/tests/e2e/cursor/model.spec.ts` |
| deepseek-harness | W3-MODEL | `frontend/tests/e2e/deepseek-harness/model.spec.ts` |
| dirac | W3-MODEL | `frontend/tests/e2e/dirac/model.spec.ts` |
| factory-droid | W3-MODEL | `frontend/tests/e2e/factory-droid/model.spec.ts` |
| fast-agent | W3-MODEL | `frontend/tests/e2e/fast-agent/mode.spec.ts` |
| gemini-cli | W3-MODEL | `frontend/tests/e2e/gemini-cli/model.spec.ts` |
| github-copilot | W3-MODEL | `frontend/tests/e2e/github-copilot/model.spec.ts` |
| goose | W3-MODEL | `frontend/tests/e2e/goose/model.spec.ts` |
| grok-build | W3-MODEL | `frontend/tests/e2e/grok-build/model.spec.ts` |
| junie | W3-MODEL | `frontend/tests/e2e/junie/model.spec.ts` |
| kilo | W3-MODEL | `frontend/tests/e2e/kilo/model.spec.ts` |
| kimi-code | W3-MODEL | `frontend/tests/e2e/kimi-code/model.spec.ts` |
| kiro | W3-MODEL | `frontend/tests/e2e/kiro/model.spec.ts` |
| letta-code | W3-MODEL | `frontend/tests/e2e/letta-code/model.spec.ts` |
| mimo-code | W3-MODEL | `frontend/tests/e2e/mimo-code/model.spec.ts` |
| oh-my-pi | W3-MODEL | `frontend/tests/e2e/oh-my-pi/model.spec.ts` |
| opencode | W3-MODEL | `frontend/tests/e2e/opencode/model.spec.ts` |
| pi | W3-MODEL | `frontend/tests/e2e/pi/model.spec.ts` |
| qoder-cli | W3-MODEL | `frontend/tests/e2e/qoder-cli/model.spec.ts` |
| qwen-code | W3-MODEL | `frontend/tests/e2e/qwen-code/model.spec.ts` |
| reasonix | W3-MODEL | `frontend/tests/e2e/reasonix/model.spec.ts` |
| zcode | W3-MODEL | `frontend/tests/e2e/zcode/model.spec.ts` |

### L058: P1 FT1 / FT1

Kind: port. Execution wave: 3.

FT1. A native edit draws a visible diff with the old and the new line.

| Target | Requirement group | Complete browser file |
|---|---|---|
| command-code | W3-FILE | `frontend/tests/e2e/command-code/file-tool-execution.spec.ts` |

### L059: P1 FT2 / FT2

Kind: port. Execution wave: 3.

FT2. The edit diff stays after a page reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W3-FILE | `frontend/tests/e2e/amp/file-tool-execution.spec.ts` |
| cline | W3-FILE | `frontend/tests/e2e/cline/file-tool-execution.spec.ts` |
| codewhale | W3-FILE | `frontend/tests/e2e/codewhale/file-tool-execution.spec.ts` |
| grok-build | W3-FILE | `frontend/tests/e2e/grok-build/file-tool-execution.spec.ts` |
| kimi-code | W3-FILE | `frontend/tests/e2e/kimi-code/file-tool-execution.spec.ts` |
| kiro | W3-FILE | `frontend/tests/e2e/kiro/file-tool-execution.spec.ts` |
| mimo-code | W3-FILE | `frontend/tests/e2e/mimo-code/file-tool-execution.spec.ts` |
| oh-my-pi | W3-FILE | `frontend/tests/e2e/oh-my-pi/file-tool-execution.spec.ts` |
| qwen-code | W3-FILE | `frontend/tests/e2e/qwen-code/file-tool-execution.spec.ts` |

### L060: P1 FT3 / FT3

Kind: port. Execution wave: 3.

FT3. A native write creates a new file with the stated bytes.

| Target | Requirement group | Complete browser file |
|---|---|---|
| grok-build | W3-FILE | `frontend/tests/e2e/grok-build/file-tool-execution.spec.ts` |
| mimo-code | W3-FILE | `frontend/tests/e2e/mimo-code/file-tool-execution.spec.ts` |
| qwen-code | W3-FILE | `frontend/tests/e2e/qwen-code/file-tool-execution.spec.ts` |

### L061: P1 FT4 / FT4

Kind: port. Execution wave: 3.

FT4. The transcript identifies the write: a tool row shows the written file's name or text.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-FILE | `frontend/tests/e2e/claude-code/file-tool-execution.spec.ts` |
| codebuddy-code | W3-FILE | `frontend/tests/e2e/codebuddy-code/file-tool-execution.spec.ts` |
| codewhale | W3-FILE | `frontend/tests/e2e/codewhale/file-tool-execution.spec.ts` |
| command-code | W3-FILE | `frontend/tests/e2e/command-code/file-tool-execution.spec.ts` |
| cursor | W3-FILE | `frontend/tests/e2e/cursor/file-tool-execution.spec.ts` |
| deepseek-harness | W3-FILE | `frontend/tests/e2e/deepseek-harness/file-tool-execution.spec.ts` |
| dirac | W3-FILE | `frontend/tests/e2e/dirac/file-tool-execution.spec.ts` |
| fast-agent | W3-FILE | `frontend/tests/e2e/fast-agent/file-tool-execution.spec.ts` |
| gemini-cli | W3-FILE | `frontend/tests/e2e/gemini-cli/file-tool-execution.spec.ts` |
| github-copilot | W3-FILE | `frontend/tests/e2e/github-copilot/file-tool-execution.spec.ts` |
| goose | W3-FILE | `frontend/tests/e2e/goose/file-tool-execution.spec.ts` |
| grok-build | W3-FILE | `frontend/tests/e2e/grok-build/file-tool-execution.spec.ts` |
| junie | W3-FILE | `frontend/tests/e2e/junie/file-tool-execution.spec.ts` |
| kilo | W3-FILE | `frontend/tests/e2e/kilo/file-tool-execution.spec.ts` |
| kimi-code | W3-FILE | `frontend/tests/e2e/kimi-code/file-tool-execution.spec.ts` |
| mimo-code | W3-FILE | `frontend/tests/e2e/mimo-code/file-tool-execution.spec.ts` |
| opencode | W3-FILE | `frontend/tests/e2e/opencode/file-tool-execution.spec.ts` |
| qoder-cli | W3-FILE | `frontend/tests/e2e/qoder-cli/file-tool-execution.spec.ts` |
| qwen-code | W3-FILE | `frontend/tests/e2e/qwen-code/file-tool-execution.spec.ts` |
| reasonix | W3-FILE | `frontend/tests/e2e/reasonix/file-tool-execution.spec.ts` |
| zcode | W3-FILE | `frontend/tests/e2e/zcode/file-tool-execution.spec.ts` |

### L062: P1 FT5 / FT5

Kind: port. Execution wave: 3.

FT5. The read row draws the lines of the file, not the numbered form that the native tool gives its model.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-FILE | `frontend/tests/e2e/claude-code/file-tool-execution.spec.ts` |
| codebuddy-code | W3-FILE | `frontend/tests/e2e/codebuddy-code/file-tool-execution.spec.ts` |
| codex | W3-FILE | `frontend/tests/e2e/codex/file-tool-execution.spec.ts` |
| command-code | W3-FILE | `frontend/tests/e2e/command-code/file-tool-execution.spec.ts` |
| cursor | W3-FILE | `frontend/tests/e2e/cursor/file-tool-execution.spec.ts` |
| deepseek-harness | W3-FILE | `frontend/tests/e2e/deepseek-harness/file-tool-execution.spec.ts` |
| dirac | W3-FILE | `frontend/tests/e2e/dirac/file-tool-execution.spec.ts` |
| factory-droid | W3-FILE | `frontend/tests/e2e/factory-droid/file-tool-execution.spec.ts` |
| fast-agent | W3-FILE | `frontend/tests/e2e/fast-agent/file-tool-execution.spec.ts` |
| gemini-cli | W3-FILE | `frontend/tests/e2e/gemini-cli/file-tool-execution.spec.ts` |
| github-copilot | W3-FILE | `frontend/tests/e2e/github-copilot/file-tool-execution.spec.ts` |
| goose | W3-FILE | `frontend/tests/e2e/goose/file-tool-execution.spec.ts` |
| grok-build | W3-FILE | `frontend/tests/e2e/grok-build/file-tool-execution.spec.ts` |
| junie | W3-FILE | `frontend/tests/e2e/junie/file-tool-execution.spec.ts` |
| kilo | W3-FILE | `frontend/tests/e2e/kilo/file-tool-execution.spec.ts` |
| kimi-code | W3-FILE | `frontend/tests/e2e/kimi-code/file-tool-execution.spec.ts` |
| kiro | W3-FILE | `frontend/tests/e2e/kiro/file-tool-execution.spec.ts` |
| letta-code | W3-FILE | `frontend/tests/e2e/letta-code/file-tool-execution.spec.ts` |
| mimo-code | W3-FILE | `frontend/tests/e2e/mimo-code/file-tool-execution.spec.ts` |
| opencode | W3-FILE | `frontend/tests/e2e/opencode/file-tool-execution.spec.ts` |
| pi | W3-FILE | `frontend/tests/e2e/pi/file-tool-execution.spec.ts` |
| qoder-cli | W3-FILE | `frontend/tests/e2e/qoder-cli/file-tool-execution.spec.ts` |
| qwen-code | W3-FILE | `frontend/tests/e2e/qwen-code/file-tool-execution.spec.ts` |
| reasonix | W3-FILE | `frontend/tests/e2e/reasonix/file-tool-execution.spec.ts` |
| zcode | W3-FILE | `frontend/tests/e2e/zcode/file-tool-execution.spec.ts` |

### L063: P1 FT6 / FT6

Kind: port. Execution wave: 3.

FT6. A failed native edit leaves the file unchanged and marks the row failed.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W3-FILE | `frontend/tests/e2e/amp/file-tool-execution.spec.ts` |
| claude-code | W3-FILE | `frontend/tests/e2e/claude-code/file-tool-execution.spec.ts` |
| cline | W3-FILE | `frontend/tests/e2e/cline/file-tool-execution.spec.ts` |
| codebuddy-code | W3-FILE | `frontend/tests/e2e/codebuddy-code/file-tool-execution.spec.ts` |
| codewhale | W3-FILE | `frontend/tests/e2e/codewhale/file-tool-execution.spec.ts` |
| codex | W3-FILE | `frontend/tests/e2e/codex/file-tool-execution.spec.ts` |
| command-code | W3-FILE | `frontend/tests/e2e/command-code/file-tool-execution.spec.ts` |
| cursor | W3-FILE | `frontend/tests/e2e/cursor/file-tool-execution.spec.ts` |
| deepseek-harness | W3-FILE | `frontend/tests/e2e/deepseek-harness/file-tool-execution.spec.ts` |
| dirac | W3-FILE | `frontend/tests/e2e/dirac/file-tool-execution.spec.ts` |
| factory-droid | W3-FILE | `frontend/tests/e2e/factory-droid/file-tool-execution.spec.ts` |
| gemini-cli | W3-FILE | `frontend/tests/e2e/gemini-cli/file-tool-execution.spec.ts` |
| goose | W3-FILE | `frontend/tests/e2e/goose/file-tool-execution.spec.ts` |
| grok-build | W3-FILE | `frontend/tests/e2e/grok-build/file-tool-execution.spec.ts` |
| junie | W3-FILE | `frontend/tests/e2e/junie/file-tool-execution.spec.ts` |
| kilo | W3-FILE | `frontend/tests/e2e/kilo/file-tool-execution.spec.ts` |
| kimi-code | W3-FILE | `frontend/tests/e2e/kimi-code/file-tool-execution.spec.ts` |
| kiro | W3-FILE | `frontend/tests/e2e/kiro/file-tool-execution.spec.ts` |
| letta-code | W3-FILE | `frontend/tests/e2e/letta-code/file-tool-execution.spec.ts` |
| mimo-code | W3-FILE | `frontend/tests/e2e/mimo-code/file-tool-execution.spec.ts` |
| oh-my-pi | W3-FILE | `frontend/tests/e2e/oh-my-pi/file-tool-execution.spec.ts` |
| opencode | W3-FILE | `frontend/tests/e2e/opencode/file-tool-execution.spec.ts` |
| pi | W3-FILE | `frontend/tests/e2e/pi/file-tool-execution.spec.ts` |
| qoder-cli | W3-FILE | `frontend/tests/e2e/qoder-cli/file-tool-execution.spec.ts` |
| qwen-code | W3-FILE | `frontend/tests/e2e/qwen-code/file-tool-execution.spec.ts` |
| reasonix | W3-FILE | `frontend/tests/e2e/reasonix/file-tool-execution.spec.ts` |
| zcode | W3-FILE | `frontend/tests/e2e/zcode/file-tool-execution.spec.ts` |

### L064: P1 PAB-1 / B1

Kind: port. Execution wave: 3.

B1. Reject with an empty composer sends the native refusal, the banner closes, and the agent stays in plan mode.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-PLANBANNER | `frontend/tests/e2e/claude-code/plan-approval-banner.spec.ts` |
| cline | W3-PLANBANNER | `frontend/tests/e2e/cline/plan-approval-banner.spec.ts` |
| codebuddy-code | W3-PLANBANNER | `frontend/tests/e2e/codebuddy-code/plan-approval-banner.spec.ts` |
| codex | W3-PLANBANNER | `frontend/tests/e2e/codex/plan-approval-banner.spec.ts` |
| kimi-code | W3-PLANBANNER | `frontend/tests/e2e/kimi-code/plan-approval-banner.spec.ts` |
| qoder-cli | W3-PLANBANNER | `frontend/tests/e2e/qoder-cli/plan-approval-banner.spec.ts` |
| zcode | W3-PLANBANNER | `frontend/tests/e2e/zcode/plan-approval-banner.spec.ts` |

### L065: P1 PAB-2 / B2

Kind: port. Execution wave: 3.

B2. Reject with typed feedback brings the feedback to the model (native field or follow-up message), and the agent stays in plan mode.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W3-PLANBANNER | `frontend/tests/e2e/cursor/plan-approval-banner.spec.ts` |
| deepseek-harness | W3-PLANBANNER | `frontend/tests/e2e/deepseek-harness/plan-approval-banner.spec.ts` |
| gemini-cli | W3-PLANBANNER | `frontend/tests/e2e/gemini-cli/plan-approval-banner.spec.ts` |
| github-copilot | W3-PLANBANNER | `frontend/tests/e2e/github-copilot/plan-approval-banner.spec.ts` |
| grok-build | W3-PLANBANNER | `frontend/tests/e2e/grok-build/plan-approval-banner.spec.ts` |
| junie | W3-PLANBANNER | `frontend/tests/e2e/junie/plan-approval-banner.spec.ts` |
| kimi-code | W3-PLANBANNER | `frontend/tests/e2e/kimi-code/plan-approval-banner.spec.ts` |
| pi | W3-PLANBANNER | `frontend/tests/e2e/pi/plan-approval-banner.spec.ts` |
| qwen-code | W3-PLANBANNER | `frontend/tests/e2e/qwen-code/plan-approval-banner.spec.ts` |

### L066: P1 PAB-3 / B5

Kind: port. Execution wave: 3.

B5. An approval with Clear Context starts a fresh native session, shows "Context cleared", and runs the plan in that session.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W3-PLANBANNER | `frontend/tests/e2e/cline/plan-approval-banner.spec.ts` |
| codebuddy-code | W3-PLANBANNER | `frontend/tests/e2e/codebuddy-code/plan-approval-banner.spec.ts` |
| deepseek-harness | W3-PLANBANNER | `frontend/tests/e2e/deepseek-harness/plan-approval-banner.spec.ts` |
| factory-droid | W3-PLANBANNER | `frontend/tests/e2e/factory-droid/plan-approval-banner.spec.ts` |
| gemini-cli | W3-PLANBANNER | `frontend/tests/e2e/gemini-cli/plan-approval-banner.spec.ts` |
| github-copilot | W3-PLANBANNER | `frontend/tests/e2e/github-copilot/plan-approval-banner.spec.ts` |
| grok-build | W3-PLANBANNER | `frontend/tests/e2e/grok-build/plan-approval-banner.spec.ts` |
| kimi-code | W3-PLANBANNER | `frontend/tests/e2e/kimi-code/plan-approval-banner.spec.ts` |
| mimo-code | W3-PLANBANNER | `frontend/tests/e2e/mimo-code/plan-approval-banner.spec.ts` |
| qoder-cli | W3-PLANBANNER | `frontend/tests/e2e/qoder-cli/plan-approval-banner.spec.ts` |
| zcode | W3-PLANBANNER | `frontend/tests/e2e/zcode/plan-approval-banner.spec.ts` |

### L067: P1 PAB-4 / B6

Kind: port. Execution wave: 3.

B6. An approval applies the permission preset that the banner's pill group selects, and the chip shows the resulting mode.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W3-PLANBANNER | `frontend/tests/e2e/cline/plan-approval-banner.spec.ts` |
| codebuddy-code | W3-PLANBANNER | `frontend/tests/e2e/codebuddy-code/plan-approval-banner.spec.ts` |
| codex | W3-PLANBANNER | `frontend/tests/e2e/codex/plan-approval-banner.spec.ts` |
| factory-droid | W3-PLANBANNER | `frontend/tests/e2e/factory-droid/plan-approval-banner.spec.ts` |
| gemini-cli | W3-PLANBANNER | `frontend/tests/e2e/gemini-cli/plan-approval-banner.spec.ts` |
| github-copilot | W3-PLANBANNER | `frontend/tests/e2e/github-copilot/plan-approval-banner.spec.ts` |
| qwen-code | W3-PLANBANNER | `frontend/tests/e2e/qwen-code/plan-approval-banner.spec.ts` |

### L068: P1 PAB-5 / B9

Kind: port. Execution wave: 3.

B9. The plan title renames an auto-named agent tab, and a manual rename survives a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W3-PLANBANNER | `frontend/tests/e2e/cline/plan-approval-banner.spec.ts` |
| codex | W3-PLANBANNER | `frontend/tests/e2e/codex/plan-approval-banner.spec.ts` |
| deepseek-harness | W3-PLANBANNER | `frontend/tests/e2e/deepseek-harness/plan-approval-banner.spec.ts` |
| gemini-cli | W3-PLANBANNER | `frontend/tests/e2e/gemini-cli/plan-approval-banner.spec.ts` |
| grok-build | W3-PLANBANNER | `frontend/tests/e2e/grok-build/plan-approval-banner.spec.ts` |
| kimi-code | W3-PLANBANNER | `frontend/tests/e2e/kimi-code/plan-approval-banner.spec.ts` |
| mimo-code | W3-PLANBANNER | `frontend/tests/e2e/mimo-code/plan-approval-banner.spec.ts` |
| qwen-code | W3-PLANBANNER | `frontend/tests/e2e/qwen-code/plan-approval-banner.spec.ts` |

### L069: P1 PAB-6 / B10

Kind: port. Execution wave: 3.

B10. The banner shows the plan text that the native request carries.

| Target | Requirement group | Complete browser file |
|---|---|---|
| grok-build | W3-PLANBANNER | `frontend/tests/e2e/grok-build/plan-approval-banner.spec.ts` |
| mimo-code | W3-PLANBANNER | `frontend/tests/e2e/mimo-code/plan-approval-banner.spec.ts` |
| pi | W3-PLANBANNER | `frontend/tests/e2e/pi/plan-approval-banner.spec.ts` |

### L070: P1 PAB-7 / B11

Kind: port. Execution wave: 3.

B11. The saved answer row shows the decision word, and the row survives a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W3-PLANBANNER | `frontend/tests/e2e/cline/plan-approval-banner.spec.ts` |
| codebuddy-code | W3-PLANBANNER | `frontend/tests/e2e/codebuddy-code/plan-approval-banner.spec.ts` |
| codex | W3-PLANBANNER | `frontend/tests/e2e/codex/plan-approval-banner.spec.ts` |
| deepseek-harness | W3-PLANBANNER | `frontend/tests/e2e/deepseek-harness/plan-approval-banner.spec.ts` |
| gemini-cli | W3-PLANBANNER | `frontend/tests/e2e/gemini-cli/plan-approval-banner.spec.ts` |
| kimi-code | W3-PLANBANNER | `frontend/tests/e2e/kimi-code/plan-approval-banner.spec.ts` |
| qoder-cli | W3-PLANBANNER | `frontend/tests/e2e/qoder-cli/plan-approval-banner.spec.ts` |

### L071: P2 bypass-permissions-shortcut-P3 / bypass-permissions-shortcut-B3

Kind: port. Execution wave: 3.

On a plan-approval banner, the Bypass pill plus Approve continues the plan in the bypass mode.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W3-PLANBANNER | `frontend/tests/e2e/cline/plan-approval-banner.spec.ts` |
| codebuddy-code | W3-PLANBANNER | `frontend/tests/e2e/codebuddy-code/plan-approval-banner.spec.ts` |
| codex | W3-PLANBANNER | `frontend/tests/e2e/codex/plan-approval-banner.spec.ts` |
| factory-droid | W3-PLANBANNER | `frontend/tests/e2e/factory-droid/plan-approval-banner.spec.ts` |
| gemini-cli | W3-PLANBANNER | `frontend/tests/e2e/gemini-cli/plan-approval-banner.spec.ts` |
| github-copilot | W3-PLANBANNER | `frontend/tests/e2e/github-copilot/plan-approval-banner.spec.ts` |
| kimi-code | W3-PLANBANNER | `frontend/tests/e2e/kimi-code/plan-approval-banner.spec.ts` |
| qwen-code | W3-PLANBANNER | `frontend/tests/e2e/qwen-code/plan-approval-banner.spec.ts` |

### L072: P2 model-B4 unclear / model-B4

Kind: probe. Execution wave: 3.

The Default model entry resolves to a concrete model, and the effort menu of that model returns.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W3-MODEL | `frontend/tests/e2e/cline/model.spec.ts` |

### L073: P2 model-P1 / model-B1

Kind: port. Execution wave: 3.

A model switch writes the chat notice `Model (<old label> → <new label>)` with the catalog labels.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W3-MODEL | `frontend/tests/e2e/cline/model.spec.ts` |
| codebuddy-code | W3-MODEL | `frontend/tests/e2e/codebuddy-code/model.spec.ts` |
| codewhale | W3-MODEL | `frontend/tests/e2e/codewhale/model.spec.ts` |
| codex | W3-MODEL | `frontend/tests/e2e/codex/model.spec.ts` |
| command-code | W3-MODEL | `frontend/tests/e2e/command-code/model.spec.ts` |
| cursor | W3-MODEL | `frontend/tests/e2e/cursor/model.spec.ts` |
| deepseek-harness | W3-MODEL | `frontend/tests/e2e/deepseek-harness/model.spec.ts` |
| dirac | W3-MODEL | `frontend/tests/e2e/dirac/model.spec.ts` |
| factory-droid | W3-MODEL | `frontend/tests/e2e/factory-droid/model.spec.ts` |
| gemini-cli | W3-MODEL | `frontend/tests/e2e/gemini-cli/model.spec.ts` |
| github-copilot | W3-MODEL | `frontend/tests/e2e/github-copilot/model.spec.ts` |
| goose | W3-MODEL | `frontend/tests/e2e/goose/model.spec.ts` |
| grok-build | W3-MODEL | `frontend/tests/e2e/grok-build/model.spec.ts` |
| junie | W3-MODEL | `frontend/tests/e2e/junie/model.spec.ts` |
| kilo | W3-MODEL | `frontend/tests/e2e/kilo/model.spec.ts` |
| kimi-code | W3-MODEL | `frontend/tests/e2e/kimi-code/model.spec.ts` |
| kiro | W3-MODEL | `frontend/tests/e2e/kiro/model.spec.ts` |
| letta-code | W3-MODEL | `frontend/tests/e2e/letta-code/model.spec.ts` |
| mimo-code | W3-MODEL | `frontend/tests/e2e/mimo-code/model.spec.ts` |
| oh-my-pi | W3-MODEL | `frontend/tests/e2e/oh-my-pi/model.spec.ts` |
| opencode | W3-MODEL | `frontend/tests/e2e/opencode/model.spec.ts` |
| pi | W3-MODEL | `frontend/tests/e2e/pi/model.spec.ts` |
| qoder-cli | W3-MODEL | `frontend/tests/e2e/qoder-cli/model.spec.ts` |
| qwen-code | W3-MODEL | `frontend/tests/e2e/qwen-code/model.spec.ts` |
| reasonix | W3-MODEL | `frontend/tests/e2e/reasonix/model.spec.ts` |
| zcode | W3-MODEL | `frontend/tests/e2e/zcode/model.spec.ts` |

### L074: P2 model-P2 / model-B4

Kind: port. Execution wave: 3.

The Default model entry resolves to a concrete model, and the effort menu of that model returns.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W3-MODEL | `frontend/tests/e2e/codex/model.spec.ts` |
| command-code | W3-MODEL | `frontend/tests/e2e/command-code/model.spec.ts` |
| github-copilot | W3-MODEL | `frontend/tests/e2e/github-copilot/model.spec.ts` |

### L075: P2 model-P3 / model-B5

Kind: port. Execution wave: 3.

A model switch keeps the native session, and the next request still holds the earlier conversation.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W3-MODEL | `frontend/tests/e2e/claude-code/model.spec.ts` |
| cline | W3-MODEL | `frontend/tests/e2e/cline/model.spec.ts` |
| codebuddy-code | W3-MODEL | `frontend/tests/e2e/codebuddy-code/model.spec.ts` |
| codewhale | W3-MODEL | `frontend/tests/e2e/codewhale/model.spec.ts` |
| codex | W3-MODEL | `frontend/tests/e2e/codex/model.spec.ts` |
| command-code | W3-MODEL | `frontend/tests/e2e/command-code/model.spec.ts` |
| cursor | W3-MODEL | `frontend/tests/e2e/cursor/model.spec.ts` |
| deepseek-harness | W3-MODEL | `frontend/tests/e2e/deepseek-harness/model.spec.ts` |
| dirac | W3-MODEL | `frontend/tests/e2e/dirac/model.spec.ts` |
| factory-droid | W3-MODEL | `frontend/tests/e2e/factory-droid/model.spec.ts` |
| github-copilot | W3-MODEL | `frontend/tests/e2e/github-copilot/model.spec.ts` |
| goose | W3-MODEL | `frontend/tests/e2e/goose/model.spec.ts` |
| grok-build | W3-MODEL | `frontend/tests/e2e/grok-build/model.spec.ts` |
| junie | W3-MODEL | `frontend/tests/e2e/junie/model.spec.ts` |
| kilo | W3-MODEL | `frontend/tests/e2e/kilo/model.spec.ts` |
| kimi-code | W3-MODEL | `frontend/tests/e2e/kimi-code/model.spec.ts` |
| kiro | W3-MODEL | `frontend/tests/e2e/kiro/model.spec.ts` |
| letta-code | W3-MODEL | `frontend/tests/e2e/letta-code/model.spec.ts` |
| mimo-code | W3-MODEL | `frontend/tests/e2e/mimo-code/model.spec.ts` |
| oh-my-pi | W3-MODEL | `frontend/tests/e2e/oh-my-pi/model.spec.ts` |
| opencode | W3-MODEL | `frontend/tests/e2e/opencode/model.spec.ts` |
| pi | W3-MODEL | `frontend/tests/e2e/pi/model.spec.ts` |
| qoder-cli | W3-MODEL | `frontend/tests/e2e/qoder-cli/model.spec.ts` |
| qwen-code | W3-MODEL | `frontend/tests/e2e/qwen-code/model.spec.ts` |
| reasonix | W3-MODEL | `frontend/tests/e2e/reasonix/model.spec.ts` |
| zcode | W3-MODEL | `frontend/tests/e2e/zcode/model.spec.ts` |

### L076: P2 model-P4 / model-B6

Kind: port. Execution wave: 3.

Before any switch, the model chip shows the launch model.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W3-MODEL | `frontend/tests/e2e/codebuddy-code/model.spec.ts` |
| codewhale | W3-MODEL | `frontend/tests/e2e/codewhale/model.spec.ts` |
| codex | W3-MODEL | `frontend/tests/e2e/codex/model.spec.ts` |
| command-code | W3-MODEL | `frontend/tests/e2e/command-code/model.spec.ts` |
| cursor | W3-MODEL | `frontend/tests/e2e/cursor/model.spec.ts` |
| deepseek-harness | W3-MODEL | `frontend/tests/e2e/deepseek-harness/model.spec.ts` |
| dirac | W3-MODEL | `frontend/tests/e2e/dirac/model.spec.ts` |
| factory-droid | W3-MODEL | `frontend/tests/e2e/factory-droid/model.spec.ts` |
| gemini-cli | W3-MODEL | `frontend/tests/e2e/gemini-cli/model.spec.ts` |
| github-copilot | W3-MODEL | `frontend/tests/e2e/github-copilot/model.spec.ts` |
| goose | W3-MODEL | `frontend/tests/e2e/goose/model.spec.ts` |
| grok-build | W3-MODEL | `frontend/tests/e2e/grok-build/model.spec.ts` |
| junie | W3-MODEL | `frontend/tests/e2e/junie/model.spec.ts` |
| kilo | W3-MODEL | `frontend/tests/e2e/kilo/model.spec.ts` |
| kimi-code | W3-MODEL | `frontend/tests/e2e/kimi-code/model.spec.ts` |
| kiro | W3-MODEL | `frontend/tests/e2e/kiro/model.spec.ts` |
| mimo-code | W3-MODEL | `frontend/tests/e2e/mimo-code/model.spec.ts` |
| oh-my-pi | W3-MODEL | `frontend/tests/e2e/oh-my-pi/model.spec.ts` |
| opencode | W3-MODEL | `frontend/tests/e2e/opencode/model.spec.ts` |
| pi | W3-MODEL | `frontend/tests/e2e/pi/model.spec.ts` |
| qoder-cli | W3-MODEL | `frontend/tests/e2e/qoder-cli/model.spec.ts` |
| qwen-code | W3-MODEL | `frontend/tests/e2e/qwen-code/model.spec.ts` |
| reasonix | W3-MODEL | `frontend/tests/e2e/reasonix/model.spec.ts` |
| zcode | W3-MODEL | `frontend/tests/e2e/zcode/model.spec.ts` |

### L077: P2 session-resume-P1 / session-resume-B2

Kind: port. Execution wave: 3.

While the tab of a native session is open, the picker does not offer that session, and with nothing else to offer the field shows the session-ID text box. After the tab closes, the picker offers that session.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W3-PICKER | `frontend/tests/e2e/amp/session-resume.spec.ts` |
| cline | W3-PICKER | `frontend/tests/e2e/cline/session-resume.spec.ts` |
| codebuddy-code | W3-PICKER | `frontend/tests/e2e/codebuddy-code/session-resume.spec.ts` |
| codewhale | W3-PICKER | `frontend/tests/e2e/codewhale/session-resume.spec.ts` |
| codex | W3-PICKER | `frontend/tests/e2e/codex/session-resume.spec.ts` |
| command-code | W3-PICKER | `frontend/tests/e2e/command-code/session-resume.spec.ts` |
| cursor | W3-PICKER | `frontend/tests/e2e/cursor/session-resume.spec.ts` |
| deepseek-harness | W3-PICKER | `frontend/tests/e2e/deepseek-harness/session-resume.spec.ts` |
| dirac | W3-PICKER | `frontend/tests/e2e/dirac/session-resume.spec.ts` |
| factory-droid | W3-PICKER | `frontend/tests/e2e/factory-droid/session-resume.spec.ts` |
| fast-agent | W3-PICKER | `frontend/tests/e2e/fast-agent/session-resume.spec.ts` |
| gemini-cli | W3-PICKER | `frontend/tests/e2e/gemini-cli/session-resume.spec.ts` |
| github-copilot | W3-PICKER | `frontend/tests/e2e/github-copilot/session-resume.spec.ts` |
| goose | W3-PICKER | `frontend/tests/e2e/goose/session-resume.spec.ts` |
| grok-build | W3-PICKER | `frontend/tests/e2e/grok-build/session-resume.spec.ts` |
| junie | W3-PICKER | `frontend/tests/e2e/junie/session-resume.spec.ts` |
| kilo | W3-PICKER | `frontend/tests/e2e/kilo/session-resume.spec.ts` |
| kimi-code | W3-PICKER | `frontend/tests/e2e/kimi-code/session-resume.spec.ts` |
| kiro | W3-PICKER | `frontend/tests/e2e/kiro/session-resume.spec.ts` |
| letta-code | W3-PICKER | `frontend/tests/e2e/letta-code/session-resume.spec.ts` |
| mimo-code | W3-PICKER | `frontend/tests/e2e/mimo-code/session-resume.spec.ts` |
| oh-my-pi | W3-PICKER | `frontend/tests/e2e/oh-my-pi/session-resume.spec.ts` |
| opencode | W3-PICKER | `frontend/tests/e2e/opencode/session-resume.spec.ts` |
| pi | W3-PICKER | `frontend/tests/e2e/pi/session-resume.spec.ts` |
| qoder-cli | W3-PICKER | `frontend/tests/e2e/qoder-cli/session-resume.spec.ts` |
| qwen-code | W3-PICKER | `frontend/tests/e2e/qwen-code/session-resume.spec.ts` |
| reasonix | W3-PICKER | `frontend/tests/e2e/reasonix/session-resume.spec.ts` |
| zcode | W3-PICKER | `frontend/tests/e2e/zcode/session-resume.spec.ts` |

### L078: P2 session-resume-P2 / session-resume-B3

Kind: port. Execution wave: 3.

In a directory with no stored session of the provider, the field shows the session-ID text box under the label "Resume an existing session".

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W3-PICKER | `frontend/tests/e2e/amp/session-resume.spec.ts` |
| cline | W3-PICKER | `frontend/tests/e2e/cline/session-resume.spec.ts` |
| codebuddy-code | W3-PICKER | `frontend/tests/e2e/codebuddy-code/session-resume.spec.ts` |
| codewhale | W3-PICKER | `frontend/tests/e2e/codewhale/session-resume.spec.ts` |
| codex | W3-PICKER | `frontend/tests/e2e/codex/session-resume.spec.ts` |
| command-code | W3-PICKER | `frontend/tests/e2e/command-code/session-resume.spec.ts` |
| cursor | W3-PICKER | `frontend/tests/e2e/cursor/session-resume.spec.ts` |
| deepseek-harness | W3-PICKER | `frontend/tests/e2e/deepseek-harness/session-resume.spec.ts` |
| dirac | W3-PICKER | `frontend/tests/e2e/dirac/session-resume.spec.ts` |
| factory-droid | W3-PICKER | `frontend/tests/e2e/factory-droid/session-resume.spec.ts` |
| fast-agent | W3-PICKER | `frontend/tests/e2e/fast-agent/session-resume.spec.ts` |
| gemini-cli | W3-PICKER | `frontend/tests/e2e/gemini-cli/session-resume.spec.ts` |
| github-copilot | W3-PICKER | `frontend/tests/e2e/github-copilot/session-resume.spec.ts` |
| goose | W3-PICKER | `frontend/tests/e2e/goose/session-resume.spec.ts` |
| grok-build | W3-PICKER | `frontend/tests/e2e/grok-build/session-resume.spec.ts` |
| junie | W3-PICKER | `frontend/tests/e2e/junie/session-resume.spec.ts` |
| kilo | W3-PICKER | `frontend/tests/e2e/kilo/session-resume.spec.ts` |
| kimi-code | W3-PICKER | `frontend/tests/e2e/kimi-code/session-resume.spec.ts` |
| kiro | W3-PICKER | `frontend/tests/e2e/kiro/session-resume.spec.ts` |
| letta-code | W3-PICKER | `frontend/tests/e2e/letta-code/session-resume.spec.ts` |
| mimo-code | W3-PICKER | `frontend/tests/e2e/mimo-code/session-resume.spec.ts` |
| oh-my-pi | W3-PICKER | `frontend/tests/e2e/oh-my-pi/session-resume.spec.ts` |
| opencode | W3-PICKER | `frontend/tests/e2e/opencode/session-resume.spec.ts` |
| pi | W3-PICKER | `frontend/tests/e2e/pi/session-resume.spec.ts` |
| qoder-cli | W3-PICKER | `frontend/tests/e2e/qoder-cli/session-resume.spec.ts` |
| qwen-code | W3-PICKER | `frontend/tests/e2e/qwen-code/session-resume.spec.ts` |
| reasonix | W3-PICKER | `frontend/tests/e2e/reasonix/session-resume.spec.ts` |
| zcode | W3-PICKER | `frontend/tests/e2e/zcode/session-resume.spec.ts` |

### L079: P2 session-resume-P3 / session-resume-B5

Kind: port. Execution wave: 3.

The menu row that opens the text box, and the text box placeholder, state the handle form of the provider (a session ID, or a session ID or a file path).

| Target | Requirement group | Complete browser file |
|---|---|---|
| oh-my-pi | W3-PICKER | `frontend/tests/e2e/oh-my-pi/session-resume.spec.ts` |
| pi | W3-PICKER | `frontend/tests/e2e/pi/session-resume.spec.ts` |

### L080: P2 session-resume-P4 / session-resume-B7

Kind: port. Execution wave: 3.

After a Worker restart, the Worker starts the agent again in its stored native session without a message, and the agent answers the next message.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W3-RESTART | `frontend/tests/e2e/amp/session-resume.spec.ts` |
| cline | W3-RESTART | `frontend/tests/e2e/cline/session-resume.spec.ts` |
| codebuddy-code | W3-RESTART | `frontend/tests/e2e/codebuddy-code/session-resume.spec.ts` |
| codewhale | W3-RESTART | `frontend/tests/e2e/codewhale/session-resume.spec.ts` |
| codex | W3-RESTART | `frontend/tests/e2e/codex/session-resume.spec.ts` |
| command-code | W3-RESTART | `frontend/tests/e2e/command-code/session-resume.spec.ts` |
| cursor | W3-RESTART | `frontend/tests/e2e/cursor/session-resume.spec.ts` |
| deepseek-harness | W3-RESTART | `frontend/tests/e2e/deepseek-harness/session-resume.spec.ts` |
| dirac | W3-RESTART | `frontend/tests/e2e/dirac/session-resume.spec.ts` |
| factory-droid | W3-RESTART | `frontend/tests/e2e/factory-droid/session-resume.spec.ts` |
| fast-agent | W3-RESTART | `frontend/tests/e2e/fast-agent/session-resume.spec.ts` |
| gemini-cli | W3-RESTART | `frontend/tests/e2e/gemini-cli/session-resume.spec.ts` |
| github-copilot | W3-RESTART | `frontend/tests/e2e/github-copilot/session-resume.spec.ts` |
| goose | W3-RESTART | `frontend/tests/e2e/goose/session-resume.spec.ts` |
| grok-build | W3-RESTART | `frontend/tests/e2e/grok-build/session-resume.spec.ts` |
| junie | W3-RESTART | `frontend/tests/e2e/junie/session-resume.spec.ts` |
| kilo | W3-RESTART | `frontend/tests/e2e/kilo/session-resume.spec.ts` |
| kimi-code | W3-RESTART | `frontend/tests/e2e/kimi-code/session-resume.spec.ts` |
| kiro | W3-RESTART | `frontend/tests/e2e/kiro/session-resume.spec.ts` |
| letta-code | W3-RESTART | `frontend/tests/e2e/letta-code/session-resume.spec.ts` |
| mimo-code | W3-RESTART | `frontend/tests/e2e/mimo-code/session-resume.spec.ts` |
| oh-my-pi | W3-RESTART | `frontend/tests/e2e/oh-my-pi/session-resume.spec.ts` |
| opencode | W3-RESTART | `frontend/tests/e2e/opencode/session-resume.spec.ts` |
| pi | W3-RESTART | `frontend/tests/e2e/pi/session-resume.spec.ts` |
| qoder-cli | W3-RESTART | `frontend/tests/e2e/qoder-cli/session-resume.spec.ts` |
| qwen-code | W3-RESTART | `frontend/tests/e2e/qwen-code/session-resume.spec.ts` |
| reasonix | W3-RESTART | `frontend/tests/e2e/reasonix/session-resume.spec.ts` |
| zcode | W3-RESTART | `frontend/tests/e2e/zcode/session-resume.spec.ts` |

### L081: P2 session-resume-P5 / session-resume-B8

Kind: port. Execution wave: 3.

After a Worker restart, a non-default setting chosen before the restart is still applied, and a setting change and `/clear` still reach the resumed agent.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W3-RESTART | `frontend/tests/e2e/amp/session-resume.spec.ts` |
| cline | W3-RESTART | `frontend/tests/e2e/cline/session-resume.spec.ts` |
| codebuddy-code | W3-RESTART | `frontend/tests/e2e/codebuddy-code/session-resume.spec.ts` |
| codewhale | W3-RESTART | `frontend/tests/e2e/codewhale/session-resume.spec.ts` |
| codex | W3-RESTART | `frontend/tests/e2e/codex/session-resume.spec.ts` |
| command-code | W3-RESTART | `frontend/tests/e2e/command-code/session-resume.spec.ts` |
| cursor | W3-RESTART | `frontend/tests/e2e/cursor/session-resume.spec.ts` |
| deepseek-harness | W3-RESTART | `frontend/tests/e2e/deepseek-harness/session-resume.spec.ts` |
| dirac | W3-RESTART | `frontend/tests/e2e/dirac/session-resume.spec.ts` |
| factory-droid | W3-RESTART | `frontend/tests/e2e/factory-droid/session-resume.spec.ts` |
| fast-agent | W3-RESTART | `frontend/tests/e2e/fast-agent/session-resume.spec.ts` |
| gemini-cli | W3-RESTART | `frontend/tests/e2e/gemini-cli/session-resume.spec.ts` |
| github-copilot | W3-RESTART | `frontend/tests/e2e/github-copilot/session-resume.spec.ts` |
| goose | W3-RESTART | `frontend/tests/e2e/goose/session-resume.spec.ts` |
| grok-build | W3-RESTART | `frontend/tests/e2e/grok-build/session-resume.spec.ts` |
| junie | W3-RESTART | `frontend/tests/e2e/junie/session-resume.spec.ts` |
| kilo | W3-RESTART | `frontend/tests/e2e/kilo/session-resume.spec.ts` |
| kimi-code | W3-RESTART | `frontend/tests/e2e/kimi-code/session-resume.spec.ts` |
| kiro | W3-RESTART | `frontend/tests/e2e/kiro/session-resume.spec.ts` |
| letta-code | W3-RESTART | `frontend/tests/e2e/letta-code/session-resume.spec.ts` |
| mimo-code | W3-RESTART | `frontend/tests/e2e/mimo-code/session-resume.spec.ts` |
| oh-my-pi | W3-RESTART | `frontend/tests/e2e/oh-my-pi/session-resume.spec.ts` |
| opencode | W3-RESTART | `frontend/tests/e2e/opencode/session-resume.spec.ts` |
| pi | W3-RESTART | `frontend/tests/e2e/pi/session-resume.spec.ts` |
| qoder-cli | W3-RESTART | `frontend/tests/e2e/qoder-cli/session-resume.spec.ts` |
| qwen-code | W3-RESTART | `frontend/tests/e2e/qwen-code/session-resume.spec.ts` |
| reasonix | W3-RESTART | `frontend/tests/e2e/reasonix/session-resume.spec.ts` |
| zcode | W3-RESTART | `frontend/tests/e2e/zcode/session-resume.spec.ts` |

### L082: P1 I3 unclear / I3

Kind: probe. Execution wave: 4.

I3. A partial streamed answer keeps the marker `Text truncated by interruption.` after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W4-INTMARK | `frontend/tests/e2e/cursor/interrupt-a-turn.spec.ts` |
| dirac | W4-INTMARK | `frontend/tests/e2e/dirac/interrupt-a-turn.spec.ts` |
| factory-droid | W4-INTMARK | `frontend/tests/e2e/factory-droid/interrupt-a-turn.spec.ts` |
| fast-agent | W4-INTMARK | `frontend/tests/e2e/fast-agent/interrupt-a-turn.spec.ts` |
| gemini-cli | W4-INTMARK | `frontend/tests/e2e/gemini-cli/interrupt-a-turn.spec.ts` |
| goose | W4-INTMARK | `frontend/tests/e2e/goose/interrupt-a-turn.spec.ts` |
| grok-build | W4-INTMARK | `frontend/tests/e2e/grok-build/interrupt-a-turn.spec.ts` |
| junie | W4-INTMARK | `frontend/tests/e2e/junie/interrupt-a-turn.spec.ts` |
| kilo | W4-INTMARK | `frontend/tests/e2e/kilo/interrupt-a-turn.spec.ts` |
| kiro | W4-INTMARK | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts` |
| letta-code | W4-INTMARK | `frontend/tests/e2e/letta-code/interrupt-a-turn.spec.ts` |
| opencode | W4-INTMARK | `frontend/tests/e2e/opencode/interrupt-a-turn.spec.ts` |
| qoder-cli | W4-INTMARK | `frontend/tests/e2e/qoder-cli/interrupt-a-turn.spec.ts` |
| qwen-code | W4-INTMARK | `frontend/tests/e2e/qwen-code/interrupt-a-turn.spec.ts` |
| reasonix | W4-INTMARK | `frontend/tests/e2e/reasonix/interrupt-a-turn.spec.ts` |

### L083: P2 subagent-live-transcript-P1 / subagent-live-transcript-B1

Kind: port. Execution wave: 4.

After a live child ends, its tab shows one completion and no thinking indicator, also after a reload

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W4-LIVECHILD | `frontend/tests/e2e/cline/subagent-live-transcript.spec.ts` |
| codebuddy-code | W4-LIVECHILD | `frontend/tests/e2e/codebuddy-code/subagent-live-transcript.spec.ts` |
| codewhale | W4-LIVECHILD | `frontend/tests/e2e/codewhale/subagent-live-transcript.spec.ts` |
| command-code | W4-LIVECHILD | `frontend/tests/e2e/command-code/subagent-live-transcript.spec.ts` |
| cursor | W4-LIVECHILD | `frontend/tests/e2e/cursor/subagent-live-transcript.spec.ts` |
| deepseek-harness | W4-LIVECHILD | `frontend/tests/e2e/deepseek-harness/subagent-live-transcript.spec.ts` |
| factory-droid | W4-LIVECHILD | `frontend/tests/e2e/factory-droid/subagent-live-transcript.spec.ts` |
| gemini-cli | W4-LIVECHILD | `frontend/tests/e2e/gemini-cli/subagent-live-transcript.spec.ts` |
| github-copilot | W4-LIVECHILD | `frontend/tests/e2e/github-copilot/subagent-live-transcript.spec.ts` |
| goose | W4-LIVECHILD | `frontend/tests/e2e/goose/subagent-live-transcript.spec.ts` |
| grok-build | W4-LIVECHILD | `frontend/tests/e2e/grok-build/subagent-live-transcript.spec.ts` |
| junie | W4-LIVECHILD | `frontend/tests/e2e/junie/subagent-live-transcript.spec.ts` |
| kimi-code | W4-LIVECHILD | `frontend/tests/e2e/kimi-code/subagent-live-transcript.spec.ts` |
| kiro | W4-LIVECHILD | `frontend/tests/e2e/kiro/subagent-live-transcript.spec.ts` |
| letta-code | W4-LIVECHILD | `frontend/tests/e2e/letta-code/subagent-live-transcript.spec.ts` |
| mimo-code | W4-LIVECHILD | `frontend/tests/e2e/mimo-code/subagent-live-transcript.spec.ts` |
| oh-my-pi | W4-LIVECHILD | `frontend/tests/e2e/oh-my-pi/subagent-live-transcript.spec.ts` |
| qoder-cli | W4-LIVECHILD | `frontend/tests/e2e/qoder-cli/subagent-live-transcript.spec.ts` |
| qwen-code | W4-LIVECHILD | `frontend/tests/e2e/qwen-code/subagent-live-transcript.spec.ts` |
| zcode | W4-LIVECHILD | `frontend/tests/e2e/zcode/subagent-live-transcript.spec.ts` |

### L084: P2 subagent-live-transcript-P2 / subagent-live-transcript-B2

Kind: port. Execution wave: 4.

While the child runs, its tab shows the text that precedes a tool call, and the tab keeps the native row order

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W4-LIVECHILD | `frontend/tests/e2e/claude-code/subagent-live-transcript.spec.ts` |
| cline | W4-LIVECHILD | `frontend/tests/e2e/cline/subagent-live-transcript.spec.ts` |
| codebuddy-code | W4-LIVECHILD | `frontend/tests/e2e/codebuddy-code/subagent-live-transcript.spec.ts` |
| codewhale | W4-LIVECHILD | `frontend/tests/e2e/codewhale/subagent-live-transcript.spec.ts` |
| codex | W4-LIVECHILD | `frontend/tests/e2e/codex/subagent-live-transcript.spec.ts` |
| cursor | W4-LIVECHILD | `frontend/tests/e2e/cursor/subagent-live-transcript.spec.ts` |
| deepseek-harness | W4-LIVECHILD | `frontend/tests/e2e/deepseek-harness/subagent-live-transcript.spec.ts` |
| github-copilot | W4-LIVECHILD | `frontend/tests/e2e/github-copilot/subagent-live-transcript.spec.ts` |
| grok-build | W4-LIVECHILD | `frontend/tests/e2e/grok-build/subagent-live-transcript.spec.ts` |
| kimi-code | W4-LIVECHILD | `frontend/tests/e2e/kimi-code/subagent-live-transcript.spec.ts` |
| kiro | W4-LIVECHILD | `frontend/tests/e2e/kiro/subagent-live-transcript.spec.ts` |
| mimo-code | W4-LIVECHILD | `frontend/tests/e2e/mimo-code/subagent-live-transcript.spec.ts` |
| oh-my-pi | W4-LIVECHILD | `frontend/tests/e2e/oh-my-pi/subagent-live-transcript.spec.ts` |
| qoder-cli | W4-LIVECHILD | `frontend/tests/e2e/qoder-cli/subagent-live-transcript.spec.ts` |
| qwen-code | W4-LIVECHILD | `frontend/tests/e2e/qwen-code/subagent-live-transcript.spec.ts` |

### L085: P2 subagent-transcript-tab-P1 / subagent-transcript-tab-B1

Kind: port. Execution wave: 4, 5.

The rows of a child stay out of the parent tab

| Target | Requirement group | Complete browser file |
|---|---|---|
| dirac | W5-SUBTAB-H | `frontend/tests/e2e/dirac/subagent-transcript-tab.spec.ts` |
| fast-agent | W5-SUBTAB-H | `frontend/tests/e2e/fast-agent/subagent-transcript-tab.spec.ts` |
| gemini-cli | W4-LIVECHILD | `frontend/tests/e2e/gemini-cli/subagent-transcript-tab.spec.ts` |
| github-copilot | W4-LIVECHILD | `frontend/tests/e2e/github-copilot/subagent-transcript-tab.spec.ts` |
| goose | W4-LIVECHILD | `frontend/tests/e2e/goose/subagent-transcript-tab.spec.ts` |
| zcode | W4-LIVECHILD | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` |

### L086: P3 MR-B1 / MR-B1

Kind: port. Execution wave: 4.

An external native session (written outside LeapMux) appears in the picker of its own directory only (the sessions of another directory, and archived or empty ones, stay out), and after the reopen the next request holds its context although no Worker rows exist.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W4-EXTSESSION-A | `frontend/tests/e2e/claude-code/session-resume.spec.ts` |
| codebuddy-code | W4-EXTSESSION-B | `frontend/tests/e2e/codebuddy-code/session-resume.spec.ts` |
| codewhale | W4-EXTSESSION-A | `frontend/tests/e2e/codewhale/session-resume.spec.ts` |
| codex | W4-EXTSESSION-A | `frontend/tests/e2e/codex/session-resume.spec.ts` |
| command-code | W4-EXTSESSION-B | `frontend/tests/e2e/command-code/session-resume.spec.ts` |
| cursor | W4-EXTSESSION-A | `frontend/tests/e2e/cursor/session-resume.spec.ts` |
| deepseek-harness | W4-EXTSESSION-B | `frontend/tests/e2e/deepseek-harness/session-resume.spec.ts` |
| factory-droid | W4-EXTSESSION-B | `frontend/tests/e2e/factory-droid/session-resume.spec.ts` |
| fast-agent | W4-EXTSESSION-B | `frontend/tests/e2e/fast-agent/session-resume.spec.ts` |
| gemini-cli | W4-EXTSESSION-B | `frontend/tests/e2e/gemini-cli/session-resume.spec.ts` |
| github-copilot | W4-EXTSESSION-A | `frontend/tests/e2e/github-copilot/session-resume.spec.ts` |
| goose | W4-EXTSESSION-A | `frontend/tests/e2e/goose/session-resume.spec.ts` |
| grok-build | W4-EXTSESSION-B | `frontend/tests/e2e/grok-build/session-resume.spec.ts` |
| junie | W4-EXTSESSION-B | `frontend/tests/e2e/junie/session-resume.spec.ts` |
| kilo | W4-EXTSESSION-A | `frontend/tests/e2e/kilo/session-resume.spec.ts` |
| kimi-code | W4-EXTSESSION-A | `frontend/tests/e2e/kimi-code/session-resume.spec.ts` |
| kiro | W4-EXTSESSION-B | `frontend/tests/e2e/kiro/session-resume.spec.ts` |
| letta-code | W4-EXTSESSION-B | `frontend/tests/e2e/letta-code/session-resume.spec.ts` |
| mimo-code | W4-EXTSESSION-A | `frontend/tests/e2e/mimo-code/session-resume.spec.ts` |
| oh-my-pi | W4-EXTSESSION-B | `frontend/tests/e2e/oh-my-pi/session-resume.spec.ts` |
| opencode | W4-EXTSESSION-A | `frontend/tests/e2e/opencode/session-resume.spec.ts` |
| pi | W4-EXTSESSION-A | `frontend/tests/e2e/pi/session-resume.spec.ts` |
| qoder-cli | W4-EXTSESSION-B | `frontend/tests/e2e/qoder-cli/session-resume.spec.ts` |
| qwen-code | W4-EXTSESSION-B | `frontend/tests/e2e/qwen-code/session-resume.spec.ts` |
| reasonix | W4-EXTSESSION-A | `frontend/tests/e2e/reasonix/session-resume.spec.ts` |
| zcode | W4-EXTSESSION-A | `frontend/tests/e2e/zcode/session-resume.spec.ts` |

### L087: P3 MR-B3 unclear / MR-B3

Kind: probe. Execution wave: 4.

A session that LeapMux created reopens through the picker with exactly its stored Worker rows, and the next request holds the prior exchange.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W4-EXTSESSION-A | `frontend/tests/e2e/cline/session-resume.spec.ts` |

### L088: P1 Q-P1 / AQ1

Kind: port. Execution wave: 5, 6.

AQ1. One call asks several questions. The banner shows one page for each question, and every answer reaches the native reply.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W5-QUESTIONS-A | `frontend/tests/e2e/codex/agent-questions.spec.ts` |
| cursor | W5-QUESTIONS-A | `frontend/tests/e2e/cursor/agent-questions.spec.ts` |
| deepseek-harness | W5-QUESTIONS-A | `frontend/tests/e2e/deepseek-harness/agent-questions.spec.ts` |
| grok-build | W6-QUESTIONS-B | `frontend/tests/e2e/grok-build/agent-questions.spec.ts` |
| kilo | W5-QUESTIONS-A | `frontend/tests/e2e/kilo/agent-questions.spec.ts` |
| kimi-code | W6-QUESTIONS-B | `frontend/tests/e2e/kimi-code/agent-questions.spec.ts` |
| letta-code | W6-QUESTIONS-B | `frontend/tests/e2e/letta-code/agent-questions.spec.ts` |
| oh-my-pi | W6-QUESTIONS-B | `frontend/tests/e2e/oh-my-pi/agent-questions.spec.ts` |
| opencode | W5-QUESTIONS-A | `frontend/tests/e2e/opencode/agent-questions.spec.ts` |
| pi | W5-QUESTIONS-A | `frontend/tests/e2e/pi/agent-questions.spec.ts` |
| qoder-cli | W6-QUESTIONS-B | `frontend/tests/e2e/qoder-cli/agent-questions.spec.ts` |
| qwen-code | W6-QUESTIONS-B | `frontend/tests/e2e/qwen-code/agent-questions.spec.ts` |
| zcode | W5-QUESTIONS-A | `frontend/tests/e2e/zcode/agent-questions.spec.ts` |

### L089: P1 Q-P3 / AQ4

Kind: port. Execution wave: 5, 6.

AQ4. A typed answer (composer text, or the text field of a form) reaches the native reply.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W5-QUESTIONS-A | `frontend/tests/e2e/cline/agent-questions.spec.ts` |
| codewhale | W6-QUESTIONS-B | `frontend/tests/e2e/codewhale/agent-questions.spec.ts` |
| codex | W5-QUESTIONS-A | `frontend/tests/e2e/codex/agent-questions.spec.ts` |
| cursor | W5-QUESTIONS-A | `frontend/tests/e2e/cursor/agent-questions.spec.ts` |
| deepseek-harness | W5-QUESTIONS-A | `frontend/tests/e2e/deepseek-harness/agent-questions.spec.ts` |
| factory-droid | W6-QUESTIONS-B | `frontend/tests/e2e/factory-droid/agent-questions.spec.ts` |
| github-copilot | W5-QUESTIONS-A | `frontend/tests/e2e/github-copilot/agent-questions.spec.ts` |
| kilo | W5-QUESTIONS-A | `frontend/tests/e2e/kilo/agent-questions.spec.ts` |
| kimi-code | W6-QUESTIONS-B | `frontend/tests/e2e/kimi-code/agent-questions.spec.ts` |
| kiro | W6-QUESTIONS-B | `frontend/tests/e2e/kiro/agent-questions.spec.ts` |
| letta-code | W6-QUESTIONS-B | `frontend/tests/e2e/letta-code/agent-questions.spec.ts` |
| oh-my-pi | W6-QUESTIONS-B | `frontend/tests/e2e/oh-my-pi/agent-questions.spec.ts` |
| opencode | W5-QUESTIONS-A | `frontend/tests/e2e/opencode/agent-questions.spec.ts` |
| qoder-cli | W6-QUESTIONS-B | `frontend/tests/e2e/qoder-cli/agent-questions.spec.ts` |
| qwen-code | W6-QUESTIONS-B | `frontend/tests/e2e/qwen-code/agent-questions.spec.ts` |
| zcode | W5-QUESTIONS-A | `frontend/tests/e2e/zcode/agent-questions.spec.ts` |

### L090: P1 Q-P4 / AQ6

Kind: port. Execution wave: 5, 6.

AQ6. The reader refuses the question (Stop on the banner, Cancel on a form, Deny on a permission-shaped question). The control closes, and the native reply states the refusal with no option chosen.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W5-QUESTIONS-A | `frontend/tests/e2e/cline/agent-questions.spec.ts` |
| codex | W5-QUESTIONS-A | `frontend/tests/e2e/codex/agent-questions.spec.ts` |
| cursor | W5-QUESTIONS-A | `frontend/tests/e2e/cursor/agent-questions.spec.ts` |
| deepseek-harness | W5-QUESTIONS-A | `frontend/tests/e2e/deepseek-harness/agent-questions.spec.ts` |
| factory-droid | W6-QUESTIONS-B | `frontend/tests/e2e/factory-droid/agent-questions.spec.ts` |
| github-copilot | W5-QUESTIONS-A | `frontend/tests/e2e/github-copilot/agent-questions.spec.ts` |
| grok-build | W6-QUESTIONS-B | `frontend/tests/e2e/grok-build/agent-questions.spec.ts` |
| junie | W6-QUESTIONS-B | `frontend/tests/e2e/junie/agent-questions.spec.ts` |
| kilo | W5-QUESTIONS-A | `frontend/tests/e2e/kilo/agent-questions.spec.ts` |
| kimi-code | W6-QUESTIONS-B | `frontend/tests/e2e/kimi-code/agent-questions.spec.ts` |
| kiro | W6-QUESTIONS-B | `frontend/tests/e2e/kiro/agent-questions.spec.ts` |
| letta-code | W6-QUESTIONS-B | `frontend/tests/e2e/letta-code/agent-questions.spec.ts` |
| oh-my-pi | W6-QUESTIONS-B | `frontend/tests/e2e/oh-my-pi/agent-questions.spec.ts` |
| opencode | W5-QUESTIONS-A | `frontend/tests/e2e/opencode/agent-questions.spec.ts` |
| pi | W5-QUESTIONS-A | `frontend/tests/e2e/pi/agent-questions.spec.ts` |
| qoder-cli | W6-QUESTIONS-B | `frontend/tests/e2e/qoder-cli/agent-questions.spec.ts` |
| qwen-code | W6-QUESTIONS-B | `frontend/tests/e2e/qwen-code/agent-questions.spec.ts` |
| zcode | W5-QUESTIONS-A | `frontend/tests/e2e/zcode/agent-questions.spec.ts` |

### L091: P2 background-tasks-sidebar-B3 unclear / background-tasks-sidebar-B3

Kind: probe. Execution wave: 5.

An interrupt ends the running background command, and its row shows Stopped.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-BGSHELL | `frontend/tests/e2e/claude-code/background-tasks-sidebar.spec.ts` |
| codebuddy-code | W5-BGSHELL | `frontend/tests/e2e/codebuddy-code/background-tasks-sidebar.spec.ts` |
| codewhale | W5-BGSHELL | `frontend/tests/e2e/codewhale/background-tasks-sidebar.spec.ts` |
| grok-build | W5-BGSHELL | `frontend/tests/e2e/grok-build/background-tasks-sidebar.spec.ts` |
| kimi-code | W5-BGSHELL | `frontend/tests/e2e/kimi-code/background-tasks-sidebar.spec.ts` |
| oh-my-pi | W5-BGSHELL | `frontend/tests/e2e/oh-my-pi/background-tasks-sidebar.spec.ts` |
| qwen-code | W5-BGSHELL | `frontend/tests/e2e/qwen-code/background-tasks-sidebar.spec.ts` |
| zcode | W5-BGSHELL | `frontend/tests/e2e/zcode/background-tasks-sidebar.spec.ts` |

### L092: P2 background-tasks-sidebar-B4 unclear / background-tasks-sidebar-B4

Kind: probe. Execution wave: 5.

The end of a background command starts a turn of the provider's own, and LeapMux shows its answer.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W5-BGSHELL | `frontend/tests/e2e/amp/background-tasks-sidebar.spec.ts` |
| codebuddy-code | W5-BGSHELL | `frontend/tests/e2e/codebuddy-code/background-tasks-sidebar.spec.ts` |
| codewhale | W5-BGSHELL | `frontend/tests/e2e/codewhale/background-tasks-sidebar.spec.ts` |
| cursor | W5-BGSHELL | `frontend/tests/e2e/cursor/background-tasks-sidebar.spec.ts` |
| grok-build | W5-BGSHELL | `frontend/tests/e2e/grok-build/background-tasks-sidebar.spec.ts` |
| oh-my-pi | W5-BGSHELL | `frontend/tests/e2e/oh-my-pi/background-tasks-sidebar.spec.ts` |
| qwen-code | W5-BGSHELL | `frontend/tests/e2e/qwen-code/background-tasks-sidebar.spec.ts` |
| zcode | W5-BGSHELL | `frontend/tests/e2e/zcode/background-tasks-sidebar.spec.ts` |

### L093: P2 background-tasks-sidebar-B6 unclear / background-tasks-sidebar-B6

Kind: probe. Execution wave: 5.

A background command that has no status route creates no false shell row.

| Target | Requirement group | Complete browser file |
|---|---|---|
| command-code | W5-BGSHELL | `frontend/tests/e2e/command-code/background-tasks-sidebar.spec.ts` |
| deepseek-harness | W5-BGSHELL | `frontend/tests/e2e/deepseek-harness/background-tasks-sidebar.spec.ts` |
| gemini-cli | W5-BGSHELL | `frontend/tests/e2e/gemini-cli/background-tasks-sidebar.spec.ts` |
| kilo | W5-BGSHELL | `frontend/tests/e2e/kilo/background-tasks-sidebar.spec.ts` |
| kiro | W5-BGSHELL | `frontend/tests/e2e/kiro/background-tasks-sidebar.spec.ts` |
| letta-code | W5-BGSHELL | `frontend/tests/e2e/letta-code/background-tasks-sidebar.spec.ts` |
| opencode | W5-BGSHELL | `frontend/tests/e2e/opencode/background-tasks-sidebar.spec.ts` |
| qoder-cli | W5-BGSHELL | `frontend/tests/e2e/qoder-cli/background-tasks-sidebar.spec.ts` |
| reasonix | W5-BGSHELL | `frontend/tests/e2e/reasonix/background-tasks-sidebar.spec.ts` |

### L094: P2 background-tasks-sidebar-P1 / background-tasks-sidebar-B1

Kind: port. Execution wave: 5.

A background shell command opens a non-clickable shell row that shows its command while it runs.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W5-BGSHELL | `frontend/tests/e2e/codebuddy-code/background-tasks-sidebar.spec.ts` |
| fast-agent | W5-BGSHELL | `frontend/tests/e2e/fast-agent/background-tasks-sidebar.spec.ts` |
| goose | W5-BGSHELL | `frontend/tests/e2e/goose/background-tasks-sidebar.spec.ts` |
| grok-build | W5-BGSHELL | `frontend/tests/e2e/grok-build/background-tasks-sidebar.spec.ts` |
| oh-my-pi | W5-BGSHELL | `frontend/tests/e2e/oh-my-pi/background-tasks-sidebar.spec.ts` |
| qwen-code | W5-BGSHELL | `frontend/tests/e2e/qwen-code/background-tasks-sidebar.spec.ts` |
| zcode | W5-BGSHELL | `frontend/tests/e2e/zcode/background-tasks-sidebar.spec.ts` |

### L095: P2 background-tasks-sidebar-P3 / background-tasks-sidebar-B2

Kind: port. Execution wave: 5.

The shell row reaches a final status when its command ends.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W5-BGSHELL | `frontend/tests/e2e/amp/background-tasks-sidebar.spec.ts` |
| claude-code | W5-BGSHELL | `frontend/tests/e2e/claude-code/background-tasks-sidebar.spec.ts` |

### L096: P2 background-tasks-sidebar-P4 / background-tasks-sidebar-B4

Kind: port. Execution wave: 5.

The end of a background command starts a turn of the provider's own, and LeapMux shows its answer.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-BGSHELL | `frontend/tests/e2e/claude-code/background-tasks-sidebar.spec.ts` |

### L097: P2 bypass-permissions-shortcut-P1 / bypass-permissions-shortcut-B1

Kind: port. Execution wave: 5.

The bypass mode survives a page reload, and a native tool still runs with no banner after it.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codewhale | W5-SHORTCUTS | `frontend/tests/e2e/codewhale/bypass-permissions-shortcut.spec.ts` |
| codex | W5-SHORTCUTS | `frontend/tests/e2e/codex/bypass-permissions-shortcut.spec.ts` |
| factory-droid | W5-SHORTCUTS | `frontend/tests/e2e/factory-droid/bypass-permissions-shortcut.spec.ts` |
| github-copilot | W5-SHORTCUTS | `frontend/tests/e2e/github-copilot/bypass-permissions-shortcut.spec.ts` |
| goose | W5-SHORTCUTS | `frontend/tests/e2e/goose/bypass-permissions-shortcut.spec.ts` |
| reasonix | W5-SHORTCUTS | `frontend/tests/e2e/reasonix/bypass-permissions-shortcut.spec.ts` |

### L098: P2 bypass-permissions-shortcut-P2 / bypass-permissions-shortcut-B2

Kind: port. Execution wave: 5.

On a permission banner, the Bypass pill plus Allow answers the request and then applies the bypass mode. The next tool runs with no banner.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W5-SHORTCUTS | `frontend/tests/e2e/amp/bypass-permissions-shortcut.spec.ts` |
| claude-code | W5-SHORTCUTS | `frontend/tests/e2e/claude-code/bypass-permissions-shortcut.spec.ts` |
| cline | W5-SHORTCUTS | `frontend/tests/e2e/cline/bypass-permissions-shortcut.spec.ts` |
| codebuddy-code | W5-SHORTCUTS | `frontend/tests/e2e/codebuddy-code/bypass-permissions-shortcut.spec.ts` |
| codex | W5-SHORTCUTS | `frontend/tests/e2e/codex/bypass-permissions-shortcut.spec.ts` |
| deepseek-harness | W5-SHORTCUTS | `frontend/tests/e2e/deepseek-harness/bypass-permissions-shortcut.spec.ts` |
| factory-droid | W5-SHORTCUTS | `frontend/tests/e2e/factory-droid/bypass-permissions-shortcut.spec.ts` |
| gemini-cli | W5-SHORTCUTS | `frontend/tests/e2e/gemini-cli/bypass-permissions-shortcut.spec.ts` |
| github-copilot | W5-SHORTCUTS | `frontend/tests/e2e/github-copilot/bypass-permissions-shortcut.spec.ts` |
| goose | W5-SHORTCUTS | `frontend/tests/e2e/goose/bypass-permissions-shortcut.spec.ts` |
| grok-build | W5-SHORTCUTS | `frontend/tests/e2e/grok-build/bypass-permissions-shortcut.spec.ts` |
| kimi-code | W5-SHORTCUTS | `frontend/tests/e2e/kimi-code/bypass-permissions-shortcut.spec.ts` |
| kiro | W5-SHORTCUTS | `frontend/tests/e2e/kiro/bypass-permissions-shortcut.spec.ts` |
| letta-code | W5-SHORTCUTS | `frontend/tests/e2e/letta-code/bypass-permissions-shortcut.spec.ts` |
| mimo-code | W5-SHORTCUTS | `frontend/tests/e2e/mimo-code/bypass-permissions-shortcut.spec.ts` |
| oh-my-pi | W5-SHORTCUTS | `frontend/tests/e2e/oh-my-pi/bypass-permissions-shortcut.spec.ts` |
| qwen-code | W5-SHORTCUTS | `frontend/tests/e2e/qwen-code/bypass-permissions-shortcut.spec.ts` |
| reasonix | W5-SHORTCUTS | `frontend/tests/e2e/reasonix/bypass-permissions-shortcut.spec.ts` |

### L099: P2 session-goal-set-and-clear-P1 / session-goal-set-and-clear-B1

Kind: port. Execution wave: 5.

A cleared goal stays cleared after a page reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-GOALS | `frontend/tests/e2e/claude-code/session-goal-set-and-clear.spec.ts` |
| codex | W5-GOALS | `frontend/tests/e2e/codex/session-goal-set-and-clear.spec.ts` |
| github-copilot | W5-GOALS | `frontend/tests/e2e/github-copilot/session-goal-set-and-clear.spec.ts` |
| grok-build | W5-GOALS | `frontend/tests/e2e/grok-build/session-goal-set-and-clear.spec.ts` |
| kimi-code | W5-GOALS | `frontend/tests/e2e/kimi-code/session-goal-set-and-clear.spec.ts` |
| kiro | W5-GOALS | `frontend/tests/e2e/kiro/session-goal-set-and-clear.spec.ts` |
| mimo-code | W5-GOALS | `frontend/tests/e2e/mimo-code/session-goal-set-and-clear.spec.ts` |
| qoder-cli | W5-GOALS | `frontend/tests/e2e/qoder-cli/session-goal-set-and-clear.spec.ts` |
| qwen-code | W5-GOALS | `frontend/tests/e2e/qwen-code/session-goal-set-and-clear.spec.ts` |
| zcode | W5-GOALS | `frontend/tests/e2e/zcode/session-goal-set-and-clear.spec.ts` |

### L100: P2 session-goal-set-and-clear-P2 / session-goal-set-and-clear-B2

Kind: port. Execution wave: 5.

A goal action travels as queued input, and the card changes only after the queue delivers it.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W5-GOALS | `frontend/tests/e2e/codebuddy-code/session-goal-set-and-clear.spec.ts` |
| grok-build | W5-GOALS | `frontend/tests/e2e/grok-build/session-goal-set-and-clear.spec.ts` |
| kimi-code | W5-GOALS | `frontend/tests/e2e/kimi-code/session-goal-set-and-clear.spec.ts` |
| kiro | W5-GOALS | `frontend/tests/e2e/kiro/session-goal-set-and-clear.spec.ts` |

### L101: P2 session-goal-set-and-clear-P3 / session-goal-set-and-clear-B3

Kind: port. Execution wave: 5.

The goal card follows a status that the native goal loop sets by itself (blocked with its reason, done, auto-paused, or removed).

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-GOALS | `frontend/tests/e2e/claude-code/session-goal-set-and-clear.spec.ts` |
| codex | W5-GOALS | `frontend/tests/e2e/codex/session-goal-set-and-clear.spec.ts` |
| deepseek-harness | W5-GOALS | `frontend/tests/e2e/deepseek-harness/session-goal-set-and-clear.spec.ts` |
| github-copilot | W5-GOALS | `frontend/tests/e2e/github-copilot/session-goal-set-and-clear.spec.ts` |
| pi | W5-GOALS | `frontend/tests/e2e/pi/session-goal-set-and-clear.spec.ts` |
| qoder-cli | W5-GOALS | `frontend/tests/e2e/qoder-cli/session-goal-set-and-clear.spec.ts` |
| reasonix | W5-GOALS | `frontend/tests/e2e/reasonix/session-goal-set-and-clear.spec.ts` |
| zcode | W5-GOALS | `frontend/tests/e2e/zcode/session-goal-set-and-clear.spec.ts` |

### L102: P2 session-goal-set-and-clear-P4 / session-goal-set-and-clear-B7

Kind: port. Execution wave: 5.

An objective that equals a goal command word ("clear") stays a literal objective or is refused with a reason; it never clears the goal.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-GOALS | `frontend/tests/e2e/claude-code/session-goal-set-and-clear.spec.ts` |
| github-copilot | W5-GOALS | `frontend/tests/e2e/github-copilot/session-goal-set-and-clear.spec.ts` |
| kilo | W5-GOALS | `frontend/tests/e2e/kilo/session-goal-set-and-clear.spec.ts` |
| kiro | W5-GOALS | `frontend/tests/e2e/kiro/session-goal-set-and-clear.spec.ts` |
| mimo-code | W5-GOALS | `frontend/tests/e2e/mimo-code/session-goal-set-and-clear.spec.ts` |
| pi | W5-GOALS | `frontend/tests/e2e/pi/session-goal-set-and-clear.spec.ts` |

### L103: P2 smart-permissions-shortcut-P1 / smart-permissions-shortcut-B1

Kind: port. Execution wave: 5.

The Smart mode survives a page reload, and the native safety behavior still holds after it.

| Target | Requirement group | Complete browser file |
|---|---|---|
| github-copilot | W5-SHORTCUTS | `frontend/tests/e2e/github-copilot/smart-permissions-shortcut.spec.ts` |
| goose | W5-SHORTCUTS | `frontend/tests/e2e/goose/smart-permissions-shortcut.spec.ts` |
| kimi-code | W5-SHORTCUTS | `frontend/tests/e2e/kimi-code/smart-permissions-shortcut.spec.ts` |
| qoder-cli | W5-SHORTCUTS | `frontend/tests/e2e/qoder-cli/smart-permissions-shortcut.spec.ts` |

### L104: P2 smart-permissions-shortcut-P2 / smart-permissions-shortcut-B2

Kind: port. Execution wave: 5.

Under Smart, a real native tool call goes through the native safety step (it runs, asks, or is blocked).

| Target | Requirement group | Complete browser file |
|---|---|---|
| qoder-cli | W5-SHORTCUTS | `frontend/tests/e2e/qoder-cli/smart-permissions-shortcut.spec.ts` |

### L105: P2 smart-permissions-shortcut-P3 / smart-permissions-shortcut-B3

Kind: port. Execution wave: 5.

The Smart and Bypass shortcuts switch the session between their two native modes in both directions, and the shortcut of the active preset shows as disabled.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codewhale | W5-SHORTCUTS | `frontend/tests/e2e/codewhale/smart-permissions-shortcut.spec.ts` |
| grok-build | W5-SHORTCUTS | `frontend/tests/e2e/grok-build/smart-permissions-shortcut.spec.ts` |
| kimi-code | W5-SHORTCUTS | `frontend/tests/e2e/kimi-code/smart-permissions-shortcut.spec.ts` |
| qwen-code | W5-SHORTCUTS | `frontend/tests/e2e/qwen-code/smart-permissions-shortcut.spec.ts` |

### L106: P2 subagent-transcript-tab-B3 unclear / subagent-transcript-tab-B3

Kind: probe. Execution wave: 5, 6.

A child that reuses a native ID after a context clear opens a new tab without the earlier rows

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W6-SUBTAB-A | `frontend/tests/e2e/claude-code/subagent-transcript-tab.spec.ts` |
| cline | W6-SUBTAB-A | `frontend/tests/e2e/cline/subagent-transcript-tab.spec.ts` |
| codebuddy-code | W6-SUBTAB-A | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` |
| codewhale | W6-SUBTAB-A | `frontend/tests/e2e/codewhale/subagent-transcript-tab.spec.ts` |
| codex | W5-SUBTAB-H | `frontend/tests/e2e/codex/subagent-transcript-tab.spec.ts` |
| command-code | W6-SUBTAB-A | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` |
| cursor | W6-SUBTAB-A | `frontend/tests/e2e/cursor/subagent-transcript-tab.spec.ts` |
| deepseek-harness | W6-SUBTAB-A | `frontend/tests/e2e/deepseek-harness/subagent-transcript-tab.spec.ts` |
| factory-droid | W6-SUBTAB-A | `frontend/tests/e2e/factory-droid/subagent-transcript-tab.spec.ts` |
| github-copilot | W6-SUBTAB-A | `frontend/tests/e2e/github-copilot/subagent-transcript-tab.spec.ts` |
| goose | W6-SUBTAB-A | `frontend/tests/e2e/goose/subagent-transcript-tab.spec.ts` |
| grok-build | W6-SUBTAB-B | `frontend/tests/e2e/grok-build/subagent-transcript-tab.spec.ts` |
| junie | W5-SUBTAB-H | `frontend/tests/e2e/junie/subagent-transcript-tab.spec.ts` |
| kilo | W6-SUBTAB-B | `frontend/tests/e2e/kilo/subagent-transcript-tab.spec.ts` |
| kiro | W6-SUBTAB-B | `frontend/tests/e2e/kiro/subagent-transcript-tab.spec.ts` |
| letta-code | W6-SUBTAB-B | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` |
| mimo-code | W6-SUBTAB-B | `frontend/tests/e2e/mimo-code/subagent-transcript-tab.spec.ts` |
| opencode | W6-SUBTAB-B | `frontend/tests/e2e/opencode/subagent-transcript-tab.spec.ts` |
| pi | W6-SUBTAB-B | `frontend/tests/e2e/pi/subagent-transcript-tab.spec.ts` |
| qoder-cli | W6-SUBTAB-B | `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts` |
| qwen-code | W6-SUBTAB-B | `frontend/tests/e2e/qwen-code/subagent-transcript-tab.spec.ts` |
| reasonix | W6-SUBTAB-B | `frontend/tests/e2e/reasonix/subagent-transcript-tab.spec.ts` |
| zcode | W6-SUBTAB-B | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` |

### L107: P2 subagent-transcript-tab-B5 unclear / subagent-transcript-tab-B5

Kind: probe. Execution wave: 5, 6.

After the root session closes and reopens, the completed child tab shows its transcript once

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W6-SUBTAB-A | `frontend/tests/e2e/cline/subagent-transcript-tab.spec.ts` |
| codebuddy-code | W6-SUBTAB-A | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` |
| codewhale | W6-SUBTAB-A | `frontend/tests/e2e/codewhale/subagent-transcript-tab.spec.ts` |
| codex | W5-SUBTAB-H | `frontend/tests/e2e/codex/subagent-transcript-tab.spec.ts` |
| command-code | W6-SUBTAB-A | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` |
| cursor | W6-SUBTAB-A | `frontend/tests/e2e/cursor/subagent-transcript-tab.spec.ts` |
| deepseek-harness | W6-SUBTAB-A | `frontend/tests/e2e/deepseek-harness/subagent-transcript-tab.spec.ts` |
| factory-droid | W6-SUBTAB-A | `frontend/tests/e2e/factory-droid/subagent-transcript-tab.spec.ts` |
| grok-build | W6-SUBTAB-B | `frontend/tests/e2e/grok-build/subagent-transcript-tab.spec.ts` |
| junie | W5-SUBTAB-H | `frontend/tests/e2e/junie/subagent-transcript-tab.spec.ts` |
| kimi-code | W5-SUBTAB-H | `frontend/tests/e2e/kimi-code/subagent-transcript-tab.spec.ts` |
| oh-my-pi | W5-SUBTAB-H | `frontend/tests/e2e/oh-my-pi/subagent-transcript-tab.spec.ts` |
| pi | W6-SUBTAB-B | `frontend/tests/e2e/pi/subagent-transcript-tab.spec.ts` |
| zcode | W6-SUBTAB-B | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` |

### L108: P2 subagent-transcript-tab-B7 unclear / subagent-transcript-tab-B7

Kind: probe. Execution wave: 5, 6.

The spawn card and the spawn result draw no rail of their own

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W6-SUBTAB-A | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` |
| command-code | W6-SUBTAB-A | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` |
| deepseek-harness | W6-SUBTAB-A | `frontend/tests/e2e/deepseek-harness/subagent-transcript-tab.spec.ts` |
| factory-droid | W6-SUBTAB-A | `frontend/tests/e2e/factory-droid/subagent-transcript-tab.spec.ts` |
| gemini-cli | W6-SUBTAB-A | `frontend/tests/e2e/gemini-cli/subagent-transcript-tab.spec.ts` |
| junie | W5-SUBTAB-H | `frontend/tests/e2e/junie/subagent-transcript-tab.spec.ts` |
| letta-code | W6-SUBTAB-B | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` |

### L109: P2 subagent-transcript-tab-P2 / subagent-transcript-tab-B2

Kind: port. Execution wave: 5, 6.

Two children that run at the same time each get a tab with only their own rows

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W6-SUBTAB-A | `frontend/tests/e2e/claude-code/subagent-transcript-tab.spec.ts` |
| codebuddy-code | W6-SUBTAB-A | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` |
| codewhale | W6-SUBTAB-A | `frontend/tests/e2e/codewhale/subagent-transcript-tab.spec.ts` |
| codex | W5-SUBTAB-H | `frontend/tests/e2e/codex/subagent-transcript-tab.spec.ts` |
| command-code | W6-SUBTAB-A | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` |
| cursor | W6-SUBTAB-A | `frontend/tests/e2e/cursor/subagent-transcript-tab.spec.ts` |
| deepseek-harness | W6-SUBTAB-A | `frontend/tests/e2e/deepseek-harness/subagent-transcript-tab.spec.ts` |
| dirac | W5-SUBTAB-H | `frontend/tests/e2e/dirac/subagent-transcript-tab.spec.ts` |
| factory-droid | W6-SUBTAB-A | `frontend/tests/e2e/factory-droid/subagent-transcript-tab.spec.ts` |
| fast-agent | W5-SUBTAB-H | `frontend/tests/e2e/fast-agent/subagent-transcript-tab.spec.ts` |
| gemini-cli | W6-SUBTAB-A | `frontend/tests/e2e/gemini-cli/subagent-transcript-tab.spec.ts` |
| github-copilot | W6-SUBTAB-A | `frontend/tests/e2e/github-copilot/subagent-transcript-tab.spec.ts` |
| goose | W6-SUBTAB-A | `frontend/tests/e2e/goose/subagent-transcript-tab.spec.ts` |
| grok-build | W6-SUBTAB-B | `frontend/tests/e2e/grok-build/subagent-transcript-tab.spec.ts` |
| junie | W5-SUBTAB-H | `frontend/tests/e2e/junie/subagent-transcript-tab.spec.ts` |
| kilo | W6-SUBTAB-B | `frontend/tests/e2e/kilo/subagent-transcript-tab.spec.ts` |
| kimi-code | W5-SUBTAB-H | `frontend/tests/e2e/kimi-code/subagent-transcript-tab.spec.ts` |
| kiro | W6-SUBTAB-B | `frontend/tests/e2e/kiro/subagent-transcript-tab.spec.ts` |
| letta-code | W6-SUBTAB-B | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` |
| oh-my-pi | W5-SUBTAB-H | `frontend/tests/e2e/oh-my-pi/subagent-transcript-tab.spec.ts` |
| opencode | W6-SUBTAB-B | `frontend/tests/e2e/opencode/subagent-transcript-tab.spec.ts` |
| pi | W6-SUBTAB-B | `frontend/tests/e2e/pi/subagent-transcript-tab.spec.ts` |
| qwen-code | W6-SUBTAB-B | `frontend/tests/e2e/qwen-code/subagent-transcript-tab.spec.ts` |
| reasonix | W6-SUBTAB-B | `frontend/tests/e2e/reasonix/subagent-transcript-tab.spec.ts` |
| zcode | W6-SUBTAB-B | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` |

### L110: P2 subagent-transcript-tab-P3 / subagent-transcript-tab-B3

Kind: port. Execution wave: 5.

A child that reuses a native ID after a context clear opens a new tab without the earlier rows

| Target | Requirement group | Complete browser file |
|---|---|---|
| kimi-code | W5-SUBTAB-H | `frontend/tests/e2e/kimi-code/subagent-transcript-tab.spec.ts` |
| oh-my-pi | W5-SUBTAB-H | `frontend/tests/e2e/oh-my-pi/subagent-transcript-tab.spec.ts` |

### L111: P2 subagent-transcript-tab-P5 / subagent-transcript-tab-B5

Kind: port. Execution wave: 5, 6.

After the root session closes and reopens, the completed child tab shows its transcript once

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W6-SUBTAB-A | `frontend/tests/e2e/claude-code/subagent-transcript-tab.spec.ts` |
| dirac | W5-SUBTAB-H | `frontend/tests/e2e/dirac/subagent-transcript-tab.spec.ts` |
| gemini-cli | W6-SUBTAB-A | `frontend/tests/e2e/gemini-cli/subagent-transcript-tab.spec.ts` |
| github-copilot | W6-SUBTAB-A | `frontend/tests/e2e/github-copilot/subagent-transcript-tab.spec.ts` |
| letta-code | W6-SUBTAB-B | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` |
| mimo-code | W6-SUBTAB-B | `frontend/tests/e2e/mimo-code/subagent-transcript-tab.spec.ts` |
| qoder-cli | W6-SUBTAB-B | `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts` |

### L112: P2 subagent-transcript-tab-P6 / subagent-transcript-tab-B7

Kind: port. Execution wave: 5, 6.

The spawn card and the spawn result draw no rail of their own

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W6-SUBTAB-A | `frontend/tests/e2e/cline/subagent-transcript-tab.spec.ts` |
| codewhale | W6-SUBTAB-A | `frontend/tests/e2e/codewhale/subagent-transcript-tab.spec.ts` |
| codex | W5-SUBTAB-H | `frontend/tests/e2e/codex/subagent-transcript-tab.spec.ts` |
| cursor | W6-SUBTAB-A | `frontend/tests/e2e/cursor/subagent-transcript-tab.spec.ts` |
| dirac | W5-SUBTAB-H | `frontend/tests/e2e/dirac/subagent-transcript-tab.spec.ts` |
| fast-agent | W5-SUBTAB-H | `frontend/tests/e2e/fast-agent/subagent-transcript-tab.spec.ts` |
| github-copilot | W6-SUBTAB-A | `frontend/tests/e2e/github-copilot/subagent-transcript-tab.spec.ts` |
| goose | W6-SUBTAB-A | `frontend/tests/e2e/goose/subagent-transcript-tab.spec.ts` |
| grok-build | W6-SUBTAB-B | `frontend/tests/e2e/grok-build/subagent-transcript-tab.spec.ts` |
| kilo | W6-SUBTAB-B | `frontend/tests/e2e/kilo/subagent-transcript-tab.spec.ts` |
| kimi-code | W5-SUBTAB-H | `frontend/tests/e2e/kimi-code/subagent-transcript-tab.spec.ts` |
| kiro | W6-SUBTAB-B | `frontend/tests/e2e/kiro/subagent-transcript-tab.spec.ts` |
| mimo-code | W6-SUBTAB-B | `frontend/tests/e2e/mimo-code/subagent-transcript-tab.spec.ts` |
| oh-my-pi | W5-SUBTAB-H | `frontend/tests/e2e/oh-my-pi/subagent-transcript-tab.spec.ts` |
| opencode | W6-SUBTAB-B | `frontend/tests/e2e/opencode/subagent-transcript-tab.spec.ts` |
| pi | W6-SUBTAB-B | `frontend/tests/e2e/pi/subagent-transcript-tab.spec.ts` |
| qoder-cli | W6-SUBTAB-B | `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts` |
| qwen-code | W6-SUBTAB-B | `frontend/tests/e2e/qwen-code/subagent-transcript-tab.spec.ts` |
| reasonix | W6-SUBTAB-B | `frontend/tests/e2e/reasonix/subagent-transcript-tab.spec.ts` |
| zcode | W6-SUBTAB-B | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` |

### L113: P2 subagent-transcript-tab-P7 / subagent-transcript-tab-B14

Kind: port. Execution wave: 5, 6.

A background subagent gets a tab with its prompt and its rows, and its row closes when the native end notice arrives

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W6-SUBTAB-A | `frontend/tests/e2e/claude-code/subagent-transcript-tab.spec.ts` |
| codebuddy-code | W6-SUBTAB-A | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` |
| command-code | W6-SUBTAB-A | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` |
| cursor | W6-SUBTAB-A | `frontend/tests/e2e/cursor/subagent-transcript-tab.spec.ts` |
| grok-build | W6-SUBTAB-B | `frontend/tests/e2e/grok-build/subagent-transcript-tab.spec.ts` |
| kimi-code | W5-SUBTAB-H | `frontend/tests/e2e/kimi-code/subagent-transcript-tab.spec.ts` |
| letta-code | W6-SUBTAB-B | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` |
| mimo-code | W6-SUBTAB-B | `frontend/tests/e2e/mimo-code/subagent-transcript-tab.spec.ts` |
| oh-my-pi | W5-SUBTAB-H | `frontend/tests/e2e/oh-my-pi/subagent-transcript-tab.spec.ts` |
| pi | W6-SUBTAB-B | `frontend/tests/e2e/pi/subagent-transcript-tab.spec.ts` |
| zcode | W6-SUBTAB-B | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` |

### L114: P2 subagent-transcript-tab-P8 / subagent-transcript-tab-B15

Kind: port. Execution wave: 5, 6.

The parent tab shows the report of the child on the spawn result row

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W6-SUBTAB-A | `frontend/tests/e2e/cline/subagent-transcript-tab.spec.ts` |
| codebuddy-code | W6-SUBTAB-A | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` |
| codex | W5-SUBTAB-H | `frontend/tests/e2e/codex/subagent-transcript-tab.spec.ts` |
| command-code | W6-SUBTAB-A | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` |
| cursor | W6-SUBTAB-A | `frontend/tests/e2e/cursor/subagent-transcript-tab.spec.ts` |
| deepseek-harness | W6-SUBTAB-A | `frontend/tests/e2e/deepseek-harness/subagent-transcript-tab.spec.ts` |
| dirac | W5-SUBTAB-H | `frontend/tests/e2e/dirac/subagent-transcript-tab.spec.ts` |
| factory-droid | W6-SUBTAB-A | `frontend/tests/e2e/factory-droid/subagent-transcript-tab.spec.ts` |
| fast-agent | W5-SUBTAB-H | `frontend/tests/e2e/fast-agent/subagent-transcript-tab.spec.ts` |
| gemini-cli | W6-SUBTAB-A | `frontend/tests/e2e/gemini-cli/subagent-transcript-tab.spec.ts` |
| github-copilot | W6-SUBTAB-A | `frontend/tests/e2e/github-copilot/subagent-transcript-tab.spec.ts` |
| goose | W6-SUBTAB-A | `frontend/tests/e2e/goose/subagent-transcript-tab.spec.ts` |
| junie | W5-SUBTAB-H | `frontend/tests/e2e/junie/subagent-transcript-tab.spec.ts` |
| kilo | W6-SUBTAB-B | `frontend/tests/e2e/kilo/subagent-transcript-tab.spec.ts` |
| kimi-code | W5-SUBTAB-H | `frontend/tests/e2e/kimi-code/subagent-transcript-tab.spec.ts` |
| letta-code | W6-SUBTAB-B | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` |
| oh-my-pi | W5-SUBTAB-H | `frontend/tests/e2e/oh-my-pi/subagent-transcript-tab.spec.ts` |
| opencode | W6-SUBTAB-B | `frontend/tests/e2e/opencode/subagent-transcript-tab.spec.ts` |
| pi | W6-SUBTAB-B | `frontend/tests/e2e/pi/subagent-transcript-tab.spec.ts` |
| qoder-cli | W6-SUBTAB-B | `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts` |
| zcode | W6-SUBTAB-B | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` |

### L115: P3 MI-B1 / MI-B1

Kind: port. Execution wave: 5.

Typed form values (the integer 0, the boolean false, an enum constant) reach the MCP server exactly.

| Target | Requirement group | Complete browser file |
|---|---|---|
| grok-build | W5-MCPINPUT | `frontend/tests/e2e/grok-build/mcp-input-request.spec.ts` |
| kiro | W5-MCPINPUT | `frontend/tests/e2e/kiro/mcp-input-request.spec.ts` |

### L116: P3 MI-B3 / MI-B3

Kind: port. Execution wave: 5.

The typed form answers survive a page reload before the submit, and the restored form submits them.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-MCPINPUT | `frontend/tests/e2e/claude-code/mcp-input-request.spec.ts` |
| codex | W5-MCPINPUT | `frontend/tests/e2e/codex/mcp-input-request.spec.ts` |
| grok-build | W5-MCPINPUT | `frontend/tests/e2e/grok-build/mcp-input-request.spec.ts` |
| kiro | W5-MCPINPUT | `frontend/tests/e2e/kiro/mcp-input-request.spec.ts` |
| qoder-cli | W5-MCPINPUT | `frontend/tests/e2e/qoder-cli/mcp-input-request.spec.ts` |

### L117: P3 MI-B4 / MI-B4

Kind: port. Execution wave: 5.

The native permission request of the MCP tool comes first. The form appears only after Allow.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-MCPINPUT | `frontend/tests/e2e/claude-code/mcp-input-request.spec.ts` |
| github-copilot | W5-MCPINPUT | `frontend/tests/e2e/github-copilot/mcp-input-request.spec.ts` |
| kiro | W5-MCPINPUT | `frontend/tests/e2e/kiro/mcp-input-request.spec.ts` |
| reasonix | W5-MCPINPUT | `frontend/tests/e2e/reasonix/mcp-input-request.spec.ts` |

### L118: P3 MI-B5 / MI-B5

Kind: port. Execution wave: 5.

The control banner shows the MCP server's own form message.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W5-MCPINPUT | `frontend/tests/e2e/claude-code/mcp-input-request.spec.ts` |
| codex | W5-MCPINPUT | `frontend/tests/e2e/codex/mcp-input-request.spec.ts` |
| github-copilot | W5-MCPINPUT | `frontend/tests/e2e/github-copilot/mcp-input-request.spec.ts` |
| goose | W5-MCPINPUT | `frontend/tests/e2e/goose/mcp-input-request.spec.ts` |
| qoder-cli | W5-MCPINPUT | `frontend/tests/e2e/qoder-cli/mcp-input-request.spec.ts` |
| reasonix | W5-MCPINPUT | `frontend/tests/e2e/reasonix/mcp-input-request.spec.ts` |

### L119: P1 AQ1 unclear / AQ1

Kind: probe. Execution wave: 6.

AQ1. One call asks several questions. The banner shows one page for each question, and every answer reaches the native reply.

| Target | Requirement group | Complete browser file |
|---|---|---|
| junie | W6-QUESTIONS-B | `frontend/tests/e2e/junie/agent-questions.spec.ts` |

### L120: P1 AQ14 unclear / AQ14

Kind: probe. Execution wave: 6.

AQ14. The banner shows the description of each option.

| Target | Requirement group | Complete browser file |
|---|---|---|
| junie | W6-QUESTIONS-B | `frontend/tests/e2e/junie/agent-questions.spec.ts` |

### L121: P1 AQ15 unclear / AQ15

Kind: probe. Execution wave: 6.

AQ15. An option preview shows in a region of its own, a code preview renders as code, and the banner does not overflow.

| Target | Requirement group | Complete browser file |
|---|---|---|
| qoder-cli | W6-QUESTIONS-B | `frontend/tests/e2e/qoder-cli/agent-questions.spec.ts` |
| qwen-code | W6-QUESTIONS-B | `frontend/tests/e2e/qwen-code/agent-questions.spec.ts` |

### L122: P2 mcp-tool-execution-B3 unclear / mcp-tool-execution-B3

Kind: probe. Execution wave: 6, 7.

The MCP result row shows the structured content of the result in its Structured block, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W6-MCP | `frontend/tests/e2e/cursor/mcp-tool-execution.spec.ts` |
| fast-agent | W6-MCP | `frontend/tests/e2e/fast-agent/mcp-tool-execution.spec.ts` |
| goose | W6-MCP | `frontend/tests/e2e/goose/mcp-tool-execution.spec.ts` |
| grok-build | W6-MCP | `frontend/tests/e2e/grok-build/mcp-tool-execution.spec.ts` |
| junie | W6-MCP | `frontend/tests/e2e/junie/mcp-tool-execution.spec.ts` |
| kilo | W6-MCP | `frontend/tests/e2e/kilo/mcp-tool-execution.spec.ts` |
| kiro | W6-MCP | `frontend/tests/e2e/kiro/mcp-tool-execution.spec.ts` |
| opencode | W6-MCP | `frontend/tests/e2e/opencode/mcp-tool-execution.spec.ts` |
| qwen-code | W6-MCP | `frontend/tests/e2e/qwen-code/mcp-tool-execution.spec.ts` |
| reasonix | W6-MCP | `frontend/tests/e2e/reasonix/mcp-tool-execution.spec.ts` |
| zcode | W7-IMAGES | `frontend/tests/e2e/zcode/mcp-tool-execution.spec.ts` |

### L123: P2 mcp-tool-execution-P1 / mcp-tool-execution-B1

Kind: port. Execution wave: 6, 7.

The MCP result row shows the returned text, and the row keeps it after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W6-MCP | `frontend/tests/e2e/amp/mcp-tool-execution.spec.ts` |
| claude-code | W6-MCP | `frontend/tests/e2e/claude-code/mcp-tool-execution.spec.ts` |
| cline | W6-MCP | `frontend/tests/e2e/cline/mcp-tool-execution.spec.ts` |
| codebuddy-code | W6-MCP | `frontend/tests/e2e/codebuddy-code/mcp-tool-execution.spec.ts` |
| codewhale | W6-MCP | `frontend/tests/e2e/codewhale/mcp-tool-execution.spec.ts` |
| codex | W6-MCP | `frontend/tests/e2e/codex/mcp-tool-execution.spec.ts` |
| factory-droid | W6-MCP | `frontend/tests/e2e/factory-droid/mcp-tool-execution.spec.ts` |
| fast-agent | W6-MCP | `frontend/tests/e2e/fast-agent/mcp-tool-execution.spec.ts` |
| github-copilot | W6-MCP | `frontend/tests/e2e/github-copilot/mcp-tool-execution.spec.ts` |
| goose | W6-MCP | `frontend/tests/e2e/goose/mcp-tool-execution.spec.ts` |
| grok-build | W6-MCP | `frontend/tests/e2e/grok-build/mcp-tool-execution.spec.ts` |
| junie | W6-MCP | `frontend/tests/e2e/junie/mcp-tool-execution.spec.ts` |
| kilo | W6-MCP | `frontend/tests/e2e/kilo/mcp-tool-execution.spec.ts` |
| kimi-code | W6-MCP | `frontend/tests/e2e/kimi-code/mcp-tool-execution.spec.ts` |
| kiro | W6-MCP | `frontend/tests/e2e/kiro/mcp-tool-execution.spec.ts` |
| letta-code | W6-MCP | `frontend/tests/e2e/letta-code/mcp-tool-execution.spec.ts` |
| mimo-code | W6-MCP | `frontend/tests/e2e/mimo-code/mcp-tool-execution.spec.ts` |
| oh-my-pi | W6-MCP | `frontend/tests/e2e/oh-my-pi/mcp-tool-execution.spec.ts` |
| opencode | W6-MCP | `frontend/tests/e2e/opencode/mcp-tool-execution.spec.ts` |
| qoder-cli | W6-MCP | `frontend/tests/e2e/qoder-cli/mcp-tool-execution.spec.ts` |
| qwen-code | W6-MCP | `frontend/tests/e2e/qwen-code/mcp-tool-execution.spec.ts` |
| reasonix | W6-MCP | `frontend/tests/e2e/reasonix/mcp-tool-execution.spec.ts` |
| zcode | W7-IMAGES | `frontend/tests/e2e/zcode/mcp-tool-execution.spec.ts` |

### L124: P2 mcp-tool-execution-P2 / mcp-tool-execution-B2

Kind: port. Execution wave: 6, 7.

A failed MCP result shows a failed row with its error text, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W6-MCP | `frontend/tests/e2e/amp/mcp-tool-execution.spec.ts` |
| claude-code | W6-MCP | `frontend/tests/e2e/claude-code/mcp-tool-execution.spec.ts` |
| cline | W6-MCP | `frontend/tests/e2e/cline/mcp-tool-execution.spec.ts` |
| codebuddy-code | W6-MCP | `frontend/tests/e2e/codebuddy-code/mcp-tool-execution.spec.ts` |
| codewhale | W6-MCP | `frontend/tests/e2e/codewhale/mcp-tool-execution.spec.ts` |
| codex | W6-MCP | `frontend/tests/e2e/codex/mcp-tool-execution.spec.ts` |
| command-code | W6-MCP | `frontend/tests/e2e/command-code/mcp-tool-execution.spec.ts` |
| cursor | W6-MCP | `frontend/tests/e2e/cursor/mcp-tool-execution.spec.ts` |
| factory-droid | W6-MCP | `frontend/tests/e2e/factory-droid/mcp-tool-execution.spec.ts` |
| fast-agent | W6-MCP | `frontend/tests/e2e/fast-agent/mcp-tool-execution.spec.ts` |
| github-copilot | W6-MCP | `frontend/tests/e2e/github-copilot/mcp-tool-execution.spec.ts` |
| goose | W6-MCP | `frontend/tests/e2e/goose/mcp-tool-execution.spec.ts` |
| grok-build | W6-MCP | `frontend/tests/e2e/grok-build/mcp-tool-execution.spec.ts` |
| junie | W6-MCP | `frontend/tests/e2e/junie/mcp-tool-execution.spec.ts` |
| kilo | W6-MCP | `frontend/tests/e2e/kilo/mcp-tool-execution.spec.ts` |
| kimi-code | W6-MCP | `frontend/tests/e2e/kimi-code/mcp-tool-execution.spec.ts` |
| kiro | W6-MCP | `frontend/tests/e2e/kiro/mcp-tool-execution.spec.ts` |
| letta-code | W6-MCP | `frontend/tests/e2e/letta-code/mcp-tool-execution.spec.ts` |
| mimo-code | W6-MCP | `frontend/tests/e2e/mimo-code/mcp-tool-execution.spec.ts` |
| oh-my-pi | W6-MCP | `frontend/tests/e2e/oh-my-pi/mcp-tool-execution.spec.ts` |
| opencode | W6-MCP | `frontend/tests/e2e/opencode/mcp-tool-execution.spec.ts` |
| qoder-cli | W6-MCP | `frontend/tests/e2e/qoder-cli/mcp-tool-execution.spec.ts` |
| qwen-code | W6-MCP | `frontend/tests/e2e/qwen-code/mcp-tool-execution.spec.ts` |
| reasonix | W6-MCP | `frontend/tests/e2e/reasonix/mcp-tool-execution.spec.ts` |
| zcode | W7-IMAGES | `frontend/tests/e2e/zcode/mcp-tool-execution.spec.ts` |

### L125: P2 mcp-tool-execution-P3 / mcp-tool-execution-B3

Kind: port. Execution wave: 6.

The MCP result row shows the structured content of the result in its Structured block, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W6-MCP | `frontend/tests/e2e/claude-code/mcp-tool-execution.spec.ts` |
| codex | W6-MCP | `frontend/tests/e2e/codex/mcp-tool-execution.spec.ts` |
| github-copilot | W6-MCP | `frontend/tests/e2e/github-copilot/mcp-tool-execution.spec.ts` |

### L126: P2 steer-mid-turn-B1 unclear / steer-mid-turn-B1

Kind: probe. Execution wave: 6.

An attachment that the provider cannot steer keeps the input out of the running turn, and the input goes, with its bytes, in the next turn.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codewhale | W6-STEER | `frontend/tests/e2e/codewhale/steer-mid-turn.spec.ts` |
| dirac | W6-STEER | `frontend/tests/e2e/dirac/steer-mid-turn.spec.ts` |
| grok-build | W6-STEER | `frontend/tests/e2e/grok-build/steer-mid-turn.spec.ts` |

### L127: P2 steer-mid-turn-B2 unclear / steer-mid-turn-B2

Kind: probe. Execution wave: 6.

Steer reaches a turn that the provider started by itself (a session-goal turn).

| Target | Requirement group | Complete browser file |
|---|---|---|
| deepseek-harness | W6-STEER | `frontend/tests/e2e/deepseek-harness/steer-mid-turn.spec.ts` |
| github-copilot | W6-STEER | `frontend/tests/e2e/github-copilot/steer-mid-turn.spec.ts` |
| grok-build | W6-STEER | `frontend/tests/e2e/grok-build/steer-mid-turn.spec.ts` |
| kiro | W6-STEER | `frontend/tests/e2e/kiro/steer-mid-turn.spec.ts` |
| mimo-code | W6-STEER | `frontend/tests/e2e/mimo-code/steer-mid-turn.spec.ts` |
| pi | W6-STEER | `frontend/tests/e2e/pi/steer-mid-turn.spec.ts` |
| qoder-cli | W6-STEER | `frontend/tests/e2e/qoder-cli/steer-mid-turn.spec.ts` |
| qwen-code | W6-STEER | `frontend/tests/e2e/qwen-code/steer-mid-turn.spec.ts` |
| reasonix | W6-STEER | `frontend/tests/e2e/reasonix/steer-mid-turn.spec.ts` |

### L128: P2 steer-mid-turn-P1 / steer-mid-turn-B1

Kind: port. Execution wave: 6.

An attachment that the provider cannot steer keeps the input out of the running turn, and the input goes, with its bytes, in the next turn.

| Target | Requirement group | Complete browser file |
|---|---|---|
| command-code | W6-STEER | `frontend/tests/e2e/command-code/steer-mid-turn.spec.ts` |
| kiro | W6-STEER | `frontend/tests/e2e/kiro/steer-mid-turn.spec.ts` |

### L129: P2 steer-mid-turn-P2 / steer-mid-turn-B2

Kind: port. Execution wave: 6.

Steer reaches a turn that the provider started by itself (a session-goal turn).

| Target | Requirement group | Complete browser file |
|---|---|---|
| codewhale | W6-STEER | `frontend/tests/e2e/codewhale/steer-mid-turn.spec.ts` |

### L130: P2 subagent-transcript-tab-B14 unclear / subagent-transcript-tab-B14

Kind: probe. Execution wave: 6.

A background subagent gets a tab with its prompt and its rows, and its row closes when the native end notice arrives

| Target | Requirement group | Complete browser file |
|---|---|---|
| qoder-cli | W6-SUBTAB-B | `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts` |

### L131: P2 subagent-transcript-tab-P4 / subagent-transcript-tab-B4

Kind: port. Execution wave: 6.

Two children with the same prompt keep separate transcripts when the Worker finds a stored child by its prompt

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W6-SUBTAB-A | `frontend/tests/e2e/cline/subagent-transcript-tab.spec.ts` |

### L132: P3 CA-B2 / CA-B2

Kind: port. Execution wave: 6, 7.

The owned process tree holds an MCP server process that the native agent started, and the close ends that process.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W6-MCP | `frontend/tests/e2e/amp/mcp-tool-execution.spec.ts` |
| claude-code | W6-MCP | `frontend/tests/e2e/claude-code/mcp-tool-execution.spec.ts` |
| cline | W6-MCP | `frontend/tests/e2e/cline/mcp-tool-execution.spec.ts` |
| codebuddy-code | W6-MCP | `frontend/tests/e2e/codebuddy-code/mcp-tool-execution.spec.ts` |
| codex | W6-MCP | `frontend/tests/e2e/codex/mcp-tool-execution.spec.ts` |
| command-code | W6-MCP | `frontend/tests/e2e/command-code/mcp-tool-execution.spec.ts` |
| cursor | W6-MCP | `frontend/tests/e2e/cursor/mcp-tool-execution.spec.ts` |
| deepseek-harness | W6-MCP | `frontend/tests/e2e/deepseek-harness/mcp-tool-execution.spec.ts` |
| factory-droid | W6-MCP | `frontend/tests/e2e/factory-droid/mcp-tool-execution.spec.ts` |
| fast-agent | W6-MCP | `frontend/tests/e2e/fast-agent/mcp-tool-execution.spec.ts` |
| gemini-cli | W6-MCP | `frontend/tests/e2e/gemini-cli/mcp-tool-execution.spec.ts` |
| github-copilot | W6-MCP | `frontend/tests/e2e/github-copilot/mcp-tool-execution.spec.ts` |
| goose | W6-MCP | `frontend/tests/e2e/goose/mcp-tool-execution.spec.ts` |
| grok-build | W6-MCP | `frontend/tests/e2e/grok-build/mcp-tool-execution.spec.ts` |
| junie | W6-MCP | `frontend/tests/e2e/junie/mcp-tool-execution.spec.ts` |
| kilo | W6-MCP | `frontend/tests/e2e/kilo/mcp-tool-execution.spec.ts` |
| kimi-code | W6-MCP | `frontend/tests/e2e/kimi-code/mcp-tool-execution.spec.ts` |
| kiro | W6-MCP | `frontend/tests/e2e/kiro/mcp-tool-execution.spec.ts` |
| letta-code | W6-MCP | `frontend/tests/e2e/letta-code/mcp-tool-execution.spec.ts` |
| mimo-code | W6-MCP | `frontend/tests/e2e/mimo-code/mcp-tool-execution.spec.ts` |
| oh-my-pi | W6-MCP | `frontend/tests/e2e/oh-my-pi/mcp-tool-execution.spec.ts` |
| opencode | W6-MCP | `frontend/tests/e2e/opencode/mcp-tool-execution.spec.ts` |
| qoder-cli | W6-MCP | `frontend/tests/e2e/qoder-cli/mcp-tool-execution.spec.ts` |
| qwen-code | W6-MCP | `frontend/tests/e2e/qwen-code/mcp-tool-execution.spec.ts` |
| reasonix | W6-MCP | `frontend/tests/e2e/reasonix/mcp-tool-execution.spec.ts` |
| zcode | W7-IMAGES | `frontend/tests/e2e/zcode/mcp-tool-execution.spec.ts` |

### L133: P3 SGPR-B2 / SGPR-B2

Kind: port. Execution wave: 6.

Resume starts new native goal work: a new goal round reaches the model (or, for Copilot, the runtime returns its continuation prompt again).

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W6-GOALPAUSE | `frontend/tests/e2e/codex/session-goal-pause-and-resume.spec.ts` |
| zcode | W6-GOALPAUSE | `frontend/tests/e2e/zcode/session-goal-pause-and-resume.spec.ts` |

### L134: P3 SGPR-B3 / SGPR-B3

Kind: port. Execution wave: 6.

A pause that arrives while a native goal round runs takes effect with the provider's own timing (the round is cancelled, or it finishes first), and no further goal round runs until Resume.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W6-GOALPAUSE | `frontend/tests/e2e/codex/session-goal-pause-and-resume.spec.ts` |
| github-copilot | W6-GOALPAUSE | `frontend/tests/e2e/github-copilot/session-goal-pause-and-resume.spec.ts` |
| kilo | W6-GOALPAUSE | `frontend/tests/e2e/kilo/session-goal-pause-and-resume.spec.ts` |
| kimi-code | W6-GOALPAUSE | `frontend/tests/e2e/kimi-code/session-goal-pause-and-resume.spec.ts` |
| zcode | W6-GOALPAUSE | `frontend/tests/e2e/zcode/session-goal-pause-and-resume.spec.ts` |

### L135: P3 SGPR-B3 unclear / SGPR-B3

Kind: probe. Execution wave: 6.

A pause that arrives while a native goal round runs takes effect with the provider's own timing (the round is cancelled, or it finishes first), and no further goal round runs until Resume.

| Target | Requirement group | Complete browser file |
|---|---|---|
| qoder-cli | W6-GOALPAUSE | `frontend/tests/e2e/qoder-cli/session-goal-pause-and-resume.spec.ts` |

### L136: P3 SGPR-B4 unclear / SGPR-B4

Kind: probe. Execution wave: 6.

The proof shows that the reader's Pause, not an automatic pause of the provider, put the goal in the paused state (the Worker holds PAUSED with no status detail, or the card shows no limit reason).

| Target | Requirement group | Complete browser file |
|---|---|---|
| github-copilot | W6-GOALPAUSE | `frontend/tests/e2e/github-copilot/session-goal-pause-and-resume.spec.ts` |
| qoder-cli | W6-GOALPAUSE | `frontend/tests/e2e/qoder-cli/session-goal-pause-and-resume.spec.ts` |

### L137: P3 SGPR-B5 / SGPR-B5

Kind: port. Execution wave: 6.

Pause, reload, and Resume act on one native goal: the Worker snapshot keeps the same native goal ID throughout.

| Target | Requirement group | Complete browser file |
|---|---|---|
| deepseek-harness | W6-GOALPAUSE | `frontend/tests/e2e/deepseek-harness/session-goal-pause-and-resume.spec.ts` |
| github-copilot | W6-GOALPAUSE | `frontend/tests/e2e/github-copilot/session-goal-pause-and-resume.spec.ts` |
| grok-build | W6-GOALPAUSE | `frontend/tests/e2e/grok-build/session-goal-pause-and-resume.spec.ts` |
| kimi-code | W6-GOALPAUSE | `frontend/tests/e2e/kimi-code/session-goal-pause-and-resume.spec.ts` |
| qoder-cli | W6-GOALPAUSE | `frontend/tests/e2e/qoder-cli/session-goal-pause-and-resume.spec.ts` |
| qwen-code | W6-GOALPAUSE | `frontend/tests/e2e/qwen-code/session-goal-pause-and-resume.spec.ts` |
| zcode | W6-GOALPAUSE | `frontend/tests/e2e/zcode/session-goal-pause-and-resume.spec.ts` |

### L138: P1 OP1 / OP1

Kind: port. Execution wave: 7.

OP1. The provider's shell tool proves the path outcome and the preview after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| pi | W7-OUTPATH | `frontend/tests/e2e/pi/output-file-paths.spec.ts` |

### L139: P1 OP1 unclear / OP1

Kind: probe. Execution wave: 7.

OP1. The provider's shell tool proves the path outcome and the preview after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| zcode | W7-OUTPATH | `frontend/tests/e2e/zcode/output-file-paths.spec.ts` |

### L140: P1 OP2 / OP2

Kind: port. Execution wave: 7.

OP2. An MCP tool result keeps its native output path and preview after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-OUTPATH | `frontend/tests/e2e/claude-code/output-file-paths.spec.ts` |
| codebuddy-code | W7-OUTPATH | `frontend/tests/e2e/codebuddy-code/output-file-paths.spec.ts` |

### L141: P1 OP2 unclear / OP2

Kind: probe. Execution wave: 7.

OP2. An MCP tool result keeps its native output path and preview after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| kilo | W7-OUTPATH | `frontend/tests/e2e/kilo/output-file-paths.spec.ts` |
| kiro | W7-OUTPATH | `frontend/tests/e2e/kiro/output-file-paths.spec.ts` |
| letta-code | W7-OUTPATH | `frontend/tests/e2e/letta-code/output-file-paths.spec.ts` |
| mimo-code | W7-OUTPATH | `frontend/tests/e2e/mimo-code/output-file-paths.spec.ts` |
| opencode | W7-OUTPATH | `frontend/tests/e2e/opencode/output-file-paths.spec.ts` |
| qoder-cli | W7-OUTPATH | `frontend/tests/e2e/qoder-cli/output-file-paths.spec.ts` |
| zcode | W7-OUTPATH | `frontend/tests/e2e/zcode/output-file-paths.spec.ts` |

### L142: P1 OP3 / OP3

Kind: port. Execution wave: 7.

OP3. A failed command with a large output keeps its paths, its failure preview and the failed status after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| dirac | W7-OUTPATH | `frontend/tests/e2e/dirac/output-file-paths.spec.ts` |
| grok-build | W7-OUTPATH | `frontend/tests/e2e/grok-build/output-file-paths.spec.ts` |
| kilo | W7-OUTPATH | `frontend/tests/e2e/kilo/output-file-paths.spec.ts` |
| kiro | W7-OUTPATH | `frontend/tests/e2e/kiro/output-file-paths.spec.ts` |
| mimo-code | W7-OUTPATH | `frontend/tests/e2e/mimo-code/output-file-paths.spec.ts` |
| opencode | W7-OUTPATH | `frontend/tests/e2e/opencode/output-file-paths.spec.ts` |

### L143: P1 OP3 unclear / OP3

Kind: probe. Execution wave: 7.

OP3. A failed command with a large output keeps its paths, its failure preview and the failed status after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-OUTPATH | `frontend/tests/e2e/claude-code/output-file-paths.spec.ts` |
| codebuddy-code | W7-OUTPATH | `frontend/tests/e2e/codebuddy-code/output-file-paths.spec.ts` |
| codewhale | W7-OUTPATH | `frontend/tests/e2e/codewhale/output-file-paths.spec.ts` |
| command-code | W7-OUTPATH | `frontend/tests/e2e/command-code/output-file-paths.spec.ts` |
| deepseek-harness | W7-OUTPATH | `frontend/tests/e2e/deepseek-harness/output-file-paths.spec.ts` |
| factory-droid | W7-OUTPATH | `frontend/tests/e2e/factory-droid/output-file-paths.spec.ts` |
| github-copilot | W7-OUTPATH | `frontend/tests/e2e/github-copilot/output-file-paths.spec.ts` |
| kimi-code | W7-OUTPATH | `frontend/tests/e2e/kimi-code/output-file-paths.spec.ts` |
| letta-code | W7-OUTPATH | `frontend/tests/e2e/letta-code/output-file-paths.spec.ts` |
| pi | W7-OUTPATH | `frontend/tests/e2e/pi/output-file-paths.spec.ts` |
| qoder-cli | W7-OUTPATH | `frontend/tests/e2e/qoder-cli/output-file-paths.spec.ts` |
| zcode | W7-OUTPATH | `frontend/tests/e2e/zcode/output-file-paths.spec.ts` |

### L144: P1 OP4 / OP4

Kind: port. Execution wave: 7.

OP4. A subagent's tool row keeps its native output path and preview in the child tab after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-OUTPATH | `frontend/tests/e2e/claude-code/output-file-paths.spec.ts` |
| codebuddy-code | W7-OUTPATH | `frontend/tests/e2e/codebuddy-code/output-file-paths.spec.ts` |
| codewhale | W7-OUTPATH | `frontend/tests/e2e/codewhale/output-file-paths.spec.ts` |
| github-copilot | W7-OUTPATH | `frontend/tests/e2e/github-copilot/output-file-paths.spec.ts` |
| grok-build | W7-OUTPATH | `frontend/tests/e2e/grok-build/output-file-paths.spec.ts` |
| junie | W7-OUTPATH | `frontend/tests/e2e/junie/output-file-paths.spec.ts` |
| kimi-code | W7-OUTPATH | `frontend/tests/e2e/kimi-code/output-file-paths.spec.ts` |
| mimo-code | W7-OUTPATH | `frontend/tests/e2e/mimo-code/output-file-paths.spec.ts` |
| qoder-cli | W7-OUTPATH | `frontend/tests/e2e/qoder-cli/output-file-paths.spec.ts` |
| zcode | W7-OUTPATH | `frontend/tests/e2e/zcode/output-file-paths.spec.ts` |

### L145: P2 generation-progress-B1 unclear / generation-progress-B1

Kind: probe. Execution wave: 7.

While a real shell command streams output, the byte counter advances (or, without a byte path, no counter shows), and the completed result keeps both output segments after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W7-GENPROG | `frontend/tests/e2e/cursor/generation-progress.spec.ts` |
| gemini-cli | W7-GENPROG | `frontend/tests/e2e/gemini-cli/generation-progress.spec.ts` |
| grok-build | W7-GENPROG | `frontend/tests/e2e/grok-build/generation-progress.spec.ts` |
| kilo | W7-GENPROG | `frontend/tests/e2e/kilo/generation-progress.spec.ts` |
| opencode | W7-GENPROG | `frontend/tests/e2e/opencode/generation-progress.spec.ts` |
| qwen-code | W7-GENPROG | `frontend/tests/e2e/qwen-code/generation-progress.spec.ts` |
| reasonix | W7-GENPROG | `frontend/tests/e2e/reasonix/generation-progress.spec.ts` |

### L146: P2 generation-progress-B2 unclear / generation-progress-B2

Kind: probe. Execution wave: 7.

While a shell command runs, its one live tool row shows the current output tail, a later tail replaces the earlier one, no result row shows yet, and the finished call leaves one completed result row.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W7-GENPROG | `frontend/tests/e2e/cursor/generation-progress.spec.ts` |
| gemini-cli | W7-GENPROG | `frontend/tests/e2e/gemini-cli/generation-progress.spec.ts` |
| grok-build | W7-GENPROG | `frontend/tests/e2e/grok-build/generation-progress.spec.ts` |
| kilo | W7-GENPROG | `frontend/tests/e2e/kilo/generation-progress.spec.ts` |
| opencode | W7-GENPROG | `frontend/tests/e2e/opencode/generation-progress.spec.ts` |
| qwen-code | W7-GENPROG | `frontend/tests/e2e/qwen-code/generation-progress.spec.ts` |
| reasonix | W7-GENPROG | `frontend/tests/e2e/reasonix/generation-progress.spec.ts` |

### L147: P2 generation-progress-P1 / generation-progress-B1

Kind: port. Execution wave: 7.

While a real shell command streams output, the byte counter advances (or, without a byte path, no counter shows), and the completed result keeps both output segments after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W7-GENPROG | `frontend/tests/e2e/cline/generation-progress.spec.ts` |
| goose | W7-GENPROG | `frontend/tests/e2e/goose/generation-progress.spec.ts` |
| kimi-code | W7-GENPROG | `frontend/tests/e2e/kimi-code/generation-progress.spec.ts` |
| kiro | W7-GENPROG | `frontend/tests/e2e/kiro/generation-progress.spec.ts` |
| mimo-code | W7-GENPROG | `frontend/tests/e2e/mimo-code/generation-progress.spec.ts` |
| oh-my-pi | W7-GENPROG | `frontend/tests/e2e/oh-my-pi/generation-progress.spec.ts` |
| pi | W7-GENPROG | `frontend/tests/e2e/pi/generation-progress.spec.ts` |
| zcode | W7-GENPROG | `frontend/tests/e2e/zcode/generation-progress.spec.ts` |

### L148: P2 generation-progress-P2 / generation-progress-B2

Kind: port. Execution wave: 7.

While a shell command runs, its one live tool row shows the current output tail, a later tail replaces the earlier one, no result row shows yet, and the finished call leaves one completed result row.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W7-GENPROG | `frontend/tests/e2e/cline/generation-progress.spec.ts` |
| codex | W7-GENPROG | `frontend/tests/e2e/codex/generation-progress.spec.ts` |
| command-code | W7-GENPROG | `frontend/tests/e2e/command-code/generation-progress.spec.ts` |
| github-copilot | W7-GENPROG | `frontend/tests/e2e/github-copilot/generation-progress.spec.ts` |
| goose | W7-GENPROG | `frontend/tests/e2e/goose/generation-progress.spec.ts` |
| junie | W7-GENPROG | `frontend/tests/e2e/junie/generation-progress.spec.ts` |
| kimi-code | W7-GENPROG | `frontend/tests/e2e/kimi-code/generation-progress.spec.ts` |
| kiro | W7-GENPROG | `frontend/tests/e2e/kiro/generation-progress.spec.ts` |
| mimo-code | W7-GENPROG | `frontend/tests/e2e/mimo-code/generation-progress.spec.ts` |
| oh-my-pi | W7-GENPROG | `frontend/tests/e2e/oh-my-pi/generation-progress.spec.ts` |
| pi | W7-GENPROG | `frontend/tests/e2e/pi/generation-progress.spec.ts` |
| zcode | W7-GENPROG | `frontend/tests/e2e/zcode/generation-progress.spec.ts` |

### L149: P2 images-in-tool-results-B3 unclear / images-in-tool-results-B3

Kind: probe. Execution wave: 7.

An image that a native file-read tool returns is drawn in its row.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W7-IMAGES | `frontend/tests/e2e/cursor/images-in-tool-results.spec.ts` |
| fast-agent | W7-IMAGES | `frontend/tests/e2e/fast-agent/images-in-tool-results.spec.ts` |
| zcode | W7-IMAGES | `frontend/tests/e2e/zcode/images-in-tool-results.spec.ts` |

### L150: P2 images-in-tool-results-P1 / images-in-tool-results-B1

Kind: port. Execution wave: 7.

The tool-result image stays drawn after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W7-IMAGES | `frontend/tests/e2e/amp/images-in-tool-results.spec.ts` |
| claude-code | W7-IMAGES | `frontend/tests/e2e/claude-code/images-in-tool-results.spec.ts` |
| codex | W7-IMAGES | `frontend/tests/e2e/codex/images-in-tool-results.spec.ts` |
| factory-droid | W7-IMAGES | `frontend/tests/e2e/factory-droid/images-in-tool-results.spec.ts` |
| fast-agent | W7-IMAGES | `frontend/tests/e2e/fast-agent/images-in-tool-results.spec.ts` |
| github-copilot | W7-IMAGES | `frontend/tests/e2e/github-copilot/images-in-tool-results.spec.ts` |
| goose | W7-IMAGES | `frontend/tests/e2e/goose/images-in-tool-results.spec.ts` |
| grok-build | W7-IMAGES | `frontend/tests/e2e/grok-build/images-in-tool-results.spec.ts` |
| kilo | W7-IMAGES | `frontend/tests/e2e/kilo/images-in-tool-results.spec.ts` |
| kimi-code | W7-IMAGES | `frontend/tests/e2e/kimi-code/images-in-tool-results.spec.ts` |
| kiro | W7-IMAGES | `frontend/tests/e2e/kiro/images-in-tool-results.spec.ts` |
| mimo-code | W7-IMAGES | `frontend/tests/e2e/mimo-code/images-in-tool-results.spec.ts` |
| oh-my-pi | W7-IMAGES | `frontend/tests/e2e/oh-my-pi/images-in-tool-results.spec.ts` |
| opencode | W7-IMAGES | `frontend/tests/e2e/opencode/images-in-tool-results.spec.ts` |
| qoder-cli | W7-IMAGES | `frontend/tests/e2e/qoder-cli/images-in-tool-results.spec.ts` |
| zcode | W7-IMAGES | `frontend/tests/e2e/zcode/images-in-tool-results.spec.ts` |

### L151: P2 images-in-tool-results-P1 / images-in-tool-results-B4

Kind: port. Execution wave: 7.

The row draws the image and does not print its base64 text.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W7-IMAGES | `frontend/tests/e2e/amp/images-in-tool-results.spec.ts` |
| claude-code | W7-IMAGES | `frontend/tests/e2e/claude-code/images-in-tool-results.spec.ts` |
| codewhale | W7-IMAGES | `frontend/tests/e2e/codewhale/images-in-tool-results.spec.ts` |
| codex | W7-IMAGES | `frontend/tests/e2e/codex/images-in-tool-results.spec.ts` |
| command-code | W7-IMAGES | `frontend/tests/e2e/command-code/images-in-tool-results.spec.ts` |
| cursor | W7-IMAGES | `frontend/tests/e2e/cursor/images-in-tool-results.spec.ts` |
| deepseek-harness | W7-IMAGES | `frontend/tests/e2e/deepseek-harness/images-in-tool-results.spec.ts` |
| fast-agent | W7-IMAGES | `frontend/tests/e2e/fast-agent/images-in-tool-results.spec.ts` |
| gemini-cli | W7-IMAGES | `frontend/tests/e2e/gemini-cli/images-in-tool-results.spec.ts` |
| github-copilot | W7-IMAGES | `frontend/tests/e2e/github-copilot/images-in-tool-results.spec.ts` |
| goose | W7-IMAGES | `frontend/tests/e2e/goose/images-in-tool-results.spec.ts` |
| grok-build | W7-IMAGES | `frontend/tests/e2e/grok-build/images-in-tool-results.spec.ts` |
| kilo | W7-IMAGES | `frontend/tests/e2e/kilo/images-in-tool-results.spec.ts` |
| kimi-code | W7-IMAGES | `frontend/tests/e2e/kimi-code/images-in-tool-results.spec.ts` |
| kiro | W7-IMAGES | `frontend/tests/e2e/kiro/images-in-tool-results.spec.ts` |
| mimo-code | W7-IMAGES | `frontend/tests/e2e/mimo-code/images-in-tool-results.spec.ts` |
| oh-my-pi | W7-IMAGES | `frontend/tests/e2e/oh-my-pi/images-in-tool-results.spec.ts` |
| opencode | W7-IMAGES | `frontend/tests/e2e/opencode/images-in-tool-results.spec.ts` |
| pi | W7-IMAGES | `frontend/tests/e2e/pi/images-in-tool-results.spec.ts` |
| qoder-cli | W7-IMAGES | `frontend/tests/e2e/qoder-cli/images-in-tool-results.spec.ts` |
| zcode | W7-IMAGES | `frontend/tests/e2e/zcode/images-in-tool-results.spec.ts` |

### L152: P2 images-in-tool-results-P2 / images-in-tool-results-B2

Kind: port. Execution wave: 7.

An image that an MCP tool returns is drawn in the MCP result row.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-IMAGES | `frontend/tests/e2e/claude-code/images-in-tool-results.spec.ts` |
| codex | W7-IMAGES | `frontend/tests/e2e/codex/images-in-tool-results.spec.ts` |
| command-code | W7-IMAGES | `frontend/tests/e2e/command-code/images-in-tool-results.spec.ts` |
| cursor | W7-IMAGES | `frontend/tests/e2e/cursor/images-in-tool-results.spec.ts` |
| deepseek-harness | W7-IMAGES | `frontend/tests/e2e/deepseek-harness/images-in-tool-results.spec.ts` |
| factory-droid | W7-IMAGES | `frontend/tests/e2e/factory-droid/images-in-tool-results.spec.ts` |
| gemini-cli | W7-IMAGES | `frontend/tests/e2e/gemini-cli/images-in-tool-results.spec.ts` |
| github-copilot | W7-IMAGES | `frontend/tests/e2e/github-copilot/images-in-tool-results.spec.ts` |
| goose | W7-IMAGES | `frontend/tests/e2e/goose/images-in-tool-results.spec.ts` |
| kilo | W7-IMAGES | `frontend/tests/e2e/kilo/images-in-tool-results.spec.ts` |
| kimi-code | W7-IMAGES | `frontend/tests/e2e/kimi-code/images-in-tool-results.spec.ts` |
| mimo-code | W7-IMAGES | `frontend/tests/e2e/mimo-code/images-in-tool-results.spec.ts` |
| oh-my-pi | W7-IMAGES | `frontend/tests/e2e/oh-my-pi/images-in-tool-results.spec.ts` |
| opencode | W7-IMAGES | `frontend/tests/e2e/opencode/images-in-tool-results.spec.ts` |
| qoder-cli | W7-IMAGES | `frontend/tests/e2e/qoder-cli/images-in-tool-results.spec.ts` |

### L153: P2 manual-compaction-P1 / manual-compaction-B1

Kind: port. Execution wave: 7.

A manual compaction that does not succeed shows its native error or nothing, and never a completed notice.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-COMPACTFAIL | `frontend/tests/e2e/claude-code/manual-compaction.spec.ts` |
| codebuddy-code | W7-COMPACTFAIL | `frontend/tests/e2e/codebuddy-code/manual-compaction.spec.ts` |
| codewhale | W7-COMPACTFAIL | `frontend/tests/e2e/codewhale/manual-compaction.spec.ts` |
| codex | W7-COMPACTFAIL | `frontend/tests/e2e/codex/manual-compaction.spec.ts` |
| command-code | W7-COMPACTFAIL | `frontend/tests/e2e/command-code/manual-compaction.spec.ts` |
| dirac | W7-COMPACTFAIL | `frontend/tests/e2e/dirac/manual-compaction.spec.ts` |
| factory-droid | W7-COMPACTFAIL | `frontend/tests/e2e/factory-droid/manual-compaction.spec.ts` |
| github-copilot | W7-COMPACTFAIL | `frontend/tests/e2e/github-copilot/manual-compaction.spec.ts` |
| goose | W7-COMPACTFAIL | `frontend/tests/e2e/goose/manual-compaction.spec.ts` |
| grok-build | W7-COMPACTFAIL | `frontend/tests/e2e/grok-build/manual-compaction.spec.ts` |
| kilo | W7-COMPACTFAIL | `frontend/tests/e2e/kilo/manual-compaction.spec.ts` |
| kimi-code | W7-COMPACTFAIL | `frontend/tests/e2e/kimi-code/manual-compaction.spec.ts` |
| kiro | W7-COMPACTFAIL | `frontend/tests/e2e/kiro/manual-compaction.spec.ts` |
| mimo-code | W7-COMPACTFAIL | `frontend/tests/e2e/mimo-code/manual-compaction.spec.ts` |
| oh-my-pi | W7-COMPACTFAIL | `frontend/tests/e2e/oh-my-pi/manual-compaction.spec.ts` |
| opencode | W7-COMPACTFAIL | `frontend/tests/e2e/opencode/manual-compaction.spec.ts` |
| qwen-code | W7-COMPACTFAIL | `frontend/tests/e2e/qwen-code/manual-compaction.spec.ts` |
| zcode | W7-COMPACTFAIL | `frontend/tests/e2e/zcode/manual-compaction.spec.ts` |

### L154: P3 CN-B4 / CN-B4

Kind: port. Execution wave: 7, 8.

A native compaction that fails or that the agent refuses shows its own status or error and no completed notice, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W8-COMPACTNOTE | `frontend/tests/e2e/cline/compaction-notice.spec.ts` |
| command-code | W7-COMPACTFAIL | `frontend/tests/e2e/command-code/manual-compaction.spec.ts` |
| github-copilot | W7-COMPACTFAIL | `frontend/tests/e2e/github-copilot/manual-compaction.spec.ts` |
| oh-my-pi | W7-COMPACTFAIL | `frontend/tests/e2e/oh-my-pi/manual-compaction.spec.ts` |
| qoder-cli | W7-COMPACTFAIL | `frontend/tests/e2e/qoder-cli/manual-compaction.spec.ts` |
| zcode | W7-COMPACTFAIL | `frontend/tests/e2e/zcode/manual-compaction.spec.ts` |

### L155: P3 CN-B4 unclear / CN-B4

Kind: probe. Execution wave: 7.

A native compaction that fails or that the agent refuses shows its own status or error and no completed notice, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-COMPACTFAIL | `frontend/tests/e2e/claude-code/manual-compaction.spec.ts` |
| codewhale | W7-COMPACTFAIL | `frontend/tests/e2e/codewhale/manual-compaction.spec.ts` |
| codex | W7-COMPACTFAIL | `frontend/tests/e2e/codex/manual-compaction.spec.ts` |
| deepseek-harness | W7-COMPACTFAIL | `frontend/tests/e2e/deepseek-harness/manual-compaction.spec.ts` |
| dirac | W7-COMPACTFAIL | `frontend/tests/e2e/dirac/manual-compaction.spec.ts` |
| factory-droid | W7-COMPACTFAIL | `frontend/tests/e2e/factory-droid/manual-compaction.spec.ts` |
| goose | W7-COMPACTFAIL | `frontend/tests/e2e/goose/manual-compaction.spec.ts` |
| kimi-code | W7-COMPACTFAIL | `frontend/tests/e2e/kimi-code/manual-compaction.spec.ts` |
| mimo-code | W7-COMPACTFAIL | `frontend/tests/e2e/mimo-code/manual-compaction.spec.ts` |

### L156: P3 CN-B5 unclear / CN-B5

Kind: probe. Execution wave: 7.

The next request proves the native history policy: a provider that replaces the whole history drops even the newer seed answer.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-COMPACTFAIL | `frontend/tests/e2e/claude-code/manual-compaction.spec.ts` |
| codewhale | W7-COMPACTFAIL | `frontend/tests/e2e/codewhale/manual-compaction.spec.ts` |
| deepseek-harness | W7-COMPACTFAIL | `frontend/tests/e2e/deepseek-harness/manual-compaction.spec.ts` |
| dirac | W7-COMPACTFAIL | `frontend/tests/e2e/dirac/manual-compaction.spec.ts` |
| factory-droid | W7-COMPACTFAIL | `frontend/tests/e2e/factory-droid/manual-compaction.spec.ts` |
| goose | W7-COMPACTFAIL | `frontend/tests/e2e/goose/manual-compaction.spec.ts` |
| kimi-code | W7-COMPACTFAIL | `frontend/tests/e2e/kimi-code/manual-compaction.spec.ts` |
| mimo-code | W7-COMPACTFAIL | `frontend/tests/e2e/mimo-code/manual-compaction.spec.ts` |
| oh-my-pi | W7-COMPACTFAIL | `frontend/tests/e2e/oh-my-pi/manual-compaction.spec.ts` |
| pi | W7-COMPACTFAIL | `frontend/tests/e2e/pi/manual-compaction.spec.ts` |
| qoder-cli | W7-COMPACTFAIL | `frontend/tests/e2e/qoder-cli/manual-compaction.spec.ts` |
| zcode | W7-COMPACTFAIL | `frontend/tests/e2e/zcode/manual-compaction.spec.ts` |

### L157: P3 CU-B1 / CU-B1

Kind: port. Execution wave: 7.

The Context row comes back after a page reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W7-CONTEXT | `frontend/tests/e2e/codex/context-usage.spec.ts` |
| github-copilot | W7-CONTEXT | `frontend/tests/e2e/github-copilot/context-usage.spec.ts` |
| oh-my-pi | W7-CONTEXT | `frontend/tests/e2e/oh-my-pi/context-usage.spec.ts` |
| pi | W7-CONTEXT | `frontend/tests/e2e/pi/context-usage.spec.ts` |
| zcode | W7-CONTEXT | `frontend/tests/e2e/zcode/context-usage.spec.ts` |

### L158: P3 CU-B2 / CU-B2

Kind: port. Execution wave: 7.

A turn that reports zero tokens shows an explicit 0 in the Context row, live and after reload, and not an absent reading.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W7-CONTEXT | `frontend/tests/e2e/amp/context-usage.spec.ts` |
| claude-code | W7-CONTEXT | `frontend/tests/e2e/claude-code/context-usage.spec.ts` |
| cline | W7-CONTEXT | `frontend/tests/e2e/cline/context-usage.spec.ts` |
| codebuddy-code | W7-CONTEXT | `frontend/tests/e2e/codebuddy-code/context-usage.spec.ts` |
| codex | W7-CONTEXT | `frontend/tests/e2e/codex/context-usage.spec.ts` |
| command-code | W7-CONTEXT | `frontend/tests/e2e/command-code/context-usage.spec.ts` |
| fast-agent | W7-CONTEXT | `frontend/tests/e2e/fast-agent/context-usage.spec.ts` |
| gemini-cli | W7-CONTEXT | `frontend/tests/e2e/gemini-cli/context-usage.spec.ts` |
| grok-build | W7-CONTEXT | `frontend/tests/e2e/grok-build/context-usage.spec.ts` |
| letta-code | W7-CONTEXT | `frontend/tests/e2e/letta-code/context-usage.spec.ts` |
| mimo-code | W7-CONTEXT | `frontend/tests/e2e/mimo-code/context-usage.spec.ts` |
| pi | W7-CONTEXT | `frontend/tests/e2e/pi/context-usage.spec.ts` |
| qwen-code | W7-CONTEXT | `frontend/tests/e2e/qwen-code/context-usage.spec.ts` |
| zcode | W7-CONTEXT | `frontend/tests/e2e/zcode/context-usage.spec.ts` |

### L159: P3 CU-B2 unclear / CU-B2

Kind: probe. Execution wave: 7.

A turn that reports zero tokens shows an explicit 0 in the Context row, live and after reload, and not an absent reading.

| Target | Requirement group | Complete browser file |
|---|---|---|
| dirac | W7-CONTEXT | `frontend/tests/e2e/dirac/context-usage.spec.ts` |
| goose | W7-CONTEXT | `frontend/tests/e2e/goose/context-usage.spec.ts` |
| junie | W7-CONTEXT | `frontend/tests/e2e/junie/context-usage.spec.ts` |
| kilo | W7-CONTEXT | `frontend/tests/e2e/kilo/context-usage.spec.ts` |
| opencode | W7-CONTEXT | `frontend/tests/e2e/opencode/context-usage.spec.ts` |
| reasonix | W7-CONTEXT | `frontend/tests/e2e/reasonix/context-usage.spec.ts` |

### L160: P3 CU-B3 / CU-B3

Kind: port. Execution wave: 7.

The Context row states the context window that the provider reports, not the 200k default.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-CONTEXT | `frontend/tests/e2e/claude-code/context-usage.spec.ts` |
| cline | W7-CONTEXT | `frontend/tests/e2e/cline/context-usage.spec.ts` |
| codebuddy-code | W7-CONTEXT | `frontend/tests/e2e/codebuddy-code/context-usage.spec.ts` |
| codex | W7-CONTEXT | `frontend/tests/e2e/codex/context-usage.spec.ts` |
| command-code | W7-CONTEXT | `frontend/tests/e2e/command-code/context-usage.spec.ts` |
| dirac | W7-CONTEXT | `frontend/tests/e2e/dirac/context-usage.spec.ts` |
| goose | W7-CONTEXT | `frontend/tests/e2e/goose/context-usage.spec.ts` |
| grok-build | W7-CONTEXT | `frontend/tests/e2e/grok-build/context-usage.spec.ts` |
| junie | W7-CONTEXT | `frontend/tests/e2e/junie/context-usage.spec.ts` |
| kilo | W7-CONTEXT | `frontend/tests/e2e/kilo/context-usage.spec.ts` |
| kimi-code | W7-CONTEXT | `frontend/tests/e2e/kimi-code/context-usage.spec.ts` |
| mimo-code | W7-CONTEXT | `frontend/tests/e2e/mimo-code/context-usage.spec.ts` |
| opencode | W7-CONTEXT | `frontend/tests/e2e/opencode/context-usage.spec.ts` |
| pi | W7-CONTEXT | `frontend/tests/e2e/pi/context-usage.spec.ts` |
| qwen-code | W7-CONTEXT | `frontend/tests/e2e/qwen-code/context-usage.spec.ts` |
| reasonix | W7-CONTEXT | `frontend/tests/e2e/reasonix/context-usage.spec.ts` |
| zcode | W7-CONTEXT | `frontend/tests/e2e/zcode/context-usage.spec.ts` |

### L161: P3 TD-B2 / TD-B2

Kind: port. Execution wave: 7.

A later native write changes the status of items that the sidebar already shows.

| Target | Requirement group | Complete browser file |
|---|---|---|
| oh-my-pi | W7-TODO | `frontend/tests/e2e/oh-my-pi/to-do-sidebar.spec.ts` |

### L162: P3 TD-B3 / TD-B3

Kind: port. Execution wave: 7.

An incremental native operation (a create or an update of one item by its ID, or a merge) changes one item and keeps the others.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-TODO | `frontend/tests/e2e/claude-code/to-do-sidebar.spec.ts` |
| cursor | W7-TODO | `frontend/tests/e2e/cursor/to-do-sidebar.spec.ts` |

### L163: P3 TD-B4 / TD-B4

Kind: port. Execution wave: 7.

An empty native list, a clear action, or a list that the native tool removes empties the sidebar, and the empty state survives a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cursor | W7-TODO | `frontend/tests/e2e/cursor/to-do-sidebar.spec.ts` |
| kilo | W7-TODO | `frontend/tests/e2e/kilo/to-do-sidebar.spec.ts` |
| kimi-code | W7-TODO | `frontend/tests/e2e/kimi-code/to-do-sidebar.spec.ts` |
| opencode | W7-TODO | `frontend/tests/e2e/opencode/to-do-sidebar.spec.ts` |
| zcode | W7-TODO | `frontend/tests/e2e/zcode/to-do-sidebar.spec.ts` |

### L164: P3 TD-B4 unclear / TD-B4

Kind: probe. Execution wave: 7.

An empty native list, a clear action, or a list that the native tool removes empties the sidebar, and the empty state survives a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-TODO | `frontend/tests/e2e/claude-code/to-do-sidebar.spec.ts` |
| codewhale | W7-TODO | `frontend/tests/e2e/codewhale/to-do-sidebar.spec.ts` |
| codex | W7-TODO | `frontend/tests/e2e/codex/to-do-sidebar.spec.ts` |
| command-code | W7-TODO | `frontend/tests/e2e/command-code/to-do-sidebar.spec.ts` |
| deepseek-harness | W7-TODO | `frontend/tests/e2e/deepseek-harness/to-do-sidebar.spec.ts` |
| dirac | W7-TODO | `frontend/tests/e2e/dirac/to-do-sidebar.spec.ts` |
| factory-droid | W7-TODO | `frontend/tests/e2e/factory-droid/to-do-sidebar.spec.ts` |
| github-copilot | W7-TODO | `frontend/tests/e2e/github-copilot/to-do-sidebar.spec.ts` |
| goose | W7-TODO | `frontend/tests/e2e/goose/to-do-sidebar.spec.ts` |
| grok-build | W7-TODO | `frontend/tests/e2e/grok-build/to-do-sidebar.spec.ts` |
| junie | W7-TODO | `frontend/tests/e2e/junie/to-do-sidebar.spec.ts` |
| kiro | W7-TODO | `frontend/tests/e2e/kiro/to-do-sidebar.spec.ts` |
| letta-code | W7-TODO | `frontend/tests/e2e/letta-code/to-do-sidebar.spec.ts` |
| mimo-code | W7-TODO | `frontend/tests/e2e/mimo-code/to-do-sidebar.spec.ts` |
| oh-my-pi | W7-TODO | `frontend/tests/e2e/oh-my-pi/to-do-sidebar.spec.ts` |
| qoder-cli | W7-TODO | `frontend/tests/e2e/qoder-cli/to-do-sidebar.spec.ts` |
| qwen-code | W7-TODO | `frontend/tests/e2e/qwen-code/to-do-sidebar.spec.ts` |
| reasonix | W7-TODO | `frontend/tests/e2e/reasonix/to-do-sidebar.spec.ts` |

### L165: P3 TD-B5 / TD-B5

Kind: port. Execution wave: 7.

Each native status word beyond pending, in progress, and completed (cancelled, deleted, abandoned, blocked) reaches the sidebar as its own checkbox state.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-TODO | `frontend/tests/e2e/claude-code/to-do-sidebar.spec.ts` |
| codebuddy-code | W7-TODO | `frontend/tests/e2e/codebuddy-code/to-do-sidebar.spec.ts` |
| command-code | W7-TODO | `frontend/tests/e2e/command-code/to-do-sidebar.spec.ts` |
| cursor | W7-TODO | `frontend/tests/e2e/cursor/to-do-sidebar.spec.ts` |
| kilo | W7-TODO | `frontend/tests/e2e/kilo/to-do-sidebar.spec.ts` |
| letta-code | W7-TODO | `frontend/tests/e2e/letta-code/to-do-sidebar.spec.ts` |
| mimo-code | W7-TODO | `frontend/tests/e2e/mimo-code/to-do-sidebar.spec.ts` |
| oh-my-pi | W7-TODO | `frontend/tests/e2e/oh-my-pi/to-do-sidebar.spec.ts` |
| opencode | W7-TODO | `frontend/tests/e2e/opencode/to-do-sidebar.spec.ts` |
| pi | W7-TODO | `frontend/tests/e2e/pi/to-do-sidebar.spec.ts` |
| qoder-cli | W7-TODO | `frontend/tests/e2e/qoder-cli/to-do-sidebar.spec.ts` |

### L166: P3 TD-B5 unclear / TD-B5

Kind: probe. Execution wave: 7.

Each native status word beyond pending, in progress, and completed (cancelled, deleted, abandoned, blocked) reaches the sidebar as its own checkbox state.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codewhale | W7-TODO | `frontend/tests/e2e/codewhale/to-do-sidebar.spec.ts` |
| factory-droid | W7-TODO | `frontend/tests/e2e/factory-droid/to-do-sidebar.spec.ts` |

### L167: P3 TD-B6 / TD-B6

Kind: port. Execution wave: 7.

The transcript draws the native to-do call as a to-do row that holds the list (for example "3 tasks").

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W7-TODO | `frontend/tests/e2e/codebuddy-code/to-do-sidebar.spec.ts` |
| codewhale | W7-TODO | `frontend/tests/e2e/codewhale/to-do-sidebar.spec.ts` |
| codex | W7-TODO | `frontend/tests/e2e/codex/to-do-sidebar.spec.ts` |
| command-code | W7-TODO | `frontend/tests/e2e/command-code/to-do-sidebar.spec.ts` |
| cursor | W7-TODO | `frontend/tests/e2e/cursor/to-do-sidebar.spec.ts` |
| deepseek-harness | W7-TODO | `frontend/tests/e2e/deepseek-harness/to-do-sidebar.spec.ts` |
| dirac | W7-TODO | `frontend/tests/e2e/dirac/to-do-sidebar.spec.ts` |
| factory-droid | W7-TODO | `frontend/tests/e2e/factory-droid/to-do-sidebar.spec.ts` |
| gemini-cli | W7-TODO | `frontend/tests/e2e/gemini-cli/to-do-sidebar.spec.ts` |
| github-copilot | W7-TODO | `frontend/tests/e2e/github-copilot/to-do-sidebar.spec.ts` |
| goose | W7-TODO | `frontend/tests/e2e/goose/to-do-sidebar.spec.ts` |
| grok-build | W7-TODO | `frontend/tests/e2e/grok-build/to-do-sidebar.spec.ts` |
| junie | W7-TODO | `frontend/tests/e2e/junie/to-do-sidebar.spec.ts` |
| kilo | W7-TODO | `frontend/tests/e2e/kilo/to-do-sidebar.spec.ts` |
| letta-code | W7-TODO | `frontend/tests/e2e/letta-code/to-do-sidebar.spec.ts` |
| oh-my-pi | W7-TODO | `frontend/tests/e2e/oh-my-pi/to-do-sidebar.spec.ts` |
| opencode | W7-TODO | `frontend/tests/e2e/opencode/to-do-sidebar.spec.ts` |
| pi | W7-TODO | `frontend/tests/e2e/pi/to-do-sidebar.spec.ts` |
| qoder-cli | W7-TODO | `frontend/tests/e2e/qoder-cli/to-do-sidebar.spec.ts` |
| qwen-code | W7-TODO | `frontend/tests/e2e/qwen-code/to-do-sidebar.spec.ts` |
| reasonix | W7-TODO | `frontend/tests/e2e/reasonix/to-do-sidebar.spec.ts` |
| zcode | W7-TODO | `frontend/tests/e2e/zcode/to-do-sidebar.spec.ts` |

### L168: P3 TD-B6 unclear / TD-B6

Kind: probe. Execution wave: 7.

The transcript draws the native to-do call as a to-do row that holds the list (for example "3 tasks").

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W7-TODO | `frontend/tests/e2e/claude-code/to-do-sidebar.spec.ts` |

### L169: P1 I4 unclear / I4

Kind: probe. Execution wave: 8.

I4. A root interrupt ends the root turn while a spawned child keeps running, and the child's sidebar row stays `running`.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-ROOTCANCEL | `frontend/tests/e2e/claude-code/interrupt-a-turn.spec.ts` |
| codebuddy-code | W8-ROOTCANCEL | `frontend/tests/e2e/codebuddy-code/interrupt-a-turn.spec.ts` |
| codewhale | W8-ROOTCANCEL | `frontend/tests/e2e/codewhale/interrupt-a-turn.spec.ts` |
| command-code | W8-ROOTCANCEL | `frontend/tests/e2e/command-code/interrupt-a-turn.spec.ts` |
| cursor | W8-ROOTCANCEL | `frontend/tests/e2e/cursor/interrupt-a-turn.spec.ts` |
| deepseek-harness | W8-ROOTCANCEL | `frontend/tests/e2e/deepseek-harness/interrupt-a-turn.spec.ts` |
| factory-droid | W8-ROOTCANCEL | `frontend/tests/e2e/factory-droid/interrupt-a-turn.spec.ts` |
| fast-agent | W8-ROOTCANCEL | `frontend/tests/e2e/fast-agent/interrupt-a-turn.spec.ts` |
| gemini-cli | W8-ROOTCANCEL | `frontend/tests/e2e/gemini-cli/interrupt-a-turn.spec.ts` |
| github-copilot | W8-ROOTCANCEL | `frontend/tests/e2e/github-copilot/interrupt-a-turn.spec.ts` |
| goose | W8-ROOTCANCEL | `frontend/tests/e2e/goose/interrupt-a-turn.spec.ts` |
| grok-build | W8-ROOTCANCEL | `frontend/tests/e2e/grok-build/interrupt-a-turn.spec.ts` |
| junie | W8-ROOTCANCEL | `frontend/tests/e2e/junie/interrupt-a-turn.spec.ts` |
| kilo | W8-ROOTCANCEL | `frontend/tests/e2e/kilo/interrupt-a-turn.spec.ts` |
| kimi-code | W8-ROOTCANCEL | `frontend/tests/e2e/kimi-code/interrupt-a-turn.spec.ts` |
| kiro | W8-ROOTCANCEL | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts` |
| letta-code | W8-ROOTCANCEL | `frontend/tests/e2e/letta-code/interrupt-a-turn.spec.ts` |
| mimo-code | W8-ROOTCANCEL | `frontend/tests/e2e/mimo-code/interrupt-a-turn.spec.ts` |
| oh-my-pi | W8-ROOTCANCEL | `frontend/tests/e2e/oh-my-pi/interrupt-a-turn.spec.ts` |
| opencode | W8-ROOTCANCEL | `frontend/tests/e2e/opencode/interrupt-a-turn.spec.ts` |
| pi | W8-ROOTCANCEL | `frontend/tests/e2e/pi/interrupt-a-turn.spec.ts` |
| qoder-cli | W8-ROOTCANCEL | `frontend/tests/e2e/qoder-cli/interrupt-a-turn.spec.ts` |
| qwen-code | W8-ROOTCANCEL | `frontend/tests/e2e/qwen-code/interrupt-a-turn.spec.ts` |
| zcode | W8-ROOTCANCEL | `frontend/tests/e2e/zcode/interrupt-a-turn.spec.ts` |

### L170: P1 T-P1 / WT1

Kind: port. Execution wave: 8.

WT1. While a real native permission runs, every native control frame classifies through the provider's own control reader as a non-dialog control. No banner text and no permission title offers a workspace-trust decision.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W8-SMALL | `frontend/tests/e2e/codebuddy-code/workspace-trust.spec.ts` |
| cursor | W8-SMALL | `frontend/tests/e2e/cursor/workspace-trust.spec.ts` |
| deepseek-harness | W8-SMALL | `frontend/tests/e2e/deepseek-harness/workspace-trust.spec.ts` |
| dirac | W8-SMALL | `frontend/tests/e2e/dirac/workspace-trust.spec.ts` |
| factory-droid | W8-SMALL | `frontend/tests/e2e/factory-droid/workspace-trust.spec.ts` |
| fast-agent | W8-SMALL | `frontend/tests/e2e/fast-agent/workspace-trust.spec.ts` |
| gemini-cli | W8-SMALL | `frontend/tests/e2e/gemini-cli/workspace-trust.spec.ts` |
| github-copilot | W8-SMALL | `frontend/tests/e2e/github-copilot/workspace-trust.spec.ts` |
| goose | W8-SMALL | `frontend/tests/e2e/goose/workspace-trust.spec.ts` |
| junie | W8-SMALL | `frontend/tests/e2e/junie/workspace-trust.spec.ts` |
| kilo | W8-SMALL | `frontend/tests/e2e/kilo/workspace-trust.spec.ts` |
| letta-code | W8-SMALL | `frontend/tests/e2e/letta-code/workspace-trust.spec.ts` |
| opencode | W8-SMALL | `frontend/tests/e2e/opencode/workspace-trust.spec.ts` |
| qoder-cli | W8-SMALL | `frontend/tests/e2e/qoder-cli/workspace-trust.spec.ts` |
| reasonix | W8-SMALL | `frontend/tests/e2e/reasonix/workspace-trust.spec.ts` |
| zcode | W8-SMALL | `frontend/tests/e2e/zcode/workspace-trust.spec.ts` |

### L171: P2 code-execution-B2 unclear / code-execution-B2

Kind: probe. Execution wave: 8.

A script that calls another tool shows the computed result of that nested call in its row, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| grok-build | W8-CODEEXEC | `frontend/tests/e2e/grok-build/code-execution.spec.ts` |
| qoder-cli | W8-CODEEXEC | `frontend/tests/e2e/qoder-cli/code-execution.spec.ts` |
| zcode | W8-CODEEXEC | `frontend/tests/e2e/zcode/code-execution.spec.ts` |

### L172: P2 code-execution-P1 / code-execution-B1

Kind: port. Execution wave: 8.

A completed script with empty output shows a completed (not failed) result, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W8-CODEEXEC | `frontend/tests/e2e/codebuddy-code/code-execution.spec.ts` |
| codewhale | W8-CODEEXEC | `frontend/tests/e2e/codewhale/code-execution.spec.ts` |
| deepseek-harness | W8-CODEEXEC | `frontend/tests/e2e/deepseek-harness/code-execution.spec.ts` |
| factory-droid | W8-CODEEXEC | `frontend/tests/e2e/factory-droid/code-execution.spec.ts` |
| goose | W8-CODEEXEC | `frontend/tests/e2e/goose/code-execution.spec.ts` |
| kilo | W8-CODEEXEC | `frontend/tests/e2e/kilo/code-execution.spec.ts` |
| mimo-code | W8-CODEEXEC | `frontend/tests/e2e/mimo-code/code-execution.spec.ts` |
| oh-my-pi | W8-CODEEXEC | `frontend/tests/e2e/oh-my-pi/code-execution.spec.ts` |
| opencode | W8-CODEEXEC | `frontend/tests/e2e/opencode/code-execution.spec.ts` |
| qwen-code | W8-CODEEXEC | `frontend/tests/e2e/qwen-code/code-execution.spec.ts` |

### L173: P2 code-execution-P2 / code-execution-B1

Kind: port. Execution wave: 8.

A completed script with empty output shows a completed (not failed) result, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-CODEEXEC | `frontend/tests/e2e/claude-code/code-execution.spec.ts` |
| qoder-cli | W8-CODEEXEC | `frontend/tests/e2e/qoder-cli/code-execution.spec.ts` |
| grok-build | W8-CODEEXEC | `frontend/tests/e2e/grok-build/code-execution.spec.ts` |
| zcode | W8-CODEEXEC | `frontend/tests/e2e/zcode/code-execution.spec.ts` |

### L174: P2 code-execution-P3 / code-execution-B2

Kind: port. Execution wave: 8.

A script that calls another tool shows the computed result of that nested call in its row, also after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W8-CODEEXEC | `frontend/tests/e2e/codebuddy-code/code-execution.spec.ts` |
| codewhale | W8-CODEEXEC | `frontend/tests/e2e/codewhale/code-execution.spec.ts` |
| codex | W8-CODEEXEC | `frontend/tests/e2e/codex/code-execution.spec.ts` |
| factory-droid | W8-CODEEXEC | `frontend/tests/e2e/factory-droid/code-execution.spec.ts` |
| goose | W8-CODEEXEC | `frontend/tests/e2e/goose/code-execution.spec.ts` |
| mimo-code | W8-CODEEXEC | `frontend/tests/e2e/mimo-code/code-execution.spec.ts` |
| oh-my-pi | W8-CODEEXEC | `frontend/tests/e2e/oh-my-pi/code-execution.spec.ts` |
| qwen-code | W8-CODEEXEC | `frontend/tests/e2e/qwen-code/code-execution.spec.ts` |

### L175: P3 CN-B1 / CN-B1

Kind: port. Execution wave: 8.

The completed notice survives a page reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| cline | W8-COMPACTNOTE | `frontend/tests/e2e/cline/compaction-notice.spec.ts` |

### L176: P3 CN-B2 / CN-B2

Kind: port. Execution wave: 8.

An automatic native compaction (a threshold or a context overflow) draws the completed notice, and the next request drops the compacted content.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-COMPACTNOTE | `frontend/tests/e2e/claude-code/compaction-notice.spec.ts` |
| codewhale | W8-COMPACTNOTE | `frontend/tests/e2e/codewhale/compaction-notice.spec.ts` |
| command-code | W8-COMPACTNOTE | `frontend/tests/e2e/command-code/compaction-notice.spec.ts` |
| github-copilot | W8-COMPACTNOTE | `frontend/tests/e2e/github-copilot/compaction-notice.spec.ts` |
| mimo-code | W8-COMPACTNOTE | `frontend/tests/e2e/mimo-code/compaction-notice.spec.ts` |
| oh-my-pi | W8-COMPACTNOTE | `frontend/tests/e2e/oh-my-pi/compaction-notice.spec.ts` |
| pi | W8-COMPACTNOTE | `frontend/tests/e2e/pi/compaction-notice.spec.ts` |

### L177: P3 CN-B2 unclear / CN-B2

Kind: probe. Execution wave: 8.

An automatic native compaction (a threshold or a context overflow) draws the completed notice, and the next request drops the compacted content.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W8-COMPACTNOTE | `frontend/tests/e2e/codex/compaction-notice.spec.ts` |
| deepseek-harness | W8-COMPACTNOTE | `frontend/tests/e2e/deepseek-harness/compaction-notice.spec.ts` |
| dirac | W8-COMPACTNOTE | `frontend/tests/e2e/dirac/compaction-notice.spec.ts` |
| factory-droid | W8-COMPACTNOTE | `frontend/tests/e2e/factory-droid/compaction-notice.spec.ts` |
| goose | W8-COMPACTNOTE | `frontend/tests/e2e/goose/compaction-notice.spec.ts` |
| kimi-code | W8-COMPACTNOTE | `frontend/tests/e2e/kimi-code/compaction-notice.spec.ts` |
| qoder-cli | W8-COMPACTNOTE | `frontend/tests/e2e/qoder-cli/compaction-notice.spec.ts` |
| zcode | W8-COMPACTNOTE | `frontend/tests/e2e/zcode/compaction-notice.spec.ts` |

### L178: P3 CN-B3 / CN-B3

Kind: port. Execution wave: 8.

The notice states the detail that the native event carries: the trigger (manual or automatic), the token counts, or both.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-COMPACTNOTE | `frontend/tests/e2e/claude-code/compaction-notice.spec.ts` |
| cline | W8-COMPACTNOTE | `frontend/tests/e2e/cline/compaction-notice.spec.ts` |
| command-code | W8-COMPACTNOTE | `frontend/tests/e2e/command-code/compaction-notice.spec.ts` |
| github-copilot | W8-COMPACTNOTE | `frontend/tests/e2e/github-copilot/compaction-notice.spec.ts` |
| kimi-code | W8-COMPACTNOTE | `frontend/tests/e2e/kimi-code/compaction-notice.spec.ts` |
| mimo-code | W8-COMPACTNOTE | `frontend/tests/e2e/mimo-code/compaction-notice.spec.ts` |
| oh-my-pi | W8-COMPACTNOTE | `frontend/tests/e2e/oh-my-pi/compaction-notice.spec.ts` |
| pi | W8-COMPACTNOTE | `frontend/tests/e2e/pi/compaction-notice.spec.ts` |
| qoder-cli | W8-COMPACTNOTE | `frontend/tests/e2e/qoder-cli/compaction-notice.spec.ts` |

### L179: P3 CN-B3 unclear / CN-B3

Kind: probe. Execution wave: 8.

The notice states the detail that the native event carries: the trigger (manual or automatic), the token counts, or both.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W8-COMPACTNOTE | `frontend/tests/e2e/codex/compaction-notice.spec.ts` |

### L180: P3 CN-B6 / CN-B6

Kind: port. Execution wave: 8.

The transcript draws no raw or unrendered row for the native compaction frames.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-COMPACTNOTE | `frontend/tests/e2e/claude-code/compaction-notice.spec.ts` |
| cline | W8-COMPACTNOTE | `frontend/tests/e2e/cline/compaction-notice.spec.ts` |
| codewhale | W8-COMPACTNOTE | `frontend/tests/e2e/codewhale/compaction-notice.spec.ts` |
| codex | W8-COMPACTNOTE | `frontend/tests/e2e/codex/compaction-notice.spec.ts` |
| command-code | W8-COMPACTNOTE | `frontend/tests/e2e/command-code/compaction-notice.spec.ts` |
| deepseek-harness | W8-COMPACTNOTE | `frontend/tests/e2e/deepseek-harness/compaction-notice.spec.ts` |
| dirac | W8-COMPACTNOTE | `frontend/tests/e2e/dirac/compaction-notice.spec.ts` |
| github-copilot | W8-COMPACTNOTE | `frontend/tests/e2e/github-copilot/compaction-notice.spec.ts` |
| goose | W8-COMPACTNOTE | `frontend/tests/e2e/goose/compaction-notice.spec.ts` |
| kimi-code | W8-COMPACTNOTE | `frontend/tests/e2e/kimi-code/compaction-notice.spec.ts` |
| mimo-code | W8-COMPACTNOTE | `frontend/tests/e2e/mimo-code/compaction-notice.spec.ts` |
| oh-my-pi | W8-COMPACTNOTE | `frontend/tests/e2e/oh-my-pi/compaction-notice.spec.ts` |
| pi | W8-COMPACTNOTE | `frontend/tests/e2e/pi/compaction-notice.spec.ts` |
| qoder-cli | W8-COMPACTNOTE | `frontend/tests/e2e/qoder-cli/compaction-notice.spec.ts` |
| zcode | W8-COMPACTNOTE | `frontend/tests/e2e/zcode/compaction-notice.spec.ts` |

### L181: P3 CI-B1 unclear / CI-B1

Kind: probe. Execution wave: 8.

During a native turn, a call of the native CLI to the OS credential store reaches the private refusing stub (`security` on macOS), not the user's keychain.

| Target | Requirement group | Complete browser file |
|---|---|---|
| amp | W8-SMALL | `frontend/tests/e2e/amp/credential-isolation.spec.ts` |
| claude-code | W8-SMALL | `frontend/tests/e2e/claude-code/credential-isolation.spec.ts` |
| cline | W8-SMALL | `frontend/tests/e2e/cline/credential-isolation.spec.ts` |
| codebuddy-code | W8-SMALL | `frontend/tests/e2e/codebuddy-code/credential-isolation.spec.ts` |
| codewhale | W8-SMALL | `frontend/tests/e2e/codewhale/credential-isolation.spec.ts` |
| codex | W8-SMALL | `frontend/tests/e2e/codex/credential-isolation.spec.ts` |
| command-code | W8-SMALL | `frontend/tests/e2e/command-code/credential-isolation.spec.ts` |
| deepseek-harness | W8-SMALL | `frontend/tests/e2e/deepseek-harness/credential-isolation.spec.ts` |
| dirac | W8-SMALL | `frontend/tests/e2e/dirac/credential-isolation.spec.ts` |
| factory-droid | W8-SMALL | `frontend/tests/e2e/factory-droid/credential-isolation.spec.ts` |
| fast-agent | W8-SMALL | `frontend/tests/e2e/fast-agent/credential-isolation.spec.ts` |
| github-copilot | W8-SMALL | `frontend/tests/e2e/github-copilot/credential-isolation.spec.ts` |
| goose | W8-SMALL | `frontend/tests/e2e/goose/credential-isolation.spec.ts` |
| grok-build | W8-SMALL | `frontend/tests/e2e/grok-build/credential-isolation.spec.ts` |
| kilo | W8-SMALL | `frontend/tests/e2e/kilo/credential-isolation.spec.ts` |
| kimi-code | W8-SMALL | `frontend/tests/e2e/kimi-code/credential-isolation.spec.ts` |
| kiro | W8-SMALL | `frontend/tests/e2e/kiro/credential-isolation.spec.ts` |
| letta-code | W8-SMALL | `frontend/tests/e2e/letta-code/credential-isolation.spec.ts` |
| mimo-code | W8-SMALL | `frontend/tests/e2e/mimo-code/credential-isolation.spec.ts` |
| oh-my-pi | W8-SMALL | `frontend/tests/e2e/oh-my-pi/credential-isolation.spec.ts` |
| opencode | W8-SMALL | `frontend/tests/e2e/opencode/credential-isolation.spec.ts` |
| pi | W8-SMALL | `frontend/tests/e2e/pi/credential-isolation.spec.ts` |
| qoder-cli | W8-SMALL | `frontend/tests/e2e/qoder-cli/credential-isolation.spec.ts` |
| qwen-code | W8-SMALL | `frontend/tests/e2e/qwen-code/credential-isolation.spec.ts` |
| reasonix | W8-SMALL | `frontend/tests/e2e/reasonix/credential-isolation.spec.ts` |
| zcode | W8-SMALL | `frontend/tests/e2e/zcode/credential-isolation.spec.ts` |

### L182: P3 ET-B1 / ET-B1

Kind: port. Execution wave: 8.

The thinking option group follows the selected model without a reload: its labels or its presence change when the model changes.

| Target | Requirement group | Complete browser file |
|---|---|---|
| kiro | W8-SMALL | `frontend/tests/e2e/kiro/extended-thinking.spec.ts` |

### L183: P3 ET-B1 unclear / ET-B1

Kind: probe. Execution wave: 8.

The thinking option group follows the selected model without a reload: its labels or its presence change when the model changes.

| Target | Requirement group | Complete browser file |
|---|---|---|
| dirac | W8-SMALL | `frontend/tests/e2e/dirac/extended-thinking.spec.ts` |

### L184: P3 FM-B1 / FM-B1

Kind: port. Execution wave: 8.

The Fast state survives a page reload: after the reload, the menu still shows Fast and the next native request still carries the fast setting.

| Target | Requirement group | Complete browser file |
|---|---|---|
| dirac | W8-SMALL | `frontend/tests/e2e/dirac/fast-mode.spec.ts` |

### L185: P3 FM-B2 / FM-B2

Kind: port. Execution wave: 8.

A fast change leaves the other selected settings unchanged: the next native requests keep the same model, and the Worker keeps the same effort.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-SMALL | `frontend/tests/e2e/claude-code/fast-mode.spec.ts` |
| codex | W8-SMALL | `frontend/tests/e2e/codex/fast-mode.spec.ts` |

### L186: P3 IS-B1 / IS-B1

Kind: port. Execution wave: 8.

After the stop, the child transcript shows no end divider and no thinking indicator, live and after a page reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W8-CHILDOPS | `frontend/tests/e2e/codex/interrupt-a-subagent.spec.ts` |
| deepseek-harness | W8-CHILDOPS | `frontend/tests/e2e/deepseek-harness/interrupt-a-subagent.spec.ts` |

### L187: P3 IS-B2 / IS-B2

Kind: port. Execution wave: 8.

After the stop, no model request asks for the child's turn again.

| Target | Requirement group | Complete browser file |
|---|---|---|
| deepseek-harness | W8-CHILDOPS | `frontend/tests/e2e/deepseek-harness/interrupt-a-subagent.spec.ts` |

### L188: P3 OB-B3 / OB-B3

Kind: port. Execution wave: 8.

The native request carries the binary file once: the current user turn holds exactly one typed part of that file.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codebuddy-code | W8-SMALL | `frontend/tests/e2e/codebuddy-code/other-binary-attachments.spec.ts` |

### L189: P3 PM-B1 / PM-B1

Kind: port. Execution wave: 8.

After a page reload, the settings menu still shows Plan mode, and the next native turn still runs in Plan mode.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-PLANMODE | `frontend/tests/e2e/claude-code/plan-mode.spec.ts` |
| dirac | W8-PLANMODE | `frontend/tests/e2e/dirac/plan-mode.spec.ts` |
| gemini-cli | W8-PLANMODE | `frontend/tests/e2e/gemini-cli/plan-mode.spec.ts` |

### L190: P3 PM-B2 / PM-B2

Kind: port. Execution wave: 8.

The user leaves Plan mode for the execution mode in LeapMux, and the next native turn runs without the plan state (the refused action runs, or the plan tools or plan instructions are gone).

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-PLANMODE | `frontend/tests/e2e/claude-code/plan-mode.spec.ts` |
| codebuddy-code | W8-PLANMODE | `frontend/tests/e2e/codebuddy-code/plan-mode.spec.ts` |
| command-code | W8-PLANMODE | `frontend/tests/e2e/command-code/plan-mode.spec.ts` |
| cursor | W8-PLANMODE | `frontend/tests/e2e/cursor/plan-mode.spec.ts` |
| deepseek-harness | W8-PLANMODE | `frontend/tests/e2e/deepseek-harness/plan-mode.spec.ts` |
| dirac | W8-PLANMODE | `frontend/tests/e2e/dirac/plan-mode.spec.ts` |
| factory-droid | W8-PLANMODE | `frontend/tests/e2e/factory-droid/plan-mode.spec.ts` |
| gemini-cli | W8-PLANMODE | `frontend/tests/e2e/gemini-cli/plan-mode.spec.ts` |
| github-copilot | W8-PLANMODE | `frontend/tests/e2e/github-copilot/plan-mode.spec.ts` |
| junie | W8-PLANMODE | `frontend/tests/e2e/junie/plan-mode.spec.ts` |
| kilo | W8-PLANMODE | `frontend/tests/e2e/kilo/plan-mode.spec.ts` |
| kimi-code | W8-PLANMODE | `frontend/tests/e2e/kimi-code/plan-mode.spec.ts` |
| kiro | W8-PLANMODE | `frontend/tests/e2e/kiro/plan-mode.spec.ts` |
| mimo-code | W8-PLANMODE | `frontend/tests/e2e/mimo-code/plan-mode.spec.ts` |
| opencode | W8-PLANMODE | `frontend/tests/e2e/opencode/plan-mode.spec.ts` |
| qoder-cli | W8-PLANMODE | `frontend/tests/e2e/qoder-cli/plan-mode.spec.ts` |
| qwen-code | W8-PLANMODE | `frontend/tests/e2e/qwen-code/plan-mode.spec.ts` |

### L191: P3 PM-B3 / PM-B3

Kind: port. Execution wave: 8.

Shift+Tab in the composer enters Plan mode, and a second Shift+Tab returns to the previous mode.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-PLANMODE | `frontend/tests/e2e/claude-code/plan-mode.spec.ts` |
| codex | W8-PLANMODE | `frontend/tests/e2e/codex/plan-mode.spec.ts` |
| command-code | W8-PLANMODE | `frontend/tests/e2e/command-code/plan-mode.spec.ts` |
| cursor | W8-PLANMODE | `frontend/tests/e2e/cursor/plan-mode.spec.ts` |
| deepseek-harness | W8-PLANMODE | `frontend/tests/e2e/deepseek-harness/plan-mode.spec.ts` |
| dirac | W8-PLANMODE | `frontend/tests/e2e/dirac/plan-mode.spec.ts` |
| gemini-cli | W8-PLANMODE | `frontend/tests/e2e/gemini-cli/plan-mode.spec.ts` |
| github-copilot | W8-PLANMODE | `frontend/tests/e2e/github-copilot/plan-mode.spec.ts` |
| grok-build | W8-PLANMODE | `frontend/tests/e2e/grok-build/plan-mode.spec.ts` |
| junie | W8-PLANMODE | `frontend/tests/e2e/junie/plan-mode.spec.ts` |
| kilo | W8-PLANMODE | `frontend/tests/e2e/kilo/plan-mode.spec.ts` |
| kimi-code | W8-PLANMODE | `frontend/tests/e2e/kimi-code/plan-mode.spec.ts` |
| kiro | W8-PLANMODE | `frontend/tests/e2e/kiro/plan-mode.spec.ts` |
| mimo-code | W8-PLANMODE | `frontend/tests/e2e/mimo-code/plan-mode.spec.ts` |
| opencode | W8-PLANMODE | `frontend/tests/e2e/opencode/plan-mode.spec.ts` |
| qwen-code | W8-PLANMODE | `frontend/tests/e2e/qwen-code/plan-mode.spec.ts` |
| reasonix | W8-PLANMODE | `frontend/tests/e2e/reasonix/plan-mode.spec.ts` |
| zcode | W8-PLANMODE | `frontend/tests/e2e/zcode/plan-mode.spec.ts` |

### L192: P3 PM-B4 / PM-B4

Kind: port. Execution wave: 8.

A change into Plan mode keeps the other selected settings (model or effort) in the native request and after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-PLANMODE | `frontend/tests/e2e/claude-code/plan-mode.spec.ts` |
| cline | W8-PLANMODE | `frontend/tests/e2e/cline/plan-mode.spec.ts` |
| codex | W8-PLANMODE | `frontend/tests/e2e/codex/plan-mode.spec.ts` |
| command-code | W8-PLANMODE | `frontend/tests/e2e/command-code/plan-mode.spec.ts` |
| deepseek-harness | W8-PLANMODE | `frontend/tests/e2e/deepseek-harness/plan-mode.spec.ts` |
| factory-droid | W8-PLANMODE | `frontend/tests/e2e/factory-droid/plan-mode.spec.ts` |
| gemini-cli | W8-PLANMODE | `frontend/tests/e2e/gemini-cli/plan-mode.spec.ts` |
| junie | W8-PLANMODE | `frontend/tests/e2e/junie/plan-mode.spec.ts` |
| kimi-code | W8-PLANMODE | `frontend/tests/e2e/kimi-code/plan-mode.spec.ts` |
| qwen-code | W8-PLANMODE | `frontend/tests/e2e/qwen-code/plan-mode.spec.ts` |
| zcode | W8-PLANMODE | `frontend/tests/e2e/zcode/plan-mode.spec.ts` |

### L193: P3 RL-B1 / RL-B1

Kind: port. Execution wave: 8.

The reported quota window stays on the agent info card after a reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W8-SMALL | `frontend/tests/e2e/codex/rate-limit-state.spec.ts` |

### L194: P3 RL-B2 / RL-B2

Kind: port. Execution wave: 8.

A second window type (the weekly window) shows under its own label.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-SMALL | `frontend/tests/e2e/claude-code/rate-limit-state.spec.ts` |
| github-copilot | W8-SMALL | `frontend/tests/e2e/github-copilot/rate-limit-state.spec.ts` |

### L195: P3 RL-B3 / RL-B3

Kind: port. Execution wave: 8.

A later report of explicit zero use replaces a near-limit window, live and after reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| codex | W8-SMALL | `frontend/tests/e2e/codex/rate-limit-state.spec.ts` |

### L196: P3 RL-B3 unclear / RL-B3

Kind: probe. Execution wave: 8.

A later report of explicit zero use replaces a near-limit window, live and after reload.

| Target | Requirement group | Complete browser file |
|---|---|---|
| claude-code | W8-SMALL | `frontend/tests/e2e/claude-code/rate-limit-state.spec.ts` |

### L197: P3 SS-B2 / SS-B2

Kind: port. Execution wave: 8.

The child's next turn carries the child's own earlier conversation.

| Target | Requirement group | Complete browser file |
|---|---|---|
| kimi-code | W8-CHILDOPS | `frontend/tests/e2e/kimi-code/send-to-a-subagent.spec.ts` |

### L198: P3 SS-B3 / SS-B3

Kind: port. Execution wave: 8.

The child's next turn does not carry the parent's own conversation.

| Target | Requirement group | Complete browser file |
|---|---|---|
| deepseek-harness | W8-CHILDOPS | `frontend/tests/e2e/deepseek-harness/send-to-a-subagent.spec.ts` |
| kimi-code | W8-CHILDOPS | `frontend/tests/e2e/kimi-code/send-to-a-subagent.spec.ts` |

### L199: P3 SS-B4 / SS-B4

Kind: port. Execution wave: 8.

A message queued while the child runs, then steered, joins the child's running turn.

| Target | Requirement group | Complete browser file |
|---|---|---|
| deepseek-harness | W8-CHILDOPS | `frontend/tests/e2e/deepseek-harness/send-to-a-subagent.spec.ts` |
| factory-droid | W8-CHILDOPS | `frontend/tests/e2e/factory-droid/send-to-a-subagent.spec.ts` |

## Final product gap audit

### G01: P1 G1 / PAB-5 gap

- Execution group: W4-PLANFILE.
- Receipt state: pending.
- Final evidence: Call the provider-owned UpdatePlan path with the native heading and plan file. Prove tab auto-title, retained manual rename, reload, and Plan File row where the native request has a file.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| cursor | W4-PLANFILE | `frontend/tests/e2e/cursor/plan-approval-banner.spec.ts` | pending |
| github-copilot | W4-PLANFILE | `frontend/tests/e2e/github-copilot/plan-approval-banner.spec.ts` | pending |
| pi | W4-PLANFILE | `frontend/tests/e2e/pi/plan-approval-banner.spec.ts` | pending |
| zcode | W4-PLANFILE | `frontend/tests/e2e/zcode/plan-approval-banner.spec.ts` | pending |
| codebuddy-code | W4-PLANFILE | `frontend/tests/e2e/codebuddy-code/plan-approval-banner.spec.ts` | pending |
| junie | W4-PLANFILE | `frontend/tests/e2e/junie/plan-approval-banner.spec.ts` | pending |
| qoder-cli | W4-PLANFILE | `frontend/tests/e2e/qoder-cli/plan-approval-banner.spec.ts` | pending |
| factory-droid | W4-PLANFILE | `frontend/tests/e2e/factory-droid/plan-approval-banner.spec.ts` | pending |

### G02: P1 G2 native

- Execution group: W2-DECLINE-NATIVE.
- Receipt state: pending.
- Final evidence: Recognize exact native refusal in the provider plugin. Prove Declined and the native reason live and after reload. Preserve ordinary execution failures and interruptions.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| claude-code | W2-DECLINE-NATIVE | `frontend/tests/e2e/claude-code/permission-prompts.spec.ts` | pending |
| codebuddy-code | W2-DECLINE-NATIVE | `frontend/tests/e2e/codebuddy-code/permission-prompts.spec.ts` | pending |
| codewhale | W2-DECLINE-NATIVE | `frontend/tests/e2e/codewhale/permission-prompts.spec.ts` | pending |
| deepseek-harness | W2-DECLINE-NATIVE | `frontend/tests/e2e/deepseek-harness/permission-prompts.spec.ts` | pending |
| factory-droid | W2-DECLINE-NATIVE | `frontend/tests/e2e/factory-droid/permission-prompts.spec.ts` | pending |
| kimi-code | W2-DECLINE-NATIVE | `frontend/tests/e2e/kimi-code/permission-prompts.spec.ts` | pending |
| letta-code | W2-DECLINE-NATIVE | `frontend/tests/e2e/letta-code/permission-prompts.spec.ts` | pending |
| oh-my-pi | W2-DECLINE-NATIVE | `frontend/tests/e2e/oh-my-pi/permission-prompts.spec.ts` | pending |
| qoder-cli | W2-DECLINE-NATIVE | `frontend/tests/e2e/qoder-cli/permission-prompts.spec.ts` | pending |
| zcode | W2-DECLINE-NATIVE | `frontend/tests/e2e/zcode/permission-prompts.spec.ts` | pending |

### G03: P1 G2 ACP

- Execution group: W2-DECLINE-ACP.
- Receipt state: pending.
- Final evidence: Recognize the native refused result around each complete adapter build. Use a shared rule only for a proved neutral field. Preserve failed command output and partial content.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| dirac | W2-DECLINE-ACP | `frontend/tests/e2e/dirac/permission-prompts.spec.ts` | pending |
| goose | W2-DECLINE-ACP | `frontend/tests/e2e/goose/permission-prompts.spec.ts` | pending |
| grok-build | W2-DECLINE-ACP | `frontend/tests/e2e/grok-build/permission-prompts.spec.ts` | pending |
| junie | W2-DECLINE-ACP | `frontend/tests/e2e/junie/permission-prompts.spec.ts` | pending |
| kiro | W2-DECLINE-ACP | `frontend/tests/e2e/kiro/permission-prompts.spec.ts` | pending |
| reasonix | W2-DECLINE-ACP | `frontend/tests/e2e/reasonix/permission-prompts.spec.ts` | pending |

### G04: P1 G3 / I3 / IT-2

- Execution group: W4-INTMARK.
- Receipt state: pending.
- Final evidence: Correct native partial-answer completion at the provider boundary. Require the interruption marker live and after reload. A locally sent interrupt alone cannot override native normal completion.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| cursor | W4-INTMARK | `frontend/tests/e2e/cursor/interrupt-a-turn.spec.ts` | pending |
| kilo | W4-INTMARK | `frontend/tests/e2e/kilo/interrupt-a-turn.spec.ts` | pending |
| opencode | W4-INTMARK | `frontend/tests/e2e/opencode/interrupt-a-turn.spec.ts` | pending |
| goose | W4-INTMARK | `frontend/tests/e2e/goose/interrupt-a-turn.spec.ts` | pending |
| reasonix | W4-INTMARK | `frontend/tests/e2e/reasonix/interrupt-a-turn.spec.ts` | pending |
| qwen-code | W4-INTMARK | `frontend/tests/e2e/qwen-code/interrupt-a-turn.spec.ts` | pending |
| grok-build | W4-INTMARK | `frontend/tests/e2e/grok-build/interrupt-a-turn.spec.ts` | pending |
| kiro | W4-INTMARK | `frontend/tests/e2e/kiro/interrupt-a-turn.spec.ts` | pending |
| dirac | W4-INTMARK | `frontend/tests/e2e/dirac/interrupt-a-turn.spec.ts` | pending |
| fast-agent | W4-INTMARK | `frontend/tests/e2e/fast-agent/interrupt-a-turn.spec.ts` | pending |
| gemini-cli | W4-INTMARK | `frontend/tests/e2e/gemini-cli/interrupt-a-turn.spec.ts` | pending |
| junie | W4-INTMARK | `frontend/tests/e2e/junie/interrupt-a-turn.spec.ts` | pending |
| factory-droid | W4-INTMARK | `frontend/tests/e2e/factory-droid/interrupt-a-turn.spec.ts` | pending |
| letta-code | W4-INTMARK | `frontend/tests/e2e/letta-code/interrupt-a-turn.spec.ts` | pending |
| qoder-cli | W4-INTMARK | `frontend/tests/e2e/qoder-cli/interrupt-a-turn.spec.ts` | pending |

### G05: P1 G4

- Execution group: W2-DECLINE-NATIVE.
- Receipt state: pending.
- Final evidence: Expose only the offered native scope options. Return the exact selected native reply. Prove a covered second real call raises no banner at any time and restore private rule files.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| claude-code | W2-DECLINE-NATIVE | `frontend/tests/e2e/claude-code/permission-prompts.spec.ts` | pending |
| zcode | W2-DECLINE-NATIVE | `frontend/tests/e2e/zcode/permission-prompts.spec.ts` | pending |
| qoder-cli | W2-DECLINE-NATIVE | `frontend/tests/e2e/qoder-cli/permission-prompts.spec.ts` | pending |
| factory-droid | W2-DECLINE-NATIVE | `frontend/tests/e2e/factory-droid/permission-prompts.spec.ts` | pending |

### G06: P1 G5

- Execution group: W1-SHELL.
- Receipt state: pending.
- Final evidence: Parse the native Process exited trailer into the command result. Remove only that proved trailer. Require the native error exit in the heading and actual output in the body.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| factory-droid | W1-SHELL | `frontend/tests/e2e/factory-droid/shell-tool-execution.spec.ts` | pending |

### G07: P1 G6 / PP-9 guard

- Execution group: W4-GUARD.
- Receipt state: pending.
- Final evidence: Probe native project settings for every candidate. Implement one shared Worker guard and one provider-owned detector per applicable provider. Move Amp onto that guard. Require refusal before native process/turn launch.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| amp | W4-GUARD | `frontend/tests/e2e/amp/permission-prompts.spec.ts` | pending |
| claude-code | W4-GUARD | `frontend/tests/e2e/claude-code/permission-prompts.spec.ts` | pending |
| cline | W4-GUARD | `frontend/tests/e2e/cline/permission-prompts.spec.ts` | pending |
| codebuddy-code | W4-GUARD | `frontend/tests/e2e/codebuddy-code/permission-prompts.spec.ts` | pending |
| codewhale | W4-GUARD | `frontend/tests/e2e/codewhale/permission-prompts.spec.ts` | pending |
| codex | W4-GUARD | `frontend/tests/e2e/codex/permission-prompts.spec.ts` | pending |
| command-code | W4-GUARD | `frontend/tests/e2e/command-code/permission-prompts.spec.ts` | pending |
| cursor | W4-GUARD | `frontend/tests/e2e/cursor/permission-prompts.spec.ts` | pending |
| deepseek-harness | W4-GUARD | `frontend/tests/e2e/deepseek-harness/permission-prompts.spec.ts` | pending |
| dirac | W4-GUARD | `frontend/tests/e2e/dirac/permission-prompts.spec.ts` | pending |
| factory-droid | W4-GUARD | `frontend/tests/e2e/factory-droid/permission-prompts.spec.ts` | pending |
| fast-agent | W4-GUARD | `frontend/tests/e2e/fast-agent/permission-prompts.spec.ts` | pending |
| gemini-cli | W4-GUARD | `frontend/tests/e2e/gemini-cli/permission-prompts.spec.ts` | pending |
| github-copilot | W4-GUARD | `frontend/tests/e2e/github-copilot/permission-prompts.spec.ts` | pending |
| goose | W4-GUARD | `frontend/tests/e2e/goose/permission-prompts.spec.ts` | pending |
| grok-build | W4-GUARD | `frontend/tests/e2e/grok-build/permission-prompts.spec.ts` | pending |
| junie | W4-GUARD | `frontend/tests/e2e/junie/permission-prompts.spec.ts` | pending |
| kilo | W4-GUARD | `frontend/tests/e2e/kilo/permission-prompts.spec.ts` | pending |
| kimi-code | W4-GUARD | `frontend/tests/e2e/kimi-code/permission-prompts.spec.ts` | pending |
| kiro | W4-GUARD | `frontend/tests/e2e/kiro/permission-prompts.spec.ts` | pending |
| letta-code | W4-GUARD | `frontend/tests/e2e/letta-code/permission-prompts.spec.ts` | pending |
| mimo-code | W4-GUARD | `frontend/tests/e2e/mimo-code/permission-prompts.spec.ts` | pending |
| oh-my-pi | W4-GUARD | `frontend/tests/e2e/oh-my-pi/permission-prompts.spec.ts` | pending |
| opencode | W4-GUARD | `frontend/tests/e2e/opencode/permission-prompts.spec.ts` | pending |
| pi | W4-GUARD | `frontend/tests/e2e/pi/permission-prompts.spec.ts` | pending |
| qoder-cli | W4-GUARD | `frontend/tests/e2e/qoder-cli/permission-prompts.spec.ts` | pending |
| qwen-code | W4-GUARD | `frontend/tests/e2e/qwen-code/permission-prompts.spec.ts` | pending |
| reasonix | W4-GUARD | `frontend/tests/e2e/reasonix/permission-prompts.spec.ts` | pending |
| zcode | W4-GUARD | `frontend/tests/e2e/zcode/permission-prompts.spec.ts` | pending |

### G08: P1 G7

- Execution group: W2-MODE.
- Receipt state: pending.
- Final evidence: Retain the intentional settling activity that protects queued input. Correct it only if a native/browser proof demonstrates a wrong visible state. Source analysis alone does not accept the complete file.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| cline | W2-MODE | `frontend/tests/e2e/cline/mode.spec.ts` | pending |

### G09: P3 D3 duration

- Execution group: W1-BASICCHAT.
- Receipt state: pending.
- Final evidence: Show the Worker duration in the two provider dividers. Prove the timed label live and after reload. Correct the separate CodeBuddy/Qoder comments about the native duration source.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| command-code | W1-BASICCHAT | `frontend/tests/e2e/command-code/basic-chat.spec.ts` | pending |
| deepseek-harness | W1-BASICCHAT | `frontend/tests/e2e/deepseek-harness/basic-chat.spec.ts` | pending |

### G10: P3 D5 plan toggle gap

- Execution group: W8-PLANMODE.
- Receipt state: pending.
- Final evidence: Declare planMode in each provider configuration. Prove Shift+Tab enters and leaves native Plan. Keep all eighteen restored PM-B3 ports and the generic shortcut proof.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| codebuddy-code | W8-PLANMODE | `frontend/tests/e2e/codebuddy-code/plan-mode.spec.ts` | pending |
| qoder-cli | W8-PLANMODE | `frontend/tests/e2e/qoder-cli/plan-mode.spec.ts` | pending |

### G11: P2 D15 child keys

- Execution group: W5-SUBTAB-H.
- Receipt state: pending.
- Final evidence: Scope shared child identity by the owning native parent session. Prove a repeated call/key after clear opens a new child without earlier rows. Preserve restart replay, stored-registry lookup, and concurrent identity.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| claude-code | W5-SUBTAB-H | `frontend/tests/e2e/claude-code/subagent-transcript-tab.spec.ts` | pending |
| cline | W5-SUBTAB-H | `frontend/tests/e2e/cline/subagent-transcript-tab.spec.ts` | pending |
| codebuddy-code | W5-SUBTAB-H | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` | pending |
| codewhale | W5-SUBTAB-H | `frontend/tests/e2e/codewhale/subagent-transcript-tab.spec.ts` | pending |
| codex | W5-SUBTAB-H | `frontend/tests/e2e/codex/subagent-transcript-tab.spec.ts` | pending |
| command-code | W5-SUBTAB-H | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` | pending |
| cursor | W5-SUBTAB-H | `frontend/tests/e2e/cursor/subagent-transcript-tab.spec.ts` | pending |
| deepseek-harness | W5-SUBTAB-H | `frontend/tests/e2e/deepseek-harness/subagent-transcript-tab.spec.ts` | pending |
| dirac | W5-SUBTAB-H | `frontend/tests/e2e/dirac/subagent-transcript-tab.spec.ts` | pending |
| factory-droid | W5-SUBTAB-H | `frontend/tests/e2e/factory-droid/subagent-transcript-tab.spec.ts` | pending |
| fast-agent | W5-SUBTAB-H | `frontend/tests/e2e/fast-agent/subagent-transcript-tab.spec.ts` | pending |
| gemini-cli | W5-SUBTAB-H | `frontend/tests/e2e/gemini-cli/subagent-transcript-tab.spec.ts` | pending |
| github-copilot | W5-SUBTAB-H | `frontend/tests/e2e/github-copilot/subagent-transcript-tab.spec.ts` | pending |
| goose | W5-SUBTAB-H | `frontend/tests/e2e/goose/subagent-transcript-tab.spec.ts` | pending |
| grok-build | W5-SUBTAB-H | `frontend/tests/e2e/grok-build/subagent-transcript-tab.spec.ts` | pending |
| junie | W5-SUBTAB-H | `frontend/tests/e2e/junie/subagent-transcript-tab.spec.ts` | pending |
| kilo | W5-SUBTAB-H | `frontend/tests/e2e/kilo/subagent-transcript-tab.spec.ts` | pending |
| kimi-code | W5-SUBTAB-H | `frontend/tests/e2e/kimi-code/subagent-transcript-tab.spec.ts` | pending |
| kiro | W5-SUBTAB-H | `frontend/tests/e2e/kiro/subagent-transcript-tab.spec.ts` | pending |
| letta-code | W5-SUBTAB-H | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` | pending |
| mimo-code | W5-SUBTAB-H | `frontend/tests/e2e/mimo-code/subagent-transcript-tab.spec.ts` | pending |
| oh-my-pi | W5-SUBTAB-H | `frontend/tests/e2e/oh-my-pi/subagent-transcript-tab.spec.ts` | pending |
| opencode | W5-SUBTAB-H | `frontend/tests/e2e/opencode/subagent-transcript-tab.spec.ts` | pending |
| pi | W5-SUBTAB-H | `frontend/tests/e2e/pi/subagent-transcript-tab.spec.ts` | pending |
| qoder-cli | W5-SUBTAB-H | `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts` | pending |
| qwen-code | W5-SUBTAB-H | `frontend/tests/e2e/qwen-code/subagent-transcript-tab.spec.ts` | pending |
| reasonix | W5-SUBTAB-H | `frontend/tests/e2e/reasonix/subagent-transcript-tab.spec.ts` | pending |
| zcode | W5-SUBTAB-H | `frontend/tests/e2e/zcode/subagent-transcript-tab.spec.ts` | pending |

### G12: P2 D16 spawn rails

- Execution group: W5-SUBTAB-H, W6-SUBTAB-A, W6-SUBTAB-B.
- Receipt state: pending.
- Final evidence: Remove spawn-owned rail/color allocation at its provider source. Preserve tool count, child routing, and registry links. Prove both spawn request and result have no rail.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| codebuddy-code | W6-SUBTAB-A | `frontend/tests/e2e/codebuddy-code/subagent-transcript-tab.spec.ts` | pending |
| letta-code | W6-SUBTAB-B | `frontend/tests/e2e/letta-code/subagent-transcript-tab.spec.ts` | pending |
| factory-droid | W6-SUBTAB-A | `frontend/tests/e2e/factory-droid/subagent-transcript-tab.spec.ts` | pending |
| command-code | W6-SUBTAB-A | `frontend/tests/e2e/command-code/subagent-transcript-tab.spec.ts` | pending |
| deepseek-harness | W6-SUBTAB-A | `frontend/tests/e2e/deepseek-harness/subagent-transcript-tab.spec.ts` | pending |
| gemini-cli | W6-SUBTAB-A | `frontend/tests/e2e/gemini-cli/subagent-transcript-tab.spec.ts` | pending |
| junie | W5-SUBTAB-H | `frontend/tests/e2e/junie/subagent-transcript-tab.spec.ts` | pending |

### G13: P2 D17 attachment steering

- Execution group: W6-STEER.
- Receipt state: pending.
- Final evidence: Return ErrSteeringUnsupported for a refused attachment. Keep the queued input for the next native turn. Never discard an attachment silently or mark it as a delivery failure.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| grok-build | W6-STEER | `frontend/tests/e2e/grok-build/steer-mid-turn.spec.ts` | pending |
| codewhale | W6-STEER | `frontend/tests/e2e/codewhale/steer-mid-turn.spec.ts` | pending |
| dirac | W6-STEER | `frontend/tests/e2e/dirac/steer-mid-turn.spec.ts` | pending |

### G14: P2 D18 Default model

- Execution group: W3-MODEL.
- Receipt state: pending.
- Final evidence: Resolve Default to the configured concrete model in a running session. Prove restored effort choices and context after the native setting write.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| cline | W3-MODEL | `frontend/tests/e2e/cline/model.spec.ts` | pending |

### G15: P2 D19 Pi dialog stop

- Execution group: W2-SMALL.
- Receipt state: pending.
- Final evidence: Use the accepted Wave 1 stop/control transaction. Require the native editor cancellation receipt and withdrawn card, then a later usable prompt. Preserve failed answer-write ownership.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| pi | W2-SMALL | `frontend/tests/e2e/pi/editor-requests.spec.ts` | pending |

### G16: P2 D20 required background rows

- Execution group: W5-BGSHELL.
- Receipt state: pending.
- Final evidence: Support the native background shell route and final state. Correct the cell note to the proved behavior. Require actual execution, owned row identity, and completion.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| command-code | W5-BGSHELL | `frontend/tests/e2e/command-code/background-tasks-sidebar.spec.ts` | pending |
| deepseek-harness | W5-BGSHELL | `frontend/tests/e2e/deepseek-harness/background-tasks-sidebar.spec.ts` | pending |
| qoder-cli | W5-BGSHELL | `frontend/tests/e2e/qoder-cli/background-tasks-sidebar.spec.ts` | pending |

### G17: P2 D20 conditional native background rows

- Execution group: W5-BGSHELL.
- Receipt state: pending.
- Final evidence: Probe the native background route first. Implement product support when it exists. Record an actual native absence otherwise. Do not treat missing mock support as that absence.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| kiro | W5-BGSHELL | `frontend/tests/e2e/kiro/background-tasks-sidebar.spec.ts` | pending |
| letta-code | W5-BGSHELL | `frontend/tests/e2e/letta-code/background-tasks-sidebar.spec.ts` | pending |

### G18: P2 D21 MCP image note

- Execution group: W7-IMAGES.
- Receipt state: pending.
- Final evidence: Run the actual MCP image port. Apply the user note that matches the proved result and retain the existing file-image proof.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| command-code | W7-IMAGES | `frontend/tests/e2e/command-code/images-in-tool-results.spec.ts` | pending |

### G19: P2 D23 structured content

- Execution group: W6-MCP.
- Receipt state: pending.
- Final evidence: Capture native structuredContent once per target. Preserve structuredJson when the native wire carries it. Prove the structured body, error state, and reload. Record a native absence with evidence.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| amp | W6-MCP | `frontend/tests/e2e/amp/mcp-tool-execution.spec.ts` | pending |
| cline | W6-MCP | `frontend/tests/e2e/cline/mcp-tool-execution.spec.ts` | pending |
| codebuddy-code | W6-MCP | `frontend/tests/e2e/codebuddy-code/mcp-tool-execution.spec.ts` | pending |
| codewhale | W6-MCP | `frontend/tests/e2e/codewhale/mcp-tool-execution.spec.ts` | pending |
| command-code | W6-MCP | `frontend/tests/e2e/command-code/mcp-tool-execution.spec.ts` | pending |
| deepseek-harness | W6-MCP | `frontend/tests/e2e/deepseek-harness/mcp-tool-execution.spec.ts` | pending |
| factory-droid | W6-MCP | `frontend/tests/e2e/factory-droid/mcp-tool-execution.spec.ts` | pending |
| kimi-code | W6-MCP | `frontend/tests/e2e/kimi-code/mcp-tool-execution.spec.ts` | pending |
| letta-code | W6-MCP | `frontend/tests/e2e/letta-code/mcp-tool-execution.spec.ts` | pending |
| mimo-code | W6-MCP | `frontend/tests/e2e/mimo-code/mcp-tool-execution.spec.ts` | pending |
| oh-my-pi | W6-MCP | `frontend/tests/e2e/oh-my-pi/mcp-tool-execution.spec.ts` | pending |
| qoder-cli | W6-MCP | `frontend/tests/e2e/qoder-cli/mcp-tool-execution.spec.ts` | pending |

### G20: P2 D24 reserved objective

- Execution group: W5-GOALS.
- Receipt state: pending.
- Final evidence: Add providerkittest.AssertRefusesAnObjectiveThatClears unit coverage and retain all six approved browser ports of session-goal-set-and-clear-P4.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| kilo | W5-GOALS | `frontend/tests/e2e/kilo/session-goal-set-and-clear.spec.ts` | pending |
| kiro | W5-GOALS | `frontend/tests/e2e/kiro/session-goal-set-and-clear.spec.ts` | pending |

### G21: P2 D25 background child

- Execution group: W6-SUBTAB-B.
- Receipt state: pending.
- Final evidence: Probe the native background Agent schema and completion. Implement its child route if supported. Prove live prompt/rows and native final state without a guessed completion.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| qoder-cli | W6-SUBTAB-B | `frontend/tests/e2e/qoder-cli/subagent-transcript-tab.spec.ts` | pending |

### G22: P3 D12 running-child notes

- Execution group: W8-CHILDOPS.
- Receipt state: pending.
- Final evidence: Correct the notes to state that input to a busy child waits until the turn ends. Keep the native busy refusal and completed-child delivery proof.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| kimi-code | W8-CHILDOPS | `frontend/tests/e2e/kimi-code/send-to-a-subagent.spec.ts` | pending |
| factory-droid | W8-CHILDOPS | `frontend/tests/e2e/factory-droid/send-to-a-subagent.spec.ts` | pending |

### G23: P3 D14 zero title

- Execution group: W7-CONTEXT.
- Receipt state: pending.
- Final evidence: Apply CU-B2 so the existing title proves actual zero usage. Retain the exact nonzero usage assertions.

| Exact target | Execution group | Complete browser host | Receipt |
|---|---|---|---|
| command-code | W7-CONTEXT | `frontend/tests/e2e/command-code/context-usage.spec.ts` | pending |

## Final native-setting gap audit

| Cell ID | Execution group | Current source state | Complete final spec | Receipt |
|---|---|---|---|---|
| P3-D13/extended-thinking/cursor | W9-GAP-CURSOR | leapmux-limit; historical test flag | `frontend/tests/e2e/cursor/extended-thinking.spec.ts` | pending |
| P3-D13/fast-mode/cursor | W9-GAP-CURSOR | leapmux-limit; historical test flag | `frontend/tests/e2e/cursor/fast-mode.spec.ts` | pending |
| P3-D13/extended-thinking/cline | W9-GAP-CLINE | leapmux-limit; historical test flag | `frontend/tests/e2e/cline/extended-thinking.spec.ts` | pending |
| P3-D13/swarm-mode/cline | W9-GAP-CLINE | leapmux-limit; historical test flag | `frontend/tests/e2e/cline/swarm-mode.spec.ts` | pending |
| P3-D13/extended-thinking/codebuddy-code | W9-GAP-CODEBUDDY | leapmux-limit; historical test flag | `frontend/tests/e2e/codebuddy-code/extended-thinking.spec.ts` | pending |
| P3-D13/output-style/codebuddy-code | W9-GAP-CODEBUDDY | leapmux-limit; historical test flag | `frontend/tests/e2e/codebuddy-code/output-style.spec.ts` | pending |
| P3-D13/swarm-mode/codebuddy-code | W9-GAP-CODEBUDDY | leapmux-limit; historical test flag | `frontend/tests/e2e/codebuddy-code/swarm-mode.spec.ts` | pending |
| P3-D13/extended-thinking/qoder-cli | W9-GAP-QODER | leapmux-limit; historical test flag | `frontend/tests/e2e/qoder-cli/extended-thinking.spec.ts` | pending |
| P3-D13/output-style/qoder-cli | W9-GAP-QODER | leapmux-limit; historical test flag | `frontend/tests/e2e/qoder-cli/output-style.spec.ts` | pending |
| P3-D13/fast-mode/oh-my-pi | W9-GAP-OHMYPI | leapmux-limit; historical test flag | `frontend/tests/e2e/oh-my-pi/fast-mode.spec.ts` | pending |
| P3-D13/output-style/oh-my-pi | W9-GAP-OHMYPI | leapmux-limit; historical test flag | `frontend/tests/e2e/oh-my-pi/output-style.spec.ts` | pending |
| P3-D13/fast-mode/amp | W9-GAP-AMP | leapmux-limit; historical test flag | `frontend/tests/e2e/amp/fast-mode.spec.ts` | pending |
| P3-D13/output-style/codewhale | W10-GAP-CODEWHALE | leapmux-limit; historical test flag | `frontend/tests/e2e/codewhale/output-style.spec.ts` | pending |
| P3-D13/swarm-mode/codewhale | W10-GAP-CODEWHALE | leapmux-limit; historical test flag | `frontend/tests/e2e/codewhale/swarm-mode.spec.ts` | pending |
| P3-D13/output-style/qwen-code | W10-GAP-QWEN | leapmux-limit; historical test flag | `frontend/tests/e2e/qwen-code/output-style.spec.ts` | pending |
| P3-D13/output-style/factory-droid | W10-GAP-DROID | leapmux-limit; historical test flag | `frontend/tests/e2e/factory-droid/output-style.spec.ts` | pending |
| P3-D13/swarm-mode/github-copilot | W10-GAP-COPILOT | leapmux-limit; historical test flag | `frontend/tests/e2e/github-copilot/swarm-mode.spec.ts` | pending |
| P3-D13/swarm-mode/kilo | W10-GAP-KILO | leapmux-limit; historical test flag | `frontend/tests/e2e/kilo/swarm-mode.spec.ts` | pending |

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
