import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * Goose-specific wire-shape helpers for its subagent tool-request ACP updates.
 * Kept in a standalone module (not goose.tsx) so the renderer and the Goose
 * plugin registration can both import them without a cycle through
 * registerACPProvider (which pulls in the renderers). See `gooseSubagentFromToolCallUpdate`
 * in `backend/internal/worker/agent/goose.go` for the backend twin.
 */

/**
 * Detect a Goose subagent tool-request update by its two-level-nested _meta
 * shape: `_meta.toolNotification.type === "message"` and the discriminator
 * `params.data.type === "subagent_tool_request"`. Goose only ever ships tool
 * REQUESTS over ACP (never results); each carries the subagent_id and the
 * requested tool_call name.
 */
export function isGooseSubagentToolRequest(parent: Record<string, unknown>): boolean {
  const meta = pickObject(parent, '_meta')
  if (!meta)
    return false
  const toolNotification = pickObject(meta, 'toolNotification')
  if (!toolNotification || toolNotification.type !== 'message')
    return false
  const params = pickObject(toolNotification, 'params')
  if (!params)
    return false
  const data = pickObject(params, 'data')
  return data?.type === 'subagent_tool_request'
}

/**
 * Extract the requested tool name from a Goose subagent tool-request update
 * (the `_meta.toolNotification.params.data.tool_call.name` path). Returns the
 * empty string when the shape is absent.
 */
export function gooseSubagentToolRequestName(parent: Record<string, unknown>): string {
  const meta = pickObject(parent, '_meta')
  const data = pickObject(pickObject(pickObject(meta, 'toolNotification'), 'params'), 'data')
  const toolCall = pickObject(data, 'tool_call')
  return toolCall ? pickString(toolCall, 'name') : ''
}
