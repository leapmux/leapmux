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
      const ownID = pickString(own.toolCall, 'id')
      const matches = (raw: Record<string, unknown> | undefined) => {
        const step = antigravityStep(raw)
        return step && pickString(step.toolCall, 'id') === ownID
          ? raw
          : undefined
      }
      const row = antigravityToolRow(parent, matches(span.result?.parentObject), input.completion ?? parsed.completion)
      if (!row)
        return null
      if (input.spanId)
        row.registryKey = input.spanId
      return toolCallRow(antigravityToolCall(row), antigravityStepOpen(own)
        ? 'request'
        : 'result', span.visibleRows)
    }
    case 'user_content': {
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
