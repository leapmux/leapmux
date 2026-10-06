import type { ComputedNativeToolOutput } from '../helpers/nativeToolOutput'
import { QWEN_OUTPUT_FILES, QWEN_SHELL_RESULT } from '../../../src/components/chat/providers/qwen/protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'

export interface QwenOutputPathReceipt {
  callId: string
  status: string
  paths: readonly string[]
  preview: string
  exitCode?: number
}

/** Decode the native packet independently from the application path reader. */
function nativeNoticePath(text: string): string | undefined {
  const lines = text.split('\n', 3)
  if (lines.length < 3 || lines[0] !== 'Tool output was too large and has been truncated.')
    return undefined
  const pointer = lines[1]
  const label = 'The full output has been saved to: '
  if (pointer === undefined || !pointer.startsWith(label))
    return undefined
  const path = pointer.substring(label.length)
  return isFilesystemPath(path) ? path : undefined
}

function nativeTextBlocks(value: unknown): string[] {
  if (!Array.isArray(value))
    throw new Error('The native Qwen result requires a content array.')
  return value.map((block: unknown) => {
    if (!isObject(block) || block.type !== 'content' || !isObject(block.content)
      || block.content.type !== 'text' || typeof block.content.text !== 'string') {
      throw new Error('The native Qwen result requires text content blocks.')
    }
    return block.content.text
  })
}

/** Read paths and the original inline preview from one exact native result. */
export function qwenOutputPathReceipt(value: unknown): QwenOutputPathReceipt {
  if (!isObject(value) || typeof value.toolCallId !== 'string' || typeof value.status !== 'string' || !acpClosedToolCall(value, value.toolCallId))
    throw new Error('The native Qwen receipt requires a completed or failed tool call.')
  const content = nativeTextBlocks(value.content)
  const raw = value.rawOutput
  let paths: string[]
  let preview: string
  let exitCode: number | undefined
  if (raw === undefined) {
    paths = content.map(nativeNoticePath).filter((path): path is string => path !== undefined)
    if (paths.length !== 1)
      throw new Error('The native Qwen background result requires one output path notice.')
    preview = content.join('\n')
  }
  else {
    const nativePaths = isObject(raw) ? raw[QWEN_OUTPUT_FILES] : undefined
    if (!isObject(raw) || raw.type !== QWEN_SHELL_RESULT || raw.version !== 1 || !Array.isArray(nativePaths)
      || nativePaths.length === 0 || !nativePaths.every((path): path is string => isFilesystemPath(path))
      || typeof raw.output !== 'string' || (raw.error !== null && typeof raw.error !== 'string')) {
      throw new Error('The native Qwen shell result requires its original paths and preview.')
    }
    paths = [...new Set(nativePaths)]
    preview = value.status === 'failed' ? content.join('\n') : [raw.output, raw.error ?? ''].filter(text => text !== '').join('\n')
    if (typeof raw.exitCode === 'number' && Number.isSafeInteger(raw.exitCode))
      exitCode = raw.exitCode
  }
  return { callId: value.toolCallId, status: value.status, paths, preview, ...(exitCode === undefined ? {} : { exitCode }) }
}

/** Read the pointer Qwen sends back to the model. It supplies no file contents. */
export function qwenModelOutputPath(text: string): string {
  let texts = [text]
  if (text.startsWith('[')) {
    let blocks: unknown
    try {
      blocks = JSON.parse(text)
    }
    catch (cause) {
      throw new Error('The native Qwen model result contains an invalid content array.', { cause })
    }
    if (!Array.isArray(blocks) || blocks.length === 0)
      throw new Error('The native Qwen model result requires text blocks.')
    texts = blocks.map((block: unknown) => {
      if (!isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
        throw new Error('The native Qwen model result contains a non-text block.')
      return block.text
    })
  }
  const paths = texts.map(nativeNoticePath).filter((path): path is string => path !== undefined)
  const first = texts[0]
  if (paths.length !== 1 || first === undefined || nativeNoticePath(first) === undefined)
    throw new Error('The native Qwen model result requires its exact output path notice.')
  const path = paths[0]
  if (path === undefined)
    throw new Error('The native Qwen model result contains no output path.')
  return path
}

/**
 * The large-output command that makes Qwen save a shell result to a file, from the shared generator. Eight thousand
 * lines of thirty padding characters keep the native large-output trigger, so the proof needs no read of the file.
 */
export function qwenOutputPathCommand(prefix: string, exitCode = 0): ComputedNativeToolOutput & { command: string } {
  const output = computedNativeToolOutput({ prefix, lineCount: 8000, padding: 30 })
  return { ...output, command: nativeOutputFileCommand(output, { exitCode }) }
}
