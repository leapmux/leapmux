import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ShellCommand, ShellResultEvidence } from '../helpers/nativeToolExecution'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResultContent, nativeToolResultEntry } from '../helpers/nativeToolResult'

/** Read the one text record that CodeBuddy supplied for this tool result. */
function recordText(content: unknown): string {
  if (typeof content === 'string')
    return content
  if (Array.isArray(content) && content.length === 1 && isObject(content[0])
    && content[0].type === 'text' && typeof content[0].text === 'string') {
    return content[0].text
  }
  throw new Error('The CodeBuddy shell result requires one native text record.')
}

/** Reject metadata that contradicts the completed command record. */
function validateMetadata(entry: Record<string, unknown>, exitCode: number): void {
  if (entry._meta === undefined)
    return
  if (!isObject(entry._meta))
    throw new Error('The CodeBuddy result metadata must be an object.')
  const raw = entry._meta.rawResponse
  if (raw === undefined)
    return
  if (!isObject(raw))
    throw new Error('The CodeBuddy raw response must be an object.')
  if (Object.hasOwn(raw, 'exitCode') && raw.exitCode !== exitCode)
    throw new Error('The CodeBuddy exit metadata conflicts with its native record.')
  if (raw.signal !== undefined && raw.signal !== null)
    throw new Error('The CodeBuddy signal metadata conflicts with its native record.')
  for (const flag of ['interrupted', 'sandboxDenied']) {
    if (Object.hasOwn(raw, flag) && raw[flag] !== false)
      throw new Error(`The CodeBuddy native command states ${flag}.`)
  }
  for (const field of ['stdoutBytesTruncated', 'stderrBytesTruncated']) {
    if (Object.hasOwn(raw, field) && raw[field] !== 0)
      throw new Error(`The CodeBuddy native command truncated ${field}.`)
  }
}

/**
 * Prove the generated command against CodeBuddy's complete native record.
 * CodeBuddy 2.160.0 stops its pipe readers on process exit and can omit stderr.
 * Only its exact empty-stderr failure preserves the record instead of the missing marker.
 */
export function readCodeBuddyShellResult(request: MockModelRequestRecord, command: Readonly<ShellCommand>): ShellResultEvidence {
  if (!command.command.trim() || /[\r\n\0]/u.test(command.command) || !command.output.trim()
    || /[\r\n\0]/u.test(command.output) || !Number.isSafeInteger(command.exitCode)) {
    throw new Error('The CodeBuddy shell proof requires one command and one output marker.')
  }
  const record = recordText(nativeToolResultContent(request, command.callId))
  const prefix = `Command: ${command.command}\nStdout: `
  if (!record.startsWith(prefix))
    throw new Error('The CodeBuddy native record states another command.')
  const footerStart = record.lastIndexOf('\nExit Code: ')
  const footer = footerStart >= prefix.length ? /^\nExit Code: (0|-?[1-9]\d*)\nSignal: \(none\)$/u.exec(record.slice(footerStart)) : null
  const exitCode = footer ? Number(footer[1]) : Number.NaN
  if (!Number.isSafeInteger(exitCode) || exitCode !== command.exitCode)
    throw new Error('The CodeBuddy native record has an invalid exit or signal.')
  const streams = record.slice(prefix.length, footerStart).split('\nStderr: ')
  if (streams.length !== 2 || streams.some(text => /^(?:Command|Stdout|Stderr|Exit Code|Signal):(?: |$)/mu.test(text)))
    throw new Error('The CodeBuddy shell record repeats or conflicts with a field.')
  validateMetadata(nativeToolResultEntry(request, command.callId), exitCode)
  const stdout = streams[0]!
  const stderr = streams[1]!
  const output = exitCode === 0 ? stdout : stderr
  if (output.includes(command.output))
    return { kind: 'output', outcome: { text: record, exitCode, failed: exitCode !== 0 }, absentRowText: ['Stdout:', 'Stderr:', 'Exit Code:', 'Signal:'] }
  if (exitCode === 7 && stdout === '(empty)' && stderr === '(empty)')
    return { kind: 'record', outcome: { text: record, exitCode, failed: true }, record }
  throw new Error('The CodeBuddy native record omits the required command output.')
}
