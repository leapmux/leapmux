import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeToolDescriptor } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { kiroCurrentUserInput } from '../helpers/kiroSurface'
import { MCP_ECHO_SERVER_NAME } from '../helpers/mcpEchoServer'
import { mcpToolCall } from '../helpers/providerToolCalls'

function currentTools(request: MockModelRequestRecord): unknown[] {
  if (request.protocol !== 'aws-event-stream')
    throw new Error('The native Kiro catalog requires an actual AWS model request.')
  const user = kiroCurrentUserInput(request.body)
  const context = isObject(user?.userInputMessageContext) ? user.userInputMessageContext : undefined
  if (!Array.isArray(context?.tools) || context.tools.length === 0)
    throw new Error('The native Kiro request contains no tool catalog.')
  return context.tools
}

/** Read Kiro's actual AWS tool specifications from its current user context. */
export function kiroNativeToolNames(request: MockModelRequestRecord): string[] {
  return currentTools(request).map((tool) => {
    const specification = isObject(tool) && isObject(tool.toolSpecification) ? tool.toolSpecification : undefined
    if (typeof specification?.name !== 'string' || !specification.name)
      throw new Error('The native Kiro catalog contains an invalid tool specification.')
    return specification.name
  })
}

/**
 * Read the JSON input schema of the tool `name` from Kiro's actual AWS tool specifications.
 * The read fails when the request holds no catalog, when the catalog holds no tool of that name, and when the tool
 * states no JSON schema. Unlike {@link kiroActiveToolCatalog}, it accepts a catalog that keeps its deferred paths.
 */
export function kiroToolInputSchema(request: MockModelRequestRecord, name: string): Record<string, unknown> {
  const specification = currentTools(request)
    .map(tool => isObject(tool) && isObject(tool.toolSpecification) ? tool.toolSpecification : undefined)
    .find(candidate => candidate?.name === name)
  if (!specification)
    throw new Error(`The native Kiro catalog holds no tool ${name}.`)
  const schema = isObject(specification.inputSchema) ? specification.inputSchema.json : undefined
  if (!isObject(schema))
    throw new Error(`The native Kiro tool ${name} states no JSON input schema.`)
  return schema
}

/** Read every active descriptor when the native deferred paths are disabled. */
export function kiroActiveToolCatalog(request: MockModelRequestRecord): NativeToolDescriptor[] {
  const seen = new Set<string>()
  return currentTools(request).map((tool) => {
    const specification = isObject(tool) && isObject(tool.toolSpecification) ? tool.toolSpecification : undefined
    const schema = isObject(specification?.inputSchema) ? specification.inputSchema.json : undefined
    if (typeof specification?.name !== 'string' || !specification.name.trim() || typeof specification.description !== 'string'
      || !isObject(schema) || schema.type !== 'object' || !isObject(schema.properties)) {
      throw new Error('The native Kiro active catalog contains an incomplete descriptor.')
    }
    if (seen.has(specification.name))
      throw new Error('The native Kiro active catalog contains duplicate tool identities.')
    if (specification.name === 'tool_search' || specification.name === 'tool_load' || specification.name === 'tool_call')
      throw new Error('The native Kiro catalog exposes a deferred discovery path and is not a complete active inventory.')
    seen.add(specification.name)
    return { name: specification.name, description: specification.description, inputSchema: schema }
  })
}

/** Detect source-language execution from each complete native descriptor. */
export function kiroScriptExecutors(catalog: readonly NativeToolDescriptor[]): NativeToolDescriptor[] {
  return catalog.filter(tool => /(?:run|execute|evaluate|interpret).*?(?:javascript|typescript|python|script|code)|repl|interpreter/i.test(tool.description)
    && Object.keys(isObject(tool.inputSchema.properties) ? tool.inputSchema.properties : {}).some(field => /^(?:source|script|code|expression|input)$/i.test(field)))
}

// KAS 0.66.8 xRr assembles these native tools for the current LeapMux capabilities.
// Reject a new identity or source field until its native implementation receives an audit.
const KIRO_ACTIVE_BUILTIN_FIELDS: ReadonlyMap<string, readonly string[]> = new Map([
  ['read_file', ['path', 'offset', 'limit']],
  ['execute_bash', ['command', 'description', 'cwd', 'run_in_background', 'timeout']],
  ['execute_pwsh', ['command', 'description', 'cwd', 'run_in_background', 'timeout']],
  ['fs_write', ['path', 'text']],
  ['str_replace', ['path', 'oldStr', 'newStr', 'replace_all']],
  ['list_directory', ['path', 'explanation', 'depth']],
  ['delete_file', ['explanation', 'targetFile']],
  ['fs_append', ['path', 'text']],
  ['file_search', ['explanation', 'query', 'excludePattern', 'includeIgnoredFiles']],
  ['grep_search', ['query', 'caseSensitive', 'file_type', 'includePattern', 'excludePattern', 'context', 'context_before', 'context_after', 'limit', 'offset', 'explanation']],
  ['web_fetch', ['url', 'mode', 'searchPhrase']],
  ['todo_list', ['command', 'tasks', 'task_list_description', 'completed_task_ids', 'context_update', 'modified_files', 'new_tasks', 'new_description', 'remove_task_ids']],
  ['update_session_information', ['title', 'description', 'status']],
  ['invoke_sub_agent', ['name', 'prompt', 'explanation', 'preset', 'contextFiles']],
  ['disclose_context', ['name']],
  ['kiro_powers', ['action', 'powerName', 'serverName', 'toolName', 'arguments', 'steeringFile', 'skillName']],
  ['createHook', ['id', 'name', 'trigger', 'description', 'matcher', 'actionType', 'prompt', 'command', 'timeout']],
  ['run_workflow', ['workflowPath', 'workflowPrompt', 'inputs', 'runLabel']],
  ['inspect_workflow', ['workflowId']],
  ['update_workflow', ['workflowId', 'action', 'status', 'statusReason', 'remainingSteps']],
  ['validate_workflow', ['workflow']],
  ['send_message', ['sessionId', 'message', 'severity']],
])

/** The catalog name of the echo tool of the MCP echo server that `kiro/code-execution.spec.ts` registers. */
const KIRO_ECHO_TOOL = mcpToolCall(AgentProvider.KIRO, 'catalog', { server: MCP_ECHO_SERVER_NAME, tool: 'echo', input: {} }).name

/** Require only source-audited native tools and the one controlled MCP descriptor. */
export function assertKiroActiveCatalog(catalog: readonly NativeToolDescriptor[]): void {
  if (catalog.length === 0)
    throw new Error('The native Kiro active inventory must not be empty.')
  const names = new Set<string>()
  for (const tool of catalog) {
    if (names.has(tool.name))
      throw new Error('The native Kiro active inventory contains duplicate identities.')
    names.add(tool.name)
    const fields = tool.name === KIRO_ECHO_TOOL ? ['value'] : KIRO_ACTIVE_BUILTIN_FIELDS.get(tool.name)
    if (!fields || !isObject(tool.inputSchema.properties))
      throw new Error(`The native Kiro tool ${tool.name} has no audited active descriptor.`)
    if (Object.keys(tool.inputSchema.properties).some(field => !fields.includes(field)))
      throw new Error(`The native Kiro tool ${tool.name} exposes an unaudited argument field.`)
    const sourceFields = ['source', 'script', 'code', 'expression']
    const schemaValues: unknown[] = [tool.inputSchema]
    const visited = new Set<object>()
    while (schemaValues.length > 0) {
      const value = schemaValues.pop()
      if (typeof value !== 'object' || value === null)
        continue
      if (visited.has(value))
        throw new Error('The native Kiro argument schema contains a repeated object.')
      visited.add(value)
      if (Array.isArray(value)) {
        schemaValues.push(...value)
        continue
      }
      if (!isObject(value))
        continue
      if (isObject(value.properties) && Object.keys(value.properties).some(field => sourceFields.includes(field)))
        throw new Error(`The native Kiro tool ${tool.name} contains an unaudited source-language field.`)
      schemaValues.push(...Object.values(value))
    }
    if (tool.name === 'execute_bash' || tool.name === 'execute_pwsh' || tool.name === 'read_file' || tool.name === KIRO_ECHO_TOOL) {
      const field = tool.name === 'read_file' ? 'path' : tool.name === KIRO_ECHO_TOOL ? 'value' : 'command'
      const property = tool.inputSchema.properties[field]
      if (!isObject(property) || property.type !== 'string' || !Array.isArray(tool.inputSchema.required) || !tool.inputSchema.required.includes(field))
        throw new Error(`The native Kiro tool ${tool.name} lacks its required string input.`)
    }
  }
  if (!names.has('execute_bash') || !names.has('read_file') || !names.has(KIRO_ECHO_TOOL))
    throw new Error('The native Kiro active inventory lacks its shell, file, or controlled MCP descriptor.')
}
