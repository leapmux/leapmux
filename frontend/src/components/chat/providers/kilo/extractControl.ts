import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import type { PermissionOption } from '~/components/chat/model/controlPrompt'
import { acpPermissionPrompt } from '../acp/extractControl'

/**
 * The pair Kilo itself answers with, for a request that offers no options.
 *
 * These are the daemon's real option ids -- it maps an unknown id to reject, so a
 * synthesized pair with invented ids would turn every Allow into a reject.
 */
const DEFAULT_OPTIONS: readonly PermissionOption[] = [
  { optionId: 'once', kind: 'allow_once', name: 'Allow' },
  { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
]

/** Read Kilo permissions and supply its native IDs when no options arrive. */
export function kiloExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const permission = acpPermissionPrompt(input)
  if (!permission)
    return null
  return {
    kind: 'permission',
    permission: permission.options.length > 0
      ? permission
      : {
          ...permission,
          options: [...DEFAULT_OPTIONS],
        },
  }
}
