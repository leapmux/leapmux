import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { assertKiroActiveCatalog, kiroActiveToolCatalog, kiroNativeToolNames, kiroScriptExecutors } from './toolCatalog'

function request(tools: unknown): MockModelRequestRecord {
  return { protocol: 'aws-event-stream', path: '/', body: { conversationState: { currentMessage: { userInputMessage: { userInputMessageContext: { tools } } } } } }
}

describe('kiroNativeToolNames', () => {
  it('reads only native tool specifications', () => {
    expect(kiroNativeToolNames(request([{ toolSpecification: { name: 'native_read' } }]))).toEqual(['native_read'])
  })

  it.each([null, [], [{}], [{ toolSpecification: { name: '' } }]].map(tools => ({ tools })))('refuses an absent or malformed native catalog: %j', ({ tools }) => {
    expect(() => kiroNativeToolNames(request(tools))).toThrow(/no tool catalog|invalid tool specification/)
  })
})

describe('kiroActiveToolCatalog', () => {
  const tool = (name: string, description = 'Run a native shell command.') => ({ toolSpecification: { name, description, inputSchema: { json: { type: 'object', properties: { command: { type: 'string' } }, required: ['command'] } } } })

  it('retains the complete native description and input schema', () => {
    const input = tool('execute_bash')
    expect(kiroActiveToolCatalog(request([input]))).toEqual([{ name: 'execute_bash', description: input.toolSpecification.description, inputSchema: input.toolSpecification.inputSchema.json }])
  })

  it.each(['tool_search', 'tool_load', 'tool_call'])('rejects the native deferred path %s', (name) => {
    expect(() => kiroActiveToolCatalog(request([tool('execute_bash'), tool(name)]))).toThrow('deferred discovery path')
  })

  it.each([
    [],
    [tool('execute_bash'), tool('execute_bash')],
    [{ toolSpecification: { name: 'execute_bash' } }],
    [{ toolSpecification: { name: 'execute_bash', description: '', inputSchema: { json: { type: 'array', properties: {} } } } }],
    [{ toolSpecification: { name: '', description: '', inputSchema: { json: { type: 'object', properties: {} } } } }],
    [{ toolSpecification: { name: 'execute_bash', description: null, inputSchema: { json: { type: 'object', properties: {} } } } }],
  ].map(tools => ({ tools })))('rejects an incomplete active inventory %j', ({ tools }) => {
    expect(() => kiroActiveToolCatalog(request(tools))).toThrow()
  })

  it('rejects another model protocol with an imitated Kiro body', () => {
    expect(() => kiroActiveToolCatalog({ ...request([tool('execute_bash')]), protocol: 'openai-chat-completions' })).toThrow('AWS model request')
  })
})

describe('kiroScriptExecutors', () => {
  it('detects an arbitrary native tool with a direct source-language execution schema', () => {
    const tool = { name: 'unexpected_native_tool', description: 'Execute JavaScript source.', inputSchema: { type: 'object', properties: { source: { type: 'string' } } } }
    expect(kiroScriptExecutors([tool])).toEqual([tool])
  })

  it('keeps native shell and code intelligence separate from language execution', () => {
    const tools = [
      { name: 'run_command', description: 'Run a shell command.', inputSchema: { properties: { command: { type: 'string' } } } },
      { name: 'code', description: 'Code intelligence with tree-sitter and LSP for semantic code analysis and navigation.', inputSchema: { properties: { operation: { type: 'string' }, path: { type: 'string' } } } },
    ]
    expect(kiroScriptExecutors(tools)).toEqual([])
  })
})

describe('assertKiroActiveCatalog', () => {
  const tool = (name: string, properties: Record<string, unknown>) => ({ name, description: 'Native descriptor.', inputSchema: { type: 'object', properties, required: Object.keys(properties) } })
  const catalog = () => [tool('execute_bash', { command: { type: 'string' } }), tool('read_file', { path: { type: 'string' } }), tool('mcp_echo_probe_echo', { value: { type: 'string' } })]

  it('accepts the audited native shell and file tools with the exact controlled MCP identity', () => {
    expect(() => assertKiroActiveCatalog(catalog())).not.toThrow()
  })

  it('rejects an unknown native tool even when its description gives no execution keyword', () => {
    expect(() => assertKiroActiveCatalog([...catalog(), tool('unfamiliar_native_capability', { payload: { type: 'string' } })])).toThrow('no audited active descriptor')
  })

  it('rejects a new source argument on a known native tool', () => {
    expect(() => assertKiroActiveCatalog([...catalog(), tool('fs_write', { path: { type: 'string' }, text: { type: 'string' }, source: { type: 'string' } })])).toThrow('unaudited argument field')
  })

  it('rejects an unaudited nested source-language field on a known workflow tool', () => {
    expect(() => assertKiroActiveCatalog([...catalog(), tool('validate_workflow', { workflow: { type: 'object', properties: { source: { type: 'string' } } } })])).toThrow('source-language field')
  })

  it('rejects a changed native shell argument type', () => {
    const tools = catalog()
    tools[0] = tool('execute_bash', { command: { type: 'object' } })
    expect(() => assertKiroActiveCatalog(tools)).toThrow('required string input')
  })

  it.each([[], [tool('execute_bash', {})], [...catalog(), catalog()[0]!], [...catalog(), tool('mcp_uncontrolled_echo', { value: { type: 'string' } })]].map(tools => ({ tools })))('rejects an incomplete or uncorrelated inventory %j', ({ tools }) => {
    expect(() => assertKiroActiveCatalog(tools)).toThrow()
  })
})
