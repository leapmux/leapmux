import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { DROID_CONFIRMATION_TYPE, DROID_NOTIFICATION_FIELD, DROID_REQUEST_TYPE } from '~/generated/contracts/droid-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { droidToolKind } from './toolKinds'

/**
 * `Provider.extractControl` for Factory Droid.
 *
 * The worker publishes one request per `droid.request_permission`, in an
 * envelope of its own. Droid offers its own list of options on a permission
 * request. The shared Allow and Deny answer it, and the worker turns them into
 * `proceed_once` and `cancel`.
 *
 * A QUESTION takes its own path — `askUserQuestion.isRequest` recognizes it
 * before this reader runs — so this reader answers a permission alone.
 */
export function droidExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (pickString(payload, 'type') !== DROID_REQUEST_TYPE.Permission)
    return null

  const toolUse = pickObject(payload, DROID_NOTIFICATION_FIELD.ToolUse) ?? {}
  // Droid's `toolUse` marshals `name`, not `toolName`.
  const toolName = pickString(toolUse, 'name') || pickString(toolUse, DROID_NOTIFICATION_FIELD.ToolName) || 'Tool'
  const toolInput = pickObject(toolUse, 'input') ?? {}

  // The confirmation type, not the tool's display name, is what makes a request a
  // review: Droid itself groups exit_spec_mode and propose_mission on it. Both
  // carry the document to review in `details` — `plan` and `proposal` — and the
  // transcript holds no other copy of it, so the card draws it.
  const confirmationType = pickString(payload, 'confirmationType')
  if (confirmationType === DROID_CONFIRMATION_TYPE.ExitSpecMode || confirmationType === DROID_CONFIRMATION_TYPE.ProposeMission) {
    const details = pickObject(payload, 'details') ?? {}
    const document = confirmationType === DROID_CONFIRMATION_TYPE.ProposeMission
      ? pickString(details, 'proposal')
      : pickString(details, 'plan')
    const text = document.trim() === '' ? '' : document
    return text ? { kind: 'plan', text } : { kind: 'plan' }
  }

  const shell = droidToolKind(toolName) === 'execute'
  const command = shell ? pickString(toolInput, 'command') || pickString(toolInput, 'cmd') : ''
  // A Droid Shield refusal states why the call was blocked in `details.reason`;
  // the generic card would draw only the command.
  const reason = confirmationType === DROID_CONFIRMATION_TYPE.DroidShieldViolation
    ? pickString(pickObject(payload, 'details') ?? {}, DROID_NOTIFICATION_FIELD.Reason)
    : ''
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      input: toolInput,
      ...(command !== '' ? { command } : {}),
      ...(reason !== '' ? { reason } : {}),
      options: [],
    },
  }
}
