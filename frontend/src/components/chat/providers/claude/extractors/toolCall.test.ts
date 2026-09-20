import type { ToolKind } from '../../../model/toolKind'
import type { ClaudeCallFacts } from './toolCall'
import type { ClaudeToolRow } from './toolCommon'
import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { TOOL_KINDS } from '../../../model/toolKind'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { input } from '../../testUtils'
import { claudeToolRowHidden } from '../toolKinds'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { CLAUDE_TOOL_READERS, claudeSpec } from './toolCall'
import { claudeToolRow } from './toolCommon'
import { CLAUDE_TOOL_REQUEST_OVERRIDES } from './toolRequests'

/** The reason the command line interface sends for a path that is not there. */
const REASON = 'File does not exist.'

/** The span sides an isolated extraction resolves to nothing. */
const NO_SIDES: ToolSpanContext = { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } }

/** One Claude REQUEST row: the tool's name and the arguments it sent. */
function requestRow(toolName: string, input: Record<string, unknown> = {}): ClaudeToolRow {
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

/** One Claude RESULT row, built from fields rather than from an envelope. */
function resultRow(toolName: string, overrides: Partial<ClaudeToolRow> = {}): ClaudeToolRow {
  return {
    id: 'tu_1',
    role: 'result',
    toolName,
    input: {},
    toolUseResult: undefined,
    resultContent: 'done',
    rawResultContent: 'done',
    images: [],
    isError: undefined,
    ...overrides,
  }
}

function factsOf(toolName: string, input: Record<string, unknown> = {}, result?: ClaudeToolRow): ClaudeCallFacts {
  return { args: requestRow(toolName, input), result, context: {} }
}

/**
 * One answered call of a tool no vocabulary lists, with an argument every declared
 * request can read something out of.
 *
 * Every reader runs against it in the case below, including the readers of the kinds
 * this row could never take. A reader that reaches for a fact this row does not carry
 * throws here rather than in the transcript, where the error boundary replaces the
 * whole message.
 */
const SMOKE_FACTS: ClaudeCallFacts = factsOf(
  'SomeToolAddedLater',
  { file_path: '/project/a.ts', pattern: '*.ts' },
  resultRow('SomeToolAddedLater'),
)

/**
 * The kinds Claude reads with no entry of its own.
 *
 * DERIVED rather than listed, so an override added later moves a kind out of this case
 * instead of leaving a stale name in it.
 */
const SHARED_REQUEST_KINDS: ToolKind[] = TOOL_KINDS.filter(
  kind => !Object.keys(CLAUDE_TOOL_REQUEST_OVERRIDES).includes(kind),
)

describe('CLAUDE_TOOL_READERS', () => {
  it('states one reader for every tool kind', () => {
    expect(Object.keys(CLAUDE_TOOL_READERS).sort()).toStrictEqual([...TOOL_KINDS].sort())
  })

  // The case the switch could not state. `claudeToolKind` answers `search` for
  // `ToolSearch`, the switch held no case for it, and the default branch built the
  // `unspecified` -- a search row with none of the fields the kind declares.
  it('answers each kind at the key that states it', () => {
    for (const kind of TOOL_KINDS)
      expect(CLAUDE_TOOL_READERS[kind](SMOKE_FACTS).kind, kind).toBe(kind)
  })

  // The label carries the kind, because the unspecified kind titles a case with nothing at
  // all and a reader cannot tell which row of the report it is.
  it.each(SHARED_REQUEST_KINDS.map(kind => [kind, kind] as const))(
    'fills the declared request of %s from the shared table',
    (_label, kind) => {
      expect(CLAUDE_TOOL_READERS[kind](SMOKE_FACTS).request).toStrictEqual(DEFAULT_TOOL_REQUESTS[kind](SMOKE_FACTS.args.input))
    },
  )

  // The other half of the same rule: a kind Claude DOES deviate on must not read the
  // shared entry. Without this the case above still passes over a table that lost every
  // override, because it walks the kinds that have none.
  it('states no reader that reads the shared table for a kind Claude overrides', () => {
    expect(SHARED_REQUEST_KINDS.length).toBeLessThan(TOOL_KINDS.length)
    const facts = factsOf(CLAUDE_TOOL_NAMES.SKILL, { skill: 'deep-review', args: '--all' })
    expect(CLAUDE_TOOL_READERS.skill(facts).request).toStrictEqual({ name: 'deep-review', args: '--all' })
    expect(DEFAULT_TOOL_REQUESTS.skill(facts.args.input)).not.toStrictEqual({ name: 'deep-review', args: '--all' })
  })
})

describe('claudeSpec', () => {
  // An MCP wire name carries its server and its tool inside itself, and the name table
  // holds no entry for one -- so the name table alone answers the unspecified kind.
  it('routes an MCP wire name to the mcp kind', () => {
    const payload = claudeSpec(requestRow('mcp__docs__lookup', { query: 'renderer' }), undefined, {})
    expect(payload.kind).toBe('mcp')
    expect(payload.kind === 'mcp' && payload.request).toStrictEqual({ server: 'docs', tool: 'lookup', args: { query: 'renderer' } })
  })

  it('takes the unspecified kind for a tool no vocabulary lists', () => {
    const payload = claudeSpec(requestRow('ToolNobodyNames', { anything: 1 }), undefined, {})
    expect(payload.kind).toBe('unspecified')
    expect(payload.kind === 'unspecified' && payload.request).toStrictEqual({ args: { anything: 1 } })
  })

  // The todo family splits on the tool NAME under one kind: `TodoWrite` states a list,
  // and a `Task*` call states the single task it acts on.
  it('titles a TaskCreate row with the words that call earns', () => {
    const payload = claudeSpec(
      requestRow(CLAUDE_TOOL_NAMES.TASK_CREATE, { subject: 'Write the parser' }),
      undefined,
      { pairedResult: { task: { task_id: 'task-7', subject: 'Write the parser' } } },
    )
    expect(payload.kind).toBe('todo')
    expect(payload.title).toBe('Task created')
  })

  it('states no title for a TodoWrite row', () => {
    const payload = claudeSpec(requestRow(CLAUDE_TOOL_NAMES.TODO_WRITE, { todos: [] }), undefined, {})
    expect(payload.kind).toBe('todo')
    expect(payload.title).toBeUndefined()
  })
})

/**
 * `ToolSearch` asks which DEFERRED tools exist before the model calls one.
 *
 * The tool registry is a corpus, so the kind is `search`: `claudeToolKind` and
 * `CLAUDE_TOOL_READERS` both answer it. A kind that reaches no reader entry builds the
 * `unspecified` instead, with none of the fields the kind declares, and the hiding two
 * files away is what would conceal that.
 *
 * The answer stays UNREAD on purpose. The matches are TOOL NAMES, and `filenames` and
 * `lines` are the two fields of `SearchResult` that state files -- `searchResultText`
 * relativizes a line as a path. The query fills the declared request all the same,
 * because that is what titles the row.
 */
describe('a ToolSearch call', () => {
  it('reads the query into the declared search request', () => {
    const payload = claudeSpec(requestRow(CLAUDE_TOOL_NAMES.TOOL_SEARCH, { query: 'select:Read,Glob' }), undefined, {})
    expect(payload.kind).toBe('search')
    expect(payload.kind === 'search' && payload.request).toStrictEqual({ pattern: 'select:Read,Glob', paths: [] })
    expect(payload.result).toBeUndefined()
  })

  it('leaves the matched tool names unread rather than drawing them as files', () => {
    const answered = resultRow(CLAUDE_TOOL_NAMES.TOOL_SEARCH, {
      resultContent: 'Read\nGlob',
      toolUseResult: { tool_name: CLAUDE_TOOL_NAMES.TOOL_SEARCH, matches: ['Read', 'Glob'], total_deferred_tools: 19 },
    })
    const payload = claudeSpec(requestRow(CLAUDE_TOOL_NAMES.TOOL_SEARCH, { query: 'select:Read,Glob' }), answered, {})
    expect(payload.kind).toBe('search')
    expect(payload.result).toStrictEqual({ unparsed: true, text: 'Read\nGlob' })
  })

  it('states the reason alone for a probe the tool failed', () => {
    const failed = resultRow(CLAUDE_TOOL_NAMES.TOOL_SEARCH, { resultContent: REASON, isError: true })
    const payload = claudeSpec(requestRow(CLAUDE_TOOL_NAMES.TOOL_SEARCH, { query: 'select:Read' }), failed, {})
    expect(payload.result).toStrictEqual({ failure: true, text: REASON })
  })

  // A REAL frame states its matches as `tool_reference` blocks, and the text walk reads
  // `text` blocks alone -- so the body of an un-hidden row would be empty under the
  // query. `resultContent` is set by hand in the two cases above, which cannot state
  // this, so the row here comes through `claudeToolRow` and the envelope it reads.
  it('answers an empty body for the tool_reference blocks a real frame carries', () => {
    const frame = {
      type: 'user',
      message: {
        role: 'user',
        content: [{
          type: 'tool_result',
          tool_use_id: 'tu_1',
          content: [{ type: 'tool_reference', tool_name: 'Read' }, { type: 'tool_reference', tool_name: 'Glob' }],
        }],
      },
      tool_use_result: { tool_name: CLAUDE_TOOL_NAMES.TOOL_SEARCH, matches: ['Read', 'Glob'], total_deferred_tools: 19 },
    }
    const answered = claudeToolRow(input(frame), CLAUDE_TOOL_NAMES.TOOL_SEARCH, NO_SIDES)
    expect(answered?.resultContent).toBe('')
    const payload = claudeSpec(requestRow(CLAUDE_TOOL_NAMES.TOOL_SEARCH, { query: 'select:Read,Glob' }), answered ?? undefined, {})
    expect(payload.result).toStrictEqual({ unparsed: true, text: '' })
  })

  // The kind resolves, and the rows stay hidden. The two statements are separate: the
  // probe says nothing a reader acts on, so drawing it is noise whether or not the
  // payload is well formed.
  it('draws neither of its two rows', () => {
    expect(claudeToolRowHidden(CLAUDE_TOOL_NAMES.TOOL_SEARCH, 'request')).toBe(true)
    expect(claudeToolRowHidden(CLAUDE_TOOL_NAMES.TOOL_SEARCH, 'result')).toBe(true)
  })
})

/**
 * The four file tools, across the two kinds `CLAUDE_TOOL_KINDS` maps them onto.
 *
 * The reader table is the one place that split is made now. The shared builder used to
 * read the tool name a second time to choose between `edit` and `write`, so the mapping
 * stood in two places and nothing held them together.
 */
describe('a Claude file change', () => {
  it.each([
    [CLAUDE_TOOL_NAMES.WRITE, 'write'],
    [CLAUDE_TOOL_NAMES.EDIT, 'edit'],
    [CLAUDE_TOOL_NAMES.MULTI_EDIT, 'edit'],
    [CLAUDE_TOOL_NAMES.NOTEBOOK_EDIT, 'edit'],
  ] as const)('reads %s as the %s kind', (toolName, kind) => {
    expect(claudeSpec(requestRow(toolName, { file_path: '/project/a.ts' }), undefined, {}).kind).toBe(kind)
  })

  it('states the landed diff of an answered edit', () => {
    const answered = resultRow(CLAUDE_TOOL_NAMES.EDIT, { toolUseResult: { filePath: '/project/a.ts', oldString: 'before', newString: 'after' } })
    const payload = claudeSpec(requestRow(CLAUDE_TOOL_NAMES.EDIT, { file_path: '/project/a.ts', old_string: 'before', new_string: 'after' }), answered, {})
    expect(payload.kind === 'edit' && payload.result).toStrictEqual({
      // A record that states no original file OMITS the key rather than stating
      // it undefined.
      changes: [{ filePath: '/project/a.ts', structuredPatch: null, oldStr: 'before', newStr: 'after' }],
    })
  })

  // A failed edit KEEPS the change it asked for. `RequestedChangesBody` refuses to draw
  // a diff under a failed row, so emptying the request removed nothing from the body
  // and took the FILE NAME out of the row's title instead.
  it('keeps the file a failed edit asked to change', () => {
    const failed = resultRow(CLAUDE_TOOL_NAMES.EDIT, { resultContent: REASON, isError: true })
    const payload = claudeSpec(requestRow(CLAUDE_TOOL_NAMES.EDIT, { file_path: '/project/a.ts', old_string: 'before', new_string: 'after' }), failed, {})
    expect(payload.result).toStrictEqual({ failure: true, text: REASON })
    expect(payload.kind === 'edit' && payload.request.changes[0]?.filePath).toBe('/project/a.ts')
  })
})

/**
 * The generic card of a tool no vocabulary lists.
 *
 * Its pictures ride INSIDE the result content, which is what `ToolCallBase.images`
 * states for the generic trio: `claudeToolCall` empties the envelope's own list for
 * those kinds.
 */
describe('a generic Claude call', () => {
  it('states the words and the pictures in one content list', () => {
    const answered = resultRow('ToolNobodyNames', { resultContent: 'rendered', images: [{ data: 'AAAA', mimeType: 'image/png' }] })
    const payload = claudeSpec(requestRow('ToolNobodyNames'), answered, {})
    expect(payload.result).toStrictEqual({
      content: [{ type: 'text', text: 'rendered' }, { type: 'image', source: { data: 'AAAA', mimeType: 'image/png' } }],
    })
    expect(payload.statusOverride).toBeUndefined()
  })

  // A `ToolFailureResult` holds text alone, so a failed call that returned PICTURES keeps
  // them in the content and states its outcome word in `statusOverride` instead.
  it('keeps the pictures a failed call returned and states the outcome beside them', () => {
    const answered = resultRow('ToolNobodyNames', { resultContent: REASON, isError: true, images: [{ data: 'AAAA', mimeType: 'image/png' }] })
    const payload = claudeSpec(requestRow('ToolNobodyNames'), answered, {})
    expect(payload.statusOverride).toBe('failed')
    expect(payload.result).toStrictEqual({
      content: [{ type: 'text', text: REASON }, { type: 'image', source: { data: 'AAAA', mimeType: 'image/png' } }],
    })
  })

  it('states the reason alone for a failed call that returned no picture', () => {
    const answered = resultRow('ToolNobodyNames', { resultContent: REASON, isError: true })
    const payload = claudeSpec(requestRow('ToolNobodyNames'), answered, {})
    expect(payload.result).toStrictEqual({ failure: true, text: REASON })
    expect(payload.statusOverride).toBeUndefined()
  })
})
