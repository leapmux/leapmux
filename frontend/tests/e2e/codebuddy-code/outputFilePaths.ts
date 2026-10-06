import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Read one original native persisted result and its exact inline preview. */
export function readCodeBuddyNativeOutput(snapshot: NativeMessageSnapshot, callId: string): NativeOutputReceipt {
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
    throw new Error('The native CodeBuddy pointer requires one exact result block.')
  const resultContent = result.content
  const texts = typeof resultContent === 'string'
    ? [resultContent]
    : Array.isArray(resultContent)
      ? resultContent.filter(isObject).filter(block => block.type === 'text').map(block => block.text).filter((text): text is string => typeof text === 'string')
      : []
  const notices = texts.filter(text => text.startsWith('<persisted-output>'))
  const notice = notices.length === 1 ? notices[0] : undefined
  if (notice && (notice.split('<persisted-output>').length !== 2 || notice.split('</persisted-output>').length !== 2))
    throw new Error('The native result repeats its persisted-output wrapper.')
  const wrapperPath = notice ? /^<persisted-output>\r?\nOutput too large \([^\r\n]+\)\. Full output saved to: ([^\r\n]+)\r?\n[\s\S]*\n<\/persisted-output>$/u.exec(notice)?.[1] : undefined
  const path = wrapperPath
  const segments = typeof path === 'string' ? path.split(/[\\/]/u) : []
  if (!isFilesystemPath(path) || segments.at(-2) !== 'tool-results'
    || !segments.includes(snapshot.agentSessionId) || segments.at(-1) !== `${callId}.txt`) {
    throw new Error('The native CodeBuddy result has no filesystem output pointer.')
  }
  const previewText = texts.join('')
  return { paths: [path], previewText, frame: record.frame, content: record.message.content }
}
