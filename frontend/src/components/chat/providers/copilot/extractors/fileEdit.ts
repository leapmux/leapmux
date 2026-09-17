import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { pickString } from '~/lib/jsonPick'

/**
 * The patch text one Copilot row carries, or the empty string for every other tool.
 *
 * The ENVELOPE inside that text is the shared apply-patch dialect, which
 * `ir/applyPatch.ts` reads for the three runtimes that send it. This is the half that
 * belongs to Copilot: which tool sends a patch, and under which argument key. Copilot
 * spells the key `input`, and `patch` on an older frame, so both reach this build from
 * a persisted transcript.
 */
export function copilotPatchText(toolName: string, input: Record<string, unknown>): string {
  if (toolName !== COPILOT_TOOL.ApplyPatch)
    return ''
  return pickString(input, 'input') || pickString(input, 'patch')
}
