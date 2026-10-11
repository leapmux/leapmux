import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { CODEWHALE_APPROVAL_FIELD, CODEWHALE_CONTROL_PAYLOAD, CODEWHALE_ENVELOPE_FIELD, CODEWHALE_EVENT } from '~/generated/contracts/codewhale-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { codewhaleToolKind } from './toolKinds'

/**
 * `Provider.extractControl` for Codewhale: an approval, read into the shared permission.
 *
 * The runtime's `approval.required` states the tool and the call, and no arguments up
 * to 0.10.0. The worker takes the arguments from the call's own start event and puts
 * them in the shared header, so the banner draws the command the call runs. The
 * event's `description` is the tool's STATIC description, which says nothing about this
 * call; the reason is the call's own intent summary, which a later release sends.
 *
 * Codewhale answers an approval with allow or deny alone. It offers no scope: a
 * remembered approval switches the whole thread to full access up to 0.10.0, which the
 * worker never sends, so the banner shows the shared Allow and Deny pair.
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
  const reason = pickString(approval, CODEWHALE_APPROVAL_FIELD.IntentSummary) || pickString(approval, CODEWHALE_APPROVAL_FIELD.Summary)
  // A command tool states the command it runs, which the banner draws as code above
  // the arguments. The kind table decides which tools those are.
  const command = codewhaleToolKind(toolName) === 'execute' ? pickString(toolInput, 'command') : ''
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      ...(reason ? { reason } : {}),
      input: toolInput,
      ...(command ? { command } : {}),
      options: [],
    },
  }
}
