import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { COMMAND_CODE_TOOL } from '../../../src/generated/contracts/commandcode-protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { commandCodeToolCompleted } from './toolCompleted'

/** Read the original completed shell event and its native pointer footer. */
export function readCommandCodeNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: commandCodeToolCompleted(COMMAND_CODE_TOOL.ShellCommand, callId),
  })
  const result = pickObject(record.frame, 'event')?.result
  if (!Array.isArray(result) || result.some(block => !isObject(block) || block.type !== 'text' || typeof block.text !== 'string'))
    throw new Error('The native Command Code result has no exact text blocks.')
  const previewText = result.filter(isObject).map(block => block.text).join('')
  const matches = [...previewText.matchAll(/^\[full output saved to: ([^\r\n]+) — read it with read_file \(offset\/limit\) or grep\]$/gmu)]
  const path = matches.length === 1 ? matches[0]?.[1] : undefined
  if (!isFilesystemPath(path))
    throw new Error('The native Command Code result has no unique filesystem pointer.')
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
