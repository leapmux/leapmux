import type { ElicitationRequest } from '../../model/controlPrompt'
import { MCP_ELICITATION_MODE } from '~/generated/contracts/mcp-elicitation'
import { ZCODE_INTERACTION, ZCODE_METHOD } from '~/generated/contracts/zcode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * Read an MCP server's input form from a stored ZCode user-input request.
 *
 * ZCode forwards the form as `interaction/requestUserInput` of the `mcp_elicitation` interaction. The request also
 * flattens the form into questions, which lose the type of each field: a choice becomes a free-text box, and a number
 * loses its limits. The form card reads the server's own schema instead, and the worker turns the typed answer back
 * into the text answers that ZCode reads.
 */
export function zcodeElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (pickString(payload, 'method') !== ZCODE_METHOD.RequestUserInput)
    return undefined
  const params: Record<string, unknown> = pickObject(payload, 'params', {})
  const schema: Record<string, unknown> = pickObject(params, 'schema', {})
  if (schema.interaction !== ZCODE_INTERACTION.McpElicitation)
    return undefined
  const input: Record<string, unknown> = pickObject(params, 'input', {})
  return {
    mode: MCP_ELICITATION_MODE.Form,
    message: pickString(input, 'message') || pickString(params, 'prompt'),
    server: pickString(schema, 'serverName') || pickString(input, 'serverName'),
    schema: schema.requestedSchema ?? input.requestedSchema,
  }
}
