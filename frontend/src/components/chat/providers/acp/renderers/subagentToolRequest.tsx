import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Wrench from 'lucide-solid/icons/wrench'
import { ToolUseLayout } from '../../../toolRenderers'
import { gooseSubagentToolRequestName } from '../classification'

/**
 * Render a Goose subagent tool-request update as a compact card.
 *
 * Goose surfaces tool REQUESTS (never results) over ACP via a two-level-nested
 * `_meta.toolNotification.params.data` payload. Each request carries the
 * subagent_id and the requested tool_call name; this card shows "Requested tool:
 * <name>". See `gooseSubagentFromToolCallUpdate` in
 * `backend/internal/worker/agent/goose.go` for the backend twin and
 * `isGooseSubagentToolRequest` in `./classification.ts` for the classifier arm.
 */
export function acpSubagentToolRequestRenderer(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const toolName = gooseSubagentToolRequestName(toolUse) || 'tool'
  return (
    <ToolUseLayout
      icon={Wrench}
      toolName="subagent_tool_request"
      title={`Requested tool: ${toolName}`}
      context={context}
    />
  )
}
