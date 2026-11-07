import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { CODEWHALE_APPROVAL_FIELD, CODEWHALE_CONTROL_PAYLOAD, CODEWHALE_ENVELOPE_FIELD, CODEWHALE_EVENT } from '~/generated/contracts/codewhale-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { codewhaleToolKind } from './toolKinds'

/**
 * `Provider.extractControl` for Codewhale: an approval, read into the shared permission.
 *
 * The current approval identifies the tool and call and may carry its input and
 * intent summary. The Worker preserves the call input in the shared header.
 * The tool description is static, so the reason comes from the call's intent.
 * LeapMux answers one call with Allow or Deny.
 *
 * A QUESTION never reaches here: `askUserQuestion.isRequest` recognizes it first.
 */
export function codewhaleExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  const event = pickObject(payload, CODEWHALE_CONTROL_PAYLOAD.Event)
  if (pickString(event, CODEWHALE_ENVELOPE_FIELD.Event) !== CODEWHALE_EVENT.ApprovalRequired)
    return null
  const approval = pickObject(event, CODEWHALE_ENVELOPE_FIELD.Payload)
  const toolName = getToolName(payload) || pickString(approval, CODEWHALE_APPROVAL_FIELD.ToolName)
  const toolInput = getToolInput(payload)
  const reason = pickString(approval, CODEWHALE_APPROVAL_FIELD.IntentSummary)
  // A command tool states the command it runs, which the banner draws as code above
  // the arguments. The kind table decides which tools those are.
  const command = codewhaleToolKind(toolName) === 'execute'
    ? pickString(toolInput, 'command')
    : ''
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      ...(reason
        ? { reason }
        : {}),
      input: toolInput,
      ...(command
        ? { command }
        : {}),
      options: [],
    },
  }
}
