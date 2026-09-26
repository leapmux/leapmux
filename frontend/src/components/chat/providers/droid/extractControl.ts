import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { DROID_NOTIFICATION_FIELD } from '~/generated/contracts/droid-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { droidToolKind } from './toolKinds'

/**
 * `Provider.extractControl` for Factory Droid.
 *
 * The worker publishes one request per `droid.request_permission`, in an
 * envelope of its own. Droid offers its 17-option list on a permission request,
 * and the shared Allow and Deny answer it.
 *
 * A QUESTION takes its own path — `askUserQuestion.isRequest` recognizes it
 * before this reader runs — so this reader answers a permission alone.
 */
export function droidExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (pickString(payload, 'type') !== 'permission_request')
    return null

  const toolUse = pickObject(payload, DROID_NOTIFICATION_FIELD.ToolUse) ?? {}
  // Droid's `toolUse` marshals `name`, not `toolName`.
  const toolName = pickString(toolUse, 'name') || pickString(toolUse, DROID_NOTIFICATION_FIELD.ToolName) || 'Tool'
  const toolInput = pickObject(toolUse, 'input') ?? {}
  const shell = droidToolKind(toolName) === 'execute'
  const command = shell ? pickString(toolInput, 'command') || pickString(toolInput, 'cmd') : ''
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
