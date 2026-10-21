import type { ElicitationRequest } from '~/components/chat/model/controlPrompt'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { MCP_ELICITATION_MODE } from '~/generated/contracts/mcp-elicitation'
import { pickString } from '~/lib/jsonPick'
import { copilotEvent } from './protocol'

/** The elicitation form one `elicitation.requested` carries. */
export function copilotElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  const event = copilotEvent(payload)
  if (!event || event.type !== COPILOT_EVENT.ElicitationRequested)
    return undefined
  const data = event.data
  return {
    mode: pickString(data, 'mode', MCP_ELICITATION_MODE.Form),
    message: pickString(data, 'message', ''),
    server: pickString(data, 'elicitationSource', ''),
    schema: data.requestedSchema,
    url: pickString(data, 'url', ''),
    title: '',
    description: '',
  }
}
