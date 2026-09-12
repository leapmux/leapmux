import type { ElicitationRequest } from '../../controls/elicitationForm'
import { MCP_ELICITATION_SUBTYPE } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'

export function claudeElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  const request: Record<string, unknown> = pickObject(payload, 'request', {})
  if (request.subtype !== MCP_ELICITATION_SUBTYPE.Claude)
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
