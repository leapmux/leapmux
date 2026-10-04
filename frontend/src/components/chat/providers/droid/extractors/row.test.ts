import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isGenericToolResult } from '../../../model/tools/generic'
import { resolveMessageForRendering } from '../../registry'
import { classifyDroidMessage } from '../classification'
import { droidExtractRow } from './row'

function resolvedDroidFrame(frame: Record<string, unknown>) {
  return resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.DROID)
}

describe('droidExtractRow', () => {
  it('normalizes a native TodoWrite request before the typed renderer reads it', () => {
    const request = resolvedDroidFrame({
      type: 'tool_call',
      toolUse: { id: 'todo-1', name: 'TodoWrite', input: { todos: [
        { content: 'Inspect the repository', status: 'completed' },
        { content: 'List three checks', status: 'in_progress' },
      ] } },
    })
    const span: ToolSpanContext = { request, result: undefined, role: 'request', visibleRows: { request: true, result: false } }
    const row = droidExtractRow({ resolved: request, category: classifyDroidMessage({ ...request, agentProvider: AgentProvider.DROID }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool' || row.call.kind !== 'todo')
      throw new Error('The native Droid TodoWrite request requires a to-do row.')
    expect(row.call.request.items).toEqual([
      { rowKey: '0:Inspect the repository', content: 'Inspect the repository', status: 'completed', activeForm: '' },
      { rowKey: '1:List three checks', content: 'List three checks', status: 'in_progress', activeForm: '' },
    ])
  })

  it('normalizes Droid Read file_path into the typed path field', () => {
    const request = resolvedDroidFrame({ type: 'tool_call', toolUse: { id: 'read-1', name: 'Read', input: { file_path: '/repo/shot.png' } } })
    const span: ToolSpanContext = { request, result: undefined, role: 'request', visibleRows: { request: true, result: false } }
    const row = droidExtractRow({ resolved: request, category: classifyDroidMessage({ ...request, agentProvider: AgentProvider.DROID }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool' || row.call.kind !== 'read')
      throw new Error('The native Droid Read request requires a read row.')
    expect(row.call.request.path).toBe('/repo/shot.png')
  })

  it('keeps native image bytes and text from a Read tool result', () => {
    const request = resolvedDroidFrame({ type: 'tool_call', toolUse: { id: 'read-1', name: 'Read', input: { file_path: '/repo/shot.png' } } })
    const result = resolvedDroidFrame({
      type: 'tool_result',
      toolUseId: 'read-1',
      content: [
        { type: 'text', text: 'Image file: shot.png' },
        { type: 'image', source: { type: 'base64', mediaType: 'image/png', data: 'iVBORw0KGgo=' } },
      ],
    })
    const span: ToolSpanContext = { request, result, role: 'result', visibleRows: { request: true, result: true } }
    const row = droidExtractRow({ resolved: result, category: classifyDroidMessage({ ...result, agentProvider: AgentProvider.DROID }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool' || row.call.kind !== 'other')
      throw new Error('The native Droid result requires a generic tool row.')
    expect(isGenericToolResult(row.call.result)).toBe(true)
    if (!isGenericToolResult(row.call.result))
      throw new Error('The native Droid result requires generic content.')
    expect(row.call.result.content).toEqual([
      { type: 'text', text: 'Image file: shot.png' },
      { type: 'image', source: { mimeType: 'image/png', data: 'iVBORw0KGgo=' } },
    ])
  })

  it('keeps a plain native result as one text block', () => {
    const result = resolvedDroidFrame({ type: 'tool_result', toolUseId: 'read-2', content: 'plain output' })
    const span: ToolSpanContext = { request: undefined, result, role: 'result', visibleRows: { request: false, result: true } }
    const row = droidExtractRow({ resolved: result, category: classifyDroidMessage({ ...result, agentProvider: AgentProvider.DROID }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool' || row.call.kind !== 'other' || !isGenericToolResult(row.call.result))
      throw new Error('The native Droid result requires generic content.')
    expect(row.call.result.content).toEqual([{ type: 'text', text: 'plain output' }])
  })

  it.each(['Read', 'Execute'])('keeps the matched native opener name on a nameless %s result', (name) => {
    const request = resolvedDroidFrame({ type: 'tool_call', toolUse: { id: 'native-call', name, input: { command: 'native command' } } })
    const result = resolvedDroidFrame({ type: 'tool_result', toolUseId: 'native-call', content: 'native preview' })
    const span: ToolSpanContext = { request, result, role: 'result', visibleRows: { request: true, result: true } }
    const row = droidExtractRow({ resolved: result, category: classifyDroidMessage({ ...result, agentProvider: AgentProvider.DROID }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The native Droid result requires a tool row.')
    expect(row.call.name).toBe(name)
    expect(isGenericToolResult(row.call.result)).toBe(true)
  })

  it('refuses tool names and arguments from a different native opener', () => {
    const request = resolvedDroidFrame({ type: 'tool_call', toolUse: { id: 'foreign-call', name: 'Execute', input: { command: 'foreign command' } } })
    const result = resolvedDroidFrame({ type: 'tool_result', toolUseId: 'native-call', content: 'native preview' })
    const span: ToolSpanContext = { request, result, role: 'result', visibleRows: { request: true, result: true } }
    const row = droidExtractRow({ resolved: result, category: classifyDroidMessage({ ...result, agentProvider: AgentProvider.DROID }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool' || row.call.kind !== 'other')
      throw new Error('The native Droid result requires a generic tool row.')
    expect(row.call.name).toBe('Tool')
    expect(row.call.request.args).toEqual({})
  })

  it('keeps an explicit native result name before the matched opener name', () => {
    const request = resolvedDroidFrame({ type: 'tool_call', toolUse: { id: 'native-call', name: 'Read', input: {} } })
    const result = resolvedDroidFrame({ type: 'tool_result', toolUseId: 'native-call', toolName: 'Execute', content: 'native preview' })
    const span: ToolSpanContext = { request, result, role: 'result', visibleRows: { request: true, result: true } }
    const row = droidExtractRow({ resolved: result, category: classifyDroidMessage({ ...result, agentProvider: AgentProvider.DROID }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The native Droid result requires a tool row.')
    expect(row.call.name).toBe('Execute')
  })
})
