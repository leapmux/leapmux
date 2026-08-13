/**
 * Shared row-render geometry metadata used before a row mounts.
 *
 * Pixel height estimation is intentionally absent: row heights come from
 * visible/hidden DOM measurement plus the virtualizer's generic unknown-row
 * fallback. This module carries the identifiers that let caches know when a
 * previously measured DOM height is stale, plus the few fixed constants that
 * the offset map and the stylesheet must agree on.
 */

/**
 * Prefix that packs a tool name into a row's entry kind (`tool_use:${toolName}`).
 * Defined once here and shared by the packer and decoders so the literal can't
 * drift.
 */
export const TOOL_USE_KIND_PREFIX = 'tool_use:'

/** The tool name packed into a `tool_use:${name}` entry kind, or undefined. */
export function toolNameFromKind(kind: string): string | undefined {
  return kind.startsWith(TOOL_USE_KIND_PREFIX) ? kind.slice(TOOL_USE_KIND_PREFIX.length) : undefined
}

/**
 * Which full-bleed band a row paints. An assistant message draws a solid line
 * on its top and bottom edge; an assistant thought draws a dashed one.
 */
export type MessageBandKind = 'text' | 'thought'

/**
 * The band a row kind paints, or undefined when the row is not a band.
 *
 * This is the ONE place that decides which kinds render as a band. The
 * virtualizer reads it to close the gap between two adjacent bands, and
 * messageClassification reads it to pick the row and bubble classes -- both
 * from here, so the offset map and the borders can never disagree. It lives in
 * this leaf module (no imports) rather than in messageClassification, whose own
 * imports pull in every provider plugin: the virtualizer must stay clear of that
 * graph.
 *
 * `plan_execution` is deliberately absent. It shares the thought expander but
 * renders as a right-aligned accent bubble, which marks the plan hand-off as a
 * distinct event rather than more agent narration.
 */
export function messageBandKind(kind: string): MessageBandKind | undefined {
  if (kind === 'assistant_text')
    return 'text'
  if (kind === 'assistant_thinking')
    return 'thought'
  return undefined
}

/**
 * Width of a band's top and bottom border, in CSS pixels.
 *
 * Two adjacent bands are placed exactly this far apart in the NEGATIVE
 * direction (see gapAfter in ~/components/chat/useChatVirtualizer.ts), so the
 * lower row's top border lands on the upper row's bottom border and the pair
 * shows one line instead of two. Keep it equal to the border width that
 * `bandRow` declares in ~/components/chat/messageStyles.css.ts.
 */
export const BAND_BORDER_PX = 1

/**
 * Per-row inputs that make a cached DOM measurement stale even when the message
 * id stays stable. Live streaming text is deliberately excluded: streaming rows
 * measure at the tail instead of invalidating the cache per delta.
 */
export interface HeightKeyInputs {
  /** Message seq -- a reseq / in-place consolidation bumps it. */
  seq: bigint
  /** A paired tool_use sibling is available. */
  hasToolUseSibling: boolean
  /**
   * The paired tool_use opener's content version (0 for non-result rows / no
   * opener). A tool_result can render from that opener, so the measurement cache
   * must change when the opener changes.
   */
  toolUseContentVersion: number
  /**
   * Paired tool_use opener identity/seq/content-version token. Content version
   * alone misses present-to-different-present sibling replacement at version 0.
   */
  toolUseRevisionKey: string
  /** A paired tool_result sibling is available. */
  hasToolResultSibling: boolean
  /**
   * The paired tool_result's content version (0 for non-opener rows / no result).
   * Some tool_use rows render from hidden result data, so the measurement cache
   * must change when that result changes.
   */
  toolResultContentVersion: number
  /** Paired tool_result identity/seq/content-version token. */
  toolResultRevisionKey: string
  /** Per-message UI version -- a per-row expand / diff-view toggle bumps it. */
  uiVersion: number
  /** Content version -- a same-seq in-place body replacement bumps it. */
  contentVersion: number
  /** Whether a renderable command stream is present for the row. */
  hasCommandStream: boolean
  /**
   * Whether the row was classified as part of a SUBAGENT's own transcript. The
   * flag re-classifies a forwarded row between a collapsed "Prompt" card and a
   * full user message -- two very different heights -- and it flips when the
   * tab's parent link hydrates, so the measurement must re-key with it.
   */
  isChildTranscript: boolean
}

export function buildHeightKey(inputs: HeightKeyInputs): string {
  const toolUseRevision = `${inputs.toolUseRevisionKey.length}:${inputs.toolUseRevisionKey}`
  const toolResultRevision = `${inputs.toolResultRevisionKey.length}:${inputs.toolResultRevisionKey}`
  return `${inputs.seq}|${inputs.hasToolUseSibling ? 's' : ''}|${inputs.toolUseContentVersion}|${toolUseRevision}|${inputs.hasToolResultSibling ? 'r' : ''}|${inputs.toolResultContentVersion}|${toolResultRevision}|${inputs.uiVersion}|${inputs.contentVersion}|${inputs.hasCommandStream ? 'c' : ''}|${inputs.isChildTranscript ? 'k' : ''}`
}

/**
 * The height-key contribution of GLOBAL layout preferences that affect only SOME row
 * kinds. Appended per row so a global toggle re-keys (hence re-measures) ONLY the rows
 * whose rendered height actually depends on that pref -- not the whole viewport, as a
 * shared epoch term would:
 *
 *  - `diffView` (unified vs split) changes height only for rows that render a file diff,
 *    which are always `tool_use` / `tool_result` rows. This pair is a proven SUPERSET of
 *    every diff render across all providers (Claude/Pi render the diff on `tool_result`,
 *    ACP/Codex on `tool_use`; no diff renders under a non-tool kind). Over-inclusion (a
 *    non-diff Bash/Read tool row) only costs a needless re-measure -- NEVER a stale,
 *    overlapping height, which a too-narrow predicate would. Do not narrow below the two
 *    kinds: `tool_result` carries no tool name to distinguish a diff result from a plain one.
 *  - the thinking-expand default (`expandAgentThoughts`) changes height only for
 *    `assistant_thinking` rows.
 *
 * Each resolver returns the row's EFFECTIVE value (per-message override ?? global default),
 * so a global toggle skips a row whose override already pins its value, and toggling a pref
 * back and forth stays cacheable (the term is a stable VALUE per state, not a monotonic
 * counter). The resolvers are thunks so only the one relevant to the kind is evaluated (a
 * diff row never resolves the thinking state, and vice versa). Returns '' for a kind no
 * scoped pref affects -- most of the window -- leaving that row's key epoch-stable.
 */
export function kindScopedLayoutKey(
  kind: string,
  resolveEffectiveDiffView: () => string,
  resolveEffectiveThinkingExpanded: () => boolean,
): string {
  if (kind === 'tool_use' || kind === 'tool_result')
    return `|d:${resolveEffectiveDiffView()}`
  if (kind === 'assistant_thinking')
    return `|t:${resolveEffectiveThinkingExpanded() ? 1 : 0}`
  return ''
}
