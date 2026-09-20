import type { ToolKind } from '../../../model/toolKind'
import type { ClaudeRowContext, ClaudeToolRow } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeSpec } from './toolCall'
import { CLAUDE_TOOL_REQUEST_OVERRIDES, claudeRequestFor } from './toolRequests'

/** One Claude REQUEST row: the tool's name and the arguments it sent, and nothing else. */
function requestRow(toolName: string, input: Record<string, unknown>): ClaudeToolRow {
  return {
    id: 'tu_1',
    role: 'request',
    toolName,
    input,
    toolUseResult: undefined,
    resultContent: '',
    rawResultContent: undefined,
    images: [],
    isError: undefined,
  }
}

/**
 * The declared request of a call that has not answered, read through the DISPATCHER.
 *
 * `claudeSpec` is what a mounted row calls, so this pins that the builder of each
 * kind really takes the routed request. A direct `claudeRequestFor` would pass for a
 * builder that still hand-built one of its own.
 */
function requestOf(toolName: string, input: Record<string, unknown>, context: ClaudeRowContext = {}) {
  return claudeSpec(requestRow(toolName, input), undefined, context).request
}

/**
 * One kind Claude reads differently from the shared table, and the call that proves it.
 *
 * Each case states four things: the tool name, the arguments, the request Claude
 * answers, and the request `DEFAULT_TOOL_REQUESTS` answers from the SAME arguments.
 * Nearly every case carries a DECOY key -- `subagent_type`, `team_name`, `path`,
 * `server`, `text`, `name`, `mode`, `shell_id`, `taskId`, `action` -- that one table
 * reads and the other does not, so the two answers differ at a key rather than only at
 * a value.
 *
 * The comparison is the point. No TYPE can refuse an override that reads the arguments
 * alone: such a function satisfies a slot that supplies the arguments and the facts, so
 * an override quietly replaced by a copy of the shared entry still compiles. Only this
 * assertion catches it.
 *
 * `execute` is the one case with no argument decoy, and that is not an omission. Its
 * whole deviation is `language`, which the tool NAME states; the command and the
 * description come from the shared entry, so every argument key reads the same in both.
 * The two answers differ at the `language` key instead.
 */
const OVERRIDE_CASES: ReadonlyArray<readonly [ToolKind, string, Record<string, unknown>, unknown, unknown]> = [
  [
    'agent',
    CLAUDE_TOOL_NAMES.AGENT,
    { description: 'Inspect the parser', subagent_type: 'reviewer', prompt: 'Read it' },
    { description: 'Inspect the parser', prompt: 'Read it', agentType: 'reviewer' },
    { description: 'Inspect the parser', prompt: 'Read it' },
  ],
  [
    'agents',
    CLAUDE_TOOL_NAMES.TEAM_CREATE,
    { team_name: '  parsers  ', channel: 'never read', q: 'never read' },
    { team: { name: 'parsers' } },
    { channel: 'never read', query: 'never read' },
  ],
  [
    'edit',
    CLAUDE_TOOL_NAMES.EDIT,
    { file_path: '/project/a.ts', old_string: 'before', new_string: 'after', replace_all: true },
    { changes: [{ filePath: '/project/a.ts', structuredPatch: null, oldStr: 'before', newStr: 'after' }], replaceAll: true },
    // The shared entry reads the same file and the same two sides, and it states the
    // operation beside them. `replace_all` is the deviation: it is Claude's own key.
    { changes: [{ filePath: '/project/a.ts', operation: 'edit', structuredPatch: null, oldStr: 'before', newStr: 'after' }] },
  ],
  [
    'write',
    CLAUDE_TOOL_NAMES.WRITE,
    { file_path: '/project/b.ts', content: 'hello' },
    // A write that states no `replace_all` OMITS the key rather than stating undefined.
    { changes: [{ filePath: '/project/b.ts', structuredPatch: null, oldStr: '', newStr: 'hello' }] },
    // The shared entry states the file and the addition, and no BODY: `content` is a
    // best-effort reading that each provider spells for itself.
    { changes: [{ filePath: '/project/b.ts', operation: 'add', structuredPatch: null, oldStr: '', newStr: '' }] },
  ],
  [
    'execute',
    CLAUDE_TOOL_NAMES.POWERSHELL,
    { command: 'Get-ChildItem', description: 'List the files' },
    { command: 'Get-ChildItem', description: 'List the files', language: 'powershell' },
    { command: 'Get-ChildItem', description: 'List the files' },
  ],
  [
    'list',
    CLAUDE_TOOL_NAMES.LIST_MCP_RESOURCES,
    { server: 'files', path: 'never read' },
    { path: 'files' },
    { path: 'never read' },
  ],
  [
    'mcp',
    'mcp__docs__lookup',
    { query: 'renderer', server: 'never read', tool: 'never read' },
    { server: 'docs', tool: 'lookup', args: { query: 'renderer', server: 'never read', tool: 'never read' } },
    { server: 'never read', tool: 'never read', args: { query: 'renderer', server: 'never read', tool: 'never read' } },
  ],
  [
    'message',
    CLAUDE_TOOL_NAMES.SEND_MESSAGE,
    { to: '  peer-1  ', message: { body: 'the whole record' }, summary: 'Ship it', text: 'never read' },
    { to: 'peer-1', text: 'Ship it', summary: 'Ship it' },
    { to: '  peer-1  ', text: 'never read', summary: 'Ship it' },
  ],
  [
    'question',
    CLAUDE_TOOL_NAMES.ASK_USER_QUESTION,
    { questions: [{ header: 'Parser', question: 'Which parser should I write?', options: [{ label: 'PEG', description: 'A grammar' }] }] },
    { questions: [{ header: 'Parser', question: 'Which parser should I write?', options: [{ label: 'PEG', description: 'A grammar' }] }] },
    { questions: [] },
  ],
  [
    'skill',
    CLAUDE_TOOL_NAMES.SKILL,
    { skill: 'deep-review', args: '--fix', name: 'never read' },
    { name: 'deep-review', args: '--fix' },
    // `prettifyArgsJson` rather than a JSON literal: the claim is that the shared entry
    // prettifies the WHOLE arguments record, and its exact line breaks are that
    // formatter's business rather than this table's.
    { name: 'never read', args: prettifyArgsJson({ skill: 'deep-review', args: '--fix', name: 'never read' }) },
  ],
  [
    'switch_mode',
    CLAUDE_TOOL_NAMES.ENTER_WORKTREE,
    { name: 'feature-x', mode: 'never read', targetModeId: 'never read', target: 'never read' },
    { mode: 'worktree', target: 'feature-x' },
    { mode: 'never read', target: 'never read' },
  ],
  [
    'task',
    CLAUDE_TOOL_NAMES.TASK_OUTPUT,
    { shell_id: 's-1', taskId: 'never read', timeout: 5000, block: true },
    { action: 'output', taskId: 's-1', timeoutMs: 5000, block: true },
    { action: 'other', taskId: 'never read' },
  ],
  [
    'todo',
    CLAUDE_TOOL_NAMES.TODO_WRITE,
    { todos: [{ content: 'Write the parser', status: 'pending' }] },
    { items: [expect.objectContaining({ content: 'Write the parser', status: 'pending' })] },
    { items: [] },
  ],
  [
    'trigger',
    CLAUDE_TOOL_NAMES.REMOTE_TRIGGER,
    { action: 'create', body: { name: 'Nightly' }, trigger_id: 't-1', name: 'never read', schedule: '0 0 * * *' },
    { action: 'create', triggerId: 't-1', name: 'Nightly', schedule: '0 0 * * *' },
    // The id and the schedule come from the shared entry, which reads the same
    // spellings, so the two answers differ on the action and the label alone.
    { action: 'other', triggerId: 't-1', name: 'never read', schedule: '0 0 * * *' },
  ],
  [
    'wait',
    CLAUDE_TOOL_NAMES.SLEEP,
    { durationMs: 1500 },
    { durationMs: 1500 },
    // The shared entry omits an absent duration rather than stating undefined.
    {},
  ],
]

describe('CLAUDE_TOOL_REQUEST_OVERRIDES', () => {
  it('deviates on exactly the kinds a Claude fact fills', () => {
    expect(Object.keys(CLAUDE_TOOL_REQUEST_OVERRIDES).sort()).toStrictEqual([
      'agent',
      'agents',
      'edit',
      'execute',
      'list',
      'mcp',
      'message',
      'question',
      'skill',
      'switch_mode',
      'task',
      'todo',
      'trigger',
      'wait',
      'write',
    ])
  })

  it('states one case for every key it deviates on', () => {
    expect(OVERRIDE_CASES.map(([kind]) => kind).sort()).toStrictEqual(Object.keys(CLAUDE_TOOL_REQUEST_OVERRIDES).sort())
  })

  it.each(OVERRIDE_CASES)('reads a %s request from the keys Claude spells', (kind, toolName, input, request) => {
    const payload = claudeSpec(requestRow(toolName, input), undefined, {})
    expect(payload.kind).toBe(kind)
    expect(payload.request).toStrictEqual(request)
  })

  it.each(OVERRIDE_CASES)('answers a %s request the shared table cannot', (kind, _toolName, input, request, shared) => {
    expect(DEFAULT_TOOL_REQUESTS[kind](input)).toStrictEqual(shared)
    expect(request).not.toStrictEqual(shared)
  })

  // The second half of the agents entry, which the table case above does not reach: a
  // call that names no team states the two roster filters, trimmed.
  it('reads the roster filters trimmed when the call names no team', () => {
    expect(requestOf(CLAUDE_TOOL_NAMES.LIST_AGENTS, { channel: '  builds  ', q: '  parser  ' }))
      .toStrictEqual({ channel: 'builds', query: 'parser' })
    expect(requestOf(CLAUDE_TOOL_NAMES.LIST_AGENTS, { channel: '   ', q: '' }))
      .toStrictEqual({})
  })

  // The two plan brackets must state NO mode. `switchModeRenderer` titles the row from
  // `request.mode` first and reads the row's own title only when the request states none.
  it.each([CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE, CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE])('states no mode for %s', (toolName) => {
    expect(requestOf(toolName, { mode: 'never read' })).toStrictEqual({})
  })

  it('reads the stop action out of the TaskStop name', () => {
    // A stop that sends no timeout and no flag OMITS both keys rather than stating
    // them undefined.
    expect(requestOf(CLAUDE_TOOL_NAMES.TASK_STOP, { task_id: 't-1' }))
      .toStrictEqual({ action: 'stop', taskId: 't-1' })
  })

  // A `Task*` request reads the paired RESULT, which no other Claude request does: the
  // result row of all three is hidden, so the REQUEST row is what draws the task.
  it('reads the single task of a TaskCreate out of the paired result', () => {
    const request = requestOf(
      CLAUDE_TOOL_NAMES.TASK_CREATE,
      { subject: 'Write the parser' },
      { pairedResult: { task: { task_id: 'task-7', subject: 'Write the parser' } } },
    )
    // A task with no description OMITS the note rather than stating it undefined.
    expect(request).toStrictEqual({ items: [expect.objectContaining({ id: 'task-7', content: 'Write the parser' })] })
  })

  // The mcp entry answers from the tool NAME, so a name that spells no server/tool pair
  // states the whole name as the tool rather than leaving both halves empty.
  it('states the whole tool name as the tool when the name spells no pair', () => {
    expect(claudeRequestFor('mcp', { q: 1 }, { toolName: 'Plain', result: undefined, context: {} }))
      .toStrictEqual({ server: '', tool: 'Plain', args: { q: 1 } })
  })
})

/**
 * The kinds Claude takes from the shared table, with no entry of its own.
 *
 * Routing a near-copy onto the shared entry WIDENS what the request reads: the shared
 * entry answers key spellings Claude's own wire does not send. Each case below states
 * the arguments a real Claude call carries, and pins that the two tables agree on them.
 * The widening itself is pinned in the block after this one.
 */
const ROUTED_CASES: ReadonlyArray<readonly [ToolKind, string, Record<string, unknown>]> = [
  ['read', CLAUDE_TOOL_NAMES.READ, { file_path: '/project/a.ts', offset: 10, limit: 20 }],
  ['grep', CLAUDE_TOOL_NAMES.GREP, { pattern: 'needle', path: '/project' }],
  ['glob', CLAUDE_TOOL_NAMES.GLOB, { pattern: '**/*.ts', path: '/project' }],
  ['fetch', CLAUDE_TOOL_NAMES.WEB_FETCH, { url: 'https://example.com', prompt: 'Summarize it' }],
  ['web_search', CLAUDE_TOOL_NAMES.WEB_SEARCH, { query: 'renderer', allowed_domains: ['example.com'] }],
  ['report', CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT, { summary: 'done', findings: [] }],
  ['unspecified', 'ToolNobodyNames', { anything: 1 }],
]

describe('claudeRequestFor', () => {
  it('states no override for the kinds it routes to the shared table', () => {
    for (const [kind] of ROUTED_CASES)
      expect(CLAUDE_TOOL_REQUEST_OVERRIDES[kind], kind).toBeUndefined()
  })

  it.each(ROUTED_CASES)('answers the shared table for a %s request', (kind, toolName, input) => {
    const payload = claudeSpec(requestRow(toolName, input), undefined, {})
    expect(payload.kind).toBe(kind)
    expect(payload.request).toStrictEqual(DEFAULT_TOOL_REQUESTS[kind](input))
  })

  // Claude spells its tool inputs snake_case throughout, and `Sleep`'s `durationMs` is
  // the lone exception. So each widening below sits on a key no Claude tool sends, and
  // it changes no transcript -- the fallback fires only when the key Claude DOES send
  // is absent.
  it('reads the file path under the three spellings the shared entry states', () => {
    // The shared entry omits an offset and a limit the call did not state, so a bare
    // path answers the path alone.
    expect(requestOf(CLAUDE_TOOL_NAMES.READ, { file_path: '/a.ts' }))
      .toStrictEqual({ path: '/a.ts' })
    // `filePath` and `path` outrank `file_path`, and no Claude Read call sends either.
    expect(DEFAULT_TOOL_REQUESTS.read({ path: '/b.ts', file_path: '/a.ts' }))
      .toStrictEqual({ path: '/b.ts' })
  })

  it('falls back to `uri` for a fetch that states no url, which Claude never sends', () => {
    expect(requestOf(CLAUDE_TOOL_NAMES.WEB_FETCH, { url: 'https://example.com', uri: 'never read' }))
      .toStrictEqual({ url: 'https://example.com' })
    expect(requestOf(CLAUDE_TOOL_NAMES.WEB_FETCH, { uri: 'https://fallback.test' }))
      .toStrictEqual({ url: 'https://fallback.test' })
  })

  it('falls back to `q` for a search that states no query, which Claude never sends', () => {
    expect(requestOf(CLAUDE_TOOL_NAMES.WEB_SEARCH, { query: 'renderer', q: 'never read' }))
      .toStrictEqual({ query: 'renderer' })
    expect(requestOf(CLAUDE_TOOL_NAMES.WEB_SEARCH, { q: 'fallback' })).toStrictEqual({ query: 'fallback' })
  })

  it.each([CLAUDE_TOOL_NAMES.GREP, CLAUDE_TOOL_NAMES.GLOB])('falls back to `query` for a %s that states no pattern', (toolName) => {
    expect(requestOf(toolName, { pattern: 'needle', query: 'never read' }))
      .toStrictEqual({ pattern: 'needle', paths: [] })
    expect(requestOf(toolName, { query: 'fallback' })).toStrictEqual({ pattern: 'fallback', paths: [] })
  })

  // The ONE widening that changes an input Claude CAN produce. A result row whose
  // paired request is unresolved carries no arguments at all, and the shared entry
  // OMITS the payload for that call rather than stating an empty record -- which is
  // what `ReportRequest.payload` being optional means.
  it('states an absent report payload for a call that carries no arguments', () => {
    expect(requestOf(CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT, {})).toStrictEqual({})
    expect(requestOf(CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT, { summary: 'done' })).toStrictEqual({ payload: { summary: 'done' } })
  })
})
