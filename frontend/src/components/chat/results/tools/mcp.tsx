import type { JSX } from 'solid-js'
import type { ParsedCall, ResolvedCall, ToolKindRenderer, ToolRowView } from './renderer'
import Blocks from 'lucide-solid/icons/blocks'
import { mcpToolCallDisplayName } from '../../model/mcpToolCall'
import { genericFallbackMeta, genericPendingBody, genericPreviewCollapsible, genericResultBody, genericResultMeta, genericTitle } from './generic'
import { previewWithoutResult } from './renderer'

/**
 * The MCP call's own renderer: its request states the server and the tool, so the
 * display name heads the row and the image tabs without any kind check -- the typed
 * `McpRequest` carries the pair the generic duo does not.
 */
export const mcpRenderer: ToolKindRenderer<'mcp'> = {
  icon: Blocks,
  label: 'MCP Tool',
  nameLeads: true,
  title(call: ParsedCall<'mcp'>): JSX.Element | string {
    return genericTitle(mcpToolCallDisplayName(call.request), call.request.args)
  },
  pending: genericPendingBody,
  pendingCollapsible: genericPreviewCollapsible,
  withoutTypedResult: previewWithoutResult(genericPendingBody),
  withoutTypedResultMeta: genericFallbackMeta,
  result(call: ResolvedCall<'mcp'>, view: ToolRowView) {
    return genericResultBody(call, view, mcpToolCallDisplayName(call.request))
  },
  resultMeta: genericResultMeta,
}
