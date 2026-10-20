import type { ElicitationRequest } from '~/components/chat/model/controlPrompt'
import { MCP_ELICITATION_METHOD, MCP_ELICITATION_MODE } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * Read the standard Agent Client Protocol elicitation request in MCP form fields.
 * A provider with another native method can use this reader for its standard request path.
 */
export function acpElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (payload.method !== MCP_ELICITATION_METHOD.ACP)
    return undefined
  const params: Record<string, unknown> = pickObject(payload, 'params', {})
  return {
    mode: pickString(params, 'mode', MCP_ELICITATION_MODE.Form),
    message: pickString(params, 'message', ''),
    server: pickString(params, 'server', ''),
    schema: params.requestedSchema,
    url: pickString(params, 'url', ''),
    title: pickString(params, 'title', ''),
    description: pickString(params, 'description', ''),
  }
}
