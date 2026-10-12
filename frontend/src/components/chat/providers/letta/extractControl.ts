import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { LETTA_DELTA_FIELD } from '~/generated/contracts/letta-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { lettaToolKind } from './toolKinds'

/**
 * `Provider.extractControl` for Letta Code.
 *
 * The worker publishes one permission request per `can_use_tool` control request,
 * and one question request per receipt of an `AskUserQuestion` call. A question
 * takes its own path — `askUserQuestion.isRequest` recognizes it before this
 * reader runs — so this reader answers a permission alone. A permission for the
 * question tool is one of them: Strict mode asks whether the tool may run.
 */
export function lettaExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (pickString(payload, 'type') !== 'permission')
    return null

  const toolName = pickString(payload, LETTA_DELTA_FIELD.ToolName) || 'Tool'
  const toolInput = pickObject(payload, LETTA_DELTA_FIELD.ToolInput) ?? {}
  const shell = lettaToolKind(toolName) === 'execute'
  const command = shell ? pickString(toolInput, 'command') : ''
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      input: toolInput,
      ...(command !== '' ? { command } : {}),
      options: [],
    },
  }
}
