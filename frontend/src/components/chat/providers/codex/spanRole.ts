import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'
import { codexRawExecRole } from './extractors/execute'
import { extractItem } from './extractors/item'
import { CODEX_STATUS } from './itemVocabulary'
import { isCodexFinishedStatus } from './status'

const CODEX_TOOL_SPANS = new Set<string>([
  CODEX_ITEM.CommandExecution,
  CODEX_ITEM.FileChange,
  CODEX_ITEM.McpToolCall,
  CODEX_ITEM.DynamicToolCall,
  CODEX_ITEM.ImageGeneration,
  CODEX_ITEM.ImageView,
  CODEX_ITEM.CollabAgentToolCall,
])

export function codexSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const executionRole = codexRawExecRole(parsed.parentObject)
  if (executionRole) {
    return retainedRowIsFinal(parsed.completion)
      ? 'result'
      : executionRole
  }
  const item = extractItem(parsed.parentObject)
  if (!item || !CODEX_TOOL_SPANS.has(pickString(item, 'type')))
    return 'other'
  const parent = parsed.parentObject
  if (retainedRowIsFinal(parsed.completion) || Number.isFinite(parent?.completedAtMs) || isCodexFinishedStatus(pickString(item, 'status')) || (item.type === CODEX_ITEM.CollabAgentToolCall && item.status === 'interrupted'))
    return 'result'
  return Number.isFinite(parent?.startedAtMs) || item.status === CODEX_STATUS.IN_PROGRESS
    ? 'request'
    : 'other'
}

/**
 * Whether one row draws from its span's request. Two rows do:
 * - The output of a native code cell states no script source, so it reads the source
 *   from its request.
 * - A finished collaboration call can state an empty tool, prompt, model, effort or
 *   target list, so it reads them from its request.
 *
 * Every other row reads its own item: a typed item states its arguments in every frame
 * of its span. A request row reads its own frame.
 */
export function codexNeedsRequest(parsed: ResolvedMessageContent): boolean {
  const executionRole = codexRawExecRole(parsed.parentObject)
  if (executionRole)
    return executionRole === 'result'
  return extractItem(parsed.parentObject)?.type === CODEX_ITEM.CollabAgentToolCall
    && codexSpanRole(parsed) === 'result'
}
