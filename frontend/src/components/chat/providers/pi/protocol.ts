/** Pi tool names that only the frontend interprets. Shared names stay in the generated contract. */
export const PI_POWERSHELL_TOOL = 'powershell'

export const PI_MCP_TOOL = {
  Gateway: 'mcp',
  Script: 'mcpScript',
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
