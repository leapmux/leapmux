import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { assertKiroActiveCatalog, kiroActiveToolCatalog, kiroNativeToolNames, kiroScriptExecutors, kiroToolInputSchema } from './toolCatalog'

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

describe('kiroToolInputSchema', () => {
  const schema = { type: 'object', properties: { value: { type: 'string' } }, required: ['value'] }

  it('reads the JSON schema of the requested tool beside a deferred path', () => {
    const tools = [{ toolSpecification: { name: 'tool_search' } }, { toolSpecification: { name: 'mcp_project_echo', inputSchema: { json: schema } } }]
    expect(kiroToolInputSchema(request(tools), 'mcp_project_echo')).toEqual(schema)
  })

  it('refuses a catalog that holds no tool of the name', () => {
    expect(() => kiroToolInputSchema(request([{ toolSpecification: { name: 'mcp_other_echo', inputSchema: { json: schema } } }]), 'mcp_project_echo')).toThrow('holds no tool mcp_project_echo')
  })

  it.each([{}, { inputSchema: {} }, { inputSchema: { json: 'object' } }].map(fields => ({ fields })))('refuses a tool with no JSON schema: %j', ({ fields }) => {
    expect(() => kiroToolInputSchema(request([{ toolSpecification: { name: 'mcp_project_echo', ...fields } }]), 'mcp_project_echo')).toThrow('states no JSON input schema')
  })

  it('refuses an absent catalog and another model protocol', () => {
    expect(() => kiroToolInputSchema(request([]), 'mcp_project_echo')).toThrow('no tool catalog')
    expect(() => kiroToolInputSchema({ ...request([{ toolSpecification: { name: 'mcp_project_echo', inputSchema: { json: schema } } }]), protocol: 'openai-chat-completions' }, 'mcp_project_echo')).toThrow('AWS model request')
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

  // Each row is a pair, so `%j` formats the `{ tools }` object alone, and the reason stays out of the title.
  it.each([
    [{ tools: [] }, 'The native Kiro request contains no tool catalog.'],
    [{ tools: [tool('execute_bash'), tool('execute_bash')] }, 'The native Kiro active catalog contains duplicate tool identities.'],
    [{ tools: [{ toolSpecification: { name: 'execute_bash' } }] }, 'The native Kiro active catalog contains an incomplete descriptor.'],
    [{ tools: [{ toolSpecification: { name: 'execute_bash', description: '', inputSchema: { json: { type: 'array', properties: {} } } } }] }, 'The native Kiro active catalog contains an incomplete descriptor.'],
    [{ tools: [{ toolSpecification: { name: '', description: '', inputSchema: { json: { type: 'object', properties: {} } } } }] }, 'The native Kiro active catalog contains an incomplete descriptor.'],
    [{ tools: [{ toolSpecification: { name: 'execute_bash', description: null, inputSchema: { json: { type: 'object', properties: {} } } } }] }, 'The native Kiro active catalog contains an incomplete descriptor.'],
  ])('rejects an incomplete active inventory %j', ({ tools }, error) => {
    expect(() => kiroActiveToolCatalog(request(tools))).toThrow(error)
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

  it.each([
    [{ tools: [] }, 'The native Kiro active inventory must not be empty.'],
    [{ tools: [tool('execute_bash', {})] }, 'The native Kiro tool execute_bash lacks its required string input.'],
    [{ tools: [...catalog(), catalog()[0]!] }, 'The native Kiro active inventory contains duplicate identities.'],
    [{ tools: [...catalog(), tool('mcp_uncontrolled_echo', { value: { type: 'string' } })] }, 'The native Kiro tool mcp_uncontrolled_echo has no audited active descriptor.'],
  ])('rejects an incomplete or uncorrelated inventory %j', ({ tools }, error) => {
    expect(() => assertKiroActiveCatalog(tools)).toThrow(error)
  })
})
