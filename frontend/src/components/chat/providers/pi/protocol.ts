/** Pi tool names that only the frontend interprets. Shared names stay in the generated contract. */
export const PI_POWERSHELL_TOOL = 'powershell'

export const PI_MCP_TOOL = {
  Gateway: 'mcp',
  Script: 'mcpScript',
} as const

/**
 * The prefix of pi-mcp-adapter's NAMESPACE PROXY tool, which carries one server per
 * tool name (`mcp__github`) and the tool itself in its arguments.
 *
 * The adapter's other spelling, `<server>_<tool>`, states the pair in one word with no
 * mark between the halves, so nothing can split it without the server list. That row
 * takes its identity from the paired result instead; see `piMcpIdentity`.
 */
export const PI_MCP_PROXY_PREFIX = 'mcp__'

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
