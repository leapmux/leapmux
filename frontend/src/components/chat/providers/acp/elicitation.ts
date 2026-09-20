import type { ElicitationRequest } from '~/components/chat/model/controlPrompt'
import { MCP_ELICITATION_METHOD } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * Every Agent Client Protocol provider's elicitation, in the MCP form fields.
 *
 * Reasonix is one of them: it sends the standard `elicitation/create` rather than
 * a vendor method, verified against its source tree. A second method used to be
 * tested here and matched nothing.
 */
export function acpElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (payload.method !== MCP_ELICITATION_METHOD.ACP)
    return undefined
  const params: Record<string, unknown> = pickObject(payload, 'params', {})
  return {
    mode: pickString(params, 'mode', 'form'),
    message: pickString(params, 'message', ''),
    server: pickString(params, 'server', ''),
    schema: params.requestedSchema,
    url: pickString(params, 'url', ''),
    title: pickString(params, 'title', ''),
    description: pickString(params, 'description', ''),
  }
}
