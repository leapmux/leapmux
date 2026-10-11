import type { ElicitationRequest } from '~/components/chat/model/controlPrompt'
import { REASONIX_METHOD } from '~/generated/contracts/reasonix-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { acpElicitation } from '../acp/elicitation'

/** Read Reasonix's own MCP request and the standard ACP form. */
export function reasonixElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (payload.method !== REASONIX_METHOD.McpRequestInteraction)
    return acpElicitation(payload)
  const params = pickObject(payload, 'params') ?? {}
  return {
    mode: pickString(params, 'mode', 'form'),
    message: pickString(params, 'message'),
    server: pickString(params, 'server'),
    schema: params.requestedSchema,
    url: pickString(params, 'url'),
    title: '',
    description: '',
  }
}
