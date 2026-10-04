import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeToolOutcome } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'

/** Read the actual foreground exit header without changing the native output bytes. */
export function readGrokShellResult(request: MockModelRequestRecord, callId: string): NativeToolOutcome {
  if (!callId)
    throw new Error('The native Grok shell result requires an exact nonempty call ID.')
  const text = nativeToolResult(request, callId)
  const lineEnd = text.indexOf('\n')
  const header = lineEnd < 0 ? null : /^exit: (0|-?[1-9]\d*)$/.exec(text.slice(0, lineEnd))
  const exitCode = header ? Number(header[1]) : undefined
  if (exitCode === undefined || !Number.isSafeInteger(exitCode))
    throw new Error('The exact native Grok result contains no complete safe integer exit header.')
  return { text, exitCode, failed: exitCode !== 0 }
}
