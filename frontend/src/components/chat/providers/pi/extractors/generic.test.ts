import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { piGenericToolSource, piMcpIdentity } from './generic'
import { nativeCodemodeFailure, nativeMcpEcho } from './mcp.fixtures'

const toolName = 'mcp__sample__lookup'
const start = (args: Record<string, unknown> = { query: 'marker', limit: 0 }) => ({ type: 'tool_execution_start', toolCallId: 'mcp-call', toolName, args })
const end = (result: Record<string, unknown>, isError = false) => ({ type: 'tool_execution_end', toolCallId: 'mcp-call', toolName, result, isError })
const details = { server: 'sample', tool: 'lookup' }

describe('piGenericToolSource', () => {
  it('extracts the captured installed MCP completion without changing its frame', () => {
    const before = JSON.stringify(nativeMcpEcho)
    const source = piGenericToolSource(nativeMcpEcho)
    expect(source).toMatchObject({ server: 'probe', tool: 'echo', content: [{ type: 'text', text: 'PI099_ECHO:native-zero' }] })
    expect(source?.structuredJson).toEqual(expect.any(String))
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ nativeName: 'echo', zero: 0, empty: '' })
    expect(JSON.stringify(nativeMcpEcho)).toBe(before)
  })

  it('retains the captured installed codemode failure and both text blocks', () => {
    const source = piGenericToolSource(nativeCodemodeFailure)
    expect(source?.failed).toBe(true)
    expect(source?.content).toEqual(nativeCodemodeFailure.result.content)
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ calls: [] })
  })

  it('uses the native identity and complete MCP content instead of model text', () => {
    const result = end({
      content: [{ type: 'text', text: 'Flattened model copy' }],
      details,
      structuredContent: {
        content: [{ type: 'resource', resource: { uri: 'probe://sample', text: 'Resource body', mimeType: 'text/plain' } }],
        structuredContent: { count: 0 },
      },
    })
    const source = piGenericToolSource(result, input(start()))
    expect(source).toMatchObject({ server: 'sample', tool: 'lookup', content: [{ type: 'resource', uri: 'probe://sample', text: 'Resource body', mimeType: 'text/plain' }] })
    expect(JSON.parse(source?.argsJson ?? '')).toEqual({ query: 'marker', limit: 0 })
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ count: 0 })
  })

  it.each([{ value: 0 }, { value: false }, { value: '' }, { value: 'A "quoted" value\n한글' }, { value: [] }, { value: {} }])('preserves a structured value of $value', ({ value }) => {
    const source = piGenericToolSource(end({ content: [], details, structuredContent: { content: [], structuredContent: value } }))
    expect(source?.content).toEqual([])
    expect(source?.structuredJson).toEqual(expect.any(String))
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual(value)
  })

  it('keeps a native empty result empty', () => {
    expect(piGenericToolSource(end({ content: [{ type: 'text', text: 'Model fallback' }], details, structuredContent: { content: [] } }))?.content).toEqual([])
  })

  it.each([{ native: undefined }, { native: null }, { native: [] }, { native: 'invalid' }, { native: { content: null } }])('retains model output when native content is absent or malformed: $native', ({ native }) => {
    const source = piGenericToolSource(end({ content: [{ type: 'text', text: 'Available output' }], details, structuredContent: native }))
    expect(source?.content).toEqual([{ type: 'text', text: 'Available output' }])
  })

  it('uses only the matching result identity for an opening request', () => {
    const request = start()
    const result = input(end({ content: [{ type: 'text', text: 'Later output' }], details, structuredContent: { content: [{ type: 'text', text: 'Native output' }] } }))
    expect(piGenericToolSource(request, undefined, result)).toMatchObject({ server: 'sample', tool: 'lookup', content: [] })
    expect(piGenericToolSource({ ...request, toolCallId: 'other' }, undefined, result)).toMatchObject({ server: '', tool: toolName, content: [] })
    expect(piGenericToolSource({ ...request, toolName: `${toolName}_other` }, undefined, result)?.server).toBe('')
  })

  it('does not reverse sanitized names or hash suffixes', () => {
    for (const name of ['mcp__server_a__lookup', 'mcp__server_a__lookup_a1234567', 'mcp__server__a__lookup']) {
      const request = { ...start(), toolName: name }
      expect(piMcpIdentity(request)).toBeUndefined()
      expect(piGenericToolSource(request)).toMatchObject({ server: '', tool: name })
      const result = input({ ...end({ content: [], details: { server: 'server.a', tool: 'lookup/a' } }), toolName: name })
      expect(piMcpIdentity(request, result)).toEqual({ server: 'server.a', tool: 'lookup/a' })
    }
  })

  it('does not unwrap a native tool argument called tool or args', () => {
    const args = { tool: 'application value', args: '{"nested":0}' }
    const source = piGenericToolSource(end({ content: [], details, structuredContent: { content: [] } }), input(start(args)))
    expect(JSON.parse(source?.argsJson ?? '')).toEqual(args)
  })

  it('does not accept arguments from another call or tool', () => {
    const result = end({ content: [], details })
    for (const request of [{ ...start(), toolCallId: 'other' }, { ...start(), toolName: 'mcp__sample__other' }])
      expect(piGenericToolSource(result, input(request))?.argsJson).toBe('')
  })

  it('reports a native MCP failure and retains its complete result', () => {
    const source = piGenericToolSource(end({ content: [{ type: 'text', text: 'Model failure' }], details, structuredContent: { content: [{ type: 'text', text: 'missing file' }], isError: true, structuredContent: { status: 0 } } }))
    expect(source?.failed).toBe(true)
    expect(source?.content).toEqual([{ type: 'text', text: 'missing file' }])
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ status: 0 })
  })

  it('retains progress without inventing a completed failure', () => {
    const source = piGenericToolSource({ type: 'tool_execution_update', toolCallId: 'mcp-call', toolName, args: {}, partialResult: { content: [{ type: 'text', text: 'First call finished' }], details, structuredContent: { content: [{ type: 'text', text: 'Partial native output' }], isError: true } } })
    expect(source?.failed).toBeUndefined()
    expect(source?.content).toEqual([{ type: 'text', text: 'Partial native output' }])
  })

  it('uses native resource contents without the flattened model copy', () => {
    const source = piGenericToolSource({ ...end({ content: [{ type: 'text', text: 'Model resource' }], details: { server: 'sample', tool: 'read_mcp_resource' }, structuredContent: { server: 'sample', uri: 'probe://sample', contents: [{ uri: 'probe://sample', text: 'Resource body', mimeType: 'text/plain' }] } }), toolName: 'read_mcp_resource' })
    expect(source).toMatchObject({ server: 'sample', tool: 'read_mcp_resource', content: [{ type: 'resource', uri: 'probe://sample', text: 'Resource body', mimeType: 'text/plain' }] })
    expect(source?.structuredJson).toBeUndefined()
  })

  it.each(['list_mcp_resources', 'list_mcp_resource_templates'])('keeps the native structured listing for %s', (name) => {
    const listing = { resources: [], errors: [{ server: 'sample', error: 'Unavailable' }] }
    const source = piGenericToolSource({ ...end({ content: [{ type: 'text', text: JSON.stringify(listing) }], details: { server: '', tool: name }, structuredContent: listing }), toolName: name })
    expect(source).toMatchObject({ server: '', tool: name })
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual(listing)
    expect(source?.structuredJsonRole).toBeUndefined()
  })

  it('retains codemode details and respects its native failure flag', () => {
    const calls = [{ id: 'script/1', name: toolName, args: {}, status: 'error', error: 'Unavailable' }]
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'script', toolName: 'codemode', isError: true, result: { content: [{ type: 'text', text: 'Script failed\nError: unavailable' }], details: { calls } } })
    expect(source?.failed).toBe(true)
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ calls })
    expect(source?.content).toEqual([{ type: 'text', text: 'Script failed\nError: unavailable' }])
  })

  it('keeps arbitrary extension details without treating a metric as failure', () => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'extension', toolName: 'custom', result: { content: [], details: { error: 'A reported metric', count: 0 } } })
    expect(source?.failed).toBeUndefined()
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ error: 'A reported metric', count: 0 })
  })

  it('does not classify another extension through server and tool fields alone', () => {
    const payload = { ...end({ content: [], details, structuredContent: { content: [{ type: 'text', text: 'Extension state' }] } }), toolName: 'custom' }
    expect(piMcpIdentity(payload)).toBeUndefined()
    expect(piGenericToolSource(payload)).toMatchObject({ server: '', tool: 'custom', content: [] })
  })

  it.each([{ server: '', tool: 'lookup' }, { server: 'sample', tool: '' }, { server: 7, tool: 'lookup' }, { server: 'sample' }])('rejects incomplete native tool identity: %j', (identity) => {
    expect(piMcpIdentity(end({ content: [], details: identity }))).toBeUndefined()
  })
})

describe('piGenericToolSource structured ownership', () => {
  it('marks only source-defined codemode details as execution metadata', () => {
    const native = { type: 'tool_execution_end', toolCallId: 'code-call', toolName: 'codemode', isError: false, result: { content: [{ type: 'text', text: 'returned code output' }], details: { calls: [], fullOutputPath: '/native/code.txt' } } }
    const before = JSON.stringify(native)
    const source = piGenericToolSource(native)
    expect(source?.structuredJsonRole).toBe('metadata')
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual(native.result.details)
    expect(source?.content).toEqual(native.result.content)
    expect(JSON.stringify(native)).toBe(before)
  })

  it('keeps genuine native MCP structured results as output', () => {
    const source = piGenericToolSource(end({ content: [], details, structuredContent: { content: [], structuredContent: { count: 0, enabled: false, nullable: null } } }))
    expect(source?.structuredJsonRole).toBeUndefined()
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ count: 0, enabled: false, nullable: null })
  })

  it('keeps arbitrary extension details as genuine returned content', () => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'extension-call', toolName: 'custom', result: { content: [], details: { count: 0, enabled: false, nullable: null } } })
    expect(source?.structuredJsonRole).toBeUndefined()
    expect(JSON.parse(source?.structuredJson ?? '')).toEqual({ count: 0, enabled: false, nullable: null })
  })

  it.each([undefined, null, {}])('does not invent codemode metadata for absent details %j', (value) => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'code-call', toolName: 'codemode', result: { content: [], ...(value !== undefined ? { details: value } : {}) } })
    expect(source?.structuredJson).toBeUndefined()
    expect(source?.structuredJsonRole).toBeUndefined()
  })
})
