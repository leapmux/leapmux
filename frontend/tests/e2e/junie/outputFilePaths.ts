import type { NativeMessageSnapshot, NativeToolOutputRecord } from '../helpers/nativeMessages'
import type { NativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { acpToolSupplement } from '../../../src/components/chat/providers/acp/toolSupplement'
import { ACP_SUPPLEMENT, ACP_TERMINAL_RESULT } from '../../../src/generated/contracts/acp-protocol'
import { JUNIE_OUTPUT_FILE_PATH, JUNIE_OUTPUT_REFERENCE, JUNIE_SUPPLEMENT, JUNIE_TERMINAL_META } from '../../../src/generated/contracts/junie-protocol'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { acpClosedToolCall, requireAcpToolSupplement } from '../helpers/acpToolFrame'
import { nativeMessageBody, nativeMessageSupplement, readNativeToolOutputRecord } from '../helpers/nativeMessages'

export interface JunieNativeOutputPaths extends NativeToolOutputRecord {
  paths: string[]
  previewText: string
  status: 'completed'
}

/** Read the exact native notice. The path alone does not prove file existence. */
export function junieNativeNoticePath(text: string, sessionId?: string): string {
  const matches = [...text.matchAll(/\[Command output exceeded the display limit and has been (?:truncated|summarized)\. See full log at: ([^\r\n]+?)\. Important information/gu)]
  const path = matches.length === 1 ? matches[0]?.[1] : undefined
  const owner = typeof path === 'string' ? /[\\/]sessions[\\/](session-[A-Za-z0-9-]+)[\\/]task-[A-Za-z0-9-]+[\\/]terminal-output[\\/]terminal-output-\d+\.txt$/u.exec(path) : null
  if (!isFilesystemPath(path) || (!path.endsWith('/.output.txt') && !owner))
    throw new Error('The Junie output notice has no unique native log path.')
  if (sessionId !== undefined && (!sessionId || (owner && owner[1] !== sessionId)))
    throw new Error('The Junie native log path belongs to another session.')
  return path
}

/** Read native host-terminal preview bytes with the exact retained call identity. */
export function junieHostTerminalPreview(snapshot: NativeMessageSnapshot, callId: string): string {
  if (!snapshot.agentId || !snapshot.agentSessionId || !callId)
    throw new Error('The Junie host preview requires its native agent, session, and call.')
  const outputs: string[] = []
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId || message.spanId !== callId || message.spanType !== 'execute'
      || message.completion !== MessageCompletion.COMPLETE) {
      continue
    }
    const frame = nativeMessageBody(message)
    const extra = nativeMessageSupplement(message)
    const provider = isObject(frame) && frame.toolCallId === callId && isObject(extra)
      ? acpToolSupplement(frame, pickObject(extra, MESSAGE_SUPPLEMENT_FIELD.Provider))
      : undefined
    if (!isObject(frame) || !provider)
      continue
    // `acpSupplementTerminals` cannot read this record. It reads an absent `truncated` as false, and it drops a
    // `signal` field whose value is empty or is not a string. This check refuses both: an absent `truncated`, and any
    // `signal` field.
    const terminals = pickObject(provider, ACP_SUPPLEMENT.Terminals)
    const ids = Array.isArray(frame.content) ? frame.content.filter(isObject).filter(block => block.type === 'terminal').map(block => block.terminalId) : []
    if (ids.length !== 1 || ids[0] !== callId)
      throw new Error('The Junie host preview refers to a different native terminal.')
    const terminal = pickObject(terminals, callId)
    const output = terminal?.[ACP_TERMINAL_RESULT.Output]
    if (typeof output !== 'string' || terminal?.[ACP_TERMINAL_RESULT.ExitCode] !== 0
      || typeof terminal[ACP_TERMINAL_RESULT.Truncated] !== 'boolean' || Object.hasOwn(terminal, ACP_TERMINAL_RESULT.Signal)) {
      throw new Error('The Junie host preview lacks exact successful native terminal fields.')
    }
    outputs.push(output)
  }
  if (outputs.length !== 1)
    throw new Error('The Junie host preview requires one exact retained native terminal.')
  return outputs[0]!
}

/** Decode the pointer-only owner receipt and the unchanged native inline preview. */
export function readJunieNativeOutputPaths(snapshot: NativeMessageSnapshot, callId: string): JunieNativeOutputPaths {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => acpClosedToolCall(frame, callId, ['completed']) && frame.kind === 'execute',
  })
  const supplemental = isObject(record.supplement) ? pickObject(record.supplement, MESSAGE_SUPPLEMENT_FIELD.Provider) : undefined
  if (!supplemental)
    throw new Error('The Junie pointer receipt has no retained provider record.')
  const provider = requireAcpToolSupplement(record.frame, supplemental, 'Junie pointer receipt')
  const receipt = pickObject(provider, JUNIE_SUPPLEMENT.OutputFilePath)
  const command = pickObject(record.frame, 'rawInput')
  const output = pickObject(record.frame, 'rawOutput')
  const exit = pickObject(pickObject(record.frame, '_meta'), JUNIE_TERMINAL_META.Exit)
  const path = receipt?.[JUNIE_OUTPUT_FILE_PATH.Path]
  const task = receipt?.[JUNIE_OUTPUT_FILE_PATH.TaskID]
  if (!receipt || !command || !output || !exit || typeof output.output !== 'string'
    || receipt[JUNIE_OUTPUT_FILE_PATH.SessionID] !== snapshot.agentSessionId || receipt[JUNIE_OUTPUT_FILE_PATH.ToolCallID] !== callId
    || receipt[JUNIE_OUTPUT_FILE_PATH.Command] !== command.command || receipt[JUNIE_OUTPUT_FILE_PATH.WorkingDirectory] !== command.cwd
    || typeof command.command !== 'string' || !command.command.trim() || !isFilesystemPath(command.cwd)
    || receipt[JUNIE_OUTPUT_FILE_PATH.ExitCode] !== 0 || exit[JUNIE_TERMINAL_META.TerminalID] !== callId || exit[JUNIE_TERMINAL_META.ExitCode] !== 0
    || (exit[JUNIE_TERMINAL_META.Signal] !== null && exit[JUNIE_TERMINAL_META.Signal] !== undefined)
    || (Object.hasOwn(output, 'exitCode') && output.exitCode !== 0)
    || typeof task !== 'string' || !new RegExp(JUNIE_OUTPUT_REFERENCE.TaskIDPattern).test(task) || !isFilesystemPath(path)) {
    throw new Error('The Junie path receipt differs from its native command, session, or exit owner.')
  }
  const tail = path.split(/[\\/]/u).slice(-5)
  if (tail[0] !== 'sessions' || tail[1] !== snapshot.agentSessionId || tail[2] !== task || tail[3] !== 'terminal-output'
    || !new RegExp(JUNIE_OUTPUT_REFERENCE.FileNamePattern).test(tail[4] ?? '')) {
    throw new Error('The Junie path receipt belongs to another native task or filename.')
  }
  return { ...record, paths: [path], previewText: output.output, status: 'completed' }
}

/** The receipt of {@link readJunieNativeOutputPaths} in the shape that `proveNativeOutputReceipt` reads. */
export interface JunieNativeOutputReceipt extends NativeOutputReceipt {
  /** The provider supplement of the row, which holds the pointer-only path receipt. */
  supplement: unknown
}

/**
 * Read the pointer-only receipt for `proveNativeOutputReceipt`, which requires it unchanged after a reload.
 * The receipt holds the original bytes of the row, not the whole Worker message, because only the bytes, the frame,
 * and the supplement belong to the native result.
 */
export function readJunieNativeOutputReceipt(snapshot: NativeMessageSnapshot, callId: string): JunieNativeOutputReceipt {
  const receipt = readJunieNativeOutputPaths(snapshot, callId)
  return { paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame, supplement: receipt.supplement, content: receipt.message.content }
}
