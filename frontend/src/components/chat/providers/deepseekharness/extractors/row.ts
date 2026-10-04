import type { ChatRow, UserMessageAttachment } from '../../../model/row'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { DEEPSEEK_HARNESS_CONTENT_TYPE, DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { deepseekHarnessAssistantBlock, deepseekHarnessContentText, deepseekHarnessEventData } from '../protocol'
import { deepseekHarnessToolCall } from './toolCall'

export function deepseekHarnessExtractRow(input: RowExtractionInput): ChatRow | null {
  const payload = input.resolved.parentObject
  switch (input.category.kind) {
    case 'assistant_text': {
      const text = pickString(deepseekHarnessAssistantBlock(payload), 'text')
      return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
    }
    case 'assistant_thinking': {
      const text = pickString(deepseekHarnessAssistantBlock(payload), 'text')
      return text ? { kind: 'assistant-thinking', text } : { kind: 'hidden' }
    }
    case 'user_content': {
      const data = deepseekHarnessEventData(payload, DEEPSEEK_HARNESS_EVENT.UserMessage)
      if (!data)
        return leapmuxUserRow(payload)
      const text = deepseekHarnessContentText(data.content)
      const attachments: UserMessageAttachment[] = Array.isArray(data.content)
        ? data.content.flatMap((block) => {
            if (!isObject(block) || block.type === DEEPSEEK_HARNESS_CONTENT_TYPE.Text)
              return []
            const ref = pickObject(block, 'attachment')
            if (!ref)
              return []
            const filename = pickString(ref, 'name')
            const mimeType = pickString(ref, 'mediaType')
            return [{ ...(filename ? { filename } : {}), ...(mimeType ? { mimeType } : {}) }]
          })
        : []
      return text || attachments.length > 0 ? { kind: 'user', text, attachments } : { kind: 'hidden' }
    }
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    case 'tool_use':
    case 'tool_result': {
      if (input.span.role === 'none')
        return null
      const call = deepseekHarnessToolCall(input)
      if (!call)
        return null
      const role = input.span.role === 'other' ? input.category.kind === 'tool_use' ? 'request' : 'result' : input.span.role
      return toolCallRow(call, role, input.span.visibleRows)
    }
    default:
      return null
  }
}
