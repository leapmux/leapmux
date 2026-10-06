/**
 * Codex-specific e2e test fixtures.
 * Extends the base fixtures with a Codex agent instead of Claude Code.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const CODEX_E2E_SKIP_REASON: string | null = missingBinaryReason('codex', 'Codex E2E requires the codex CLI on PATH')

/** How a Codex agent opens. */
export const CODEX_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, prefix: 'codex-e2e' }

export const codexTest = base.extend<CliSkipFixture & {
  authenticatedCodexWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CODEX_E2E_SKIP_REASON),
  authenticatedCodexWorkspace: authenticatedAgentWorkspace(CODEX_AGENT),
})

export { expect }
