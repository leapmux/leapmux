import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base, expect } from './fixtures'
import { GEMINI_AGENT, nativeContext } from './gemini-cli/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const GEMINI_E2E_SKIP_REASON: string | null = missingBinaryReason('gemini', 'Gemini CLI E2E requires a gemini CLI on PATH')

export const geminiTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedGeminiWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(GEMINI_E2E_SKIP_REASON),
  authenticatedGeminiWorkspace: authenticatedAgentWorkspace(GEMINI_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId }))
  },
})

export { expect }
