/**
 * ZCode vocabulary that only the frontend consumes.
 *
 * ZCode's app-server uses line-delimited JSON without a `jsonrpc` field.
 * LeapMux stores native session events as envelopes with `type` and `payload`.
 * The frontend dispatches on `type` and reads event data from `payload`.
 * User rows, control rows, and native plan-request rows use other shapes.
 *
 * Vocabulary that Go and TypeScript both consume belongs in
 * contracts/zcode-protocol.json. Import the generated values from
 * `~/generated/contracts/zcode-protocol`.
 */

/** This tool name is used only by the frontend. */
export const ZCODE_WEB_FETCH = 'WebFetch'

/** The display kinds that the app-server supplies with tool results. */
export const ZCODE_DISPLAY = {
  FileDiff: 'file_diff',
  LocalAgentMessage: 'local_agent_message',
  TaskStop: 'task_stop',
  TaskOutput: 'task_output',
  RespondToCoordinator: 'respond_to_coordinator',
  ComputerUse: 'cua',
  NodeImages: 'node_repl_images',
  McpTool: 'mcp_tool',
} as const
