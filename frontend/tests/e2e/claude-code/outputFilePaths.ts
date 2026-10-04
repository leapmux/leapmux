import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Read one original native persisted result and its exact inline preview. */
export function readClaudeNativeOutput(snapshot: NativeMessageSnapshot, callId: string): { paths: string[], previewText: string, frame: Record<string, unknown>, content: Uint8Array } {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: (frame) => {
      const content = pickObject(frame, 'message')?.content
      return frame.type === 'user' && frame.session_id === snapshot.agentSessionId && Array.isArray(content)
        && content.some(block => isObject(block) && block.type === 'tool_result' && block.tool_use_id === callId)
    },
  })
  const content = pickObject(record.frame, 'message')?.content
  const matching = Array.isArray(content) ? content.filter(isObject).filter(block => block.type === 'tool_result' && block.tool_use_id === callId) : []
  const result = matching.length === 1 ? matching[0] : undefined
  if (!result || result.is_error === true)
    throw new Error('The native Claude pointer requires one exact result block.')
  const resultContent = result.content
  const texts = typeof resultContent === 'string'
    ? [resultContent]
    : Array.isArray(resultContent)
      ? resultContent.filter(isObject).filter(block => block.type === 'text').map(block => block.text).filter((text): text is string => typeof text === 'string')
      : []
  const structured = pickObject(record.frame, 'tool_use_result')
  const notices = texts.filter(text => text.startsWith('<persisted-output>'))
  const notice = notices.length === 1 ? notices[0] : undefined
  if (notice && (notice.split('<persisted-output>').length !== 2 || notice.split('</persisted-output>').length !== 2))
    throw new Error('The native result repeats its persisted-output wrapper.')
  const wrapperPath = notice ? /^<persisted-output>\r?\nOutput too large \([^\r\n]+\)\. Full output saved to: ([^\r\n]+)\r?\n[\s\S]*\n<\/persisted-output>$/u.exec(notice)?.[1] : undefined
  const path = structured?.persistedOutputPath ?? wrapperPath
  if (structured?.persistedOutputPath !== undefined && (structured.isImage === true || matching.length !== 1
    || (Array.isArray(content) && content.filter(isObject).filter(block => block.type === 'tool_result').length !== 1))) {
    throw new Error('The native Claude frame has no sole metadata owner.')
  }
  const segments = typeof path === 'string' ? path.split(/[\\/]/u) : []
  if (!isFilesystemPath(path) || segments.at(-2) !== 'tool-results'
    || !segments.includes(snapshot.agentSessionId)) {
    throw new Error('The native Claude result has no filesystem output pointer.')
  }
  const stdout = typeof structured?.stdout === 'string' ? structured.stdout : ''
  const stderr = typeof structured?.stderr === 'string' ? structured.stderr : ''
  const previewText = stdout && stderr ? `${stdout}\n${stderr}` : stdout || stderr || texts.join('')
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
