import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { AMP_PERMISSION_REQUEST_FIELD, AMP_PERMISSION_REQUEST_TYPE } from '~/generated/contracts/amp-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { ampShellCommand } from './extractors/execute'
import { ampToolKind } from './toolKinds'

/**
 * `Provider.extractControl` for Amp.
 *
 * Amp asks for no permission in stream-JSON mode. Its `delegate` permission rule runs
 * the LeapMux helper for each call that its local executor runs, and the worker
 * publishes one request for each helper run, in an envelope of LeapMux's own: the
 * tool, its arguments, and the call's id when a transcript row matched the call.
 *
 * Amp offers no option list, so the shared Allow and Deny answer. A shell call states
 * its command and its directory, which the banner draws above the arguments. The
 * command comes from `ampShellCommand`, which the transcript reads too.
 */
export function ampExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (pickString(payload, AMP_PERMISSION_REQUEST_FIELD.Type) !== AMP_PERMISSION_REQUEST_TYPE.Request)
    return null
  const toolName = pickString(payload, AMP_PERMISSION_REQUEST_FIELD.ToolName)
  const toolInput = pickObject(payload, AMP_PERMISSION_REQUEST_FIELD.Input) ?? {}
  const shell = ampToolKind(toolName) === 'execute'
  const command = shell ? ampShellCommand(toolInput) || undefined : undefined
  const workingDirectory = shell ? pickString(toolInput, 'workdir', undefined) : undefined
  return {
    kind: 'permission',
    permission: {
      title: toolName || 'Tool',
      input: toolInput,
      ...(command !== undefined ? { command } : {}),
      ...(workingDirectory !== undefined ? { workingDirectory } : {}),
      options: [],
    },
  }
}
