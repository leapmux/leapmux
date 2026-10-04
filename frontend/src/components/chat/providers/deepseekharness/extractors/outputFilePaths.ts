import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { deepseekHarnessEventData } from '../protocol'

const STREAM_NOTICE = /(?:^|\n)\[output truncated; full output: ([^\]\r\n]+)\]/gu
const FORMATTED_NOTICE = / Full formatted result stored at: ([^\r\n]+\.txt)\. [^\r\n]*\)$/gu
const STREAM_FILE = /[\\/]dsh-subprocess-[\w-]+[\\/]dsh-subprocess-[1-9]\d*-[1-9]\d*-[0-9a-f]{12}-(?:stdout|stderr)\.log$/u
const FORMATTED_FILE = /[\\/]dsh-spill[\\/][0-9a-f]{12}[\\/][0-9a-f]{12}-[^\\/]+\.txt$/u

/** Read the native stream and formatted-result pointers without opening either file. */
export function deepseekHarnessOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const data = deepseekHarnessEventData(input.resolved.parentObject, DEEPSEEK_HARNESS_EVENT.ToolResult)
  const message = pickObject(data, 'message')
  if (input.span.role !== 'result' || message?.toolCallId !== call.id || !Array.isArray(message.content))
    return []
  const paths: string[] = []
  for (const block of message.content) {
    if (!isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
      continue
    for (const match of block.text.matchAll(STREAM_NOTICE)) {
      const path = match[1]
      if (isFilesystemPath(path) && STREAM_FILE.test(path))
        paths.push(path)
    }
    for (const match of block.text.matchAll(FORMATTED_NOTICE)) {
      const path = match[1]
      if (isFilesystemPath(path) && FORMATTED_FILE.test(path))
        paths.push(path)
    }
  }
  return [...new Set(paths)]
}
