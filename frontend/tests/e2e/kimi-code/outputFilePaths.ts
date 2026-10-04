import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { KIMI_OUTPUT_FILE_HEADER, KIMI_OUTPUT_FILE_LIMIT, KIMI_OUTPUT_FILE_POINTER } from '../../../src/components/chat/providers/kimi/protocol'
import { KIMI_TOOL } from '../../../src/generated/contracts/kimi-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** Canonical IDs stay unchanged when Kimi prepares the next OpenAI model request. */
export const KIMI_OUTPUT_PATH_CALL_IDS = {
  Header: 'native-kimi-output-path',
  PerLine: 'native-kimi-per-line-output-path',
  AmbiguousMcp: 'native-kimi-generic-mcp-output-path',
} as const

export interface KimiNativeOutputPointer {
  path: string
  chars?: number
  bytes?: number
}

function requireKimiOutputFilePath(path: string): string {
  if (!isFilesystemPath(path, true))
    throw new Error('The native Kimi pointer has an invalid filesystem path.')
  return path
}

/** Accept exactly one native complete footer. Task metadata does not supply missing complete counts. */
function kimiPerLineOutputFile(text: string): KimiNativeOutputPointer {
  const lines = text.split('\n')
  const pointers = lines.flatMap((line, index) => line.startsWith(KIMI_OUTPUT_FILE_HEADER.PerLineMarker) ? [index] : [])
  const index = pointers[0]
  if (pointers.length !== 1 || index === undefined || (lines[index] !== KIMI_OUTPUT_FILE_HEADER.PerLineComplete && lines[index] !== KIMI_OUTPUT_FILE_HEADER.PerLineCompleteText)
    || lines[index + 2] !== KIMI_OUTPUT_FILE_HEADER.PerLineNextStep || !lines[index + 1]?.startsWith(`${KIMI_OUTPUT_FILE_POINTER.Path}: `)) {
    throw new Error('The native Kimi result has no complete output-file pointer.')
  }
  return { path: requireKimiOutputFilePath(lines[index + 1]!.slice(`${KIMI_OUTPUT_FILE_POINTER.Path}: `.length)) }
}

/** Require the native complete-output pointer and its exact tool call identity. */
export function kimiNativeOutputPointer(text: string, callId: string, expectedToolName: string = KIMI_TOOL.Bash): KimiNativeOutputPointer {
  if (text.startsWith('[')) {
    const content: unknown = JSON.parse(text)
    if (!Array.isArray(content) || content.length !== 1 || !isObject(content[0]) || content[0].type !== 'text' || typeof content[0].text !== 'string')
      throw new Error('The native Kimi output path projection requires one text block.')
    text = content[0].text
  }
  text = text.replace(/^Wall time: \d+\.\d{3} seconds\n/u, '')
  text = text.replace(/^<system>ERROR: Tool execution failed\.<\/system>\n/u, '')
  if (!callId || !expectedToolName)
    throw new Error('The native Kimi result has no complete output-file pointer.')
  if (!text.startsWith(`${KIMI_OUTPUT_FILE_HEADER.Complete}\n`) && !text.startsWith(`${KIMI_OUTPUT_FILE_HEADER.CompleteText}\n`))
    return kimiPerLineOutputFile(text)
  const headerEnd = text.indexOf('\nnext_step: ')
  const nextStep = text.slice(headerEnd + 1).split('\n', 1)[0]
  if (headerEnd < 0 || nextStep !== KIMI_OUTPUT_FILE_HEADER.NextStep)
    throw new Error('The native Kimi output path requires its complete pointer header.')
  const header = text.slice(0, headerEnd)
  const allowedFields: ReadonlySet<string> = new Set(Object.values(KIMI_OUTPUT_FILE_POINTER))
  if (header.split('\n').slice(1).some(line => !allowedFields.has(line.slice(0, line.indexOf(':')))))
    throw new Error('The native Kimi output path has an unknown pointer field.')
  const readField = (field: string) => {
    const prefix = `${field}: `
    const matches = header.split('\n').filter(line => line.startsWith(prefix) && line.length > prefix.length && !line.includes('\r'))
    if (matches.length !== 1)
      throw new Error(`The native Kimi output path requires one exact ${field} field.`)
    return matches[0]!.slice(prefix.length)
  }
  if (readField(KIMI_OUTPUT_FILE_POINTER.ToolName) !== expectedToolName || readField(KIMI_OUTPUT_FILE_POINTER.ToolCallID) !== callId)
    throw new Error('The native Kimi output path belongs to another tool or call.')
  const path = readField(KIMI_OUTPUT_FILE_POINTER.Path)
  const size = readField(KIMI_OUTPUT_FILE_POINTER.CharacterCount)
  requireKimiOutputFilePath(path)
  if (!/^[1-9]\d*$/u.test(size) || !Number.isSafeInteger(Number(size)) || Number(size) <= KIMI_OUTPUT_FILE_LIMIT.InlineCharacters || Number(size) > 10_000_000) {
    throw new Error('The native Kimi output path has an invalid full path or complete character count.')
  }
  const byteFields = [...header.matchAll(/^output_size_bytes:([^\r\n]*)$/gm)]
  if (byteFields.length > 1)
    throw new Error('The native Kimi output path repeats its byte count.')
  const byteField = byteFields[0]?.[1]
  const bytes = byteField?.slice(1)
  if (byteField !== undefined && (!/^ [1-9]\d*$/u.test(byteField) || !Number.isSafeInteger(Number(bytes)) || Number(bytes) < Number(size)))
    throw new Error('The native Kimi output path has an invalid complete byte count.')
  return { path, chars: Number(size), ...(bytes === undefined ? {} : { bytes: Number(bytes) }) }
}

/** Read the original dotted result event within its exact native span. */
export function readKimiNativeOutput(snapshot: NativeMessageSnapshot, callId: string, toolName: string) {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: frame => `${snapshot.agentSessionId}/${frame.agentId}/${frame.turnId}/${callId}`,
    accepts: frame => frame.type === 'tool.result' && frame.toolCallId === callId && typeof frame.agentId === 'string'
      && frame.agentId !== '' && typeof frame.turnId === 'number' && Number.isSafeInteger(frame.turnId) && frame.turnId >= 0,
  })
  const output = record.frame.output
  const texts = typeof output === 'string'
    ? [output]
    : Array.isArray(output)
      ? output.filter(isObject).filter(block => block.type === 'text' && typeof block.text === 'string').map(block => String(block.text))
      : []
  const previewText = texts.join('\n')
  const pointer = kimiNativeOutputPointer(previewText, callId, toolName)
  const parts = pointer.path.replaceAll('\\', '/').split('/')
  if (!parts.includes(snapshot.agentSessionId) || !parts.includes(String(record.frame.agentId)))
    throw new Error('The native Kimi path belongs to another session or agent.')
  return { ...pointer, paths: [pointer.path], previewText, frame: record.frame, content: record.message.content }
}
