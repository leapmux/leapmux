/**
 * Shared row-render geometry metadata used before a row mounts.
 *
 * Pixel height estimation is intentionally absent: row heights come from
 * visible/hidden DOM measurement plus the virtualizer's generic unknown-row
 * fallback. This module only carries non-pixel identifiers that let caches know
 * when a previously measured DOM height is stale.
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
}

export function buildHeightKey(inputs: HeightKeyInputs): string {
  const toolUseRevision = `${inputs.toolUseRevisionKey.length}:${inputs.toolUseRevisionKey}`
  const toolResultRevision = `${inputs.toolResultRevisionKey.length}:${inputs.toolResultRevisionKey}`
  return `${inputs.seq}|${inputs.hasToolUseSibling ? 's' : ''}|${inputs.toolUseContentVersion}|${toolUseRevision}|${inputs.hasToolResultSibling ? 'r' : ''}|${inputs.toolResultContentVersion}|${toolResultRevision}|${inputs.uiVersion}|${inputs.contentVersion}|${inputs.hasCommandStream ? 'c' : ''}`
}
