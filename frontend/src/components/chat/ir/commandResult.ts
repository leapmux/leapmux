import type { ToolRowStatus } from './toolRowStatus'
import type { NormalizedProgressOutput } from '~/lib/normalizeProgressOutput'
import { normalizedCommandBody, PROGRESS_MAX_ROWS } from '~/lib/normalizeProgressOutput'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { toolOutcomeLabel } from './toolOutcomeLabel'

/**
 * The result of ONE command an execute call ran (Claude `Bash`, Codex
 * `commandExecution`, ACP `execute`). A call that ran several holds one of
 * these for each.
 *
 * `output` is the raw stream (may contain ANSI). Claude's structured Bash
 * payload separates `stdout` and `stderr`; the extractor joins them into
 * `output`, because the body draws one stream.
 *
 * The OUTCOME lives on the row's one status word, not here: a result that
 * also stated `interrupted` or `isError` restated what `status` already
 * says, and the two spellings drifted.
 */
export type CommandResult = CommandResultFacts & CommandExit

/**
 * How the PROCESS itself ended: with a code of its own, or with the signal that ended
 * it. Never both, and a command that has not returned states neither.
 *
 * The two are exclusive at the source -- one `os.ProcessState` answers with a code or
 * with a signal -- and the label reads the code FIRST. A result that stated both drew
 * "Success" for a process the OS had killed, and dropped the signal on the way.
 * Writing it as a union is what makes that pair impossible to build.
 */
export type CommandExit
  = | { exitCode?: number | null, signal?: never }
    | { exitCode?: never, signal: string }

interface CommandResultFacts {
  output: string
  /** A referenced output stream could not be recovered. This is different from an empty stream. */
  outputUnavailable?: boolean
  durationMs?: number | null
  /** The provider retained only a suffix of the command output. */
  truncated?: boolean
  /** What identifies this command inside a call that ran several. */
  label?: string
}

/**
 * How ONE command ended, as the row words it.
 *
 * Takes the whole result rather than two fields, so a caller cannot hand the label a
 * code and a signal that the source itself could never carry together.
 */
export function commandExit(source: CommandResult): CommandExit {
  // The CODE answers first, the same order `commandStatusLabel` reads them in. The
  // type forbids the pair, but a payload built through a cast can still carry it,
  // and the two must then word the row the same way. A `null` code is a code the
  // process reported as none; a key that never existed stays absent.
  if (source.exitCode !== undefined && source.exitCode !== null)
    return { exitCode: source.exitCode }
  if (source.signal !== undefined)
    return { signal: source.signal }
  return source.exitCode === null ? { exitCode: null } : {}
}

/**
 * The row count below which {@link CommandResultBody} stops collapsing: widened to
 * {@link PROGRESS_MAX_ROWS} when the output carried `\r`-overwrites (so the head/`…`/
 * tail rows the normalize step just produced aren't sliced back off), else the plain
 * {@link COLLAPSED_RESULT_ROWS}. The body and the toolbar's `collapsible` check
 * (`commandOutputIsCollapsible`) must agree on this threshold or the expand button
 * hides over output the body actually clips -- so both read it from here.
 */
export function commandCollapseThreshold(hadCarriageReturns: boolean): number {
  return hadCarriageReturns ? PROGRESS_MAX_ROWS : COLLAPSED_RESULT_ROWS
}

/**
 * One {@link normalizedCommandBody} per command result object.
 *
 * Keyed on the RESULT, weakly: the row IR a revision holds is one object graph, so
 * the body component and the toolbar's `collapsible` check -- which read the same
 * command from the same revision -- share ONE normalize instead of the two caches
 * they used to keep (a per-row render-cache entry beside a module-global keyed by
 * the whole output text). When the row rebuilds, the old result is garbage and the
 * entry goes with it, so nothing evicts and nothing is shed.
 *
 * `commandOutputIsCollapsible` runs from `resultMeta`, which recomputes on every
 * reactive pass; without this map a `\r`-heavy build log paid for the whole regex,
 * split, map and join again on every streamed frame.
 */
const normalizedByCommand = new WeakMap<CommandResult, NormalizedProgressOutput>()

/**
 * A normalized copy past this size is not worth holding: it is one copy of a build
 * log the row itself already carries, and a running command produces a NEW one per
 * frame. The normalize still runs -- the body needs it -- but the map does not
 * retain it.
 */
const MAX_CACHED_NORMALIZED_CHARS = 4 * 1024 * 1024

/**
 * The normalized body of one command result, computed at most once per result
 * object. Both the rendered body and the collapsibility metadata read this, so the
 * two can never disagree about what the output normalizes into.
 */
export function normalizedCommandOutput(command: CommandResult): NormalizedProgressOutput {
  const cached = normalizedByCommand.get(command)
  if (cached !== undefined)
    return cached
  const normalized = normalizedCommandBody(command.output)
  if (normalized.text.length <= MAX_CACHED_NORMALIZED_CHARS)
    normalizedByCommand.set(command, normalized)
  return normalized
}

/**
 * Mirror of {@link CommandResultBody}'s collapse decision for tool-meta
 * `collapsible` checks. `hasMoreLinesThan` against raw `\n`s under-counts
 * when output contains `\r`-overwrites (progress bars, `git rebase`, etc.)
 * because the body normalizes those into separate lines at render time —
 * leaving the toolbar's expand button hidden over output the body actually
 * clips. Use this helper for any provider feeding `CommandResultBody` so
 * the meta and the body agree.
 *
 * It reads {@link normalizedCommandOutput}, the same ONE transform the body reads.
 * Normalizing alone over-counted instead: the body strips the leading blank lines
 * afterwards, so output that led with three of them offered an expand button over
 * a body that clipped nothing.
 */
export function commandOutputIsCollapsible(command: CommandResult): boolean {
  const { text: normalized, hadCarriageReturns } = normalizedCommandOutput(command)
  return hasMoreLinesThan(normalized, commandCollapseThreshold(hadCarriageReturns))
}

/**
 * Whether the PROCESS reported a failure: a known non-zero exit code, or -- where no
 * code is known -- the signal that ended it.
 *
 * The CALL's own status is a separate question, and the caller asks it separately.
 * This took a `status` parameter once, typed as a bare `string` a caller could spell
 * `'Failed'` into; the one caller passed `undefined` and tested the status itself.
 */
export function commandIsError(exit: CommandExit): boolean {
  if (typeof exit.exitCode === 'number')
    return exit.exitCode !== 0
  return !!exit.signal
}

/**
 * Canonical status label, read from the row's ONE outcome word and the verdict the
 * command itself reported:
 *  - declined → "Declined"
 *  - cancelled → "Interrupted"
 *  - exitCode known and non-zero → "Error (exit N)"
 *  - no exit code and a signal → "Error (<signal>)", e.g. "Error (killed)"
 *  - exitCode known and zero → "Success"
 *  - failed → "Error"
 *  - otherwise → "Success"
 */
export function commandStatusLabel(status: ToolRowStatus, exit: CommandExit): string {
  if (status === 'declined')
    return toolOutcomeLabel('declined')
  if (status === 'cancelled')
    return toolOutcomeLabel('interrupted')
  if (typeof exit.exitCode === 'number' && exit.exitCode !== 0)
    return toolOutcomeLabel('failed', `exit ${exit.exitCode}`)
  // A signal answers only when no exit code does. The type already makes the pair
  // unbuildable; this states which one the row reads when only one is there.
  if (exit.signal)
    return toolOutcomeLabel('failed', exit.signal)
  // The STATUS outranks a zero exit code, and states no qualifier for one.
  //
  // The two describe different things: an exit code is the PROCESS's verdict, a
  // status is the CALL's. A call can fail around a process that exited 0 -- the
  // runtime lost the output, or refused the call after it ran -- and this interface
  // already says the outcome lives on the status word alone. A zero answering first
  // worded such a row "Success", which `showStatusHeader` then reads as "no header
  // needed" while `statesOwnOutcome` suppresses the shared one, so nothing anywhere
  // on the row stated the failure. `exit 0` is not a reason either: only a non-zero
  // code explains anything, and the body carries the real reason.
  if (status === 'failed')
    return toolOutcomeLabel('failed')
  return toolOutcomeLabel('succeeded')
}
