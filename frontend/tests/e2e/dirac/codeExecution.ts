import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { isObject } from '../../../src/lib/jsonPick'

export interface DiracScriptFrame {
  original: unknown
  supplemental: unknown
}

export interface DiracScriptReceipt {
  callId: string
  output: string
  exitCode: number
  failed: boolean
}

/** Match the native generated heredoc without accepting a substring or a shell command. */
export function diracNativeScriptCommand(command: unknown, script: string): boolean {
  if (!script || typeof command !== 'string')
    return false
  const header = /^node << '(EOF_DIRAC_SCRIPT_[A-Z0-9]{1,8})'\n/u.exec(command)
  return header !== null && command === `${header[0]}${script}\n${header[1]}`
}

/** Pair the actual script card with its completion through the native generated call ID. */
export function diracScriptReceipt(frames: readonly DiracScriptFrame[], script: string): DiracScriptReceipt {
  if (!script)
    throw new Error('The native Dirac script proof requires its exact source.')
  const starts = new Map<string, Record<string, unknown>>()
  const completions = new Map<string, Record<string, unknown>[]>()
  for (const { original, supplemental } of frames) {
    if (!isObject(original) || typeof original.toolCallId !== 'string' || !original.toolCallId)
      continue
    const provider = isObject(supplemental) ? supplemental[MESSAGE_SUPPLEMENT_FIELD.Provider] : undefined
    const request = isObject(provider) ? { ...original, ...provider } : original
    if (request.toolCallId !== original.toolCallId)
      throw new Error('The native Dirac script supplement belongs to another card.')
    const input = request.rawInput
    const name = typeof request.name === 'string' ? request.name : isObject(input) ? input.tool : undefined
    if (isObject(input) && name === 'execute_command' && (input.tool === undefined || input.tool === name) && input.language === 'node'
      && input.displayName === 'Node script' && diracNativeScriptCommand(input.command, script)) {
      starts.set(original.toolCallId, input)
    }
    if (original.sessionUpdate === 'tool_call_update' && (original.status === 'completed' || original.status === 'failed')) {
      const entries = completions.get(original.toolCallId) ?? []
      entries.push(original)
      completions.set(original.toolCallId, entries)
    }
  }
  if (starts.size !== 1)
    throw new Error('The native Dirac script requires one exact generated execution card.')
  const callId = starts.keys().next().value
  if (!callId)
    throw new Error('The native Dirac script card has no call ID.')
  const results = completions.get(callId)
  if (results?.length !== 1)
    throw new Error('The native Dirac script requires one exact completion.')
  const result = results[0]!
  const output = result.rawOutput
  if (!isObject(output) || typeof output.output !== 'string' || typeof output.exitCode !== 'number'
    || !Number.isSafeInteger(output.exitCode) || output.exitCode < 0 || output.userRejected === true
    || (output.signal !== undefined && output.signal !== null)) {
    throw new Error('The native Dirac script has no complete process result.')
  }
  const failed = result.status === 'failed'
  if (failed !== (output.exitCode !== 0))
    throw new Error('The native Dirac script status disagrees with its exit code.')
  return { callId, output: output.output, exitCode: output.exitCode, failed }
}
