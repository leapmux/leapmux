import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { JUNIE_OUTPUT_FILE_PATH, JUNIE_OUTPUT_REFERENCE, JUNIE_SUPPLEMENT, JUNIE_TERMINAL_META } from '~/generated/contracts/junie-protocol'
import { pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { acpToolSupplement } from '../../acp/toolSupplement'

const TASK_ID = new RegExp(JUNIE_OUTPUT_REFERENCE.TaskIDPattern)
const FILE_NAME = new RegExp(JUNIE_OUTPUT_REFERENCE.FileNamePattern)

/** Read the pointer-only receipt for the exact native command completion. */
export function junieOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  const sessionId = input.resolved.agentSessionId
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id || native.kind !== 'execute'
    || native.status !== 'completed' || !sessionId) {
    return []
  }
  const receipt = pickObject(acpToolSupplement(native, input.resolved.supplementalContent), JUNIE_SUPPLEMENT.OutputFilePath)
  const command = pickObject(native, 'rawInput')
  const output = pickObject(native, 'rawOutput')
  const exit = pickObject(pickObject(native, '_meta'), JUNIE_TERMINAL_META.Exit)
  const taskId = receipt?.[JUNIE_OUTPUT_FILE_PATH.TaskID]
  const path = receipt?.[JUNIE_OUTPUT_FILE_PATH.Path]
  if (!receipt || !command || !output || !exit || typeof command.command !== 'string' || !command.command.trim()
    || typeof command.cwd !== 'string' || typeof output.output !== 'string'
    || receipt[JUNIE_OUTPUT_FILE_PATH.SessionID] !== sessionId || receipt[JUNIE_OUTPUT_FILE_PATH.ToolCallID] !== call.id
    || receipt[JUNIE_OUTPUT_FILE_PATH.Command] !== command.command || receipt[JUNIE_OUTPUT_FILE_PATH.WorkingDirectory] !== command.cwd
    || receipt[JUNIE_OUTPUT_FILE_PATH.ExitCode] !== 0 || exit[JUNIE_TERMINAL_META.TerminalID] !== call.id
    || exit[JUNIE_TERMINAL_META.ExitCode] !== 0 || (exit[JUNIE_TERMINAL_META.Signal] !== undefined && exit[JUNIE_TERMINAL_META.Signal] !== null)
    || (Object.hasOwn(output, 'exitCode') && output.exitCode !== 0)
    || typeof taskId !== 'string' || !TASK_ID.test(taskId) || !isFilesystemPath(path)) {
    return []
  }
  const tail = path.split(/[\\/]/u).slice(-5)
  return tail[0] === 'sessions' && tail[1] === sessionId && tail[2] === taskId
    && tail[3] === 'terminal-output' && FILE_NAME.test(tail[4] ?? '')
    ? [path]
    : []
}
