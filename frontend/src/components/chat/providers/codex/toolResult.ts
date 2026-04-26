import type { MessageCategory } from '../../messageClassification'
import type { ToolResultMeta } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { pickString } from '~/lib/jsonPick'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { hasMoreLinesThan } from '../../results/useCollapsedLines'
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

  if (spanType === CODEX_ITEM.COMMAND_EXECUTION && (item.status === CODEX_STATUS.COMPLETED || item.status === CODEX_STATUS.FAILED)) {
    return {
      collapsible: hasMoreLinesThan(item.aggregatedOutput, COLLAPSED_RESULT_ROWS),
      hasDiff: false,
      hasCopyable: item.aggregatedOutput.length > 0,
      copyableContent: () => item.aggregatedOutput || null,
    }
  }

  if (spanType === CODEX_ITEM.FILE_CHANGE && item.status === CODEX_STATUS.COMPLETED) {
    // Walk once for hasDiff; defer the diffs[] allocation to the lazy
    // copyableContent getter so streaming re-evals don't pay for it.
    const hasDiff = item.changes.some(c => typeof c.diff === 'string' && (c.diff as string).includes(DIFF_HUNK_HEADER))
    return {
      // Completed file-change diffs render in full; the toolbar's expand button
      // would be a no-op, so we report non-collapsible.
      collapsible: false,
      hasDiff,
      hasCopyable: hasDiff,
      copyableContent: () => {
        const diffs = item.changes
          .map(change => typeof change.diff === 'string' ? change.diff as string : '')
          .filter(Boolean)
        return diffs.length > 0 ? diffs.join('\n\n') : null
      },
    }
  }

  return null
}
