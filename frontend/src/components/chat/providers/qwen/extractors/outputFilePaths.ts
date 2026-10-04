import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { QWEN_TOOL } from '~/generated/contracts/qwen-protocol'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { QWEN_OUTPUT_FILES, QWEN_OUTPUT_NOTICE_PREFIX } from '../protocol'
import { qwenShellResult, qwenToolName } from './toolCall'

/** Read the path from the native background result's first two lines. */
export function qwenOutputNoticePath(text: string): string | undefined {
  if (!text.startsWith(QWEN_OUTPUT_NOTICE_PREFIX))
    return undefined
  const end = text.indexOf('\n', QWEN_OUTPUT_NOTICE_PREFIX.length)
  if (end < 0)
    return undefined
  const path = text.slice(QWEN_OUTPUT_NOTICE_PREFIX.length, end)
  return isFilesystemPath(path) ? path : undefined
}

function backgroundPaths(content: unknown): readonly string[] {
  if (!Array.isArray(content))
    return []
  const paths: string[] = []
  for (const block of content) {
    if (!isObject(block) || block.type !== 'content' || !isObject(block.content)
      || block.content.type !== 'text' || typeof block.content.text !== 'string') {
      continue
    }
    const path = qwenOutputNoticePath(block.content.text)
    if (path !== undefined)
      paths.push(path)
  }
  return paths.length === 1 ? paths : []
}

/** Read native paths for this result. Keep the provider's inline preview intact. */
export function qwenOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const own = input.resolved.parentObject
  if (input.span.role !== 'result' || !own || !call.id)
    return []
  const result = parsedACPToolCall(own) ?? own
  if (result.sessionUpdate !== 'tool_call_update' || result.toolCallId !== call.id
    || (result.status !== 'completed' && result.status !== 'failed')) {
    return []
  }
  const opener = input.span.request?.parentObject
  const name = qwenToolName(result)
    || (opener?.toolCallId === call.id ? qwenToolName(opener) : '')
  if (name !== QWEN_TOOL.RunShellCommand)
    return []
  if (!Object.hasOwn(result, 'rawOutput'))
    return backgroundPaths(result.content)
  const raw = qwenShellResult(result)
  const paths = raw?.[QWEN_OUTPUT_FILES]
  if (!raw || raw.version !== 1 || !Array.isArray(paths)
    || !paths.every((path): path is string => isFilesystemPath(path))) {
    return []
  }
  return [...new Set(paths)]
}
