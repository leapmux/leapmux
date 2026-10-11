import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { KIMI_OUTPUT_FILE_CONTENT, KIMI_OUTPUT_FILE_CONTENT_KIND, KIMI_OUTPUT_FILE_HEADER, KIMI_OUTPUT_FILE_LIMIT, KIMI_OUTPUT_FILE_POINTER, KIMI_OUTPUT_FILE_RESULT } from '../protocol'

interface KimiOutputPointer {
  path: string
  toolName: string
}

/** Read the native header. Preview text and command metadata cannot replace its fields. */
function kimiOutputHeader(text: string, callId: string): KimiOutputPointer | null {
  const lines = text.split('\n')
  const end = lines.findIndex(line => line.startsWith('next_step:'))
  if (end < 0)
    return null
  if (lines[end] !== KIMI_OUTPUT_FILE_HEADER.NextStep)
    return null
  const fields = new Map<string, string>()
  const allowed = new Set<string>(Object.values(KIMI_OUTPUT_FILE_POINTER))
  for (const line of lines.slice(1, end)) {
    const separator = line.indexOf(': ')
    if (separator < 0)
      return null
    const key = line.slice(0, separator)
    const value = line.slice(separator + 2)
    if (!allowed.has(key) || fields.has(key) || !value)
      return null
    fields.set(key, value)
  }
  const path = fields.get(KIMI_OUTPUT_FILE_POINTER.Path)
  const count = fields.get(KIMI_OUTPUT_FILE_POINTER.CharacterCount)
  const toolName = fields.get(KIMI_OUTPUT_FILE_POINTER.ToolName)
  if (!path || !toolName || fields.get(KIMI_OUTPUT_FILE_POINTER.ToolCallID) !== callId
    || !count || !Number.isSafeInteger(Number(count)) || Number(count) <= KIMI_OUTPUT_FILE_LIMIT.InlineCharacters || String(Number(count)) !== count) {
    return null
  }
  const bytes = fields.get(KIMI_OUTPUT_FILE_POINTER.ByteCount)
  if (bytes !== undefined && (!Number.isSafeInteger(Number(bytes)) || Number(bytes) < Number(count) || String(Number(bytes)) !== bytes))
    return null
  return { path, toolName }
}

function kimiOutputFooter(lines: string[], marker: number, toolName: string): KimiOutputPointer | null {
  if ((lines[marker] !== KIMI_OUTPUT_FILE_HEADER.PerLineComplete && lines[marker] !== KIMI_OUTPUT_FILE_HEADER.PerLineCompleteText)
    || lines[marker + 2] !== KIMI_OUTPUT_FILE_HEADER.PerLineNextStep) {
    return null
  }
  const prefix = `${KIMI_OUTPUT_FILE_POINTER.Path}: `
  const pathLine = lines[marker + 1]
  if (!pathLine?.startsWith(prefix) || pathLine.length === prefix.length)
    return null
  return { path: pathLine.slice(prefix.length), toolName }
}

function kimiOutputPointer(texts: string[], callId: string, toolName: string): KimiOutputPointer | null {
  let pointer: KimiOutputPointer | null = null
  for (const text of texts) {
    const lines = text.split('\n')
    let candidate: KimiOutputPointer | null
    if (lines[0] === KIMI_OUTPUT_FILE_HEADER.Complete || lines[0] === KIMI_OUTPUT_FILE_HEADER.CompleteText) {
      candidate = kimiOutputHeader(text, callId)
    }
    else {
      let marker = -1
      for (const [index, line] of lines.entries()) {
        if (line.startsWith(KIMI_OUTPUT_FILE_HEADER.PerLineMarker)) {
          if (marker >= 0)
            return null
          marker = index
        }
      }
      if (marker < 0)
        continue
      candidate = kimiOutputFooter(lines, marker, toolName)
    }
    if (!candidate || pointer)
      return null
    pointer = candidate
  }
  return pointer
}

/** Native directory and filename fields identify the session, agent, and tool. */
function kimiOutputPathMatches(path: string, sessionId: string, agentId: string, callId: string, toolName: string): boolean {
  const parts = path.replaceAll('\\', '/').split('/')
  const task = parts.slice(-8)
  if (toolName === KIMI_TOOL.Bash && task[0] === 'sessions' && task[2] === sessionId && task[3] === 'agents'
    && task[4] === agentId && task[5] === 'tasks' && /^bash-[a-z0-9]{8}$/u.test(task[6] ?? '') && task[7] === 'output.log') {
    return true
  }
  const result = parts.slice(-7)
  if (result[0] !== 'sessions' || result[2] !== sessionId || result[3] !== 'agents' || result[4] !== agentId || result[5] !== 'tool-results')
    return false
  const stem = `${toolName}-${callId}`.replace(/[^\w.-]+/gu, '_').replace(/^_+|_+$/gu, '').slice(0, 80) || 'tool-result'
  const file = result[6] ?? ''
  const prefix = `${stem}-`
  return file.startsWith(prefix) && file.endsWith('.txt')
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/u.test(file.slice(prefix.length, -4))
}

/** Read the native header or footer. The producer permits relative output paths. */
export function kimiOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  const sessionId = input.resolved.agentSessionId
  const agentId = native?.[KIMI_OUTPUT_FILE_RESULT.AgentID]
  const turnId = native?.[KIMI_OUTPUT_FILE_RESULT.TurnID]
  if (input.span.role !== 'result' || native?.[KIMI_OUTPUT_FILE_RESULT.Type] !== KIMI_EVENT.ToolResult
    || native[KIMI_OUTPUT_FILE_RESULT.ToolCallID] !== call.id || !sessionId || !/^session_[\w.-]+$/u.test(sessionId)
    || typeof agentId !== 'string' || !/^[\w.-]+$/u.test(agentId) || agentId === '.' || agentId === '..'
    || typeof turnId !== 'number' || !Number.isSafeInteger(turnId) || turnId < 0 || !call.name) {
    return []
  }
  const output = native[KIMI_OUTPUT_FILE_RESULT.Output]
  const texts = typeof output === 'string'
    ? [output]
    : Array.isArray(output)
      ? output.filter(isObject).filter(part => part[KIMI_OUTPUT_FILE_CONTENT.Type] === KIMI_OUTPUT_FILE_CONTENT_KIND.Text).map(part => part[KIMI_OUTPUT_FILE_CONTENT.Text]).filter((text): text is string => typeof text === 'string')
      : []
  const pointer = kimiOutputPointer(texts, call.id, call.name)
  return pointer && pointer.toolName === call.name && isFilesystemPath(pointer.path, true)
    && kimiOutputPathMatches(pointer.path, sessionId, agentId, call.id, call.name)
    ? [pointer.path]
    : []
}
