import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { antigravityToolCall, antigravityToolRow } from './toolCall'
import { antigravityPlannerContent, antigravityRowOpen, antigravityStep } from './toolCommon'

function antigravityUserInputText(parent: Record<string, unknown> | undefined): string {
  const input = pickObject(parent, 'userInput')
  const response = pickString(input, 'userResponse')
  if (response)
    return response
  const items = Array.isArray(input?.items)
    ? input.items
    : []
  return items.filter(isObject).map(item => pickString(item, 'text')).filter(Boolean).join('\n')
}

export function antigravityExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const parent = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text': {
      const { text } = antigravityPlannerContent(parent)
      return text
        ? { kind: 'assistant-text', text }
        : { kind: 'hidden' }
    }
    case 'assistant_thinking':
      return {
        kind: 'assistant-thinking',
        text: antigravityPlannerContent(parent).thinking,
      }
    case 'tool_use':
    case 'tool_result': {
      // Every native step states its tool call and its arguments, so a row reads its
      // own step only. An open step is the request row, and a finished step is the
      // result row.
      const own = antigravityStep(parent)
      if (!own)
        return null
      const completion = input.completion ?? parsed.completion
      const row = antigravityToolRow(own, completion)
      if (input.spanId)
        row.registryKey = input.spanId
      return toolCallRow(antigravityToolCall(row), antigravityRowOpen(own, completion)
        ? 'request'
        : 'result', span.visibleRows)
    }
    case 'user_content': {
      const native = pickObject(pickObject(parent, 'systemMessage'), 'agentMessage')
      if (native) {
        return native.hideFromUser !== true && pickString(native, 'content')
          ? {
              kind: 'user',
              text: pickString(native, 'content'),
              attachments: [],
            }
          : { kind: 'hidden' }
      }
      if (pickObject(parent, 'userInput')) {
        const text = antigravityUserInputText(parent)
        return text
          ? { kind: 'user', text, attachments: [] }
          : { kind: 'hidden' }
      }
      return leapmuxUserRow(parent)
    }
    case 'plan_execution':
      return leapmuxPlanExecutionRow(parent)
    default:
      return null
  }
}
