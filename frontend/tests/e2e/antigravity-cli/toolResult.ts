import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResult, nativeToolResultContent } from '../helpers/nativeToolResult'

const ANTIGRAVITY_RESULT_ROLES = ['user', 'model'] as const

export function antigravityNativeToolResult(request: MockModelRequestRecord | undefined, callId: string): string {
  return nativeToolResult(request, callId, { roles: ANTIGRAVITY_RESULT_ROLES })
}

export function antigravityNativeToolResultContent(request: MockModelRequestRecord | undefined, callId: string): unknown {
  return nativeToolResultContent(request, callId, { roles: ANTIGRAVITY_RESULT_ROLES })
}

/** Read one exact native Antigravity result from its following Google request. */
export function readAntigravityToolOutput(request: MockModelRequestRecord | undefined, callId: string): string {
  if (request?.protocol !== 'google-generative-language')
    throw new Error('The Antigravity tool result requires its native Google request.')
  return antigravityResponseOutput(antigravityNativeToolResultContent(request, callId))
}

function antigravityResponseOutput(response: unknown): string {
  if (!isObject(response) || typeof response.output !== 'string')
    throw new Error('The native Antigravity response has no string output.')
  return response.output
}
