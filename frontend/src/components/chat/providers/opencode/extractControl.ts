import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { OPENCODE_NATIVE_EVENT, OPENCODE_NATIVE_PERMISSION_DECISION, OPENCODE_NATIVE_TOOL } from '~/generated/contracts/opencode-native-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

/** Read a native permission without importing the ACP envelope. */
export function openCodeExtractControl({ payload }: ControlExtractionInput): ExtractedControlRequest | null {
  if (payload.type !== OPENCODE_NATIVE_EVENT.PermissionAsked)
    return null
  const data = pickObject(payload, 'data')
  if (!data)
    return null
  const tool = pickString(data, 'action')
  const resources = Array.isArray(data.resources)
    ? data.resources.filter((value): value is string => typeof value === 'string')
    : []
  const options: PermissionOption[] = [
    { optionId: OPENCODE_NATIVE_PERMISSION_DECISION.Once, kind: 'allow_once', name: 'Allow' },
  ]
  if (Array.isArray(data.save) && data.save.length > 0) {
    options.push({
      optionId: OPENCODE_NATIVE_PERMISSION_DECISION.Always,
      kind: 'allow_always',
      name: 'Always allow',
      scope: 'project',
    })
  }
  options.push({ optionId: OPENCODE_NATIVE_PERMISSION_DECISION.Reject, kind: 'reject_once', name: 'Deny' })
  const command = tool === OPENCODE_NATIVE_TOOL.Shell
    ? resources.join('\n')
    : ''
  const message = pickString(data, 'message')
  const metadata = pickObject(data, 'metadata') ?? {}
  return {
    kind: 'permission',
    permission: {
      title: tool,
      ...(command
        ? { command }
        : {}),
      input: command
        ? metadata
        : { ...metadata, resources },
      ...(message
        ? { reason: message }
        : {}),
      options,
    },
  }
}
