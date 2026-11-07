import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { antigravityToolCall, antigravityToolRow } from './toolCall'
import { antigravityStep, antigravityStepOpen } from './toolCommon'

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
      const planner = pickObject(parent, 'plannerResponse')
      const text = pickString(planner, 'modifiedResponse') || pickString(planner, 'response')
      return text
        ? { kind: 'assistant-text', text }
        : { kind: 'hidden' }
    }
    case 'assistant_thinking':
      return {
        kind: 'assistant-thinking',
        text: pickString(pickObject(parent, 'plannerResponse'), 'thinking'),
      }
    case 'tool_use':
    case 'tool_result': {
      const own = antigravityStep(parent)
      if (!own)
        return null
      const result = span.result?.parentObject
      const row = antigravityToolRow(own, result === parent
        ? own
        : antigravityStep(result), input.completion ?? parsed.completion)
      if (input.spanId)
        row.registryKey = input.spanId
      return toolCallRow(antigravityToolCall(row), antigravityStepOpen(own)
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
