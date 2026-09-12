import type { ElicitationRequest } from '../../controls/elicitationForm'
import { MCP_ELICITATION_METHOD } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'

/** ACP and Reasonix use the MCP form fields without changing their types. */
export function acpElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (payload.method !== MCP_ELICITATION_METHOD.ACP && payload.method !== MCP_ELICITATION_METHOD.Reasonix)
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
