import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotToolComplete, copilotToolStart } from '~/test-support/copilotFixtures'
import { input } from '../testUtils'

const CALL = 'call-1'

/** The paired start frame a fixture's completion reads its name and arguments from. */
function request(toolName: string, args: Record<string, unknown> | string): ParsedMessageContent {
  return input(copilotToolStart(CALL, toolName, args))
}

/** A successful completion for one call, with its paired start beside it. */
function done(toolName: string, args: Record<string, unknown> | string, result: Record<string, unknown>): ToolResultFixture {
  return {
    payload: copilotToolComplete(CALL, { success: true, result }),
    options: { request: request(toolName, args), spanType: toolName },
  }
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [COPILOT_TOOL.WriteAgent]: done(COPILOT_TOOL.WriteAgent, { agent: 'a1' }, { content: 'written' }),
  [COPILOT_TOOL.Edit]: done(COPILOT_TOOL.Edit, { path: '/p/a.ts', old_str: 'x', new_str: 'y' }, { content: 'Edited' }),
  [COPILOT_TOOL.ExtensionsManage]: done(COPILOT_TOOL.ExtensionsManage, {}, { content: 'managed' }),
  [COPILOT_TOOL.ExtensionsReload]: done(COPILOT_TOOL.ExtensionsReload, {}, { content: 'reloaded' }),
  [COPILOT_TOOL.ExitPlanMode]: done(COPILOT_TOOL.ExitPlanMode, {}, { content: 'exited' }),
  [COPILOT_TOOL.ToolSearch]: done(COPILOT_TOOL.ToolSearch, { query: 'q' }, { content: 'matched' }),
  [COPILOT_TOOL.GenericToolSearch]: done(COPILOT_TOOL.GenericToolSearch, { query: 'q' }, { content: 'matched' }),
  [COPILOT_TOOL.StrReplace]: done(COPILOT_TOOL.StrReplace, { path: '/p/a.ts', old_str: 'x', new_str: 'y' }, { content: 'Edited' }),
  [COPILOT_TOOL.Delete]: done(COPILOT_TOOL.Delete, { path: '/p/a.ts' }, { content: 'Deleted' }),
  [COPILOT_TOOL.Move]: done(COPILOT_TOOL.Move, { path: '/p/b.ts', source: '/p/a.ts' }, { content: 'Moved' }),
  [COPILOT_TOOL.WritePowerShell]: done(COPILOT_TOOL.WritePowerShell, { shellId: '7' }, { content: 'written' }),
  [COPILOT_TOOL.LocalShell]: done(COPILOT_TOOL.LocalShell, { command: 'ls' }, { content: 'a.ts' }),
  [COPILOT_TOOL.SearchCodeSubagent]: done(COPILOT_TOOL.SearchCodeSubagent, { query: 'q' }, { content: 'found' }),
  [COPILOT_TOOL.FetchDocumentation]: done(COPILOT_TOOL.FetchDocumentation, { query: 'q' }, { content: 'docs' }),
  [COPILOT_TOOL.Lsp]: done(COPILOT_TOOL.Lsp, {}, { content: 'ok' }),
  [COPILOT_TOOL.Bash]: done(COPILOT_TOOL.Bash, { command: 'ls' }, { content: 'a.ts' }),
  [COPILOT_TOOL.View]: done(COPILOT_TOOL.View, { path: '/p/a.ts' }, { content: 'file body' }),
  [COPILOT_TOOL.Create]: done(COPILOT_TOOL.Create, { path: '/p/new.ts', file_text: 'new' }, { content: 'Created' }),
  [COPILOT_TOOL.StrReplaceEditor]: done(COPILOT_TOOL.StrReplaceEditor, { command: 'str_replace', path: '/p/a.ts', old_str: 'x', new_str: 'y' }, { content: 'Edited' }),
  [COPILOT_TOOL.ApplyPatch]: done(COPILOT_TOOL.ApplyPatch, '*** Begin Patch\n*** Update File: a.ts\n@@\n-before\n+after\n*** End Patch', { content: 'Applied' }),
  [COPILOT_TOOL.Grep]: done(COPILOT_TOOL.Grep, { pattern: 'needle' }, { content: 'a.ts:needle' }),
  [COPILOT_TOOL.Glob]: done(COPILOT_TOOL.Glob, { pattern: '*.ts' }, { content: 'a.ts' }),
  [COPILOT_TOOL.WebSearch]: done(COPILOT_TOOL.WebSearch, { query: 'q' }, { content: 'searched' }),
  [COPILOT_TOOL.Task]: done(COPILOT_TOOL.Task, { description: 'Probe', prompt: 'Run.' }, { content: 'done' }),
  [COPILOT_TOOL.UpdateTodo]: done(COPILOT_TOOL.UpdateTodo, { todos: '- [ ] one' }, { content: 'Saved' }),
  [COPILOT_TOOL.AskUser]: done(COPILOT_TOOL.AskUser, { question: 'Which?' }, { content: 'answered' }),
  [COPILOT_TOOL.ReadBash]: done(COPILOT_TOOL.ReadBash, { shellId: '7' }, { content: 'output' }),
  [COPILOT_TOOL.StopBash]: done(COPILOT_TOOL.StopBash, { shellId: '7' }, { content: 'stopped' }),
  [COPILOT_TOOL.ListBash]: done(COPILOT_TOOL.ListBash, {}, { content: 'listed' }),
  [COPILOT_TOOL.WriteBash]: done(COPILOT_TOOL.WriteBash, { shellId: '7' }, { content: 'written' }),
  [COPILOT_TOOL.Skill]: done(COPILOT_TOOL.Skill, { skill: 'deploy' }, { content: 'ran' }),
  [COPILOT_TOOL.Sql]: done(COPILOT_TOOL.Sql, { query: 'SELECT 1' }, { content: 'ok' }),
  [COPILOT_TOOL.SessionStoreSql]: done(COPILOT_TOOL.SessionStoreSql, { query: 'SELECT 1' }, { content: 'ok' }),
  [COPILOT_TOOL.TaskComplete]: done(COPILOT_TOOL.TaskComplete, {}, { content: 'complete' }),
  [COPILOT_TOOL.ReportProgress]: done(COPILOT_TOOL.ReportProgress, {}, { content: 'progress' }),
  [COPILOT_TOOL.ContextBoard]: done(COPILOT_TOOL.ContextBoard, {}, { content: 'board' }),
  [COPILOT_TOOL.ListAgents]: done(COPILOT_TOOL.ListAgents, {}, { content: '- one' }),
  [COPILOT_TOOL.ReadAgent]: done(COPILOT_TOOL.ReadAgent, { agent: 'a1' }, { content: 'read' }),
  [COPILOT_TOOL.WebFetch]: done(COPILOT_TOOL.WebFetch, { url: 'https://example.com' }, { content: '# page' }),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. Copilot's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * A FAILED completion: `success: false` and an `error` object in place of the result.
 *
 * The runtime states a failure's reason in `error` and never in `result`, so a row
 * that read `result` first would draw whatever partial output the call produced and
 * hide why it stopped.
 *
 * The request half comes from the successful fixture rather than from a second copy of
 * the `tool.execution_start`. The two frames then describe ONE call, which is what lets
 * the ladder assert that a failure keeps the kind, the tool and the request of its
 * success.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // The REQUEST half comes from the successful fixture of that name, so a name the
  // success table does not hold is a fixture-pairing mistake rather than a wire state.
  const paired = FIXTURES[name]
  if (paired === undefined)
    throw new Error(`No successful fixture pairs with the failed frame for ${name}`)
  return {
    payload: copilotToolComplete(CALL, { success: false, error: { message: ERROR_TEXT } }),
    ...(paired.options !== undefined ? { options: paired.options } : {}),
    kind,
    name,
    status,
  }
}

/**
 * The `error` objects Copilot writes for a call that did not run because permission was
 * refused, copied from the runtime's own `tool.execution_complete` events.
 *
 * The `code` is the structured fact. Copilot's own autopilot reads the same two words to
 * tell a refusal from a failure, and `CompletionReceiptToolStatus` in the SDK documents
 * them: `rejected` is the user's refusal, and `denied` is the refusal of the permission
 * service. A Deny answer with feedback reaches the runtime as `denied`.
 */
const REFUSALS = {
  rejected: { code: 'rejected', message: 'The user rejected this tool call.' },
  rejectedWithFeedback: { code: 'denied', message: 'The user rejected this tool call. User feedback: Keep the file.' },
  noResponder: { code: 'denied', message: 'Permission denied and could not request permission from user' },
  // A Reject WITHOUT feedback. A Reject that carries feedback is no refusal: the runtime
  // answers that call with a successful result, "Plan not approved. User feedback: ...",
  // and the model continues. Only the answer with no feedback ends the call as
  // `rejected`. See the comment above the case in `plan-approval-banner.spec.ts`.
  planRejectedWithoutFeedback: { code: 'rejected', message: 'User requested changes but did not provide specific feedback.' },
} as const

/** A completion that ends with one `error` object, and the outcome word the row must read. */
function ended(kind: ToolKind, name: string, status: ToolFailureFixture['status'], error: { code: string, message: string }): ToolFailureFixture {
  const paired = FIXTURES[name]
  if (paired === undefined)
    throw new Error(`No successful fixture pairs with the failed frame for ${name}`)
  return {
    payload: copilotToolComplete(CALL, { success: false, error: { ...error } }),
    ...(paired.options !== undefined ? { options: paired.options } : {}),
    kind,
    name,
    status,
  }
}

export const COPILOT_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.GITHUB_COPILOT,
  fixtures: FIXTURES,
  failures: [
    failed('message', COPILOT_TOOL.WriteAgent),
    failed('edit', COPILOT_TOOL.Edit),
    failed('skill', COPILOT_TOOL.ExtensionsManage),
    failed('switch_mode', COPILOT_TOOL.ExitPlanMode),
    failed('search', COPILOT_TOOL.ToolSearch),
    failed('delete', COPILOT_TOOL.Delete),
    failed('move', COPILOT_TOOL.Move),
    failed('execute', COPILOT_TOOL.Bash),
    failed('fetch', COPILOT_TOOL.WebFetch),
    failed('read', COPILOT_TOOL.View),
    failed('write', COPILOT_TOOL.Create),
    failed('grep', COPILOT_TOOL.Grep),
    failed('glob', COPILOT_TOOL.Glob),
    failed('web_search', COPILOT_TOOL.WebSearch),
    failed('agent', COPILOT_TOOL.Task),
    failed('todo', COPILOT_TOOL.UpdateTodo),
    failed('question', COPILOT_TOOL.AskUser),
    failed('task', COPILOT_TOOL.ReadBash),
    failed('report', COPILOT_TOOL.TaskComplete),
    failed('memory', COPILOT_TOOL.ContextBoard),
    failed('agents', COPILOT_TOOL.ListAgents),
    // A REFUSED call of every kind above. Each kind takes its own reader, and several
    // readers draw no failure state of their own, so the ladder pins the declined state
    // once for each kind.
    ended('message', COPILOT_TOOL.WriteAgent, 'declined', REFUSALS.rejected),
    ended('edit', COPILOT_TOOL.Edit, 'declined', REFUSALS.rejected),
    ended('skill', COPILOT_TOOL.ExtensionsManage, 'declined', REFUSALS.rejected),
    ended('switch_mode', COPILOT_TOOL.ExitPlanMode, 'declined', REFUSALS.planRejectedWithoutFeedback),
    ended('search', COPILOT_TOOL.ToolSearch, 'declined', REFUSALS.rejected),
    ended('delete', COPILOT_TOOL.Delete, 'declined', REFUSALS.rejected),
    ended('move', COPILOT_TOOL.Move, 'declined', REFUSALS.rejected),
    ended('execute', COPILOT_TOOL.Bash, 'declined', REFUSALS.rejected),
    ended('fetch', COPILOT_TOOL.WebFetch, 'declined', REFUSALS.rejected),
    ended('read', COPILOT_TOOL.View, 'declined', REFUSALS.rejected),
    ended('write', COPILOT_TOOL.Create, 'declined', REFUSALS.rejectedWithFeedback),
    ended('grep', COPILOT_TOOL.Grep, 'declined', REFUSALS.rejected),
    ended('glob', COPILOT_TOOL.Glob, 'declined', REFUSALS.rejected),
    ended('web_search', COPILOT_TOOL.WebSearch, 'declined', REFUSALS.rejected),
    ended('agent', COPILOT_TOOL.Task, 'declined', REFUSALS.rejected),
    ended('todo', COPILOT_TOOL.UpdateTodo, 'declined', REFUSALS.rejected),
    ended('question', COPILOT_TOOL.AskUser, 'declined', REFUSALS.rejected),
    ended('task', COPILOT_TOOL.ReadBash, 'declined', REFUSALS.rejected),
    ended('report', COPILOT_TOOL.TaskComplete, 'declined', REFUSALS.rejected),
    ended('memory', COPILOT_TOOL.ContextBoard, 'declined', REFUSALS.rejected),
    ended('agents', COPILOT_TOOL.ListAgents, 'declined', REFUSALS.rejected),
    ended('execute', COPILOT_TOOL.Bash, 'declined', REFUSALS.noResponder),
    // The CODE decides, never the words: the same sentence under the runtime's generic
    // `failure` code is a failure.
    ended('execute', COPILOT_TOOL.Bash, 'failed', { code: 'failure', message: REFUSALS.rejected.message }),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {},
}
