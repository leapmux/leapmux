import type { ToolKind } from '~/components/chat/ir/toolKind'
import type { ProviderRowOptions } from '~/test-support/toolCallIr'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { CLAUDE_TOOL_NAMES } from './toolNames'

/**
 * A Claude `tool_result` frame for every tool the kind table holds, at both outcomes.
 *
 * Each fixture is the shape the CLI sends, minimized to the fields the extractor
 * reads. The test walks these with the same extraction a mounted row runs, so a
 * name whose result no longer reads as its kind fails the suite.
 */
function result(content: unknown, toolUseResult?: Record<string, unknown>, isError?: true): ToolResultFixture['payload'] {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'r1', content, ...(isError ? { is_error: true } : {}) }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

const HUNK = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }

/** The request half a fixture needs for a call whose result reads the arguments. */
function request(name: string, args: Record<string, unknown>): ProviderRowOptions {
  return {
    request: {
      wrapper: null,
      topLevel: null,
      parentObject: {
        type: 'assistant',
        message: { role: 'assistant', content: [{ type: 'tool_use', id: 'r1', name, input: args }] },
      },
      rawText: '',
      supplementalContent: undefined,
      messageMetadata: undefined,
    },
  }
}

function fixture(name: string, content: unknown, args: Record<string, unknown>, toolUseResult?: Record<string, unknown>): ToolResultFixture {
  return { payload: result(content, toolUseResult), options: request(name, args) }
}

function proseFixture(name: string, content: string, args: Record<string, unknown>): ToolResultFixture {
  return { payload: result(content), options: request(name, args) }
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. Claude's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words. It holds
 * no colon and no line break, so a kind's parser cannot read it back as data.
 */
const ERROR_TEXT = 'The tool reported an error.'

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [CLAUDE_TOOL_NAMES.BASH]: fixture(CLAUDE_TOOL_NAMES.BASH, 'ok', { command: 'ls' }, { stdout: 'ok' }),
  [CLAUDE_TOOL_NAMES.POWERSHELL]: fixture(CLAUDE_TOOL_NAMES.POWERSHELL, 'ok', { command: 'ls' }, { stdout: 'ok' }),
  [CLAUDE_TOOL_NAMES.READ]: fixture(CLAUDE_TOOL_NAMES.READ, '1\ta', { file_path: '/p/a.ts' }, { type: 'read', file: { filePath: '/p/a.ts', content: 'a', startLine: 1, totalLines: 1, numLines: 1 } }),
  [CLAUDE_TOOL_NAMES.WRITE]: fixture(CLAUDE_TOOL_NAMES.WRITE, 'Updated', { file_path: '/p/a.ts', content: 'new' }, { type: 'update', filePath: '/p/a.ts', structuredPatch: [HUNK] }),
  [CLAUDE_TOOL_NAMES.EDIT]: fixture(CLAUDE_TOOL_NAMES.EDIT, 'Updated', { file_path: '/p/a.ts', old_string: 'old', new_string: 'new' }, { type: 'update', filePath: '/p/a.ts', structuredPatch: [HUNK] }),
  [CLAUDE_TOOL_NAMES.MULTI_EDIT]: fixture(CLAUDE_TOOL_NAMES.MULTI_EDIT, 'Updated', { file_path: '/p/a.ts', edits: [{ old_string: 'old', new_string: 'new' }] }, { type: 'update', filePath: '/p/a.ts', structuredPatch: [HUNK] }),
  [CLAUDE_TOOL_NAMES.NOTEBOOK_EDIT]: fixture(CLAUDE_TOOL_NAMES.NOTEBOOK_EDIT, 'Updated', { notebook_path: '/p/nb.ipynb', new_source: 'new' }, { type: 'update', filePath: '/p/nb.ipynb', structuredPatch: [HUNK] }),
  [CLAUDE_TOOL_NAMES.GREP]: fixture(CLAUDE_TOOL_NAMES.GREP, 'a.ts:1:x', { pattern: 'x' }, { numFiles: 1, numLines: 1, content: 'a.ts:1:x', filenames: ['a.ts'] }),
  [CLAUDE_TOOL_NAMES.GLOB]: fixture(CLAUDE_TOOL_NAMES.GLOB, 'a.ts', { pattern: '*' }, { filenames: ['a.ts'], numFiles: 1 }),
  [CLAUDE_TOOL_NAMES.AGENT]: fixture(CLAUDE_TOOL_NAMES.AGENT, 'done', { description: 'Probe', prompt: 'Run.' }, { status: 'completed', agentId: 'a1', content: [{ type: 'text', text: 'done' }] }),
  [CLAUDE_TOOL_NAMES.WEB_FETCH]: fixture(CLAUDE_TOOL_NAMES.WEB_FETCH, '# page', { url: 'https://example.com' }, { code: 200, result: '# page' }),
  [CLAUDE_TOOL_NAMES.WEB_SEARCH]: fixture(CLAUDE_TOOL_NAMES.WEB_SEARCH, 'searched', { query: 'q' }, { query: 'q', results: [{ title: 'Docs', url: 'https://example.com' }] }),
  [CLAUDE_TOOL_NAMES.TODO_WRITE]: fixture(CLAUDE_TOOL_NAMES.TODO_WRITE, 'Saved', { todos: [{ content: 'One', status: 'pending', activeForm: '' }] }, { newTodos: [{ content: 'One', status: 'pending', activeForm: '' }] }),
  [CLAUDE_TOOL_NAMES.TASK_OUTPUT]: fixture(CLAUDE_TOOL_NAMES.TASK_OUTPUT, 'out', { task_id: 't1' }, { task: { task_id: 't1', task_type: 'shell', status: 'completed', description: 'Probe', output: 'out' } }),
  [CLAUDE_TOOL_NAMES.TASK_STOP]: fixture(CLAUDE_TOOL_NAMES.TASK_STOP, 'Stopped', { task_id: 't1' }, { message: 'Stopped the task', task_id: 't1', task_type: 'shell' }),
  [CLAUDE_TOOL_NAMES.REMOTE_TRIGGER]: fixture(CLAUDE_TOOL_NAMES.REMOTE_TRIGGER, 'HTTP 200\n{}', { action: 'list' }, { status: 200, json: '{}' }),
  [CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE]: proseFixture(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE, 'Approved', {}),
  [CLAUDE_TOOL_NAMES.ASK_USER_QUESTION]: fixture(CLAUDE_TOOL_NAMES.ASK_USER_QUESTION, 'answered', { questions: [{ question: 'Which?', header: 'Pick', options: [{ label: 'A' }] }] }, { questions: [{ question: 'Which?', header: 'Pick', options: [] }], answers: { 'Which?': 'A' } }),
  [CLAUDE_TOOL_NAMES.ENTER_WORKTREE]: proseFixture(CLAUDE_TOOL_NAMES.ENTER_WORKTREE, 'moved', { name: 'wt' }),
  [CLAUDE_TOOL_NAMES.EXIT_WORKTREE]: proseFixture(CLAUDE_TOOL_NAMES.EXIT_WORKTREE, 'moved back', {}),
  [CLAUDE_TOOL_NAMES.SEND_MESSAGE]: proseFixture(CLAUDE_TOOL_NAMES.SEND_MESSAGE, 'sent', { to: 'peer', message: 'hi' }),
  [CLAUDE_TOOL_NAMES.SEND_USER_MESSAGE]: proseFixture(CLAUDE_TOOL_NAMES.SEND_USER_MESSAGE, 'sent', { message: 'hi' }),
  [CLAUDE_TOOL_NAMES.LIST_AGENTS]: fixture(CLAUDE_TOOL_NAMES.LIST_AGENTS, '- one', {}, { listing: '- one' }),
  [CLAUDE_TOOL_NAMES.TEAM_CREATE]: proseFixture(CLAUDE_TOOL_NAMES.TEAM_CREATE, 'created', { team_name: 'T' }),
  [CLAUDE_TOOL_NAMES.TEAM_DELETE]: proseFixture(CLAUDE_TOOL_NAMES.TEAM_DELETE, 'deleted', { team_name: 'T' }),
  [CLAUDE_TOOL_NAMES.SKILL]: proseFixture(CLAUDE_TOOL_NAMES.SKILL, 'ran', { skill: 'deploy' }),
  [CLAUDE_TOOL_NAMES.SLEEP]: proseFixture(CLAUDE_TOOL_NAMES.SLEEP, 'slept', { durationMs: 1000 }),
  [CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT]: proseFixture(CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT, '{"ok":true}', { value: { ok: true } }),
  [CLAUDE_TOOL_NAMES.LIST_MCP_RESOURCES]: fixture(CLAUDE_TOOL_NAMES.LIST_MCP_RESOURCES, 'listed', { server: 's' }, { resources: [{ uri: 'probe://a', name: 'A' }] }),
  [CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE]: fixture(CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE, 'doc', { server: 's', uri: 'probe://a' }, { type: 'text', file: { filePath: 'probe://a', content: 'doc', startLine: 1, totalLines: 1, numLines: 1 } }),
  [CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE_DIR]: fixture(CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE_DIR, 'doc', { server: 's', uri: 'probe://dir' }, { type: 'text', file: { filePath: 'probe://a', content: 'doc', startLine: 1, totalLines: 1, numLines: 1 } }),
  [CLAUDE_TOOL_NAMES.CRON_CREATE]: proseFixture(CLAUDE_TOOL_NAMES.CRON_CREATE, 'scheduled', { triggers: [] }),
  [CLAUDE_TOOL_NAMES.CRON_DELETE]: proseFixture(CLAUDE_TOOL_NAMES.CRON_DELETE, 'deleted', { triggers: [] }),
  [CLAUDE_TOOL_NAMES.CRON_LIST]: proseFixture(CLAUDE_TOOL_NAMES.CRON_LIST, '[]', {}),
}

/**
 * The FAILED frame of the tool one successful fixture already states.
 *
 * `is_error` on the result block, and no `tool_use_result` beside it: that pair is
 * what the CLI sends, and a failed call produces no payload, so the reason is the
 * whole answer. Every per-kind builder asks `claudeFailedResult` before it reads a
 * payload for exactly that reason.
 *
 * The request half comes from the successful fixture rather than from a second copy
 * of the arguments. The two frames then describe ONE call, which is what lets the
 * ladder assert that a failure keeps the kind, the tool and the request of its
 * success.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // The request half comes from the successful fixture of the SAME name, so the two
  // frames describe one call; a name the table above does not state is a typo here.
  const success = FIXTURES[name]
  if (!success)
    throw new Error(`No successful fixture states ${name}`)
  const options = success.options
  return { payload: result(ERROR_TEXT, undefined, true), ...(options !== undefined ? { options } : {}), kind, name, status }
}

export const CLAUDE_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.CLAUDE_CODE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', CLAUDE_TOOL_NAMES.BASH),
    failed('read', CLAUDE_TOOL_NAMES.READ),
    failed('write', CLAUDE_TOOL_NAMES.WRITE),
    failed('edit', CLAUDE_TOOL_NAMES.EDIT),
    failed('grep', CLAUDE_TOOL_NAMES.GREP),
    failed('glob', CLAUDE_TOOL_NAMES.GLOB),
    failed('agent', CLAUDE_TOOL_NAMES.AGENT),
    failed('fetch', CLAUDE_TOOL_NAMES.WEB_FETCH),
    failed('web_search', CLAUDE_TOOL_NAMES.WEB_SEARCH),
    failed('todo', CLAUDE_TOOL_NAMES.TODO_WRITE),
    // Both task tools, because the two walk SEPARATE failure paths: one reads the
    // task record the result carries and the other reads the message beside it.
    failed('task', CLAUDE_TOOL_NAMES.TASK_OUTPUT),
    failed('task', CLAUDE_TOOL_NAMES.TASK_STOP),
    failed('trigger', CLAUDE_TOOL_NAMES.REMOTE_TRIGGER),
    // The one Claude call whose refusal is an ANSWER. The reader sent the plan back
    // with feedback, and the command line interface flags `is_error` only because the
    // session did not proceed -- so the row states `declined` rather than `failed`.
    failed('switch_mode', CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE, 'declined'),
    failed('question', CLAUDE_TOOL_NAMES.ASK_USER_QUESTION),
    failed('message', CLAUDE_TOOL_NAMES.SEND_MESSAGE),
    failed('agents', CLAUDE_TOOL_NAMES.LIST_AGENTS),
    failed('skill', CLAUDE_TOOL_NAMES.SKILL),
    failed('wait', CLAUDE_TOOL_NAMES.SLEEP),
    failed('report', CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT),
    failed('list', CLAUDE_TOOL_NAMES.LIST_MCP_RESOURCES),
  ],
  noFailure: {},
  noResult: {
    [CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE]: 'Its result row is hidden: the approval itself draws as the control response.',
    [CLAUDE_TOOL_NAMES.TASK_LIST]: 'Its rows are hidden: the to-do sidebar already shows the list.',
    [CLAUDE_TOOL_NAMES.TOOL_SEARCH]: 'Its rows are hidden: a deferred-tool probe has nothing for a reader.',
    [CLAUDE_TOOL_NAMES.TASK_CREATE]: 'Its result row is hidden: the request states the one task it created.',
    [CLAUDE_TOOL_NAMES.TASK_UPDATE]: 'Its result row is hidden: the request states the one task it updated.',
    [CLAUDE_TOOL_NAMES.TASK_GET]: 'Its result row is hidden: the request states the one task it read.',
  },
  unparsed: {
    [CLAUDE_TOOL_NAMES.CRON_CREATE]: 'No reader exists for a Cron payload; only the RemoteTrigger shape parses.',
    [CLAUDE_TOOL_NAMES.CRON_DELETE]: 'No reader exists for a Cron payload; only the RemoteTrigger shape parses.',
    [CLAUDE_TOOL_NAMES.CRON_LIST]: 'No reader exists for a Cron payload; only the RemoteTrigger shape parses.',
  },
}
