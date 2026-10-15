import { GEMINI_AGENT, nativeContext } from './gemini-cli/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

export const GEMINI_E2E_SKIP_REASON: string | null = missingBinaryReason('gemini', 'Gemini CLI E2E requires a gemini CLI on PATH')

export const geminiTest = createProviderTest({
  agent: GEMINI_AGENT,
  nativeContext,
  skipReason: GEMINI_E2E_SKIP_REASON,
  workspaceName: 'authenticatedGeminiWorkspace',
})
