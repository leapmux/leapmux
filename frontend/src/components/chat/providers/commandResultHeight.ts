import type { HeightInput } from '../chatHeightEstimator'
import type { CommandResultSource } from '../results/commandResult'
import type { NormalizedProgressOutput } from '~/lib/normalizeProgressOutput'
import { PROGRESS_MAX_ROWS } from '~/lib/normalizeProgressOutput'
import { monoBody } from '../chatHeightShared'
import { commandStatusLabel } from '../results/commandResult'

/**
 * Height fields for a settled command tool-result body (CommandResultBody): the mono
 * output body, the resolved collapse state, the error flag, and a status header drawn
 * ONLY when the result label isn't "Success" -- plus the carriage-return-widened
 * collapse threshold. Shared by the Codex (commandExecution) and ACP (execute) height
 * hooks, which size the SAME renderer from a CommandResultSource. The caller normalizes
 * the output first (Codex strips its tool-use header; ACP doesn't) and adds any
 * per-provider chrome (ACP's tool header + command summary line), so this owns only the
 * body geometry the two share -- a change to how CommandResultBody charges its status
 * header or CR-collapse threshold lands here once instead of drifting between the two
 * providers' off-screen estimates.
 */
export function commandResultBodyFields(
  source: CommandResultSource,
  body: NormalizedProgressOutput,
  collapsed: boolean,
): Partial<HeightInput> {
  const fields: Partial<HeightInput> = {
    toolUseRendersResultBody: true,
    ...monoBody(body.text),
    collapsed,
    isError: source.isError,
    // CommandResultBody draws a status header only when the label isn't "Success".
    hasHeader: commandStatusLabel(source) !== 'Success',
  }
  if (body.hadCarriageReturns)
    fields.collapsedRowThreshold = PROGRESS_MAX_ROWS
  return fields
}
