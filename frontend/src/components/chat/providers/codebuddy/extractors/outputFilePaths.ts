import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { CODEBUDDY_FRAME_KIND } from '~/generated/contracts/codebuddy-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { CODEBUDDY_BLOCK_TYPE, CODEBUDDY_RESULT_FIELD } from '../protocol'

const OUTPUT_NOTICE = /^<persisted-output>\r?\nOutput too large \([^\r\n]+\)\. Full output saved to: ([^\r\n]+)\r?\n[\s\S]*\n<\/persisted-output>$/u

/** Read one native persisted pointer for this exact call and session. */
export function codebuddyOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  if (input.span.role !== 'result' || native?.[CODEBUDDY_RESULT_FIELD.Type] !== CODEBUDDY_FRAME_KIND.User
    || !native[CODEBUDDY_RESULT_FIELD.SessionID]
    || (input.resolved.agentSessionId && native[CODEBUDDY_RESULT_FIELD.SessionID] !== input.resolved.agentSessionId)) {
    return []
  }
  const blocks = pickObject(native, CODEBUDDY_RESULT_FIELD.Message)?.[CODEBUDDY_RESULT_FIELD.Content]
  if (!Array.isArray(blocks))
    return []
  const results = blocks.filter(isObject).filter(block => block[CODEBUDDY_RESULT_FIELD.Type] === CODEBUDDY_BLOCK_TYPE.ToolResult
    && block[CODEBUDDY_RESULT_FIELD.ToolUseID] === call.id)
  const result = results.length === 1 ? results[0] : undefined
  if (!result)
    return []
  const content = result[CODEBUDDY_RESULT_FIELD.Content]
  const texts = typeof content === 'string'
    ? [content]
    : Array.isArray(content)
      ? content.filter(isObject).filter(block => block[CODEBUDDY_RESULT_FIELD.Type] === CODEBUDDY_BLOCK_TYPE.Text).map(block => block[CODEBUDDY_RESULT_FIELD.Text]).filter((text): text is string => typeof text === 'string')
      : []
  const notices = texts.filter(text => text.startsWith('<persisted-output>'))
  const notice = notices.length === 1 ? notices[0] : undefined
  if (notice === undefined || notice.split('<persisted-output>').length !== 2 || notice.split('</persisted-output>').length !== 2)
    return []
  const path = OUTPUT_NOTICE.exec(notice)?.[1]
  if (!isFilesystemPath(path))
    return []

  return [path]
}
