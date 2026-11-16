import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { OPENCODE_NATIVE_MESSAGE_TYPE } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { openCodeToolCall } from './toolCall'
import { openCodeToolRecord, openCodeToolSpanRole } from './toolCommon'

/** Read native v2 rows into the provider-neutral chat model. */
export function openCodeExtractRow(input: RowExtractionInput): ChatRow | null {
  const parent = input.resolved.parentObject
  switch (input.category.kind) {
    case 'user_content':
      if (parent?.type === OPENCODE_NATIVE_MESSAGE_TYPE.User) {
        const attachments = Array.isArray(parent.files)
          ? parent.files.filter(isObject).map(file => ({
              filename: pickString(file, 'name'),
            }))
          : []
        return {
          kind: 'user',
          text: pickString(parent, 'text'),
          attachments,
        }
      }
      return leapmuxUserRow(parent)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(parent)
    case 'tool_use':
    case 'tool_result': {
      const own = openCodeToolRecord(parent)
      if (!own || !input.spanId)
        return null
      const completion = input.completion ?? input.resolved.completion
      const role = openCodeToolSpanRole(own, completion) === 'result'
        ? 'result'
        : 'request'
      return toolCallRow(openCodeToolCall({
        record: own,
        spanId: input.spanId,
        rowFinal: role === 'result',
        ...(completion !== undefined
          ? { completion }
          : {}),
      }), role, input.span.visibleRows)
    }
    default:
      return null
  }
}
