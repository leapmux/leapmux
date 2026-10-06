import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const GEMINI_E2E_SKIP_REASON: string | null = missingBinaryReason('gemini', 'Gemini CLI E2E requires a gemini CLI on PATH')

/** How a Gemini CLI agent opens. */
export const GEMINI_AGENT: ProviderAgent = { provider: AgentProvider.GEMINI_CLI, prefix: 'gemini-e2e' }

export const geminiTest = base.extend<CliSkipFixture & {
  authenticatedGeminiWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(GEMINI_E2E_SKIP_REASON),
  authenticatedGeminiWorkspace: authenticatedAgentWorkspace(GEMINI_AGENT),
})

export { expect }
