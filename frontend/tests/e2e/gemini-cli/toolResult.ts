import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResultContent } from '../helpers/nativeToolResult'

/** Read one exact native Gemini result from its following Google request. */
export function readGeminiToolOutput(request: MockModelRequestRecord | undefined, callId: string): string {
  if (request?.protocol !== 'google-generative-language')
    throw new Error('The Gemini tool result requires its native Google request.')
  return geminiResponseOutput(nativeToolResultContent(request, callId))
}

function geminiResponseOutput(response: unknown): string {
  if (!isObject(response) || typeof response.output !== 'string')
    throw new Error('The native Gemini response has no string output.')
  return response.output
}
