import { GEMINI_AGENT, nativeContext } from './gemini-cli/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const geminiTest = createProviderTest({
  agent: GEMINI_AGENT,
  nativeContext,
  workspaceName: 'authenticatedGeminiWorkspace',
})
