import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeToolOutcome } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResult } from '../helpers/nativeToolResult'

function objectText(text: string): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(text)
    return isObject(value) ? value : null
  }
  catch {
    return null
  }
}

/** Read one exact code-mode result without deriving native status from prose. */
export function readCodexExecResult(request: MockModelRequestRecord, callId: string): NativeToolOutcome {
  const raw = nativeToolResult(request, callId)
  let value: unknown
  try {
    value = JSON.parse(raw)
  }
  catch {
    throw new Error('The native Codex command result contains no structured execution metadata.')
  }
  const texts = Array.isArray(value)
    ? value.filter(isObject).filter(block => block.type === 'input_text' && typeof block.text === 'string').map(block => String(block.text))
    : [raw]
  const results = texts.map(objectText).filter(isObject).filter(result => Object.hasOwn(result, 'output'))
  if (results.length !== 1)
    throw new Error('The exact native Codex call must contain one structured execution result.')
  const result = results[0]
  if (!result || typeof result.output !== 'string')
    throw new Error('The native Codex execution result must contain string output.')
  if (Object.hasOwn(result, 'exit_code')) {
    const exitCode = result.exit_code
    if (typeof exitCode !== 'number' || !Number.isSafeInteger(exitCode))
      throw new Error('The native Codex execution exit code must be an integer.')
    return { text: result.output, exitCode, failed: exitCode !== 0 }
  }
  if (typeof result.session_id !== 'number' || !Number.isSafeInteger(result.session_id) || result.session_id < 0)
    throw new Error('A native Codex execution without an exit code must identify its active session.')
  return { text: result.output }
}
