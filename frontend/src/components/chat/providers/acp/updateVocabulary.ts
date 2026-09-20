import { ACP_UPDATE } from '~/generated/contracts/acp-protocol'

/**
 * The Agent Client Protocol `sessionUpdate` discriminators that reach a classifier,
 * as a SUBSET of the generated `ACP_UPDATE` table.
 *
 * Use these constants in classifiers and routers so wire-format strings are
 * typo-checked and centralized. `updateVocabulary.test.ts` sweeps the whole table and
 * fails for an update that reaches the raw-JSON bubble, so the subset states which
 * updates that sweep covers.
 *
 * Three members of `ACP_UPDATE` are absent, and each for its own reason. The worker
 * joins a run of the two text chunks (`agent_message_chunk`, `agent_thought_chunk`)
 * into one assembled-message row, so no chunk ever reaches the browser.
 * `current_mode_update` draws no row at all -- `classification.ts` hides it, and reads
 * the generated constant in place.
 *
 * The tool-call `kind` words carry no such subset, so a call site reads
 * `ACP_TOOL_KIND` from `~/generated/contracts/acp-protocol` directly.
 */
export const ACP_SESSION_UPDATE = {
  TOOL_CALL: ACP_UPDATE.ToolCall,
  TOOL_CALL_UPDATE: ACP_UPDATE.ToolCallUpdate,
  PLAN: ACP_UPDATE.Plan,
  USAGE_UPDATE: ACP_UPDATE.UsageUpdate,
  AVAILABLE_COMMANDS_UPDATE: ACP_UPDATE.AvailableCommandsUpdate,
  USER_MESSAGE_CHUNK: ACP_UPDATE.UserMessageChunk,
  CONFIG_OPTION_UPDATE: ACP_UPDATE.ConfigOptionUpdate,
  SESSION_INFO_UPDATE: ACP_UPDATE.SessionInfoUpdate,
} as const
