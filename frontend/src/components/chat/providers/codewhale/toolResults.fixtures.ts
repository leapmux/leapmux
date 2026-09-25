import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { CODEWHALE_EVENT, CODEWHALE_ITEM_KIND, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_KIND } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'

// Codewhale frames in the shapes the runtime sends, for this plugin's tests.
//
// Each builder states ONE envelope the way `codewhale app-server --http` streams it
// (`out/02-full` in the provider research holds the live captures), and the worker
// persists it byte for byte. The ids are fixed so a test pairs two frames by stating
// the same call.

export const CALL = 'call-1'
export const THREAD = 'thr-1'
export const TURN = 'turn-1'

/** One runtime event envelope. */
export function codewhaleEvent(event: string, payload: Record<string, unknown>, itemId = 'item-1'): Record<string, unknown> {
  return { schema_version: 1, seq: 1, event, kind: event, thread_id: THREAD, turn_id: TURN, item_id: itemId, payload }
}

/** The hidden shell tools that the runtime's heuristic gives their own item kind. */
const COMMAND_EXECUTION_TOOLS = new Set(['exec_shell', 'exec_shell_wait', 'exec_shell_interact'])

/**
 * The runtime's own item kind for a tool: the name heuristic `tool_kind_for_name`,
 * which no reader may trust. It puts `todo_write` under a file change, for one.
 */
function itemKindFor(toolName: string): string {
  const lower = toolName.toLowerCase()
  if (COMMAND_EXECUTION_TOOLS.has(lower))
    return CODEWHALE_ITEM_KIND.CommandExecution
  return /write|edit|patch/.test(lower) ? CODEWHALE_ITEM_KIND.FileChange : CODEWHALE_ITEM_KIND.ToolCall
}

/** The `item.started` of one tool call, with its arguments in both of their shapes. */
export function toolStarted(toolName: string, args: Record<string, unknown>, callId = CALL): Record<string, unknown> {
  const inputText = JSON.stringify(args)
  return codewhaleEvent(CODEWHALE_EVENT.ItemStarted, {
    item: {
      id: 'item-1',
      turn_id: TURN,
      kind: itemKindFor(toolName),
      status: 'in_progress',
      summary: `${toolName} started`,
      detail: inputText,
      metadata: { tool_use_id: callId, tool_name: toolName, tool_input: inputText },
    },
    tool: { id: callId, name: toolName, input: args },
  })
}

/** The final event of one tool call, with the words and the metadata it answered. */
export function toolFinished(
  event: string,
  toolName: string,
  args: Record<string, unknown>,
  detail: string,
  metadata: Record<string, unknown> = {},
  callId = CALL,
): Record<string, unknown> {
  const status = event === CODEWHALE_EVENT.ItemCompleted ? 'completed' : event === CODEWHALE_EVENT.ItemFailed ? 'failed' : 'interrupted'
  return codewhaleEvent(event, {
    item: {
      id: 'item-1',
      turn_id: TURN,
      kind: itemKindFor(toolName),
      status,
      summary: `${toolName}: ${detail}`.slice(0, 280),
      detail,
      metadata: { ...metadata, tool_use_id: callId, tool_name: toolName, tool_input: JSON.stringify(args) },
    },
  })
}

/** A tool call that answered. */
export function toolCompleted(toolName: string, args: Record<string, unknown>, detail: string, metadata: Record<string, unknown> = {}, callId = CALL): Record<string, unknown> {
  return toolFinished(CODEWHALE_EVENT.ItemCompleted, toolName, args, detail, { tool_result_for: callId, is_error: false, ...metadata }, callId)
}

/** A tool call that failed. */
export function toolFailed(toolName: string, args: Record<string, unknown>, detail: string, callId = CALL): Record<string, unknown> {
  return toolFinished(CODEWHALE_EVENT.ItemFailed, toolName, args, detail, {}, callId)
}

/** The final event of a non-tool item: a message, a reasoning step, a status, an error. */
export function itemFinished(kind: string, detail: string, extra: Record<string, unknown> = {}, event: string = CODEWHALE_EVENT.ItemCompleted): Record<string, unknown> {
  return codewhaleEvent(event, {
    item: { id: 'item-2', turn_id: TURN, kind, status: 'completed', summary: detail.slice(0, 280), detail },
    ...extra,
  }, 'item-2')
}

/** The `turn.completed` that ends a turn. */
export function turnCompleted(status: string, fields: Record<string, unknown> = {}): Record<string, unknown> {
  return codewhaleEvent(CODEWHALE_EVENT.TurnCompleted, { turn: { id: TURN, thread_id: THREAD, status, input_summary: 'Say hello.', ...fields } }, '')
}

/** One content block of a subagent transcript, as the worker stores it. */
export function childBlock(role: string, block: Record<string, unknown>, index = 1): Record<string, unknown> {
  return { kind: CODEWHALE_TRANSCRIPT_KIND.Message, index, block: 0, message: { role, content: [block] } }
}

/** The parsed request side of one call, as the message store hands it to a row. */
export function requestSide(frame: Record<string, unknown>): ResolvedMessageContent {
  return input(frame, undefined, AgentProvider.CODEWHALE)
}

/** A finished call: its opening frame beside its final one. */
function done(toolName: string, args: Record<string, unknown>, detail: string, metadata: Record<string, unknown> = {}): ToolResultFixture {
  return {
    payload: toolCompleted(toolName, args, detail, metadata),
    options: { spanType: toolName, request: requestSide(toolStarted(toolName, args)) },
  }
}

/** The runtime's mutation record for one file, with the unified diff it states. */
function mutation(path: string, outcome: string, diff: string): Record<string, unknown> {
  return { event: 'file.mutation', mutation: { diff, files: [{ path, outcome }], renames: [] } }
}

const EDIT_DIFF = '--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-before\n+after\n'
const WRITE_DIFF = '--- a/a.ts\n+++ b/a.ts\n@@ -0,0 +1 @@\n+hi\n'
const PATCH = '--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-before\n+after\n'
const COMMAND = { exit_code: 0, duration_ms: 12 }
const TODOS = [{ content: 'Read the code', status: 'completed' }, { content: 'Write the fix', status: 'in_progress' }]

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  // Files.
  [CODEWHALE_TOOL.Read]: done(CODEWHALE_TOOL.Read, { path: 'a.ts' }, 'alpha\nbeta'),
  [CODEWHALE_TOOL.ReadFile]: done(CODEWHALE_TOOL.ReadFile, { path: 'a.ts' }, 'alpha'),
  [CODEWHALE_TOOL.File]: done(CODEWHALE_TOOL.File, { action: 'read', path: 'a.ts' }, 'alpha'),
  [CODEWHALE_TOOL.ReadMedia]: done(CODEWHALE_TOOL.ReadMedia, { path: 'shot.png' }, 'Attached image shot.png'),
  [CODEWHALE_TOOL.ImageOcr]: done(CODEWHALE_TOOL.ImageOcr, { path: 'shot.png' }, 'Hello, world'),
  [CODEWHALE_TOOL.ImageAnalyze]: done(CODEWHALE_TOOL.ImageAnalyze, { path: 'shot.png' }, 'A window with a button'),
  [CODEWHALE_TOOL.HandleRead]: done(CODEWHALE_TOOL.HandleRead, { handle: 's1/result' }, '[1, 2, 3]'),
  [CODEWHALE_TOOL.RetrieveToolResult]: done(CODEWHALE_TOOL.RetrieveToolResult, { ref: 'art_1' }, 'stored output'),
  [CODEWHALE_TOOL.Write]: done(CODEWHALE_TOOL.Write, { path: 'a.ts', content: 'hi\n' }, 'Successfully wrote 3 bytes to a.ts', mutation('a.ts', 'created', WRITE_DIFF)),
  [CODEWHALE_TOOL.WriteFile]: done(CODEWHALE_TOOL.WriteFile, { path: 'a.ts', content: 'hi\n' }, 'Wrote a.ts', mutation('a.ts', 'created', WRITE_DIFF)),
  [CODEWHALE_TOOL.Edit]: done(CODEWHALE_TOOL.Edit, { path: 'a.ts', edits: [{ oldText: 'before', newText: 'after' }] }, 'Successfully replaced 1 block(s) in a.ts.', mutation('a.ts', 'updated', EDIT_DIFF)),
  [CODEWHALE_TOOL.EditFile]: done(CODEWHALE_TOOL.EditFile, { path: 'a.ts', old_string: 'before', new_string: 'after' }, 'Edited a.ts', mutation('a.ts', 'updated', EDIT_DIFF)),
  [CODEWHALE_TOOL.ApplyPatch]: done(CODEWHALE_TOOL.ApplyPatch, { patch: PATCH }, 'Applied the patch to 1 file', mutation('a.ts', 'updated', EDIT_DIFF)),
  [CODEWHALE_TOOL.FimEdit]: done(CODEWHALE_TOOL.FimEdit, { path: 'a.ts', prefix: 'const a = ', suffix: '\n' }, 'Filled a.ts', mutation('a.ts', 'updated', EDIT_DIFF)),
  [CODEWHALE_TOOL.ListDir]: done(CODEWHALE_TOOL.ListDir, { path: '.' }, JSON.stringify([{ name: 'src', is_dir: true }, { name: 'a.ts', is_dir: false }])),
  [CODEWHALE_TOOL.ProjectMap]: done(CODEWHALE_TOOL.ProjectMap, { max_depth: 2 }, JSON.stringify({ tree: 'src/\n  a.ts', summary: 'A TypeScript project', key_files: ['package.json'] })),
  [CODEWHALE_TOOL.FileSearch]: done(CODEWHALE_TOOL.FileSearch, { query: 'a.ts' }, JSON.stringify([{ path: 'src/a.ts', name: 'a.ts', score: 1 }])),
  [CODEWHALE_TOOL.GrepFiles]: done(CODEWHALE_TOOL.GrepFiles, { pattern: 'alpha' }, JSON.stringify({ matches: [{ file: 'src/a.ts', line_number: 3, line: 'const alpha = 1' }], total_matches: 1, files_searched: 4, truncated: false })),

  // Commands.
  [CODEWHALE_TOOL.Bash]: done(CODEWHALE_TOOL.Bash, { command: 'ls -1' }, 'a.ts\n', COMMAND),
  [CODEWHALE_TOOL.LegacyBash]: done(CODEWHALE_TOOL.LegacyBash, { command: 'ls -1' }, 'a.ts\n', COMMAND),
  [CODEWHALE_TOOL.TaskShellStart]: done(CODEWHALE_TOOL.TaskShellStart, { command: 'npm run dev' }, 'Started shell_1', COMMAND),
  [CODEWHALE_TOOL.TerminalRun]: done(CODEWHALE_TOOL.TerminalRun, { command: 'make' }, 'built', COMMAND),
  [CODEWHALE_TOOL.TerminalSend]: done(CODEWHALE_TOOL.TerminalSend, { terminal_id: 't1', input: 'y\n' }, 'sent'),
  [CODEWHALE_TOOL.CodeExecution]: done(CODEWHALE_TOOL.CodeExecution, { code: 'print(1)' }, '1'),
  [CODEWHALE_TOOL.JsExecution]: done(CODEWHALE_TOOL.JsExecution, { code: '1 + 1' }, '2'),
  [CODEWHALE_TOOL.PandocConvert]: done(CODEWHALE_TOOL.PandocConvert, { input: 'a.md', to: 'html' }, 'Wrote a.html'),
  [CODEWHALE_TOOL.Git]: done(CODEWHALE_TOOL.Git, { action: 'status' }, 'On branch main', COMMAND),
  [CODEWHALE_TOOL.GitStatus]: done(CODEWHALE_TOOL.GitStatus, {}, 'On branch main', COMMAND),
  [CODEWHALE_TOOL.GitDiff]: done(CODEWHALE_TOOL.GitDiff, { path: 'src' }, 'diff --git a/src/a.ts b/src/a.ts', COMMAND),
  [CODEWHALE_TOOL.GitLog]: done(CODEWHALE_TOOL.GitLog, {}, 'commit 1234', COMMAND),
  [CODEWHALE_TOOL.GitShow]: done(CODEWHALE_TOOL.GitShow, {}, 'commit 1234', COMMAND),
  [CODEWHALE_TOOL.GitBlame]: done(CODEWHALE_TOOL.GitBlame, { path: 'a.ts' }, '1234 (a) line', COMMAND),
  [CODEWHALE_TOOL.Run]: done(CODEWHALE_TOOL.Run, { action: 'tests' }, 'test result: ok', COMMAND),
  [CODEWHALE_TOOL.RunTests]: done(CODEWHALE_TOOL.RunTests, { args: '--lib' }, 'test result: ok', COMMAND),
  [CODEWHALE_TOOL.RunVerifiers]: done(CODEWHALE_TOOL.RunVerifiers, {}, 'All verifiers passed', COMMAND),

  // Background work.
  [CODEWHALE_TOOL.TaskShellWait]: done(CODEWHALE_TOOL.TaskShellWait, { task_id: 'shell_1' }, 'exited 0'),
  [CODEWHALE_TOOL.TerminalWait]: done(CODEWHALE_TOOL.TerminalWait, { terminal_id: 't1' }, 'idle'),
  [CODEWHALE_TOOL.TerminalCancel]: done(CODEWHALE_TOOL.TerminalCancel, { terminal_id: 't1' }, 'cancelled'),
  [CODEWHALE_TOOL.TerminalReset]: done(CODEWHALE_TOOL.TerminalReset, { terminal_id: 't1' }, 'reset'),
  [CODEWHALE_TOOL.Tasks]: done(CODEWHALE_TOOL.Tasks, { action: 'list' }, 'No tasks'),
  [CODEWHALE_TOOL.Workflow]: done(CODEWHALE_TOOL.Workflow, { action: 'start', name: 'review' }, 'Started the review run', { run_id: 'run-1', status: 'running', terminal: false }),
  [CODEWHALE_TOOL.StartMcpServer]: done(CODEWHALE_TOOL.StartMcpServer, { name: 'docs' }, 'Started docs'),
  [CODEWHALE_TOOL.StartRegistryMcpServer]: done(CODEWHALE_TOOL.StartRegistryMcpServer, { name: 'docs' }, 'Started docs'),
  [CODEWHALE_TOOL.Automation]: done(CODEWHALE_TOOL.Automation, { action: 'create', name: 'nightly', schedule: '0 3 * * *' }, 'Created nightly'),
  [CODEWHALE_TOOL.SendLater]: done(CODEWHALE_TOOL.SendLater, { message: 'Check the build', delay: '10m' }, 'Scheduled'),

  // Subagents.
  [CODEWHALE_TOOL.Agent]: done(CODEWHALE_TOOL.Agent, { action: 'start', prompt: 'Count the files.', type: 'explore', name: 'counter' }, JSON.stringify({ name: 'counter', agent_id: 'agent_1', status: 'running', terminal: false })),
  [CODEWHALE_TOOL.AgentsCoordinate]: done(CODEWHALE_TOOL.AgentsCoordinate, { plan: 'split' }, 'Coordinated 2 agents'),
  [CODEWHALE_TOOL.AgentsList]: done(CODEWHALE_TOOL.AgentsList, {}, 'agent_1 running'),
  [CODEWHALE_TOOL.AgentsInterrupt]: done(CODEWHALE_TOOL.AgentsInterrupt, { agent_id: 'agent_1' }, 'Interrupted agent_1'),
  [CODEWHALE_TOOL.AgentsFollowup]: done(CODEWHALE_TOOL.AgentsFollowup, { agent_id: 'agent_1', message: 'Also count dirs.' }, 'Delivered'),
  [CODEWHALE_TOOL.AgentsMessage]: done(CODEWHALE_TOOL.AgentsMessage, { agent_id: 'agent_1', message: 'Stop soon.' }, 'Delivered'),
  [CODEWHALE_TOOL.AgentsWait]: done(CODEWHALE_TOOL.AgentsWait, {}, 'All settled'),
  [CODEWHALE_TOOL.Notify]: done(CODEWHALE_TOOL.Notify, { title: 'Done', body: 'The build passed.' }, 'Notified'),

  // Checklists and plans.
  [CODEWHALE_TOOL.TodoWrite]: done(CODEWHALE_TOOL.TodoWrite, { todos: TODOS }, 'Todo list updated (2 items, 50% settled)', { task_updates: { checklist: { items: [{ id: 1, ...TODOS[0] }, { id: 2, ...TODOS[1] }] } } }),
  [CODEWHALE_TOOL.LegacyTodoWrite]: done(CODEWHALE_TOOL.LegacyTodoWrite, { todos: TODOS }, 'Todo list updated'),
  [CODEWHALE_TOOL.Todo]: done(CODEWHALE_TOOL.Todo, { todos: TODOS }, 'Todo list updated'),
  [CODEWHALE_TOOL.ChecklistWrite]: done(CODEWHALE_TOOL.ChecklistWrite, { todos: TODOS }, 'Todo list updated'),
  [CODEWHALE_TOOL.ChecklistUpdate]: done(CODEWHALE_TOOL.ChecklistUpdate, { todos: TODOS }, 'Todo list updated'),
  [CODEWHALE_TOOL.WorkUpdate]: done(CODEWHALE_TOOL.WorkUpdate, { todos: TODOS }, 'Todo list updated'),
  [CODEWHALE_TOOL.UpdatePlan]: done(CODEWHALE_TOOL.UpdatePlan, { explanation: 'Plan', plan: [{ step: 'Investigate', status: 'completed' }, { step: 'Implement', status: 'in_progress' }] }, 'Plan updated'),

  // The web.
  [CODEWHALE_TOOL.FetchURL]: done(CODEWHALE_TOOL.FetchURL, { url: 'https://example.com' }, '# Example Domain', { duration_ms: 40 }),
  [CODEWHALE_TOOL.WebSearch]: done(CODEWHALE_TOOL.WebSearch, { query: 'codewhale' }, '1. Codewhale - https://example.com'),
  [CODEWHALE_TOOL.Web]: done(CODEWHALE_TOOL.Web, { action: 'search', query: 'codewhale' }, '1. Codewhale'),
  [CODEWHALE_TOOL.WebRun]: done(CODEWHALE_TOOL.WebRun, { search_query: [{ q: 'codewhale' }] }, '1. Codewhale'),
  [CODEWHALE_TOOL.WaitForDevServer]: done(CODEWHALE_TOOL.WaitForDevServer, { url: 'http://localhost:3000' }, 'Ready after 2s'),

  // Searches of a corpus that is not the file tree.
  [CODEWHALE_TOOL.ToolSearch]: done(CODEWHALE_TOOL.ToolSearch, { query: 'ask the user' }, JSON.stringify({ type: 'tool_search_tool_search_result', tool_references: [{ type: 'tool_reference', tool_name: 'request_user_input' }], unavailable_tool_references: [] })),
  [CODEWHALE_TOOL.Lsp]: done(CODEWHALE_TOOL.Lsp, { action: 'definition', path: 'a.ts', line: 3 }, 'src/b.ts:10'),

  // Memory.
  [CODEWHALE_TOOL.Remember]: done(CODEWHALE_TOOL.Remember, { note: 'Use bun.' }, 'Remembered'),
  [CODEWHALE_TOOL.MemorySearch]: done(CODEWHALE_TOOL.MemorySearch, { query: 'bun' }, 'Use bun.'),
  [CODEWHALE_TOOL.MemoryGet]: done(CODEWHALE_TOOL.MemoryGet, { id: 'm1' }, 'Use bun.'),
  [CODEWHALE_TOOL.SessionSearch]: done(CODEWHALE_TOOL.SessionSearch, { query: 'deploy' }, 'session s1'),
  [CODEWHALE_TOOL.SessionGet]: done(CODEWHALE_TOOL.SessionGet, { id: 's1' }, 'The deploy session'),
  [CODEWHALE_TOOL.Note]: done(CODEWHALE_TOOL.Note, { note: 'Remember the flag.' }, 'Noted'),

  [CODEWHALE_TOOL.LoadSkill]: done(CODEWHALE_TOOL.LoadSkill, { name: 'deploy' }, '# Deploy skill'),

  // Reports.
  [CODEWHALE_TOOL.CreateGoal]: done(CODEWHALE_TOOL.CreateGoal, { objective: 'Ship it' }, '{"status":"active"}'),
  [CODEWHALE_TOOL.GetGoal]: done(CODEWHALE_TOOL.GetGoal, {}, '{"status":"active"}'),
  [CODEWHALE_TOOL.UpdateGoal]: done(CODEWHALE_TOOL.UpdateGoal, { status: 'blocked', blocker: 'Need the user.' }, '{"status":"blocked"}'),
  [CODEWHALE_TOOL.Diagnostics]: done(CODEWHALE_TOOL.Diagnostics, {}, '{"sandbox_available":true}'),
  [CODEWHALE_TOOL.ValidateData]: done(CODEWHALE_TOOL.ValidateData, { path: 'a.json' }, 'Valid'),
  [CODEWHALE_TOOL.Review]: done(CODEWHALE_TOOL.Review, { target: 'HEAD' }, 'No findings'),
  [CODEWHALE_TOOL.Verify]: done(CODEWHALE_TOOL.Verify, {}, 'Verified'),
  [CODEWHALE_TOOL.Harness]: done(CODEWHALE_TOOL.Harness, { action: 'status' }, 'Idle'),
  [CODEWHALE_TOOL.GitCommitPlan]: done(CODEWHALE_TOOL.GitCommitPlan, {}, 'One commit'),
  [CODEWHALE_TOOL.Github]: done(CODEWHALE_TOOL.Github, { action: 'issue_read', number: 1 }, 'Issue 1'),
  [CODEWHALE_TOOL.Finance]: done(CODEWHALE_TOOL.Finance, { symbol: 'ACME' }, 'ACME 1.00'),
  [CODEWHALE_TOOL.Speech]: done(CODEWHALE_TOOL.Speech, { text: 'Hello' }, 'Spoke'),
  [CODEWHALE_TOOL.RevertTurn]: done(CODEWHALE_TOOL.RevertTurn, { turn_id: TURN }, 'Reverted 1 file'),
  [CODEWHALE_TOOL.RequestPluginInstall]: done(CODEWHALE_TOOL.RequestPluginInstall, { name: 'docs' }, 'Requested'),
  [CODEWHALE_TOOL.RegistrySync]: done(CODEWHALE_TOOL.RegistrySync, {}, 'Synced'),
  [CODEWHALE_TOOL.ExecuteTools]: done(CODEWHALE_TOOL.ExecuteTools, { calls: [] }, 'Ran 0 tools'),
  [CODEWHALE_TOOL.MultiToolUseParallel]: done(CODEWHALE_TOOL.MultiToolUseParallel, { tool_uses: [] }, 'Ran 0 tools'),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose: the guard asks about the LADDER -- the outcome word, the brand,
 * the kind and the request -- rather than about the runtime's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED frame of the call one successful fixture already states. */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  const fixture = FIXTURES[name]
  const request = fixture?.options?.request?.parentObject
  const args = request ? (((request.payload as Record<string, unknown>).tool as Record<string, unknown>).input as Record<string, unknown>) : {}
  const payload = status === 'cancelled'
    ? toolFinished(CODEWHALE_EVENT.ItemInterrupted, name, args, ERROR_TEXT)
    : toolFailed(name, args, ERROR_TEXT)
  return {
    payload,
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const CODEWHALE_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.CODEWHALE,
  fixtures: FIXTURES,
  failures: [
    failed('read', CODEWHALE_TOOL.Read),
    failed('write', CODEWHALE_TOOL.Write),
    failed('edit', CODEWHALE_TOOL.Edit),
    failed('list', CODEWHALE_TOOL.ListDir),
    failed('glob', CODEWHALE_TOOL.FileSearch),
    failed('grep', CODEWHALE_TOOL.GrepFiles),
    failed('execute', CODEWHALE_TOOL.Bash),
    // An interrupted command is stopped rather than failed.
    failed('execute', CODEWHALE_TOOL.Bash, 'cancelled'),
    failed('task', CODEWHALE_TOOL.Workflow),
    failed('trigger', CODEWHALE_TOOL.Automation),
    failed('agent', CODEWHALE_TOOL.Agent),
    failed('agents', CODEWHALE_TOOL.AgentsList),
    failed('message', CODEWHALE_TOOL.AgentsMessage),
    failed('wait', CODEWHALE_TOOL.AgentsWait),
    failed('todo', CODEWHALE_TOOL.TodoWrite),
    failed('fetch', CODEWHALE_TOOL.FetchURL),
    failed('web_search', CODEWHALE_TOOL.WebSearch),
    failed('search', CODEWHALE_TOOL.ToolSearch),
    failed('memory', CODEWHALE_TOOL.Remember),
    failed('skill', CODEWHALE_TOOL.LoadSkill),
    failed('report', CODEWHALE_TOOL.UpdateGoal),
    // No failed question: the question has no successful fixture to pair with (see
    // `noResult`). `extractors/row.test.ts` pins the failed question row instead.
  ],
  noFailure: {},
  noResult: {
    // `extractors/row.test.ts` pins both halves of this.
    [CODEWHALE_TOOL.RequestUserInput]: 'The runtime redacts every result of this tool on its event stream, so the result row of an answered question states nothing and hides; the request row draws the question, and the saved answer beside it states the reply.',
  },
  unparsed: {},
}
