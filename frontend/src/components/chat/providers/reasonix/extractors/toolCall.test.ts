import type { ToolCall } from '../../../model/toolCall'
import { describe, expect, it } from 'vitest'
import { REASONIX_CAPABILITY_ACTION, REASONIX_TOOL } from '~/generated/contracts/reasonix-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isToolFailureResult, isUnparsedToolResult, typedResult } from '../../../model/toolCall'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { reasonixToolCallAdapter } from './toolCall'

function call(tool: Record<string, unknown>, supplemental?: Record<string, unknown>): ToolCall {
  const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-tool', status: 'completed', kind: 'other', ...tool }
  const extra = supplemental
    ? { sessionUpdate: frame.sessionUpdate, toolCallId: frame.toolCallId, status: frame.status, ...supplemental }
    : undefined
  return acpToolCall(frame, reasonixToolCallAdapter, extra)
}

function capability(capabilityId: string): ToolCall {
  return call({
    title: REASONIX_TOOL.UseCapability,
    rawInput: { action: REASONIX_CAPABILITY_ACTION.Call, capability_id: capabilityId, arguments: { query: 'needle' } },
  })
}

describe('reasonix Model Context Protocol names', () => {
  it('splits a native tool name into its server and its tool', () => {
    const named = call({ title: 'mcp__server__lookup' })
    // No label: every provider's MCP card heads itself with the server and the
    // tool, which identifies what actually ran.
    expect(named.label).toBeUndefined()
    expect(named.title).toBe('server / lookup')
  })

  it('keeps every further separator in the tool half', () => {
    expect(call({ title: 'mcp__server__group__lookup' }).title).toBe('server / group__lookup')
  })

  // An empty half labels the call with nothing, which states less than the raw name.
  it.each(['mcp__server__', 'mcp____lookup', 'mcp__server', 'mcp__'])('refuses the name %s', (name) => {
    const named = call({ title: name })
    // Unsplit: the row keeps the raw name rather than heading itself with a
    // server / tool pair it could not read.
    expect(named.title).not.toContain(' / ')
    expect(named.name).toBe(name)
  })

  it('splits a capability identifier into its server and its tool', () => {
    const named = capability('mcp-tool:server/lookup')
    expect(named.label).toBeUndefined()
    expect(named.title).toBe('server / lookup')
    expect(named.kind === 'mcp' ? named.request.args : undefined).toEqual({ query: 'needle' })
  })

  it.each(['mcp-tool:server/', 'mcp-tool:/lookup', 'mcp-tool:lookup', 'mcp-tool:'])('refuses the capability identifier %s', (id) => {
    expect(capability(id).title).not.toContain(' / ')
  })
})

describe('reasonix subagent launches', () => {
  it('asks with the description and reports the run', () => {
    const launch = call({ title: REASONIX_TOOL.Task, rawInput: { description: 'Inspect sample', profile: 'explore', prompt: 'Read it' }, content: [{ type: 'content', content: { type: 'text', text: 'Found two' } }] })
    expect(launch.kind).toBe('agent')
    expect(launch.kind === 'agent' ? launch.request : undefined).toEqual({ description: 'Inspect sample', agentType: 'explore', prompt: 'Read it' })
    expect(launch.kind === 'agent' ? typedResult(launch)?.agents[0]?.body : undefined).toBe('Found two')
  })

  it('falls back to the shared word when the launch describes nothing', () => {
    const launch = call({ title: REASONIX_TOOL.ReadOnlyTask, rawInput: { prompt: 'Read it' }, status: 'pending' })
    expect(launch.kind).toBe('agent')
    expect(launch.result).toBeUndefined()
  })
})

// The supplement echoes the call it answers, which is what the shared ACP resolver
// matches it on before the plugin sees it.
function withStoredBody(fields: Record<string, unknown>, record: Record<string, unknown>): ToolCall {
  return call({ content: [{ type: 'content', content: { type: 'text', text: 'clipped…(4 more chars truncated)' } }], ...fields }, {
    rawOutput: { reasonix: { role: 'tool', tool_call_id: 'reasonix-tool', ...record } },
  })
}

describe('reasonix stored tool records', () => {
  // The plugin reads `raw_content` first and `content` second, so a field name that
  // stopped matching falls through to the shorter body instead of failing the build.
  // The Go tags are pinned to the same contract in reasonix_tool_store_test.go.
  it('prefers the unabridged body the record carries', () => {
    const whole = withStoredBody({}, { name: 'bash', content: 'short body', raw_content: 'the whole body' })
    expect(whole.kind === 'execute' && typedResult(whole)?.commands[0]?.output).toBe('the whole body')
    const short = withStoredBody({}, { name: 'bash', content: 'short body' })
    expect(short.kind === 'execute' && typedResult(short)?.commands[0]?.output).toBe('short body')
  })

  it('takes the tool name from the record when the call states none', () => {
    const listed = withStoredBody({ title: '' }, { name: 'ls', content: 'a.ts\t4\n' })
    expect(listed.kind).toBe('list')
    const unnamed = withStoredBody({ title: '' }, { content: 'words' })
    expect(unnamed.kind).not.toBe('list')
  })

  // A refused record leaves the call on the frame's own words.
  it('refuses a record that answers another call', () => {
    const other = withStoredBody({}, { tool_call_id: 'another-call', name: 'bash', content: 'stored body' })
    expect(other.kind).not.toBe('execute')
    expect(other.kind === 'mcp' && typedResult(other)?.content[0]?.type === 'text' ? other.kind === 'mcp' && (typedResult(other)!.content[0] as { text: string }).text : '').toContain('clipped')
  })

  it('refuses a record that is not a tool record', () => {
    const other = withStoredBody({}, { role: 'assistant', name: 'bash', content: 'stored body' })
    expect(other.kind).not.toBe('execute')
    expect(other.kind === 'mcp' && typedResult(other)?.content[0]?.type === 'text' ? other.kind === 'mcp' && (typedResult(other)!.content[0] as { text: string }).text : '').toContain('clipped')
  })
})

/**
 * The remapped call reads the facts AGAIN, at the kind and the frame it now claims.
 *
 * A hand-written clone of the facts kept every derived fact at its pre-remap value.
 * The text is the one that bites: the adapter rebuilds the frame around the content
 * the worker RECOVERED, and the clone went on carrying the text collected from the
 * frame that holds none.
 */
describe('reasonix remapped facts', () => {
  it('reads a recovered file body that is not numbered', () => {
    const read = withStoredBody({ title: 'read_file', rawInput: { path: '/p/a.ts' }, content: [] }, {
      name: 'read_file',
      content: 'plain body, no line numbers',
    })
    expect(read.kind).toBe('read')
    expect(read.kind === 'read' && read.request.path).toBe('/p/a.ts')
    expect(isUnparsedToolResult(read.result) && read.result.text).toBe('plain body, no line numbers')
  })

  it('recovers the file path the record states when the arguments carry none', () => {
    const read = withStoredBody({ title: 'read_file', rawInput: {}, content: [] }, {
      name: 'read_file',
      content: '1\tconst a = 1\n',
      read_result: { source: { canonical_path: '/p/recovered.ts' } },
    })
    expect(read.kind === 'read' && read.request.path).toBe('/p/recovered.ts')
  })
})

/**
 * A move identifies BOTH files for the whole time it runs.
 *
 * The branch that read them was guarded on `completed` and spelled the two argument
 * keys by hand, so an in-progress, failed, cancelled or retained-succeeded move drew
 * the word "Move" over a card that stated no file at all.
 */
describe('reasonix move_file', () => {
  const rawInput = { source_path: '/p/a.ts', destination_path: '/p/b.ts' }
  const moved = { filePath: '/p/b.ts', previousPath: '/p/a.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null }

  it.each(['in_progress', 'failed', 'cancelled', 'completed'])('identifies both files while the call is %s', (status) => {
    const move = call({ title: 'move_file', status, rawInput })
    expect(move.kind).toBe('move')
    expect(move.kind === 'move' && move.request.changes).toEqual([moved])
  })

  // The turn RETAINED the row: the frame still says `in_progress`, and the completion
  // is the only thing that says the call ended.
  it('identifies both files on a retained row the frame never completed', () => {
    const move = acpToolCall(
      { sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-tool', status: 'in_progress', kind: 'other', title: 'move_file', rawInput },
      reasonixToolCallAdapter,
      undefined,
      MessageCompletion.COMPLETE,
    )
    expect(move.kind === 'move' && move.request.changes).toEqual([moved])
  })

  it('states the daemon words beside the two files', () => {
    const move = call({ title: 'move_file', rawInput, content: [{ type: 'content', content: { text: 'Moved a.ts to b.ts' } }] })
    expect(isUnparsedToolResult(move.result) && move.result.text).toBe('Moved a.ts to b.ts')
  })
})

/**
 * Whether Reasonix RECOGNIZED an empty result. It prints one wording for both halves.
 *
 * The extractor already read that sentence to build the counters -- `empty` drives
 * both `fallbackContent` and the zero match total. The flag states the same fact for
 * the row, so the renderer no longer measures a provider's bytes against LeapMux's
 * own summary prose to guess it.
 */
describe('reasonixToolCallAdapter empty search results', () => {
  const searchCall = (name: 'glob' | 'grep', output: string) => call({
    kind: name,
    title: name,
    rawInput: { pattern: 'x' },
    content: [{ type: 'content', content: { text: output } }],
  })

  const emptyOf = (name: 'glob' | 'grep', output: string) => {
    const built = searchCall(name, output)
    return built.kind === 'glob' || built.kind === 'grep' ? typedResult(built)?.empty : undefined
  }

  it('reads the wording Reasonix prints when it matched nothing', () => {
    expect(emptyOf('glob', '(no matches)')).toBe(true)
    expect(emptyOf('grep', '(no matches)')).toBe(true)
    expect(emptyOf('glob', '')).toBe(true)
  })

  it('states no empty result for a search that found something', () => {
    expect(emptyOf('glob', 'src/a.ts')).toBe(false)
    expect(emptyOf('grep', 'src/a.ts:1:hit')).toBe(false)
  })
})

/**
 * The field arithmetic a grep result carries: one count for the match LINES, and one for
 * the distinct FILES they sit in.
 *
 * The two numbers differ the moment one file holds two matches, and the summary states
 * both. `grepMatches` is the shared reading of grep's own `path:line:text` contract; the
 * empty WORDING above it is Reasonix's own, and stays in that module.
 */
describe('reasonixToolCallAdapter grep counters', () => {
  const grepResult = (output: string) => {
    const built = call({
      kind: 'grep',
      title: 'grep',
      rawInput: { pattern: 'x' },
      content: [{ type: 'content', content: { text: output } }],
    })
    return built.kind === 'grep' ? typedResult(built) : undefined
  }

  it('counts the files apart from the matches', () => {
    const source = grepResult('a.ts:1:hit\na.ts:7:hit\nb.ts:2:hit')
    expect(source?.numFiles).toBe(2)
    expect(source?.numLines).toBe(3)
    expect(source?.matchCount).toBe(3)
  })

  it('counts no line that states no match', () => {
    const source = grepResult('a.ts:1:hit\n... (truncated)')
    expect(source?.numFiles).toBe(1)
    expect(source?.numLines).toBe(1)
    expect(source?.truncated).toBe(true)
  })

  it('leaves a colon inside the matched text out of the file count', () => {
    const source = grepResult('a.ts:1:see http://example.com:8080/x\na.ts:2:plain')
    expect(source?.numFiles).toBe(1)
    expect(source?.numLines).toBe(2)
  })

  // Absent, never zero: a body this build read no match out of is a different statement
  // from a grep that matched nothing, and only Reasonix's own wording states the second.
  it('states no match total for a body it read no match out of', () => {
    expect(grepResult('unreadable output')?.matchCount).toBeUndefined()
    expect(grepResult('(no matches)')?.matchCount).toBe(0)
  })
})

/**
 * The shared table reads the search and the directory requests out of the same
 * arguments, under every path alias and a native `paths` array.
 *
 * Each of these branches replaced that request with a narrower copy of its own, which
 * read `input.path` alone -- so a call that stated its target under `filePath`,
 * `file_path` or `paths` drew a card with no target at all.
 */
describe('reasonixToolCallAdapter requests the shared table states', () => {
  it.each(['glob', 'grep'])('reads every path alias of a %s call', (name) => {
    const aliased = call({ title: name, rawInput: { pattern: '*.ts', file_path: '/p/src' } })
    expect(aliased.kind).toBe(name)
    expect(aliased.kind === 'glob' || aliased.kind === 'grep' ? aliased.request : undefined)
      .toEqual({ pattern: '*.ts', paths: ['/p/src'] })
  })

  it.each(['glob', 'grep'])('reads a native paths array of a %s call', (name) => {
    const many = call({ title: name, rawInput: { pattern: '*.ts', paths: ['/p/src', '/p/test'] } })
    expect(many.kind === 'glob' || many.kind === 'grep' ? many.request.paths : undefined).toEqual(['/p/src', '/p/test'])
  })

  // ONE branch answers both states of `ls`, so the label is spelled once and the
  // request reads the same keys whether or not the call finished.
  it.each(['in_progress', 'completed'])('reads every path alias of an ls call that is %s', (status) => {
    const listed = call({ title: 'ls', status, rawInput: { filePath: '/p/src' } })
    expect(listed.kind).toBe('list')
    expect(listed.label).toBe('List Files')
    expect(listed.kind === 'list' && listed.request.path).toBe('/p/src')
  })

  it('states the working directory for an ls call that carries no path at all', () => {
    const listed = call({ title: 'ls', rawInput: {} })
    expect(listed.kind === 'list' && listed.request.path).toBe('.')
  })

  it('reads the directory listing a completed ls call printed', () => {
    const listed = call({ title: 'ls', rawInput: { path: '/p/src' }, content: [{ type: 'content', content: { text: 'a.ts\nb.ts\n' } }] })
    expect(listed.kind === 'list' ? typedResult(listed)?.entries : undefined).toEqual([{ path: 'a.ts' }, { path: 'b.ts' }])
  })
})

/**
 * The shared ACP ladder states a failed call's reason for every kind it builds, and
 * this branch answers ahead of that ladder. Without the same test a failed server call
 * drew its (usually empty) card and `[no output]` where the reason belongs.
 */
describe('reasonixToolCallAdapter server calls', () => {
  it('states the reason a failed server call gave', () => {
    const errored = call({
      title: 'mcp__server__lookup',
      status: 'failed',
      rawInput: { query: 'needle' },
      content: [{ type: 'content', content: { text: 'the server is not reachable' } }],
    })
    expect(errored.kind).toBe('mcp')
    expect(isToolFailureResult(errored.result) && errored.result.text).toBe('the server is not reachable')
  })

  // A call the reader STOPPED is not a failure. The blocks that arrived are the part
  // of the answer they asked to see, so the card keeps them; the `Interrupted` header
  // comes from the row's own status.
  it('keeps the card a cancelled server call had built', () => {
    const stopped = call({
      title: 'mcp__server__lookup',
      status: 'cancelled',
      rawInput: { query: 'needle' },
      content: [{ type: 'content', content: { type: 'text', text: 'one hit so far' } }],
    })
    expect(stopped.kind).toBe('mcp')
    expect(isToolFailureResult(stopped.result)).toBe(false)
    expect(stopped.result).toStrictEqual({ content: [{ type: 'text', text: 'one hit so far' }] })
  })

  it('draws the card of a server call that answered', () => {
    const answered = call({
      title: 'mcp__server__lookup',
      rawInput: { query: 'needle' },
      content: [{ type: 'content', content: { type: 'text', text: 'two hits' } }],
    })
    expect(answered.kind).toBe('mcp')
    expect(answered.result).toMatchObject({ content: [{ type: 'text', text: 'two hits' }] })
  })

  it('answers nothing while the server call still runs', () => {
    expect(call({ title: 'mcp__server__lookup', status: 'in_progress', rawInput: {} }).result).toBeUndefined()
  })
})

/**
 * The checklist survives every outcome, because the tool ASKED for it.
 *
 * `todo_write` is the one tool `REASONIX_TOOL_KINDS` omits, so a row that falls past
 * this branch lands on the kind the WIRE states -- and Reasonix sends `edit` for a
 * checklist. A status test here therefore drew a failed list as a file change that
 * states no file.
 */
describe('reasonix todo_write', () => {
  const todoCall = (status: string, tool: Record<string, unknown> = {}) => call({
    title: 'todo_write',
    kind: 'edit',
    status,
    rawInput: { todos: [{ content: 'Inspect code', status: 'in_progress' }, { content: 'Run checks', status: 'pending' }] },
    ...tool,
  })

  const saved = [
    { rowKey: '0:Inspect code', content: 'Inspect code', status: 'in_progress', activeForm: '' },
    { rowKey: '1:Run checks', content: 'Run checks', status: 'pending', activeForm: '' },
  ]

  it('answers a completed list with the tasks it saved', () => {
    const written = todoCall('completed', { content: [{ type: 'content', content: { type: 'text', text: 'Todos updated' } }] })
    expect(written.kind).toBe('todo')
    expect(written.kind === 'todo' ? typedResult(written)?.items : undefined).toStrictEqual(saved)
  })

  // The reason, under the checklist's OWN kind. A status test here dropped the row to
  // the wire kind, where it drew as an edit that states no file.
  it('states the reason a failed list gave', () => {
    const errored = todoCall('failed', { content: [{ type: 'content', content: { text: 'the todo file is read only' } }] })
    expect(errored.kind).toBe('todo')
    expect(errored.kind === 'todo' ? errored.request.items : undefined).toStrictEqual(saved)
    expect(isToolFailureResult(errored.result) && errored.result.text).toBe('the todo file is read only')
  })

  // A call the reader STOPPED keeps the list it collected. The row marks it partial
  // from its own status, so nothing here states that.
  it('keeps the list a cancelled call collected', () => {
    const stopped = todoCall('cancelled', { content: [{ type: 'content', content: { text: 'stopped' } }] })
    expect(stopped.kind).toBe('todo')
    expect(isToolFailureResult(stopped.result)).toBe(false)
    expect(stopped.kind === 'todo' ? typedResult(stopped)?.items : undefined).toStrictEqual(saved)
  })

  it('answers nothing while the list is still being written', () => {
    const running = todoCall('in_progress')
    expect(running.kind).toBe('todo')
    expect(running.result).toBeUndefined()
  })
})

/**
 * A removal states the file it removes from, at every state of the call.
 *
 * The row composes its header from the REQUEST whenever no result answers for it, and
 * `RequestedChangesBody` already keeps the two bodies apart -- it draws nothing once a
 * result exists. An emptied request therefore only took the file out of the header.
 */
describe('reasonix delete_range', () => {
  const DIFF = '--- a/p/file.ts\n+++ b/p/file.ts\n@@ -4,3 +4,1 @@\n-removedFirst\n-removedSecond\n remaining\n'

  const deleteCall = (status: string, content: string) => call({
    title: 'delete_range',
    kind: 'delete',
    status,
    rawInput: { path: '/p/file.ts', start_anchor: 'delete', end_anchor: 'end' },
    content: [{ type: 'content', content: { type: 'text', text: content } }],
  })

  it('states the file the removal asked for beside the diff it applied', () => {
    const removed = deleteCall('completed', DIFF)
    expect(removed.kind).toBe('delete')
    expect(removed.kind === 'delete' ? removed.request.changes : undefined).toStrictEqual([
      { filePath: '/p/file.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null },
    ])
    expect(removed.kind === 'delete' ? typedResult(removed)?.changes.map(change => change.filePath) : undefined).toStrictEqual(['/p/file.ts'])
  })

  // The branch answers a completed call alone, and the request still states the file
  // for every other state.
  it.each(['in_progress', 'failed', 'cancelled'])('states the file while the call is %s', (status) => {
    const removed = deleteCall(status, 'the range could not be found')
    expect(removed.kind).toBe('delete')
    expect(removed.kind === 'delete' ? removed.request.changes.map(change => change.filePath) : undefined).toStrictEqual(['/p/file.ts'])
  })
})
