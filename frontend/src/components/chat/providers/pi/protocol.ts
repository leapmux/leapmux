/** Pi tool names that only the frontend interprets. Shared names stay in the generated contract. */
export const PI_POWERSHELL_TOOL = 'powershell'

/** Native MCP names can contain sanitized text or a hash. Result details supply the original identity. */
export const PI_MCP_TOOL_PREFIX = 'mcp__'

export const PI_MCP_RESOURCE_TOOL = {
  List: 'list_mcp_resources',
  ListTemplates: 'list_mcp_resource_templates',
  Read: 'read_mcp_resource',
} as const

/** The worker preserves these fields without interpreting them. */
export const PI_MCP_RESULT_FIELD = {
  StructuredContent: 'structuredContent',
  Contents: 'contents',
  Server: 'server',
  Tool: 'tool',
} as const

export const PI_SEARCH_TOOL = {
  Grep: 'grep',
  Find: 'find',
  List: 'ls',
} as const

/** These pi-subagents control tools do not launch a new subagent. */
export const PI_AGENT_TOOL = {
  GetResult: 'get_subagent_result',
  Steer: 'steer_subagent',
} as const

/** Native fields for tool result pointers. */
export const PI_CONTENT_BLOCK = {
  Type: 'type',
  Text: 'text',
} as const

/** Native fields for tool result pointers. */
export const PI_BLOCK_TYPE = {
  Text: 'text',
} as const

/** Native fields for tool result pointers. */
export const PI_TOOL_RESULT_FIELD = {
  Details: 'details',
  Content: 'content',
  FullOutputPath: 'fullOutputPath',
} as const
