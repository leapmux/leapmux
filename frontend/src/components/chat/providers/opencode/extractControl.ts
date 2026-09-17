import type { WirePermissionOption } from '../../controls/permissionOptionLabels'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { acpPermissionIR } from '../acp/extractControl'

/**
 * The pair OpenCode itself answers with, for a request that offers no options.
 *
 * These are the daemon's real option ids -- it maps an unknown id to reject, so a
 * synthesized pair with invented ids would turn every Allow into a reject.
 */
const DEFAULT_OPTIONS: readonly WirePermissionOption[] = [
  { optionId: 'once', kind: 'allow_once', name: 'Allow' },
  { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
]

/**
 * `Provider.extractControl` for OpenCode and Kilo.
 *
 * The two daemons speak the Agent Client Protocol, so the reader is the shared one.
 * They differ in ONE thing: a permission request of theirs can carry no option list
 * at all, and the daemon still expects one of its own two ids back. The shared
 * Allow/Deny pair sends neither, so the pair is stated here instead.
 */
export function openCodeExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const permission = acpPermissionIR(input)
  if (!permission)
    return null
  return {
    kind: 'permission',
    permission: permission.options.length > 0 ? permission : { ...permission, options: [...DEFAULT_OPTIONS] },
  }
}
