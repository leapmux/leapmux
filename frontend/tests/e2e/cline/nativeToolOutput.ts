import { isObject } from '../../../src/lib/jsonPick'

/** Read one completed native command with its explicit lost-middle notice. */
export function clineNativeOutputLimit(text: string): string {
  const value: unknown = JSON.parse(text)
  const operations = isObject(value) && value.event === 'tool.finished' && typeof value.sessionId === 'string' && value.sessionId !== ''
    && isObject(value.payload) && value.payload.toolName === 'run_commands' && typeof value.payload.toolCallId === 'string' && value.payload.toolCallId !== ''
    ? value.payload.output
    : value
  if (!Array.isArray(operations) || operations.length !== 1 || !isObject(operations[0]) || operations[0].success !== true || typeof operations[0].result !== 'string')
    throw new Error('The Cline large command result has no exact successful native operation.')
  if (!/\[\.\.\. output truncated: \d+ chars total\./.test(operations[0].result))
    throw new Error('The Cline large command result has no native omitted-middle notice.')
  return operations[0].result
}
