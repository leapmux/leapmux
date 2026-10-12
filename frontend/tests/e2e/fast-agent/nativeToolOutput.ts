import { basename, dirname, isAbsolute, normalize } from 'node:path'
import { ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { isObject } from '../../../src/lib/jsonPick'

/** Read the installed shell runtime's complete retained-output notice. */
export function fastAgentNativeOutput(text: string): string {
  const matches = [...text.matchAll(/The complete output is available during this session at ([^\r\n]+?)\. Use read_text_file for selected line ranges or run a targeted search against that file;/gu)]
  const path = matches.length === 1 ? matches[0]?.[1] : undefined
  if (!path)
    throw new Error('The native Fast Agent result requires one complete retained-output file.')
  if (!isAbsolute(path) || normalize(path) !== path || path.includes('\0')
    || !/^output-(?:0|[1-9]\d*)\.log$/u.test(basename(path))
    || !/^fast-agent-output-[\w-]+$/u.test(basename(dirname(path)))) {
    throw new Error('The native Fast Agent file does not use its shell runtime path.')
  }
  return path
}

/** Match the actual ACP call by its command and both native output records. */
export function fastAgentTerminalOutputFileLimit(frames: readonly unknown[], command: string, modelText: string): { callId: string, text: string, previewText: string, byteLimit: number } {
  if (!command || !modelText)
    throw new Error('The native Fast Agent terminal proof requires its command and returned text.')
  const results = frames.filter(isObject).filter(frame => frame.sessionUpdate === ACP_UPDATE.ToolCallUpdate
    && frame.status === 'completed' && isObject(frame.rawInput) && frame.rawInput.command === command
    && typeof frame.rawOutput === 'string')
  const result = results.length === 1 ? results[0] : undefined
  if (!result || typeof result.toolCallId !== 'string' || !result.toolCallId || !Array.isArray(result.content) || result.content.length !== 1)
    throw new Error('The native Fast Agent terminal proof requires one exact completed command.')
  const item = result.content[0]
  const content = isObject(item) && item.type === 'content' && isObject(item.content) ? item.content : undefined
  if (content?.type !== 'text' || content.text !== result.rawOutput)
    throw new Error('The native Fast Agent terminal content differs from its raw output.')
  const text = result.rawOutput
  if (typeof text !== 'string')
    throw new Error('The native Fast Agent completed command has no text output.')
  const match = /^\[Output truncated by ACP terminal outputByteLimit: ([1-9]\d*) bytes \(~[1-9]\d* tokens\)\. Client returned partial output only\.\]\n[\s\S]*\n\n\[Exit code: 0\]$/u.exec(text)
  const byteLimit = match ? Number(match[1]) : Number.NaN
  if (!Number.isSafeInteger(byteLimit) || byteLimit <= 0)
    throw new Error('The native Fast Agent result lacks its exact client terminal output limit.')
  if (text !== modelText && fastAgentModelOutput(text, byteLimit) !== modelText)
    throw new Error('The native Fast Agent completed command differs from its exact model result.')
  return { callId: result.toolCallId, text, previewText: text.replace(/\n\n\[Exit code: 0\]$/u, ''), byteLimit }
}

/** Reproduce the native model window after the separate client terminal limit. */
function fastAgentModelOutput(text: string, byteLimit: number): string {
  const bytes = new TextEncoder().encode(text)
  if (bytes.length <= byteLimit)
    return text
  const tailBytes = Math.max(Math.floor(byteLimit / 2), 1)
  const headBytes = Math.max(byteLimit - tailBytes, 1)
  const head = new TextDecoder().decode(bytes.slice(0, headBytes))
  const tail = new TextDecoder().decode(bytes.slice(-tailBytes))
  const retainedBytes = headBytes + tailBytes
  const retainedTokens = Math.max(Math.floor(retainedBytes / 3.3), 1)
  const totalTokens = Math.max(Math.floor(bytes.length / 3.3), 1)
  const notice = `[Tool result truncated: showing first ${headBytes} bytes and last ${tailBytes} bytes of ${bytes.length} bytes (~${retainedTokens} of ~${totalTokens} tokens); omitted ${bytes.length - retainedBytes} middle bytes. Use a narrower query or request a smaller result to retain the relevant content.]`
  return `${head.endsWith('\n') ? head : `${head}\n`}${notice}${tail.startsWith('\n') ? tail : `\n${tail}`}`
}
