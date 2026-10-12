import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { DROID_NOTIFICATION_FIELD, DROID_TOOL_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'

const OUTPUT_NOTICE = /(?:^|\r?\n)Full command output saved to: ([^\r\n]+) \([^\r\n]+\)(?=\r?\n|$)/gu
const OUTPUT_FILE = /[\\/]droid-terminal-[\w-]+[\\/][0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.log$/iu

/** Read the native footer. The terminal UUID is separate from the tool call ID. */
export function droidOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  if (input.span.role !== 'result' || native?.[DROID_NOTIFICATION_FIELD.Type] !== DROID_TOOL_NOTIFICATION.ToolResult
    || native[DROID_NOTIFICATION_FIELD.ToolUseID] !== call.id) {
    return []
  }
  const content = native[DROID_NOTIFICATION_FIELD.Content]
  const texts = typeof content === 'string'
    ? [content]
    : Array.isArray(content)
      ? content.filter(isObject).filter(block => block.type === 'text').map(block => block.text).filter((text): text is string => typeof text === 'string')
      : []
  const matches = texts.flatMap(text => [...text.matchAll(OUTPUT_NOTICE)])
  const path = matches.length === 1 ? matches[0]?.[1] : undefined
  return isFilesystemPath(path) && OUTPUT_FILE.test(path) ? [path] : []
}
