import type { MessageCategory } from '../../messageClassification'
import type { ToolMessageInput, ToolResultMeta } from '../registry'
import { isObject, pickString } from '~/lib/jsonPick'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { messageCompletionFromProto } from '../../assembledMessage'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../../results/collapse'
import { commandOutputIsCollapsible } from '../../results/commandResult'
import { mcpToolResultMeta } from '../../results/mcpToolCall'
import { codexAgentCounterpart, codexAgentResults, resolveCodexAgentItem } from './extractors/agent'
import { codexMcpFromItem } from './extractors/mcp'
import { extractItem } from './renderHelpers'

const DIFF_HUNK_HEADER = '@@ '

/**
 * Provider.toolResultMeta implementation for Codex.
 *
 * Codex's terminal-state spans are classified as `tool_use` (not
 * `tool_result`), so this checks the spanType + item.status to decide whether
 * the message should display a result toolbar.
 */
export function codexToolResultMeta(
  category: MessageCategory,
  input: ToolMessageInput,
): ToolResultMeta | null {
  if (category.kind !== 'tool_use')
    return null

  const item = extractItem(input.parsed.parentObject)
  if (!item)
    return null
  if (item.type === CODEX_ITEM.IMAGE_VIEW && input.role === 'result')
    return { collapsible: false, hasDiff: false, hasCopyable: false, copyableContent: () => null }

  if (item.type === CODEX_ITEM.COLLAB_AGENT_TOOL_CALL && (input.role === 'result' || (item.status !== CODEX_STATUS.IN_PROGRESS && !!item.status))) {
    const results = codexAgentResults(resolveCodexAgentItem(item, codexAgentCounterpart(item, input.request, 'request')))
    const text = results.map(result => result.body).filter(Boolean).join('\n\n')
    return {
      collapsible: results.some(result => hasMoreLinesThan(result.body, COLLAPSED_RESULT_ROWS)),
      hasDiff: false,
      hasCopyable: !!text,
      copyableContent: () => text || null,
    }
  }

  const mcp = codexMcpFromItem(item)
  if (mcp)
    return mcp.status === CODEX_STATUS.IN_PROGRESS && !messageCompletionFromProto(input.parsed.completion) ? null : mcpToolResultMeta(mcp)

  if (input.spanType === CODEX_ITEM.COMMAND_EXECUTION && (messageCompletionFromProto(input.parsed.completion) || item.status === CODEX_STATUS.COMPLETED || item.status === CODEX_STATUS.FAILED)) {
    const output = pickString(item, 'aggregatedOutput')
    return {
      collapsible: commandOutputIsCollapsible(output),
      hasDiff: false,
      hasCopyable: output.length > 0,
      copyableContent: () => output || null,
    }
  }

  if (input.spanType === CODEX_ITEM.FILE_CHANGE && item.status === CODEX_STATUS.COMPLETED) {
    const changes = Array.isArray(item.changes) ? item.changes.filter(isObject) : []
    // Walk once for hasDiff; defer the diffs[] allocation to the lazy
    // copyableContent getter so streaming re-evals don't pay for it.
    const hasDiff = changes.some(c => typeof c.diff === 'string' && c.diff.includes(DIFF_HUNK_HEADER))
    return {
      // Completed file-change diffs render in full; the toolbar's expand button
      // would be a no-op, so we report non-collapsible.
      collapsible: false,
      hasDiff,
      hasCopyable: hasDiff,
      copyableContent: () => {
        const diffs = changes
          .map(change => typeof change.diff === 'string' ? change.diff as string : '')
          .filter(Boolean)
        return diffs.length > 0 ? diffs.join('\n\n') : null
      },
    }
  }

  return null
}
