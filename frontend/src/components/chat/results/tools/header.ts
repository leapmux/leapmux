import type { ToolCall } from '../../model/toolCall'
import { mcpToolCallDisplayName } from '../../model/mcpToolCall'
import { humanizeWireWord } from '../../rendererUtils'
import { rendererFor } from './index'

/**
 * The name one row shows beside its icon: the provider's own label, then the
 * humanized tool NAME when the kind's word says less, then the kind's label.
 */
export function toolCallDisplayName(call: ToolCall): string {
  if (call.label)
    return call.label
  // An MCP call states its server and its tool, which the wire name buries.
  if (call.kind === 'mcp')
    return mcpToolCallDisplayName(call.request)
  const renderer = rendererFor(call)
  return renderer.nameLeads && call.name ? humanizeWireWord(call.name) : renderer.label
}
