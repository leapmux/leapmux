import type { NativeMessageSnapshot, NativeToolOutputRecord } from '../helpers/nativeMessages'
import { DEEPSEEK_HARNESS_EVENT, DEEPSEEK_HARNESS_TOOL } from '../../../src/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

export interface DeepseekHarnessNativeOutput extends NativeToolOutputRecord {
  paths: string[]
  previewText: string
  /**
   * The native content blocks of the result. `content` in a shared output receipt means the bytes of the Worker row,
   * which `message.content` holds here.
   */
  blocks: Record<string, unknown>[]
  status: 'completed' | 'failed'
}

/** Read original native paths and preview bytes with the exact Worker owner. */
export function readDeepseekHarnessNativeOutput(snapshot: NativeMessageSnapshot, callId: string): DeepseekHarnessNativeOutput {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: (frame) => {
      const message = pickObject(pickObject(frame, 'data'), 'message')
      return frame.type === DEEPSEEK_HARNESS_EVENT.ToolResult && message?.toolCallId === callId
    },
  })
  const message = pickObject(pickObject(record.frame, 'data'), 'message')
  if (!message || typeof message.isError !== 'boolean' || !Array.isArray(message.content) || message.content.some(block => !isObject(block)))
    throw new Error('The DeepSeek native output requires exact content and failure fields.')
  const blocks = message.content.filter(isObject)
  const texts = blocks.filter(block => block.type === 'text').map(block => block.text)
  if (texts.some(text => typeof text !== 'string'))
    throw new Error('The DeepSeek native text block has no text string.')
  const nativeText = texts.filter((text): text is string => typeof text === 'string')
  const execute = record.message.spanType === DEEPSEEK_HARNESS_TOOL.Bash
    || record.message.spanType === DEEPSEEK_HARNESS_TOOL.Workflow || record.message.spanType === DEEPSEEK_HARNESS_TOOL.RunCode
  let previewText = nativeText.join(execute ? '' : '\n\n')
  if (record.message.spanType === DEEPSEEK_HARNESS_TOOL.Bash)
    previewText = previewText.replace(/\n\[(?:exit code: \d+|killed by signal: [^\]\n]+)\]$/u, '')
  const paths: string[] = []
  for (const text of nativeText) {
    for (const match of text.matchAll(/(?:^|\n)\[output truncated; full output: ([^\]\r\n]+)\]/gu)) {
      const path = match[1]
      if (isFilesystemPath(path) && /[\\/]dsh-subprocess-[\w-]+[\\/]dsh-subprocess-[1-9]\d*-[1-9]\d*-[0-9a-f]{12}-(?:stdout|stderr)\.log$/u.test(path))
        paths.push(path)
    }
    for (const match of text.matchAll(/ Full formatted result stored at: ([^\r\n]+\.txt)\. [^\r\n]*\)$/gu)) {
      const path = match[1]
      // The native spill store writes `<root>/session-<12 hex>/<12 hex>-<encoded name>`.
      if (isFilesystemPath(path) && /[\\/]session-[0-9a-f]{12}[\\/][0-9a-f]{12}-(?:[\w.-]|~[0-9A-F]{4})*\.txt$/u.test(path))
        paths.push(path)
    }
  }
  return { ...record, paths: [...new Set(paths)], previewText, blocks, status: message.isError ? 'failed' : 'completed' }
}
