import type { MessageCategory } from '../../messageClassification'
import type { ToolResultMeta } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { pickString } from '~/lib/jsonPick'
import { COLLAPSED_RESULT_ROWS } from '../../toolRenderers'
import { extractItem } from './renderHelpers'

const DIFF_HUNK_HEADER = '@@ '

interface CodexItem {
  status: string
  aggregatedOutput: string
  changes: Array<Record<string, unknown>>
}

/** Resolve {item, status} for a Codex tool_use message; returns null when the shape doesn't match. */
function readCodexItem(parsed: unknown): CodexItem | null {
  const item = extractItem(parsed)
  if (!item)
    return null
  return {
    status: pickString(item, 'status'),
    aggregatedOutput: pickString(item, 'aggregatedOutput'),
    changes: Array.isArray(item.changes) ? item.changes as Array<Record<string, unknown>> : [],
  }
}

/**
 * ProviderPlugin.toolResultMeta implementation for Codex.
 *
 * Codex's terminal-state spans are classified as `tool_use` (not
 * `tool_result`), so this checks the spanType + item.status to decide whether
 * the message should display a result toolbar.
 */
export function codexToolResultMeta(
  category: MessageCategory,
  parsed: unknown,
  spanType: string | undefined,
  _toolUseParsed: ParsedMessageContent | undefined,
): ToolResultMeta | null {
  if (category.kind !== 'tool_use')
    return null

  const item = readCodexItem(parsed)
  if (!item)
    return null

  if (spanType === 'commandExecution' && (item.status === 'completed' || item.status === 'failed')) {
    return {
      collapsible: item.aggregatedOutput.split('\n').length > COLLAPSED_RESULT_ROWS,
      hasDiff: false,
      copyableContent: () => item.aggregatedOutput || null,
    }
  }

  if (spanType === 'fileChange' && item.status === 'completed') {
    const diffs = item.changes
      .map(change => typeof change.diff === 'string' ? change.diff as string : '')
      .filter(Boolean)
    const hasDiff = diffs.some(diff => diff.includes(DIFF_HUNK_HEADER))
    return {
      // Completed file-change diffs render in full; the toolbar's expand button
      // would be a no-op, so we report non-collapsible.
      collapsible: false,
      hasDiff,
      copyableContent: () => diffs.length > 0 ? diffs.join('\n\n') : null,
    }
  }

  return null
}
