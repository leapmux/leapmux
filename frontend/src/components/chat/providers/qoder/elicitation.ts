import type { ElicitationRequest } from '../../model/controlPrompt'
import { QODER_CONTROL_REQUEST_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

/** Read Qoder's MCP form request from its control channel. */
export function qoderElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  const request: Record<string, unknown> = pickObject(payload, 'request', {})
  if (request.subtype !== QODER_CONTROL_REQUEST_SUBTYPE.Elicitation)
    return undefined
  return {
    mode: pickString(request, 'mode', 'form'),
    message: pickString(request, 'message', ''),
    server: pickString(request, 'display_name', pickString(request, 'mcp_server_name', '')),
    schema: request.requested_schema,
    url: pickString(request, 'url', ''),
    title: pickString(request, 'title', ''),
    description: pickString(request, 'description', ''),
  }
}
