import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { MUSE_ITEM_KIND } from '~/generated/contracts/muse-protocol'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { museItem, museItemText } from '../protocol'
import { museToolCall } from './toolCall'
import { museWorkflowResult } from './workflow'

export function museExtractRow(input: RowExtractionInput): ChatRow | null {
  const payload = input.resolved.parentObject
  const item = museItem(payload)
  switch (input.category.kind) {
    case 'user_content':
      return item ? { kind: 'user', text: museItemText(item), attachments: [] } : leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    case 'assistant_text':
      return { kind: 'assistant-text', text: museItemText(item) }
    case 'assistant_thinking':
      return { kind: 'assistant-thinking', text: museItemText(item) }
    case 'tool_use':
    case 'tool_result': {
      const call = item?.kind === MUSE_ITEM_KIND.Workflow ? museWorkflowResult(input) : museToolCall(input)
      return call ? toolCallRow(call, input.category.kind === 'tool_use' ? 'request' : 'result', input.span.visibleRows) : null
    }
    default:
      return null
  }
}
