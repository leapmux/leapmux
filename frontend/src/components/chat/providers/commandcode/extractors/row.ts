import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { commandCodeEvent, commandCodeText } from '../protocol'
import { commandCodeToolCall } from './toolCall'

export function commandCodeExtractRow(input: RowExtractionInput): ChatRow | null {
  const payload = input.resolved.parentObject
  if (!payload)
    return null
  switch (input.category.kind) {
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    case 'assistant_text': {
      const text = commandCodeText(commandCodeEvent(payload)?.content)
      return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
    }
    case 'assistant_thinking': {
      const text = pickString(commandCodeEvent(payload), 'text')
      return text ? { kind: 'assistant-thinking', text } : { kind: 'hidden' }
    }
    case 'tool_use':
    case 'tool_result': {
      if (input.span.role === 'none')
        return null
      const call = commandCodeToolCall(input)
      return call ? toolCallRow(call, input.span.role === 'other' ? input.category.kind === 'tool_use' ? 'request' : 'result' : input.span.role, input.span.visibleRows) : null
    }
    default:
      return null
  }
}
