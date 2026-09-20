import type { ToolCall } from '../../model/toolCall'
import { describe, expect, it } from 'vitest'
import { isToolFailureResult, typedResult } from '../../model/toolCall'
import { acpToolCall } from '../acp/extractors/toolCall'
import { openCodeToolCallAdapterFor } from '../opencode/extractors/toolCall'
import { kiloToolKind } from './toolKinds'

/**
 * The smallest arguments a tool must state for its own kind to build.
 *
 * `notebook_edit` is the one file change Kilo adds, and a file change states the FILE
 * it changes: the model refuses an `edit` whose request names none -- the row composes
 * its header from that list at every state of the call -- and degrades the call to
 * the uncategorized row. Kilo spells the three keys `path`, `old_string` and
 * `new_string`, which is why `openCodeToolCall` reads every alias of them.
 */
const MINIMAL_INPUT: Readonly<Record<string, Record<string, unknown>>> = {
  notebook_edit: { path: '/project/notes.ipynb', old_string: 'before', new_string: 'after' },
}

/** One Kilo tool call, with the kind Kilo's own protocol layer answers for it. */
function kiloCall(title: string, kind = 'other', tool: Record<string, unknown> = {}): ToolCall {
  return acpToolCall(
    { sessionUpdate: 'tool_call', toolCallId: 'kilo-tool', status: 'pending', kind, title, rawInput: MINIMAL_INPUT[title], ...tool },
    openCodeToolCallAdapterFor(kiloToolKind),
    undefined,
  )
}

describe('kiloToolKind', () => {
  // Kilo answers `other` for every tool it adds, so each of these rows drew the
  // generic wrench above its raw arguments.
  it.each([
    ['semantic_search', 'search'],
    ['repo_overview', 'list'],
    ['kilo_memory_recall', 'memory'],
    ['kilo_memory_save', 'memory'],
    ['board_post', 'memory'],
    ['board_read', 'memory'],
    ['agent_manager_models', 'agents'],
    ['notebook_edit', 'edit'],
    ['notebook_execute', 'execute'],
    ['notebook_read', 'read'],
    ['open_plan', 'read'],
    ['background_process', 'execute'],
    ['browser_open', 'fetch'],
    ['plan_exit', 'switch_mode'],
    // The five that used to keep `other`. Each one drew a wrench above its raw
    // arguments, and none of them ran a tool a wrench describes.
    ['notify_user', 'message'],
    ['send_file', 'message'],
    ['chart', 'chart'],
    ['generate_image', 'image'],
    ['goal_report', 'report'],
  ])('gives %s the kind %s', (title, kind) => {
    const call = kiloCall(title)
    expect(call.kind).toBe(kind)
    expect(call.name).toBe(title)
  })

  // Every tool the table lists reaches a kind, so a row NEVER carries the
  // uncategorized one. A name from a later Kilo release still falls through, which
  // is the one case `other` exists for.
  it('leaves only an unknown tool uncategorized', () => {
    expect(kiloToolKind('a_tool_from_a_later_release')).toBeUndefined()
    expect(kiloCall('a_tool_from_a_later_release').kind).toBe('mcp')
  })

  // Kilo's own protocol layer answers `search` for the context7 pair and `execute`
  // for a shell. The table must not overwrite a kind the frame already stated.
  it.each([
    ['context7_get_library_docs', 'search'],
    ['bash', 'execute'],
    ['read', 'read'],
  ])('keeps the kind the frame states for %s', (title, kind) => {
    expect(kiloCall(title, kind).kind).toBe(kind)
  })

  // OpenCode runs a different tool set behind the same wire format, so its rows must
  // not take Kilo's names.
  it('leaves an OpenCode row alone', () => {
    const call = acpToolCall(
      { sessionUpdate: 'tool_call', toolCallId: 'oc-tool', status: 'pending', kind: 'other', title: 'semantic_search' },
      openCodeToolCallAdapterFor(),
      undefined,
    )
    // The shared build narrows the protocol's own `other` to the kind of the card it
    // draws. Kilo's table would have said `search`; OpenCode has no such tool.
    expect(call.kind).toBe('mcp')
  })
})

// Kilo's `chart` answers with the Chart.js configuration it normalized, and its own
// description tells the model "the chart is the response". The row must draw that
// configuration; the JSON dump it drew instead is the response the tool forbade.
describe('the kilo chart tool', () => {
  const CONFIG = { type: 'bar', data: { labels: ['A', 'B'], datasets: [{ label: 'Hits', data: [3, 5] }] } }

  function chartRow(output: string, metadata?: Record<string, unknown>) {
    return acpToolCall({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'kilo-chart',
      status: 'completed',
      kind: 'other',
      title: 'chart',
      rawInput: { title: 'Weekly hits', spec: JSON.stringify(CONFIG) },
      rawOutput: metadata ? { metadata } : undefined,
      content: [{ type: 'content', content: { type: 'text', text: output } }],
    }, openCodeToolCallAdapterFor(kiloToolKind), undefined)
  }

  it('reads the returned configuration into a chart result', () => {
    const call = chartRow(JSON.stringify(CONFIG), { title: 'Weekly hits', description: 'per region' })
    expect(call.kind).toBe('chart')
    expect(call.label).toBe('Chart')
    const source = call.kind === 'chart' ? typedResult(call) : undefined
    expect(source?.shape).toBe('bar')
    expect(source?.title).toBe('Weekly hits')
    expect(source?.description).toBe('per region')
    expect(source?.labels).toEqual(['A', 'B'])
    expect(source?.series[0]?.values).toEqual([3, 5])
  })

  // A chart answers on the row that COMPLETES it, like every other kind. The branch
  // attached its result unconditionally, so a FAILED chart drew the red "not readable
  // JSON" notice where the daemon's own reason belongs.
  it('answers the daemon reason when the call failed', () => {
    const call = acpToolCall({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'kilo-chart',
      status: 'failed',
      kind: 'other',
      title: 'chart',
      rawInput: { spec: '{' },
      content: [{ type: 'content', content: { type: 'text', text: 'The chart tool is not installed' } }],
    }, openCodeToolCallAdapterFor(kiloToolKind), undefined)
    expect(call.kind).toBe('chart')
    expect(isToolFailureResult(call.result) && call.result.text).toBe('The chart tool is not installed')
  })

  it('states no arguments, so the configuration is not printed beside its own picture', () => {
    const call = chartRow(JSON.stringify(CONFIG))
    expect(call.kind === 'chart' && call.request ? call.request.spec : undefined).toBe(JSON.stringify(CONFIG))
  })

  // The tool answers with a sentence rather than a configuration when the model hands
  // it something it cannot parse. The row states that reason instead of a picture.
  it('carries the reason when the call returned no configuration', () => {
    const call = chartRow('Invalid chart spec: could not parse JSON.')
    const source = call.kind === 'chart' ? typedResult(call) : undefined
    expect(source?.error).toBe('The chart configuration is not readable JSON.')
  })

  // A row written before the tool returned carries the spec it was CALLED with in
  // its REQUEST, and no result at all. Drawing the picture there claimed an answer
  // the call had not given -- and, worse, a spec the model had not finished writing
  // drew the red "not readable JSON" notice for the whole run, while the result it
  // attached suppressed the live output tail the row would otherwise show.
  it('carries the spec it was called with and answers nothing yet', () => {
    const call = acpToolCall({
      sessionUpdate: 'tool_call',
      toolCallId: 'kilo-chart',
      status: 'pending',
      kind: 'other',
      title: 'chart',
      rawInput: { title: 'Weekly hits', spec: JSON.stringify(CONFIG) },
    }, openCodeToolCallAdapterFor(kiloToolKind), undefined)
    expect(call.kind).toBe('chart')
    expect(call.kind === 'chart' && call.request.spec).toBe(JSON.stringify(CONFIG))
    expect(call.kind === 'chart' && call.request.title).toBe('Weekly hits')
    expect(call.result).toBeUndefined()
  })
})
