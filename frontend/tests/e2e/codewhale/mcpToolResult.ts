import type { NativeToolOutput } from '../helpers/nativeToolOutput'
import { prettifyJson } from '../../../src/lib/jsonFormat'

/** Compute the exact native compact JSON and the complete ordered Copy text. */
export function codewhaleMcpToolResult(output: NativeToolOutput, options: { approvedByUser?: boolean } = {}): { capture: NativeToolOutput, nativeResult: ReturnType<typeof codewhaleMcpResult>, copyText: string } {
  const nativeResult = codewhaleMcpResult(output.text)
  const prefix = options.approvedByUser ? '[approval] This tool call required approval and was approved by the user before execution.\n\n' : ''
  const text = prefix + JSON.stringify(nativeResult)
  const extra = { structuredContent: nativeResult.structuredContent, _meta: nativeResult._meta }
  return { capture: { ...output, text }, nativeResult, copyText: prefix ? text : `${output.text}\n\n${prettifyJson(extra)}` }
}

function codewhaleMcpResult(text: string) {
  return {
    content: [{ type: 'text' as const, text }, { type: 'text' as const, text: '' }],
    structuredContent: { nextCount: 0, enabled: false, text: '' },
    _meta: { privateFixture: true },
  }
}
