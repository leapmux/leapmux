import type { MessageCategory } from '../../messageClassification'
import { isGooseSubagentToolRequest } from './extractors/subagentToolRequest'

/**
 * Goose's `classifyToolCallUpdate` sub-hook. `registerACPProvider` takes it as an
 * option and hands it to `classifyACPMessage`.
 *
 * This is NOT the provider's `classify` property. `classifyACPMessage` supplies that
 * one for every member of the Agent Client Protocol family. For a `tool_call_update`
 * frame it calls this hook first, ahead of its own status rule.
 *
 * Goose carries a subagent's tool REQUEST in the frame's `_meta`, and the shared status
 * rule hides an `in_progress` update that no completion closed. This hook answers
 * `tool_use` for that shape, so the row reaches the tool renderer instead.
 * `gooseToolCallAdapter` then gives the row its "Requested tool: <name>" title.
 *
 * This hook returns `undefined` for an update of any other shape. The shared status
 * rule answers that one.
 */
export function classifyGooseToolCallUpdate(parent: Record<string, unknown>): MessageCategory | undefined {
  if (isGooseSubagentToolRequest(parent))
    return { kind: 'tool_use' }
  return undefined
}
