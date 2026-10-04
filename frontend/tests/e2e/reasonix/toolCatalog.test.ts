import { describe, expect, it } from 'vitest'
import { assertReasonixCapabilityInspection, assertReasonixCoreCatalog, parseReasonixCapabilities } from './toolCatalog'
import { reasonixCoreTools } from './toolCatalog.fixtures'

describe('assertReasonixCoreCatalog', () => {
  it('accepts every actual captured core descriptor from the installed native request', () => {
    expect(() => assertReasonixCoreCatalog({ body: { tools: reasonixCoreTools } })).not.toThrow()
  })

  it.each([undefined, null, {}, [], 'tools', 0, false])('rejects an absent or malformed complete model catalog: %j', (tools) => {
    expect(() => assertReasonixCoreCatalog({ body: { tools } })).toThrow('Reasonix')
  })

  it('rejects a missing or repeated core descriptor', () => {
    expect(() => assertReasonixCoreCatalog({ body: { tools: reasonixCoreTools.slice(1) } })).toThrow('complete installed')
    expect(() => assertReasonixCoreCatalog({ body: { tools: [...reasonixCoreTools.slice(1), reasonixCoreTools[1]] } })).toThrow('unaudited or incomplete')
  })

  it('rejects a new deferred executor in the complete native model catalog', () => {
    const tools = [...reasonixCoreTools.slice(1), { type: 'function', function: { name: 'general_script', description: 'Execute native source.', parameters: { type: 'object', properties: { source: { type: 'string' } } } } }]
    expect(() => assertReasonixCoreCatalog({ body: { tools } })).toThrow('unaudited or incomplete')
  })

  it('rejects a changed argument interface under an existing tool identity', () => {
    const tools = [...structuredClone(reasonixCoreTools)]
    tools[0] = { type: 'function', function: { name: 'ask', description: 'An altered installed interface.', parameters: { type: 'object', properties: { script: { type: 'string' } } } } }
    expect(() => assertReasonixCoreCatalog({ body: { tools } })).toThrow('unaudited or incomplete')
  })
})

describe('parseReasonixCapabilities', () => {
  const tool = { id: 'tool:notebook_edit', kind: 'tool', name: 'notebook_edit', description: 'Edit a notebook cell.' }
  const list = { capabilities: [tool], servers: [], note: 'The complete installed inventory.' }

  it('keeps deferred native tools that the model catalog hides', () => {
    expect(parseReasonixCapabilities(JSON.stringify(list))).toEqual([tool])
  })

  it('accepts the installed null list when no deferred capability exists', () => {
    expect(parseReasonixCapabilities(JSON.stringify({ ...list, capabilities: null }))).toEqual([])
  })

  it.each([
    { ...list, capabilities: [tool, tool] },
    { ...list, capabilities: [{ ...tool, name: 'new_code_executor', id: 'tool:new_code_executor' }] },
    { ...list, capabilities: [{ ...tool, name: 'other' }] },
    { ...list, capabilities: [{ ...tool, description: '' }] },
    { ...list, capabilities: undefined },
    { ...list, servers: [{ name: 'unexpected' }] },
    { ...list, truncated: true },
    { ...list, schema_omitted: 'Too large' },
    { ...list, cursor: '' },
    { ...list, next_cursor: null },
  ])('rejects incomplete, foreign, repeated, or unaudited discovery: %#', (value) => {
    expect(() => parseReasonixCapabilities(JSON.stringify(value))).toThrow('Reasonix')
  })
})

describe('assertReasonixCapabilityInspection', () => {
  const tool = { id: 'tool:notebook_edit', kind: 'tool', name: 'notebook_edit', description: 'Edit a notebook cell.' }
  const descriptor = { ...tool, tool_name: 'notebook_edit' }

  it('accepts the exact installed ordinary-tool descriptor without an invented input schema', () => {
    expect(assertReasonixCapabilityInspection(JSON.stringify(descriptor), tool)).toEqual(descriptor)
  })

  it.each([
    { ...descriptor, id: 'tool:other' },
    { ...descriptor, kind: 'skill' },
    { ...descriptor, name: 'other' },
    { ...descriptor, tool_name: 'other' },
    { ...descriptor, description: 'A changed descriptor.' },
    { ...descriptor, truncated: true },
    { ...descriptor, schema_omitted: 'Too large' },
  ])('rejects an altered, foreign, or truncated inspected descriptor: %#', (value) => {
    expect(() => assertReasonixCapabilityInspection(JSON.stringify(value), tool)).toThrow('Reasonix')
  })

  it('requires the actual installed arguments for native session result reads', () => {
    const entry = { id: 'session:tool_result', kind: 'session', name: 'tool_result' }
    const value = { ...entry, description: 'Read a complete tool result.', arguments: { tool_call_id: 'required', result_ref: 'Required for a new truncated result.', offset: 0, limit_default: 16384, limit_max: 24576 } }
    expect(assertReasonixCapabilityInspection(JSON.stringify(value), entry)).toEqual(value)
    expect(() => assertReasonixCapabilityInspection(JSON.stringify({ ...value, arguments: undefined }), entry)).toThrow('argument interface')
    expect(() => assertReasonixCapabilityInspection(JSON.stringify({ ...value, arguments: { ...value.arguments, limit_max: 1 } }), entry)).toThrow('argument interface')
  })

  it('requires the exact installed schema for an active read-strategy receipt', () => {
    const entry = { id: 'session:read_strategy_receipt', kind: 'session', name: 'read_strategy_receipt' }
    const value = { ...entry, description: 'Validate native read evidence.', arguments: { type: 'object', properties: { conclusion: {}, read_id: {}, read_tool_call_ids: {}, search_tool_call_ids: {} } } }
    expect(assertReasonixCapabilityInspection(JSON.stringify(value), entry)).toEqual(value)
    expect(() => assertReasonixCapabilityInspection(JSON.stringify({ ...value, arguments: { type: 'object', properties: { source: {} } } }), entry)).toThrow('argument interface')
  })

  it('requires a complete native skill schema and rejects raw source fields', () => {
    const entry = { id: 'skill:review', kind: 'skill', name: 'review' }
    const value = { ...entry, description: 'Delegate a review prompt.', input_schema: { type: 'object', properties: { prompt: { type: 'string' } } } }
    expect(assertReasonixCapabilityInspection(JSON.stringify(value), entry)).toEqual(value)
    expect(() => assertReasonixCapabilityInspection(JSON.stringify({ ...value, input_schema: undefined }), entry)).toThrow('argument interface')
    expect(() => assertReasonixCapabilityInspection(JSON.stringify({ ...value, input_schema: { type: 'object', properties: { script: {} } } }), entry)).toThrow('argument interface')
  })
})
