import type {} from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'
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

export function codexSpanRole(parsed: ParsedMessageContent): ToolSpanRole {
  const item = extractItem(parsed.parentObject)
  if (!item || !CODEX_TOOL_SPANS.has(pickString(item, 'type')))
    return 'other'
  const parent = parsed.parentObject
  if (retainedRowIsFinal(parsed.completion) || Number.isFinite(parent?.completedAtMs) || isCodexFinishedStatus(pickString(item, 'status')) || (item.type === CODEX_ITEM.CollabAgentToolCall && item.status === 'interrupted'))
    return 'result'
  return Number.isFinite(parent?.startedAtMs) || item.status === CODEX_STATUS.IN_PROGRESS ? 'request' : 'other'
}
