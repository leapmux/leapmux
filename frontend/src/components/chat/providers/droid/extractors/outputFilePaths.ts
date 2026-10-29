import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { DROID_NOTIFICATION_FIELD, DROID_TOOL_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'

const OUTPUT_NOTICE = /(?:^|\r?\n)Full command output saved to: ([^\r\n]+) \([^\r\n]+\)(?=\r?\n|$)/gu
const MIDDLE_CUT_NOTICE = /(?:^|\r?\n)\[\.\.\. ([1-9]\d*) bytes cut from the middle of ([1-9]\d*); whole output: ([^\r\n]+)\](?=\r?\n|$)/gu
const OUTPUT_FILE = /[\\/]droid-terminal-[\w-]+[\\/][0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.log$/iu

/** Read the native footer. The terminal UUID is separate from the tool call ID. */
export function droidOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  if (input.span.role !== 'result' || native?.[DROID_NOTIFICATION_FIELD.Type] !== DROID_TOOL_NOTIFICATION.ToolResult
    || native[DROID_NOTIFICATION_FIELD.ToolUseID] !== call.id) {
    return []
  }
  return droidNativeOutputFilePaths(native[DROID_NOTIFICATION_FIELD.Content])
}

/** Read one native output pointer across text blocks and retain the terminal UUID constraint. */
export function droidNativeOutputFilePaths(content: unknown): readonly string[] {
  const texts = typeof content === 'string'
    ? [content]
    : Array.isArray(content)
      ? content.filter(isObject).filter(block => block.type === 'text').map(block => block.text).filter((text): text is string => typeof text === 'string')
      : []
  const paths = texts.flatMap(text => [
    ...text.matchAll(OUTPUT_NOTICE),
  ].map(match => match[1]!))
  for (const text of texts) {
    for (const match of text.matchAll(MIDDLE_CUT_NOTICE)) {
      if (BigInt(match[1]!) >= BigInt(match[2]!))
        return []
      paths.push(match[3]!)
    }
  }
  const path = paths.length === 1
    ? paths[0]
    : undefined
  return isFilesystemPath(path) && OUTPUT_FILE.test(path)
    ? [path]
    : []
}
