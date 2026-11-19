import type { PersistedControlResponse } from '../persistedControlResponse'
import type { ControlResponseSummary } from '~/components/chat/model/controlResponse'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject } from '~/lib/jsonPick'
import { label } from '../persistedControlResponse'
import { createElicitationForm } from './elicitationForm'

/**
 * The display of a persisted answer to an MCP elicitation, with the form's field titles
 * and option labels, or null for a row whose recorded prompt is no elicitation.
 *
 * The prompt that the Worker recorded with the row states the form, and the answer is
 * MCP's own result in either envelope that a runtime takes: a JSON-RPC `result`, or the
 * neutral control response. So no provider reads it.
 */
export function elicitationResponseSummary(record: PersistedControlResponse): ControlResponseSummary | null {
  if (record.prompt?.kind !== 'elicitation')
    return null
  const request = record.prompt.elicitation
  const envelope = pickObject(record.response, 'response', undefined)
  const result = pickObject(record.response, 'result', undefined) ?? pickObject(envelope, 'response', undefined)
  if (result?.action === MCP_ELICITATION_ACTION.Decline)
    return label('Rejected')
  if (result?.action === MCP_ELICITATION_ACTION.Cancel)
    return label('Cancelled')
  if (result?.action !== MCP_ELICITATION_ACTION.Accept)
    return null
  const lines = ['Approved']
  if (isObject(result.content)) {
    const fields = new Map(createElicitationForm(request.schema).fields.map(field => [field.key, field]))
    for (const [key, value] of Object.entries(result.content)) {
      const field = fields.get(key)
      const option = field?.options.find(option => option.value === JSON.stringify(value))
      const text = option?.label ?? (typeof value === 'string'
        ? value
        : prettifyJson(value).trimEnd())
      lines.push(`${field?.label || key}: ${text}`)
    }
  }
  return label(lines.join('\n'))
}
