import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { KIRO_KIND, KIRO_TOOL_TITLE } from '~/generated/contracts/kiro-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { KIRO_TOOL } from './toolKinds'

const CALL = 'tooluse_1'

/**
 * The names under which the corpus keeps the calls that Kiro identifies by something
 * other than a title: a shell command (its wire kind), a subagent spawn and a question
 * (`_meta.kiro`), and a Model Context Protocol tool (its `@server/tool` title).
 */
export const KIRO_IDENTIFIED_TOOLS = {
  Shell: 'run_command',
  Subagent: 'invoke_sub_agent',
  Question: 'user_input',
  Mcp: '@probe/echo',
} as const

/**
 * One finished Kiro call, and the `tool_call` that opened it.
 *
 * The frames follow the probe transcripts of Kiro's v3 engine: the opening frame
 * states the title, the kind, the arguments and `_meta.kiro`, and the finished frame
 * repeats the title and the arguments beside the final status, the content and the
 * record Kiro writes into `rawOutput`.
 */
function kiroUpdate(opening: Record<string, unknown>, frame: Record<string, unknown>): ToolResultFixture {
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...opening, ...frame },
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, status: 'pending', ...opening }, null, AgentProvider.KIRO) as ParsedMessageContent,
    },
  }
}

function text(value: string) {
  return [{ type: 'content', content: { type: 'text', text: value } }]
}

/** The answer of one call, as Kiro states it twice: in `rawOutput` and in the content. */
function answered(message: string) {
  return { rawOutput: { message }, content: text(message) }
}

const KIRO_META_DEFAULT = { kiro: { toolOrigin: 'default' } }

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [KIRO_TOOL.ReadFile]: kiroUpdate(
    { title: KIRO_TOOL.ReadFile, kind: 'read', rawInput: { path: '/w/hello.txt', offset: null, limit: null }, locations: [{ path: '/w/hello.txt' }], _meta: KIRO_META_DEFAULT },
    answered('<file name="/w/hello.txt" language="plaintext" >\n<content>\nhello world\n\n</content>\n</file>'),
  ),
  [KIRO_TOOL.ListDirectory]: kiroUpdate(
    { title: KIRO_TOOL.ListDirectory, kind: 'search', rawInput: { path: '/w', explanation: 'see files', depth: null }, locations: [{ path: '/w' }], _meta: { kiro: { toolOrigin: 'acp' } } },
    answered('Contents of /w:\n  [FILE] hello.txt\n  [DIR] src'),
  ),
  [KIRO_TOOL.FileSearch]: kiroUpdate(
    { title: KIRO_TOOL.FileSearch, kind: 'search', rawInput: { explanation: 'find', query: 'hello' }, _meta: KIRO_META_DEFAULT },
    answered('You searched for hello and received the following complete results:\n---\nhello.txt\n---'),
  ),
  [KIRO_TOOL.GrepSearch]: kiroUpdate(
    { title: KIRO_TOOL.GrepSearch, kind: 'search', rawInput: { query: 'hello', explanation: 'grep' }, _meta: KIRO_META_DEFAULT },
    answered('You searched for hello and received the following results:\nhello.txt\n1:hello world'),
  ),
  [KIRO_TOOL.WriteFile]: kiroUpdate(
    { title: KIRO_TOOL.WriteFile, kind: 'edit', rawInput: { path: '/w/new.txt', text: 'a\nb\n' }, locations: [{ path: '/w/new.txt' }], _meta: KIRO_META_DEFAULT },
    { rawOutput: { message: 'Created the /w/new.txt file.' }, content: [{ type: 'diff', path: 'file:///w/new.txt', newText: 'a\nb\n', oldText: '' }] },
  ),
  [KIRO_TOOL.ReplaceInFile]: kiroUpdate(
    { title: KIRO_TOOL.ReplaceInFile, kind: 'edit', rawInput: { path: '/w/new.txt', oldStr: 'b', newStr: 'B' }, locations: [{ path: '/w/new.txt' }], _meta: KIRO_META_DEFAULT },
    { rawOutput: { message: 'Replaced text in /w/new.txt' }, content: [{ type: 'diff', path: 'file:///w/new.txt', newText: 'a\nB\n', oldText: 'a\nb\n' }] },
  ),
  [KIRO_TOOL.AppendToFile]: kiroUpdate(
    { title: KIRO_TOOL.AppendToFile, kind: 'edit', rawInput: { path: '/w/new.txt', text: 'c\n' }, locations: [{ path: '/w/new.txt' }], _meta: KIRO_META_DEFAULT },
    { rawOutput: { message: 'Appended text to /w/new.txt' }, content: [{ type: 'diff', path: 'file:///w/new.txt', newText: 'a\nB\nc\n', oldText: 'a\nB\n' }] },
  ),
  [KIRO_TOOL.DeleteFile]: kiroUpdate(
    { title: KIRO_TOOL.DeleteFile, kind: 'delete', rawInput: { explanation: 'clean up', targetFile: '/w/old.txt' }, locations: [{ path: '/w/old.txt' }], _meta: KIRO_META_DEFAULT },
    answered('Deleted /w/old.txt'),
  ),
  [KIRO_TOOL.FetchUrl]: kiroUpdate(
    { title: KIRO_TOOL.FetchUrl, kind: 'fetch', rawInput: { url: 'https://example.com', mode: 'full' }, _meta: KIRO_META_DEFAULT },
    answered('# Example Domain\n\nThis domain is for use in examples.'),
  ),
  [KIRO_TOOL.ControlProcess]: kiroUpdate(
    { title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'stop', terminalId: 'term-1' }, _meta: KIRO_META_DEFAULT },
    answered('Process term-1 stopped.'),
  ),
  [KIRO_TOOL.KnowledgeSearch]: kiroUpdate(
    { title: KIRO_TOOL.KnowledgeSearch, kind: 'search', rawInput: { query: 'release notes' }, _meta: KIRO_META_DEFAULT },
    answered('1. docs/release.md -- The release checklist.'),
  ),
  [KIRO_TOOL.ToolSearch]: kiroUpdate(
    { title: KIRO_TOOL.ToolSearch, kind: 'search', rawInput: { query: 'issues' }, _meta: { kiro: { toolOrigin: 'acp' } } },
    answered('@linear/list_issues: List the issues of a team.'),
  ),
  [KIRO_TOOL.Memory]: kiroUpdate(
    { title: KIRO_TOOL.Memory, kind: 'other', rawInput: { command: 'view', path: '/memories' }, _meta: KIRO_META_DEFAULT },
    answered('No memories yet.'),
  ),
  [KIRO_TOOL.ReportProgress]: kiroUpdate(
    { title: KIRO_TOOL.ReportProgress, kind: 'other', rawInput: { summary: 'Wrote the parser.' }, _meta: KIRO_META_DEFAULT },
    answered('Progress reported.'),
  ),
  [KIRO_TOOL.UpdateSessionInformation]: kiroUpdate(
    { title: KIRO_TOOL.UpdateSessionInformation, kind: 'other', rawInput: { title: 'Mock title', description: 'doing mock work', status: 'completed' }, _meta: { kiro: { toolOrigin: 'acp' } } },
    answered('Session information updated.'),
  ),
  [KIRO_TOOL_TITLE.TaskList]: kiroUpdate(
    { title: KIRO_TOOL_TITLE.TaskList, kind: 'other', rawInput: { command: 'create', tasks: { 0: { task_description: 'one' }, 1: { task_description: 'two' } }, task_list_description: 'V3 list' }, _meta: KIRO_META_DEFAULT },
    {
      rawOutput: { tasks: [{ id: '1', task_description: 'one', completed: false }, { id: '2', task_description: 'two', completed: false }], description: 'V3 list', context: [], modified_files: [] },
      content: text('{"tasks":[{"id":"1","task_description":"one","completed":false},{"id":"2","task_description":"two","completed":false}]}'),
    },
  ),
  [KIRO_TOOL_TITLE.SwitchToExecution]: kiroUpdate(
    { title: KIRO_TOOL_TITLE.SwitchToExecution, kind: 'other', rawInput: { plan: 'PLAN-BODY: 1. do a 2. do b' }, _meta: { kiro: { toolOrigin: 'acp' } } },
    answered('Switching to execution mode with the approved plan.'),
  ),
  [KIRO_IDENTIFIED_TOOLS.Shell]: kiroUpdate(
    { toolCallId: 'run_command_t_sh', title: 'Print a marker', kind: 'execute', rawInput: { command: 'echo v3-shell', description: 'Print a marker', run_in_background: false }, _meta: KIRO_META_DEFAULT },
    { toolCallId: 'run_command_t_sh', rawOutput: { output: 'v3-shell\n', exitCode: 0, message: 'Output:\nv3-shell\n\n\nExit Code: 0' }, content: text('Output:\nv3-shell\n\n\nExit Code: 0') },
  ),
  [KIRO_IDENTIFIED_TOOLS.Subagent]: kiroUpdate(
    { toolCallId: 'invoke_subagent_t_sub', title: 'Sub-agent: context-gatherer', kind: 'other', rawInput: { name: 'context-gatherer', prompt: 'CHILD3-TASK find hello', explanation: 'delegate', contextFiles: [] }, _meta: { kiro: { kind: KIRO_KIND.AgentSubtask, agentSubtaskId: 'd5789deb' } } },
    { toolCallId: 'invoke_subagent_t_sub', rawOutput: 'CHILD3 RESULT: hello world found' },
  ),
  [KIRO_IDENTIFIED_TOOLS.Question]: kiroUpdate(
    { toolCallId: 't_q', title: 'Which DB?', kind: 'other', _meta: { kiro: { toolId: 'user_input', userInputOptions: [{ title: 'Postgres', description: 'pg', recommended: true }, { title: 'SQLite', recommended: false }] } } },
    { toolCallId: 't_q' },
  ),
  [KIRO_IDENTIFIED_TOOLS.Mcp]: kiroUpdate(
    { toolCallId: 'm_echo', title: '@probe/echo', kind: 'other', rawInput: { text: 'hi', _meta: { _isValid: true, _activePath: ['text'], _completedPaths: [['text']] } }, _meta: { kiro: { serverName: 'probe', toolOrigin: 'client' } } },
    { toolCallId: 'm_echo', rawOutput: { response: 'ECHO:hi', imageBase64Urls: [], message: 'ECHO:hi' }, content: text('ECHO:hi') },
  ),
}

/** The sentence every failed fixture carries. Synthetic on purpose: the guard is about the ladder, not the wording. */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED frame of the call one successful fixture states, with the same opening frame. */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  const fixture = FIXTURES[name]
  const request = fixture?.options?.request as ParsedMessageContent | undefined
  const toolCallId = (request?.parentObject as Record<string, unknown> | undefined)?.toolCallId ?? CALL
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId, status: 'failed', content: text(ERROR_TEXT) },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const KIRO_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.KIRO,
  fixtures: FIXTURES,
  failures: [
    failed('execute', KIRO_IDENTIFIED_TOOLS.Shell),
    failed('read', KIRO_TOOL.ReadFile),
    failed('list', KIRO_TOOL.ListDirectory),
    failed('glob', KIRO_TOOL.FileSearch),
    failed('grep', KIRO_TOOL.GrepSearch),
    failed('write', KIRO_TOOL.WriteFile),
    failed('edit', KIRO_TOOL.ReplaceInFile),
    failed('delete', KIRO_TOOL.DeleteFile),
    failed('fetch', KIRO_TOOL.FetchUrl),
    failed('task', KIRO_TOOL.ControlProcess),
    failed('search', KIRO_TOOL.KnowledgeSearch),
    failed('memory', KIRO_TOOL.Memory),
    failed('report', KIRO_TOOL.UpdateSessionInformation),
    failed('todo', KIRO_TOOL_TITLE.TaskList),
    failed('switch_mode', KIRO_TOOL_TITLE.SwitchToExecution),
    failed('agent', KIRO_IDENTIFIED_TOOLS.Subagent),
    failed('question', KIRO_IDENTIFIED_TOOLS.Question),
    failed('mcp', KIRO_IDENTIFIED_TOOLS.Mcp),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    [KIRO_TOOL.DeleteFile]: 'Kiro states a deletion in words, and no diff; the requested file draws beside them.',
  },
}
