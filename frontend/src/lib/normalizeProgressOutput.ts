/**
 * Render-time helper that turns CLI progress output (lines overwritten with
 * bare `\r`) into separate logical lines so the result body shows start/end
 * instead of a single jumbled overwrite.
 *
 * Example: `Rebasing (1/10)\rRebasing (2/10)\r…\rRebasing (10/10)\rDone.`
 * becomes:
 *
 * ```
 * Rebasing (1/10)
 * Rebasing (2/10)
 * Rebasing (3/10)
 * …
 * Rebasing (9/10)
 * Rebasing (10/10)
 * Done.
 * ```
 *
 * Raw `\r` is preserved in the persisted message; this transform happens at
 * render time only.
 */

/** Lines kept from the start of a long carriage-return run. */
export const PROGRESS_HEAD = 3
/** Lines kept from the end of a long carriage-return run. */
export const PROGRESS_TAIL = 3
/**
 * Maximum displayed rows produced by the head/`…`/tail collapse:
 * `PROGRESS_HEAD + 1 (ellipsis) + PROGRESS_TAIL`. Also acts as the row
 * threshold below which a CR run is shown verbatim — collapsing fewer
 * segments would not save any rows.
 */
export const PROGRESS_MAX_ROWS = PROGRESS_HEAD + 1 + PROGRESS_TAIL
const ELLIPSIS = '…'

export interface NormalizedProgressOutput {
  text: string
  hadCarriageReturns: boolean
}

/**
 * Normalize a tool-result string so each `\r`-overwritten chunk becomes its
 * own line. CRLF is converted to LF first to avoid splitting Windows line
 * endings on the bare-`\r` step. Within each `\n`-delimited group, any run
 * of `> PROGRESS_MAX_ROWS` `\r`-separated segments collapses to first
 * `PROGRESS_HEAD` + a literal `…` line + last `PROGRESS_TAIL`.
 *
 * Returns the original reference unchanged when no `\r` is present so the
 * caller's memo can short-circuit cheaply.
 */
export function normalizeProgressOutput(text: string): NormalizedProgressOutput {
  if (!text.includes('\r'))
    return { text, hadCarriageReturns: false }
  const lfText = text.replace(/\r\n/g, '\n')
  const out = lfText.split('\n').map(collapseCarriageRunIfNeeded).join('\n')
  return { text: out, hadCarriageReturns: true }
}

function collapseCarriageRunIfNeeded(group: string): string {
  if (!group.includes('\r'))
    return group
  const segments = group.split('\r')
  if (segments.length <= PROGRESS_MAX_ROWS)
    return segments.join('\n')
  const head = segments.slice(0, PROGRESS_HEAD)
  const tail = segments.slice(-PROGRESS_TAIL)
  return [...head, ELLIPSIS, ...tail].join('\n')
}
