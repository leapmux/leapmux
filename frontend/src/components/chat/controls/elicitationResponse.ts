import type { ControlResponseDeriver } from '../persistedControlResponse'
import type { ElicitationRequest } from './elicitationForm'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject } from '~/lib/jsonPick'
import { label } from '../persistedControlResponse'
import { createElicitationForm } from './elicitationForm'

/** Use the request's field titles and option labels in the saved answer. */
export function withElicitationResponse(
  extract: (payload: Record<string, unknown>) => ElicitationRequest | undefined,
  fallback: ControlResponseDeriver,
): ControlResponseDeriver {
  return (record) => {
    const request = record.request && extract(record.request)
    if (!request)
      return fallback(record)
    const envelope = pickObject(record.response, 'response', undefined)
    const result = pickObject(record.response, 'result', undefined) ?? pickObject(envelope, 'response', undefined)
    if (result?.action === MCP_ELICITATION_ACTION.Decline)
      return label('Rejected')
    if (result?.action === MCP_ELICITATION_ACTION.Cancel)
      return label('Cancelled')
    if (result?.action !== MCP_ELICITATION_ACTION.Accept)
      return fallback(record)
    const lines = ['Approved']
    if (isObject(result.content)) {
      const fields = new Map(createElicitationForm(request.schema).fields.map(field => [field.key, field]))
      for (const [key, value] of Object.entries(result.content)) {
        const field = fields.get(key)
        const option = field?.options.find(option => option.value === JSON.stringify(value))
        const text = option?.label ?? (typeof value === 'string' ? value : prettifyJson(value).trimEnd())
        lines.push(`${field?.label || key}: ${text}`)
      }
    }
    return label(lines.join('\n'))
  }
}
