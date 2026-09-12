import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { piGenericToolSource } from './generic'

describe('pi MCP adapter results', () => {
  it.each(['script_error', 'timeout', 'aborted', 'init_failed'])('recognizes a native MCP script failure: %s', (error) => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'script', toolName: 'mcpScript', isError: false, result: {
      content: [{ type: 'text', text: 'Error: MCP_SCRIPT_PROBE_FAILURE' }],
      details: { mode: 'script', error, message: 'Error: MCP_SCRIPT_PROBE_FAILURE', timeoutMs: 30000 },
    } })
    expect(source?.status).toBe('failed')
    expect(source?.content).toEqual([{ type: 'text', text: 'Error: MCP_SCRIPT_PROBE_FAILURE' }])
    expect(JSON.parse(source!.structuredJson!)).toMatchObject({ mode: 'script', error, timeoutMs: 30000 })
  })

  it('keeps script call details on success and does not classify arbitrary extension errors', () => {
    const details = { mode: 'script', calls: [{ server: 'sample', tool: 'lookup', ok: true }], timeoutMs: 30000 }
    const payload = { type: 'tool_execution_end', toolCallId: 'script', toolName: 'mcpScript', result: { content: [], details } }
    expect(piGenericToolSource(payload)?.status).toBe('completed')
    expect(JSON.parse(piGenericToolSource(payload)!.structuredJson!)).toEqual(details)
    expect(piGenericToolSource({ ...payload, toolName: 'custom', result: { details: { error: 'A reported metric' } } })?.status).toBe('completed')
  })

  it('keeps a partial extension result in progress', () => {
    const source = piGenericToolSource({ type: 'tool_execution_update', toolCallId: 'script', toolName: 'mcpScript', partialResult: { content: [{ type: 'text', text: 'First call finished' }] } })
    expect(source?.status).toBe('inProgress')
    expect(source?.content).toEqual([{ type: 'text', text: 'First call finished' }])
  })

  it('uses native MCP resource contents without the flattened display copy', () => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'resource', toolName: 'sample_read_resource', result: {
      content: [{ type: 'text', text: '[Resource: probe://sample]\nResource body' }],
      details: { server: 'sample', resourceUri: 'probe://sample', mcpResult: { contents: [{ uri: 'probe://sample', text: 'Resource body', mimeType: 'text/plain' }] } },
    } })
    expect(source?.server).toBe('sample')
    expect(source?.content).toEqual([{ type: 'resource', uri: 'probe://sample', text: 'Resource body', mimeType: 'text/plain' }])
  })

  it('distinguishes empty resource results from missing or omitted resource data', () => {
    const source = (mcpResult: unknown) => piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'resource', toolName: 'sample_read_resource', result: {
      content: [{ type: 'text', text: 'Available output' }],
      details: { server: 'sample', resourceUri: 'probe://sample', mcpResult },
    } })
    expect(source({ contents: [] })?.content).toEqual([])
    for (const native of [undefined, { contents: null }, { omitted: true, contents: [] }])
      expect(source(native)?.content).toEqual([{ type: 'text', text: 'Available output' }])
  })

  it('uses native MCP resources and structure instead of the flattened display copy', () => {
    const request = input({ type: 'tool_execution_start', toolCallId: 'mcp-call', toolName: 'mcp', args: { tool: 'sample_lookup', args: { query: 'marker' } } })
    const result = { type: 'tool_execution_end', toolCallId: 'mcp-call', toolName: 'mcp', result: {
      content: [{ type: 'text', text: '[Resource: probe://sample]\nResource body' }],
      details: { mode: 'call', server: 'sample', tool: 'lookup', mcpResult: { content: [{ type: 'resource', resource: { uri: 'probe://sample', text: 'Resource body' } }], structuredContent: { count: 0 } } },
    } }
    const source = piGenericToolSource(result, request)
    expect(source?.server).toBe('sample')
    expect(source?.tool).toBe('lookup')
    expect(JSON.parse(source!.argsJson)).toEqual({ query: 'marker' })
    expect(source?.content).toEqual([{ type: 'resource', uri: 'probe://sample', text: 'Resource body', mimeType: undefined }])
    expect(JSON.parse(source!.structuredJson!)).toEqual({ count: 0 })
  })

  it('recognizes native MCP failures when Pi reports a successful extension call', () => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'mcp-call', toolName: 'mcp', isError: false, result: {
      content: [{ type: 'text', text: 'Error: missing file' }],
      details: { mode: 'call', server: 'sample', tool: 'lookup', error: 'tool_error', mcpResult: { isError: true, content: [{ type: 'text', text: 'missing file' }] } },
    } })
    expect(source?.status).toBe('failed')
    expect(source?.content).toEqual([{ type: 'text', text: 'missing file' }])
  })

  it('uses result identity for a request without displaying the later output', () => {
    const request = { type: 'tool_execution_start', toolCallId: 'mcp-call', toolName: 'mcp', args: { tool: 'sample_lookup', args: '{"query":"marker"}' } }
    const result = input({ type: 'tool_execution_end', toolCallId: 'mcp-call', toolName: 'mcp', result: { content: [{ type: 'text', text: 'Later output' }], details: { server: 'sample', tool: 'lookup' } } })
    const source = piGenericToolSource(request, undefined, result)
    expect(source).toMatchObject({ server: 'sample', tool: 'lookup', content: [], status: 'inProgress' })
    expect(JSON.parse(source!.argsJson)).toEqual({ query: 'marker' })
    expect(piGenericToolSource({ ...request, toolCallId: 'other' }, undefined, result)?.server).toBe('')
  })

  it('keeps display content when the native result is an omission summary', () => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'mcp-call', toolName: 'mcp', result: { content: [{ type: 'text', text: 'Available output' }], details: { mode: 'call', server: 'sample', tool: 'lookup', mcpResult: { omitted: true, content: [{ type: 'text', text: 'Summary only' }] } } } })
    expect(source?.content).toEqual([{ type: 'text', text: 'Available output' }])
  })

  it('keeps a native empty result empty and preserves a zero structured value', () => {
    const source = piGenericToolSource({ type: 'tool_execution_end', toolCallId: 'mcp-call', toolName: 'mcp', result: { content: [{ type: 'text', text: '(empty result)' }], details: { mode: 'call', mcpResult: { content: [], structuredContent: 0 } } } })
    expect(source?.content).toEqual([])
    expect(JSON.parse(source!.structuredJson!)).toBe(0)
  })
})
